package option

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badoption"
)

// V2RayXHTTPOptions is the sing-box counterpart of Xray-core's "xhttpSettings"
// (a.k.a. SplitHTTP). Field semantics and defaults follow Xray-core.
type V2RayXHTTPOptions struct {
	Host    string               `json:"host,omitempty"`
	Path    string               `json:"path,omitempty"`
	Mode    string               `json:"mode,omitempty"` // auto, packet-up, stream-up, stream-one
	Headers badoption.HTTPHeader `json:"headers,omitempty"`

	XPaddingBytes     *XHTTPRange `json:"x_padding_bytes,omitempty"`
	XPaddingObfsMode  bool        `json:"x_padding_obfs_mode,omitempty"`
	XPaddingKey       string      `json:"x_padding_key,omitempty"`
	XPaddingHeader    string      `json:"x_padding_header,omitempty"`
	XPaddingPlacement string      `json:"x_padding_placement,omitempty"` // queryInHeader, header, cookie, query
	XPaddingMethod    string      `json:"x_padding_method,omitempty"`    // repeat-x, tokenish

	UplinkHTTPMethod    string      `json:"uplink_http_method,omitempty"`
	SessionIDPlacement  string      `json:"session_id_placement,omitempty"` // path, header, cookie, query
	SessionIDKey        string      `json:"session_id_key,omitempty"`
	SessionIDTable      string      `json:"session_id_table,omitempty"`
	SessionIDLength     *XHTTPRange `json:"session_id_length,omitempty"`
	SeqPlacement        string      `json:"seq_placement,omitempty"`
	SeqKey              string      `json:"seq_key,omitempty"`
	UplinkDataPlacement string      `json:"uplink_data_placement,omitempty"` // auto, body, header, cookie
	UplinkDataKey       string      `json:"uplink_data_key,omitempty"`
	UplinkChunkSize     *XHTTPRange `json:"uplink_chunk_size,omitempty"`

	NoGRPCHeader bool `json:"no_grpc_header,omitempty"`
	NoSSEHeader  bool `json:"no_sse_header,omitempty"`

	ScMaxEachPostBytes   *XHTTPRange `json:"sc_max_each_post_bytes,omitempty"`
	ScMinPostsIntervalMs *XHTTPRange `json:"sc_min_posts_interval_ms,omitempty"`
	ScMaxBufferedPosts   int64       `json:"sc_max_buffered_posts,omitempty"`
	ScStreamUpServerSecs *XHTTPRange `json:"sc_stream_up_server_secs,omitempty"`
	ServerMaxHeaderBytes int32       `json:"server_max_header_bytes,omitempty"`

	Xmux *V2RayXHTTPXmuxOptions `json:"xmux,omitempty"`

	// Download moves the download (GET) side to another server/TLS/XHTTP configuration,
	// Xray-core's "downloadSettings". Not allowed in stream-one mode.
	Download *V2RayXHTTPDownloadOptions `json:"download,omitempty"`
}

// V2RayXHTTPDownloadOptions is a complete second endpoint used only for the download side.
type V2RayXHTTPDownloadOptions struct {
	ServerOptions
	OutboundTLSOptionsContainer
	V2RayXHTTPOptions
}

type V2RayXHTTPXmuxOptions struct {
	MaxConcurrency   *XHTTPRange `json:"max_concurrency,omitempty"`
	MaxConnections   *XHTTPRange `json:"max_connections,omitempty"`
	CMaxReuseTimes   *XHTTPRange `json:"c_max_reuse_times,omitempty"`
	HMaxRequestTimes *XHTTPRange `json:"h_max_request_times,omitempty"`
	HMaxReusableSecs *XHTTPRange `json:"h_max_reusable_secs,omitempty"`
	HKeepAlivePeriod int64       `json:"h_keep_alive_period,omitempty"`
}

// XHTTPRange is an inclusive integer range. JSON accepts 5, "5", "1-5" or {"from":1,"to":5}.
type XHTTPRange struct {
	From int32 `json:"from"`
	To   int32 `json:"to"`
}

func (r XHTTPRange) MarshalJSON() ([]byte, error) {
	if r.From == r.To {
		return json.Marshal(r.From)
	}
	return json.Marshal(strconv.Itoa(int(r.From)) + "-" + strconv.Itoa(int(r.To)))
}

func (r *XHTTPRange) UnmarshalJSON(content []byte) error {
	content = bytes.TrimSpace(content)
	if len(content) > 0 && content[0] == '{' {
		type plain XHTTPRange
		return json.Unmarshal(content, (*plain)(r))
	}
	var text string
	if len(content) > 0 && content[0] == '"' {
		err := json.Unmarshal(content, &text)
		if err != nil {
			return err
		}
	} else {
		text = string(content)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		*r = XHTTPRange{}
		return nil
	}
	fromText, toText, isRange := strings.Cut(text[1:], "-")
	fromText = text[:1] + fromText // keep a leading minus sign
	if !isRange {
		toText = fromText
	}
	from, err := strconv.ParseInt(strings.TrimSpace(fromText), 10, 32)
	if err != nil {
		return E.Cause(err, "invalid range: ", text)
	}
	to, err := strconv.ParseInt(strings.TrimSpace(toText), 10, 32)
	if err != nil {
		return E.Cause(err, "invalid range: ", text)
	}
	if to < from {
		from, to = to, from
	}
	r.From, r.To = int32(from), int32(to)
	return nil
}

func (r XHTTPRange) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return schema.AnyOf(schema.IntegerNode(), schema.StringNode(), schema.LooseObject()), nil
}
