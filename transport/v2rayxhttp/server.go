package v2rayxhttp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/badhttp"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
)

var _ adapter.V2RayServerTransport = (*Server)(nil)

type Server struct {
	ctx        context.Context
	logger     logger.ContextLogger
	tlsConfig  tls.ServerConfig
	handler    adapter.V2RayServerTransportHandler
	httpServer *http.Server
	config     *config
	path       string
	sessions   sync.Map
	sessionMu  sync.Mutex
}

type httpSession struct {
	uploadQueue *uploadQueue
	// open until the client's GET arrives; an unclaimed session is reaped
	fullyConnected chan struct{}
	connectedOnce  sync.Once
}

func NewServer(ctx context.Context, logger logger.ContextLogger, options option.V2RayXHTTPOptions, tlsConfig tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (*Server, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return nil, err
	}
	server := &Server{
		ctx:       ctx,
		logger:    logger,
		tlsConfig: tlsConfig,
		handler:   handler,
		config:    cfg,
		path:      cfg.normalizedPath(),
	}
	// plaintext HTTP/1.1 and prior-knowledge HTTP/2 (which is also what an
	// ALPN-negotiated h2 over TLS/REALITY looks like to the http.Server)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.httpServer = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 4 * time.Second,
		MaxHeaderBytes:    cfg.maxHeaderBytes(),
		Protocols:         protocols,
		HTTP2: &http.HTTP2Config{
			// Go's 1 MiB default caps the upload of a stream to 1 MiB / RTT
			MaxReceiveBufferPerStream:     h2StreamWindow,
			MaxReceiveBufferPerConnection: h2ConnectionWindow,
		},
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return log.ContextWithNewID(ctx)
		},
	}
	return server, nil
}

func (s *Server) Network() []string {
	return []string{N.NetworkTCP}
}

func (s *Server) Serve(listener net.Listener) error {
	if s.tlsConfig != nil {
		if len(s.tlsConfig.NextProtos()) == 0 {
			s.tlsConfig.SetNextProtos([]string{http2.NextProtoTLS, "http/1.1"})
		} else if !common.Contains(s.tlsConfig.NextProtos(), http2.NextProtoTLS) {
			s.tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, s.tlsConfig.NextProtos()...))
		}
		listener = aTLS.NewListener(listener, s.tlsConfig)
	}
	return s.httpServer.Serve(listener)
}

func (s *Server) ServePacket(listener net.PacketConn) error {
	return os.ErrInvalid
}

func (s *Server) Close() error {
	s.sessions.Range(func(key, value any) bool {
		value.(*httpSession).uploadQueue.Close()
		s.sessions.Delete(key)
		return true
	})
	return common.Close(common.PtrOrNil(s.httpServer))
}

func (s *Server) upsertSession(sessionID string) *httpSession {
	if session, loaded := s.sessions.Load(sessionID); loaded {
		return session.(*httpSession)
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if session, loaded := s.sessions.Load(sessionID); loaded {
		return session.(*httpSession)
	}
	session := &httpSession{
		uploadQueue:    newUploadQueue(s.config.maxBufferedPosts()),
		fullyConnected: make(chan struct{}),
	}
	s.sessions.Store(sessionID, session)
	time.AfterFunc(30*time.Second, func() {
		select {
		case <-session.fullyConnected:
		default:
			s.sessions.Delete(sessionID)
			session.uploadQueue.Close()
		}
	})
	return session
}

func validHost(requestHost, configHost string) bool {
	if host, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHost = host
	}
	requestHost = strings.Trim(requestHost, "[]")
	configHost = strings.Trim(configHost, "[]")
	if host, _, err := net.SplitHostPort(configHost); err == nil {
		configHost = host
	}
	return strings.EqualFold(requestHost, configHost)
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	cfg := s.config
	if cfg.host != "" && !validHost(request.Host, cfg.host) {
		s.invalidRequest(writer, request, http.StatusNotFound, E.New("bad host: ", request.Host))
		return
	}
	if !strings.HasPrefix(request.URL.Path, s.path) {
		s.invalidRequest(writer, request, http.StatusNotFound, E.New("bad path: ", request.URL.Path))
		return
	}

	cfg.writeResponseHeader(writer, request.Method, request.Header)
	cfg.applyXPaddingToResponse(writer)

	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusOK)
		return
	}

	paddingValue, paddingPlacement := cfg.extractXPaddingFromRequest(request)
	if !cfg.isPaddingValid(paddingValue) {
		s.invalidRequest(writer, request, http.StatusBadRequest, E.New("invalid padding (", paddingPlacement, ") length: ", len(paddingValue)))
		return
	}
	obfsPaddingAccepted := cfg.xPaddingObfsMode && paddingValue != ""

	sessionID, seqStr := cfg.extractMeta(request, s.path)
	if sessionID == "" && cfg.mode != modeAuto && cfg.mode != modeStreamOne {
		s.invalidRequest(writer, request, http.StatusBadRequest, E.New("stream-one mode is not allowed"))
		return
	}

	var session *httpSession
	if sessionID != "" {
		session = s.upsertSession(sessionID)
	}

	isUplinkRequest := true
	if request.Method == http.MethodGet {
		isUplinkRequest = seqStr != ""
	}

	switch {
	case isUplinkRequest && sessionID != "":
		if seqStr == "" {
			s.serveStreamUp(writer, request, session, obfsPaddingAccepted)
		} else {
			s.servePacketUp(writer, request, session, seqStr)
		}
	case request.Method == http.MethodGet || sessionID == "":
		s.serveDownload(writer, request, session, sessionID)
	default:
		s.invalidRequest(writer, request, http.StatusMethodNotAllowed, E.New("unsupported method: ", request.Method))
	}
}

