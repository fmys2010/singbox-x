package v2rayxhttp

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"math/rand/v2"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"

	"github.com/gofrs/uuid/v5"
)

const (
	placementQueryInHeader = "queryInHeader"
	placementCookie        = "cookie"
	placementHeader        = "header"
	placementQuery         = "query"
	placementPath          = "path"
	placementBody          = "body"
	placementAuto          = "auto"
)

const (
	modeAuto      = "auto"
	modePacketUp  = "packet-up"
	modeStreamUp  = "stream-up"
	modeStreamOne = "stream-one"

	paddingRepeatX = "repeat-x"
	paddingToken   = "tokenish"
)

var predefinedTable = map[string]string{
	"ALPHABET": "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Alphabet": "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"BASE36":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"Base62":   "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	"HEX":      "0123456789ABCDEF",
	"alphabet": "abcdefghijklmnopqrstuvwxyz",
	"base36":   "0123456789abcdefghijklmnopqrstuvwxyz",
	"hex":      "0123456789abcdef",
	"number":   "0123456789",
}

type rangeValue struct {
	From, To int32
}

func newRange(r *option.XHTTPRange) rangeValue {
	if r == nil {
		return rangeValue{}
	}
	return rangeValue{r.From, r.To}
}

func (r rangeValue) rand() int32 {
	if r.To <= r.From {
		return r.From
	}
	return r.From + rand.Int32N(r.To-r.From+1)
}

// config is the validated, defaulted form of option.V2RayXHTTPOptions.
// Defaults and validation mirror xray-core (infra/conf SplitHTTPConfig.Build).
type config struct {
	host    string
	path    string // raw, may contain "?query"
	mode    string
	headers http.Header

	xPaddingBytes     rangeValue
	xPaddingObfsMode  bool
	xPaddingKey       string
	xPaddingHeader    string
	xPaddingPlacement string
	xPaddingMethod    string

	uplinkHTTPMethod    string
	sessionIDPlacement  string
	sessionIDKey        string
	sessionIDTable      string
	sessionIDLength     rangeValue
	seqPlacement        string
	seqKey              string
	uplinkDataPlacement string
	uplinkDataKey       string
	uplinkChunkSize     rangeValue

	noGRPCHeader bool
	noSSEHeader  bool

	scMaxEachPostBytes   rangeValue
	scMinPostsIntervalMs rangeValue
	scMaxBufferedPosts   int
	scStreamUpServerSecs rangeValue
	serverMaxHeaderBytes int

	xmux xmuxConfig
}

type xmuxConfig struct {
	maxConcurrency   rangeValue
	maxConnections   rangeValue
	cMaxReuseTimes   rangeValue
	hMaxRequestTimes rangeValue
	hMaxReusableSecs rangeValue
	hKeepAlivePeriod int64
}