func (s *Server) serveStreamUp(writer http.ResponseWriter, request *http.Request, session *httpSession, obfsPaddingAccepted bool) {
	cfg := s.config
	if cfg.mode != modeAuto && cfg.mode != modeStreamUp {
		s.invalidRequest(writer, request, http.StatusBadRequest, E.New("stream-up mode is not allowed"))
		return
	}
	serverConn := newHTTPServerConn(request.Body, writer)
	err := session.uploadQueue.push(packet{reader: &streamReader{Reader: request.Body, Closer: serverConn}})
	if err != nil {
		s.invalidRequest(writer, request, http.StatusConflict, E.Cause(err, "failed to upload (push reader)"))
		return
	}
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	enableFullDuplex(writer)

	secs := cfg.streamUpServerSecs()
	hasLegacyRefererCompatMarker := request.Header.Get("Referer") != ""
	if (hasLegacyRefererCompatMarker || obfsPaddingAccepted) && secs.To > 0 {
		go func() {
			for {
				_, err := serverConn.Write(bytes.Repeat([]byte{'X'}, int(cfg.xPaddingRange().rand())))
				if err != nil {
					break
				}
				select {
				case <-time.After(time.Duration(secs.rand()) * time.Second):
				case <-serverConn.done:
					return
				}
			}
		}()
	}
	select {
	case <-request.Context().Done():
	case <-serverConn.done:
	}
	serverConn.Close()
}

func (s *Server) servePacketUp(writer http.ResponseWriter, request *http.Request, session *httpSession, seqStr string) {
	cfg := s.config
	if cfg.mode != modeAuto && cfg.mode != modePacketUp {
		s.invalidRequest(writer, request, http.StatusBadRequest, E.New("packet-up mode is not allowed"))
		return
	}
	maxPostBytes := int(cfg.maxEachPostBytes().To)
	dataPlacement := cfg.uplinkDataPlacement

	var headerPayload []byte
	if dataPlacement == placementAuto || dataPlacement == placementHeader {
		var chunks []string
		for i := 0; ; i++ {
			chunk := request.Header.Get(fmt.Sprintf("%s-%d", cfg.uplinkDataKey, i))
			if chunk == "" {
				break
			}
			chunks = append(chunks, chunk)
		}
		var err error
		headerPayload, err = base64.RawURLEncoding.DecodeString(strings.Join(chunks, ""))
		if err != nil {
			s.invalidRequest(writer, request, http.StatusBadRequest, E.Cause(err, "invalid base64 in header payload"))
			return
		}
	}

	var cookiePayload []byte
	if dataPlacement == placementAuto || dataPlacement == placementCookie {
		var chunks []string
		for i := 0; ; i++ {
			cookie, _ := request.Cookie(fmt.Sprintf("%s_%d", cfg.uplinkDataKey, i))
			if cookie == nil {
				break
			}
			chunks = append(chunks, cookie.Value)
		}
		var err error
		cookiePayload, err = base64.RawURLEncoding.DecodeString(strings.Join(chunks, ""))
		if err != nil {
			s.invalidRequest(writer, request, http.StatusBadRequest, E.Cause(err, "invalid base64 in cookie payload"))
			return
		}
	}

	var bodyPayload []byte
	if dataPlacement == placementAuto || dataPlacement == placementBody {
		if request.ContentLength > int64(maxPostBytes) {
			s.invalidRequest(writer, request, http.StatusRequestEntityTooLarge, E.New("too large upload, sc_max_each_post_bytes is ", maxPostBytes, " but the request is ", request.ContentLength, " bytes, raise it on the server to at least the client's value"))
			return
		}
		var err error
		if request.ContentLength > 0 {
			bodyPayload = make([]byte, request.ContentLength)
			_, err = io.ReadFull(request.Body, bodyPayload)
		} else {
			bodyPayload, err = io.ReadAll(io.LimitReader(request.Body, int64(maxPostBytes)+1))
		}
		if err != nil {
			s.invalidRequest(writer, request, http.StatusBadRequest, E.Cause(err, "read body payload"))
			return
		}
	}

	var payload []byte
	switch dataPlacement {
	case placementHeader:
		payload = headerPayload
	case placementCookie:
		payload = cookiePayload
	case placementBody:
		payload = bodyPayload
	default:
		payload = slices.Concat(headerPayload, cookiePayload, bodyPayload)
	}
	if len(payload) > maxPostBytes {
		s.invalidRequest(writer, request, http.StatusRequestEntityTooLarge, E.New("too large upload, sc_max_each_post_bytes is ", maxPostBytes, " but the payload is ", len(payload), " bytes, raise it on the server to at least the client's value"))
		return
	}

	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		s.invalidRequest(writer, request, http.StatusInternalServerError, E.Cause(err, "failed to upload (parse seq)"))
		return
	}
	err = session.uploadQueue.push(packet{payload: payload, seq: seq})
	if err != nil {
		s.invalidRequest(writer, request, http.StatusInternalServerError, E.Cause(err, "failed to upload (push payload)"))
		return
	}
	if len(bodyPayload) == 0 {
		// methods without a body are usually cached by default
		writer.Header().Set("Cache-Control", "no-store")
	}
	writer.WriteHeader(http.StatusOK)
}