func newConfig(options option.V2RayXHTTPOptions) (*config, error) {
	c := &config{
		host:                 options.Host,
		path:                 options.Path,
		mode:                 options.Mode,
		xPaddingBytes:        newRange(options.XPaddingBytes),
		xPaddingObfsMode:     options.XPaddingObfsMode,
		xPaddingKey:          options.XPaddingKey,
		xPaddingHeader:       options.XPaddingHeader,
		xPaddingPlacement:    options.XPaddingPlacement,
		xPaddingMethod:       options.XPaddingMethod,
		uplinkHTTPMethod:     options.UplinkHTTPMethod,
		sessionIDPlacement:   options.SessionIDPlacement,
		sessionIDKey:         options.SessionIDKey,
		sessionIDTable:       options.SessionIDTable,
		sessionIDLength:      newRange(options.SessionIDLength),
		seqPlacement:         options.SeqPlacement,
		seqKey:               options.SeqKey,
		uplinkDataPlacement:  options.UplinkDataPlacement,
		uplinkDataKey:        options.UplinkDataKey,
		uplinkChunkSize:      newRange(options.UplinkChunkSize),
		noGRPCHeader:         options.NoGRPCHeader,
		noSSEHeader:          options.NoSSEHeader,
		scMaxEachPostBytes:   newRange(options.ScMaxEachPostBytes),
		scMinPostsIntervalMs: newRange(options.ScMinPostsIntervalMs),
		scMaxBufferedPosts:   int(options.ScMaxBufferedPosts),
		scStreamUpServerSecs: newRange(options.ScStreamUpServerSecs),
		serverMaxHeaderBytes: int(options.ServerMaxHeaderBytes),
	}

	c.headers = make(http.Header)
	for key, values := range options.Headers {
		if strings.EqualFold(key, "host") {
			return nil, E.New("\"headers\" can't contain \"host\", use the \"host\" option instead")
		}
		for _, value := range values {
			c.headers.Add(key, value)
		}
	}

	switch c.mode {
	case "":
		c.mode = modeAuto
	case modeAuto, modePacketUp, modeStreamUp, modeStreamOne:
	default:
		return nil, E.New("unsupported mode: ", c.mode)
	}

	if c.xPaddingBytes != (rangeValue{}) && (c.xPaddingBytes.From <= 0 || c.xPaddingBytes.To <= 0) {
		return nil, E.New("x_padding_bytes cannot be disabled")
	}
	if c.xPaddingKey == "" {
		c.xPaddingKey = "x_padding"
	}
	if c.xPaddingHeader == "" {
		c.xPaddingHeader = "X-Padding"
	}
	switch c.xPaddingPlacement {
	case "":
		c.xPaddingPlacement = placementQueryInHeader
	case placementCookie, placementHeader, placementQuery, placementQueryInHeader:
	default:
		return nil, E.New("unsupported padding placement: ", c.xPaddingPlacement)
	}
	switch c.xPaddingMethod {
	case "":
		c.xPaddingMethod = paddingRepeatX
	case paddingRepeatX, paddingToken:
	default:
		return nil, E.New("unsupported padding method: ", c.xPaddingMethod)
	}

	switch c.uplinkDataPlacement {
	case "":
		c.uplinkDataPlacement = placementAuto
	case placementAuto, placementBody:
	case placementCookie, placementHeader:
		if c.mode != modePacketUp {
			return nil, E.New("uplink_data_placement can be ", c.uplinkDataPlacement, " only in packet-up mode")
		}
	default:
		return nil, E.New("unsupported uplink data placement: ", c.uplinkDataPlacement)
	}
	if c.uplinkHTTPMethod == "" {
		c.uplinkHTTPMethod = http.MethodPost
	}
	c.uplinkHTTPMethod = strings.ToUpper(c.uplinkHTTPMethod)
	if c.uplinkHTTPMethod == http.MethodGet && c.mode != modePacketUp {
		return nil, E.New("uplink_http_method can be GET only in packet-up mode")
	}

	switch c.sessionIDPlacement {
	case "":
		c.sessionIDPlacement = placementPath
	case placementPath, placementCookie, placementHeader, placementQuery:
	default:
		return nil, E.New("unsupported session id placement: ", c.sessionIDPlacement)
	}
	switch c.seqPlacement {
	case "":
		c.seqPlacement = placementPath
	case placementPath, placementCookie, placementHeader, placementQuery:
	default:
		return nil, E.New("unsupported seq placement: ", c.seqPlacement)
	}
	if c.sessionIDKey == "" {
		switch c.sessionIDPlacement {
		case placementCookie, placementQuery:
			c.sessionIDKey = "x_session"
		case placementHeader:
			c.sessionIDKey = "X-Session"
		}
	}
	if c.seqKey == "" {
		switch c.seqPlacement {
		case placementCookie, placementQuery:
			c.seqKey = "x_seq"
		case placementHeader:
			c.seqKey = "X-Seq"
		}
	}
	if c.uplinkDataKey == "" {
		switch c.uplinkDataPlacement {
		case placementCookie:
			c.uplinkDataKey = "x_data"
		case placementAuto, placementHeader:
			c.uplinkDataKey = "X-Data"
		}
	}

	if c.sessionIDTable != "" {
		if predefined, loaded := predefinedTable[c.sessionIDTable]; loaded {
			c.sessionIDTable = predefined
		}
		if c.sessionIDLength.From <= 0 {
			return nil, E.New("session_id_length.from must be greater than 0")
		}
		room := big.NewInt(int64(len(c.sessionIDTable)))
		room.Exp(room, big.NewInt(int64(c.sessionIDLength.From)), nil)
		if room.Cmp(big.NewInt(2<<30)) < 0 {
			return nil, E.New("session_id_table or session_id_length is too small")
		}
		for i := 0; i < len(c.sessionIDTable); i++ {
			if c.sessionIDTable[i] >= 0x80 {
				return nil, E.New("session_id_table must contain only ASCII characters")
			}
		}
	}

	if c.serverMaxHeaderBytes < 0 {
		return nil, E.New("invalid negative server_max_header_bytes")
	}

	if options.Xmux != nil {
		c.xmux = xmuxConfig{
			maxConcurrency:   newRange(options.Xmux.MaxConcurrency),
			maxConnections:   newRange(options.Xmux.MaxConnections),
			cMaxReuseTimes:   newRange(options.Xmux.CMaxReuseTimes),
			hMaxRequestTimes: newRange(options.Xmux.HMaxRequestTimes),
			hMaxReusableSecs: newRange(options.Xmux.HMaxReusableSecs),
			hKeepAlivePeriod: options.Xmux.HKeepAlivePeriod,
		}
	}
	if c.xmux.maxConnections.To > 0 && c.xmux.maxConcurrency.To > 0 {
		return nil, E.New("xmux.max_connections cannot be specified together with xmux.max_concurrency")
	}
	if c.xmux == (xmuxConfig{}) {
		c.xmux.maxConnections = rangeValue{3, 3}
		c.xmux.hMaxRequestTimes = rangeValue{600, 900}
		c.xmux.hMaxReusableSecs = rangeValue{1800, 3000}
	}
	return c, nil
}

func (c *config) normalizedPath() string {
	path, _, _ := strings.Cut(c.path, "?")
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	if c.sessionIDPlacement == placementPath || c.seqPlacement == placementPath {
		if path[len(path)-1] != '/' {
			path += "/"
		}
	}
	return path
}

func (c *config) normalizedQuery() string {
	_, query, _ := strings.Cut(c.path, "?")
	return query
}

func (c *config) xPaddingRange() rangeValue {
	if c.xPaddingBytes.To == 0 {
		return rangeValue{100, 1000}
	}
	return c.xPaddingBytes
}

func (c *config) maxEachPostBytes() rangeValue {
	if c.scMaxEachPostBytes.To == 0 {
		return rangeValue{1000000, 1000000}
	}
	return c.scMaxEachPostBytes
}

func (c *config) minPostsIntervalMs() rangeValue {
	if c.scMinPostsIntervalMs.To == 0 {
		return rangeValue{30, 30}
	}
	return c.scMinPostsIntervalMs
}

func (c *config) maxBufferedPosts() int {
	if c.scMaxBufferedPosts == 0 {
		return 30
	}
	return c.scMaxBufferedPosts
}

func (c *config) streamUpServerSecs() rangeValue {
	if c.scStreamUpServerSecs.To == 0 {
		return rangeValue{20, 80}
	}
	return c.scStreamUpServerSecs
}

func (c *config) uplinkChunkSizeRange() rangeValue {
	if c.uplinkChunkSize.To == 0 {
		switch c.uplinkDataPlacement {
		case placementCookie:
			return rangeValue{2 * 1024, 3 * 1024}
		case placementHeader:
			return rangeValue{3 * 1000, 4 * 1000}
		default:
			return c.maxEachPostBytes()
		}
	} else if c.uplinkChunkSize.From < 64 {
		return rangeValue{64, max(64, c.uplinkChunkSize.To)}
	}
	return c.uplinkChunkSize
}

func (c *config) maxHeaderBytes() int {
	if c.serverMaxHeaderBytes <= 0 {
		return 8192
	}
	return c.serverMaxHeaderBytes
}

func (c *config) generateSessionID() string {
	length := c.sessionIDLength.rand()
	if c.sessionIDTable != "" && length > 0 {
		id := make([]byte, length)
		for i := range id {
			id[i] = c.sessionIDTable[rand.IntN(len(c.sessionIDTable))]
		}
		return string(id)
	}
	id, _ := uuid.NewV4()
	return id.String()
}

func (c *config) requestHeader() http.Header {
	header := make(http.Header, len(c.headers)+10)
	for key, values := range c.headers {
		header[key] = append([]string(nil), values...)
	}
	applyDefaultHeaders(header)
	return header
}

func (c *config) requestHeaderWithPayload(payload []byte) http.Header {
	header := c.requestHeader()
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	for i := 0; len(encoded) > 0; i++ {
		chunkSize := min(int(c.uplinkChunkSizeRange().rand()), len(encoded))
		header.Set(fmt.Sprintf("%s-%d", c.uplinkDataKey, i), encoded[:chunkSize])
		encoded = encoded[chunkSize:]
	}
	return header
}