func (s *Server) serveDownload(writer http.ResponseWriter, request *http.Request, session *httpSession, sessionID string) {
	cfg := s.config
	if session != nil {
		// after the GET is done the session is finished: stop automatic reaping
		// and delete the session on return
		session.connectedOnce.Do(func() { close(session.fullyConnected) })
		defer s.sessions.Delete(sessionID)
	}

	// magic header instructing nginx + apache not to buffer the response
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.Header().Set("Cache-Control", "no-store")
	if !cfg.noSSEHeader {
		// makes middle boxes consider this an SSE stream and disable buffering
		writer.Header().Set("Content-Type", "text/event-stream")
	}
	writer.WriteHeader(http.StatusOK)
	flusher, isFlusher := writer.(http.Flusher)
	if isFlusher {
		flusher.Flush()
	}
	enableFullDuplex(writer)

	serverConn := newHTTPServerConn(request.Body, writer)
	localAddr, _ := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if localAddr == nil {
		localAddr = placeholderAddr
	}
	remoteAddr := net.Addr(&net.TCPAddr{IP: net.IPv4zero})
	if source := badhttp.SourceAddress(request); source.IsValid() {
		remoteAddr = source.TCPAddr()
	}
	conn := &splitConn{
		writer:     serverConn,
		reader:     serverConn,
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
	}
	if session != nil {
		conn.reader = session.uploadQueue
	}

	s.handler.NewConnectionEx(request.Context(), conn, badhttp.SourceAddress(request), M.Socksaddr{}, N.OnceClose(func(it error) {
		conn.Close()
	}))

	// "A ResponseWriter may not be used after ServeHTTP has returned."
	select {
	case <-request.Context().Done():
	case <-serverConn.done:
	}
	conn.Close()
}

func (s *Server) invalidRequest(writer http.ResponseWriter, request *http.Request, statusCode int, err error) {
	if statusCode > 0 {
		writer.WriteHeader(statusCode)
	}
	s.logger.ErrorContext(request.Context(), E.Cause(err, "process connection from ", request.RemoteAddr))
}

func enableFullDuplex(writer http.ResponseWriter) {
	// HTTP/1.x only; a no-op error on HTTP/2
	_ = http.NewResponseController(writer).EnableFullDuplex()
}

// httpServerConn is the response side of a request as an io.WriteCloser,
// serialising writes and failing them once the stream is done.
type httpServerConn struct {
	access sync.Mutex
	body   io.Reader
	writer http.ResponseWriter
	done   chan struct{}
	closed bool
}

func newHTTPServerConn(body io.Reader, writer http.ResponseWriter) *httpServerConn {
	return &httpServerConn{body: body, writer: writer, done: make(chan struct{})}
}

func (c *httpServerConn) Read(b []byte) (int, error) {
	return c.body.Read(b)
}

func (c *httpServerConn) Write(b []byte) (int, error) {
	c.access.Lock()
	defer c.access.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := c.writer.Write(b)
	if err == nil {
		err = http.NewResponseController(c.writer).Flush()
	}
	return n, err
}

func (c *httpServerConn) Close() error {
	c.access.Lock()
	defer c.access.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	return nil
}