func (c *config) requestCookiesWithPayload(payload []byte) []*http.Cookie {
	var cookies []*http.Cookie
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	for i := 0; len(encoded) > 0; i++ {
		chunkSize := min(int(c.uplinkChunkSizeRange().rand()), len(encoded))
		cookies = append(cookies, &http.Cookie{Name: fmt.Sprintf("%s_%d", c.uplinkDataKey, i), Value: encoded[:chunkSize]})
		encoded = encoded[chunkSize:]
	}
	return cookies
}

func (c *config) needCredentials() bool {
	return c.sessionIDPlacement == placementCookie ||
		c.seqPlacement == placementCookie ||
		c.xPaddingPlacement == placementCookie ||
		c.uplinkDataPlacement == placementCookie
}

// writeResponseHeader adds the CORS headers xray-core sends for its browser dialer.
func (c *config) writeResponseHeader(writer http.ResponseWriter, requestMethod string, requestHeader http.Header) {
	if origin := requestHeader.Get("Origin"); origin == "" {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		writer.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if c.needCredentials() {
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if requestMethod == http.MethodOptions {
		if requested := requestHeader.Get("Access-Control-Request-Method"); requested != "" {
			writer.Header().Set("Access-Control-Allow-Methods", requested)
		} else {
			writer.Header().Set("Access-Control-Allow-Methods", "*")
		}
		if requested := requestHeader.Get("Access-Control-Request-Headers"); requested != "" {
			writer.Header().Set("Access-Control-Allow-Headers", requested)
		} else {
			writer.Header().Set("Access-Control-Allow-Headers", "*")
		}
	}
}

func appendToPath(path, value string) string {
	if strings.HasSuffix(path, "/") {
		return path + value
	}
	return path + "/" + value
}

// applyMeta writes the session id / sequence number into the request.
func (c *config) applyMeta(request *http.Request, sessionID string, seq string) {
	apply := func(placement, key, value string) {
		switch placement {
		case placementPath:
			request.URL.Path = appendToPath(request.URL.Path, value)
		case placementQuery:
			query := request.URL.Query()
			query.Set(key, value)
			request.URL.RawQuery = query.Encode()
		case placementHeader:
			request.Header.Set(key, value)
		case placementCookie:
			request.AddCookie(&http.Cookie{Name: key, Value: value})
		}
	}
	if sessionID != "" {
		apply(c.sessionIDPlacement, c.sessionIDKey, sessionID)
	}
	if seq != "" {
		apply(c.seqPlacement, c.seqKey, seq)
	}
}

// extractMeta is the server side counterpart of applyMeta.
func (c *config) extractMeta(request *http.Request, path string) (sessionID string, seq string) {
	var subPath []string
	pathPart := 0
	if c.sessionIDPlacement == placementPath || c.seqPlacement == placementPath {
		subPath = strings.Split(request.URL.Path[len(path):], "/")
	}
	extract := func(placement, key string) string {
		switch placement {
		case placementPath:
			if len(subPath) > pathPart {
				pathPart++
				return subPath[pathPart-1]
			}
		case placementQuery:
			return request.URL.Query().Get(key)
		case placementHeader:
			return request.Header.Get(key)
		case placementCookie:
			if cookie, err := request.Cookie(key); err == nil {
				return cookie.Value
			}
		}
		return ""
	}
	sessionID = extract(c.sessionIDPlacement, c.sessionIDKey)
	seq = extract(c.seqPlacement, c.seqKey)
	return
}

func (c *config) fillStreamRequest(request *http.Request, sessionID string) {
	request.Header = c.requestHeader()
	c.applyXPadding(request)
	c.applyMeta(request, sessionID, "")
	if request.Body != nil && !c.noGRPCHeader {
		request.Header.Set("Content-Type", "application/grpc")
	}
}

func (c *config) fillPacketRequest(request *http.Request, sessionID string, seq string, payload []byte) {
	switch c.uplinkDataPlacement {
	case placementHeader:
		request.Header = c.requestHeaderWithPayload(payload)
	case placementCookie:
		request.Header = c.requestHeader()
		for _, cookie := range c.requestCookiesWithPayload(payload) {
			request.AddCookie(cookie)
		}
	default:
		request.Header = c.requestHeader()
		request.Body = io.NopCloser(bytes.NewReader(payload))
		request.ContentLength = int64(len(payload))
		request.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
	}
	c.applyXPadding(request)
	c.applyMeta(request, sessionID, seq)
}
