package v2rayxhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"golang.org/x/net/http2"
)

const (
	h2KeepAlivePeriod  = 45 * time.Second
	h2StreamWindow     = 16 << 20
	h2ConnectionWindow = 64 << 20
	pipeBufferSize     = 512 << 10
	// POSTs in flight at once while packet-up uploads are spread over several connections,
	// below the server reassembly limit (sc_max_buffered_posts, 30 by default)
	maxStripedPosts = 6
	idleConnTimeout = 300 * time.Second
)

var (
	_ adapter.V2RayClientTransport = (*Client)(nil)
	_ adapter.IdleConnectionKeeper = (*Client)(nil)
)

type Client struct {
	up        *endpoint
	down      *endpoint // same as up unless "download" is configured
	mode      string
	closeIdle atomic.Bool // set by SetKeepIdleConnections(false), idle connections are kept by default
	closeOnce sync.Once
}

// endpoint is one server (address, TLS, XHTTP settings) with its own connection pool.
type endpoint struct {
	dialer      N.Dialer
	tlsDialer   tls.Dialer
	serverAddr  M.Socksaddr
	config      *config
	http2       bool
	reality     bool
	scheme      string
	requestHost string
	xmux        *xmuxManager
}

func newEndpoint(dialer N.Dialer, serverAddr M.Socksaddr, cfg *config, tlsConfig tls.Config) (*endpoint, error) {
	e := &endpoint{
		dialer:     dialer,
		serverAddr: serverAddr,
		config:     cfg,
		scheme:     "http",
	}
	e.reality = tlsConfig != nil && tls.IsRealityClient(tlsConfig)
	if tlsConfig != nil {
		e.scheme = "https"
		protos := tlsConfig.NextProtos()
		switch {
		case e.reality:
			// the application protocol is picked by the client after the REALITY
			// handshake, the ClientHello ALPN is left to the uTLS fingerprint
			e.http2 = true
		case len(protos) == 1 && protos[0] == "http/1.1":
		case len(protos) == 1 && protos[0] == "h3":
			return nil, E.New("xhttp: HTTP/3 (alpn h3) is not supported")
		default:
			tlsConfig.SetNextProtos([]string{http2.NextProtoTLS})
			e.http2 = true
		}
		e.tlsDialer = tls.NewDialer(dialer, tlsConfig)
	}

	e.requestHost = cfg.host
	if e.requestHost == "" && tlsConfig != nil {
		e.requestHost = tlsConfig.ServerName()
	}
	if e.requestHost == "" {
		e.requestHost = serverAddr.AddrString()
	}
	if strings.Contains(e.requestHost, ":") && !strings.HasPrefix(e.requestHost, "[") && !isHostPort(e.requestHost) {
		e.requestHost = "[" + e.requestHost + "]"
	}
	e.xmux = newXmuxManager(cfg.xmux, e.newHTTPClient)
	return e, nil
}

func NewClient(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayXHTTPOptions, tlsConfig tls.Config) (*Client, error) {
	cfg, err := newConfig(options)
	if err != nil {
		return nil, err
	}
	up, err := newEndpoint(dialer, serverAddr, cfg, tlsConfig)
	if err != nil {
		return nil, err
	}
	client := &Client{up: up, down: up, mode: cfg.mode}

	if options.Download != nil {
		if cfg.mode == modeStreamOne {
			return nil, E.New("download cannot be used in stream-one mode")
		}
		downloadOptions := options.Download
		if downloadOptions.Download != nil {
			return nil, E.New("download cannot be nested")
		}
		downloadAddr := downloadOptions.ServerOptions.Build()
		if !downloadAddr.IsValid() {
			return nil, E.New("download: missing server")
		}
		downloadConfig, err := newConfig(downloadOptions.V2RayXHTTPOptions)
		if err != nil {
			return nil, E.Cause(err, "download")
		}
		var downloadTLS tls.Config
		if downloadOptions.TLS != nil && downloadOptions.TLS.Enabled {
			logger := log.NewNOPFactory().Logger()
			if factory := service.FromContext[log.Factory](ctx); factory != nil {
				logger = factory.NewLogger("xhttp-download")
			}
			downloadTLS, err = tls.NewClientWithOptions(tls.ClientOptions{
				Context:       ctx,
				Logger:        logger,
				ServerAddress: downloadOptions.Server,
				Options:       *downloadOptions.TLS,
			})
			if err != nil {
				return nil, E.Cause(err, "download tls")
			}
		}
		client.down, err = newEndpoint(dialer, downloadAddr, downloadConfig, downloadTLS)
		if err != nil {
			return nil, E.Cause(err, "download")
		}
	}

	client.mode = cfg.mode
	if client.mode == modeAuto {
		client.mode = modePacketUp
		if up.reality {
			client.mode = modeStreamOne
			if options.Download != nil {
				client.mode = modeStreamUp
			}
		}
	}
	return client, nil
}

func isHostPort(value string) bool {
	_, port, err := net.SplitHostPort(value)
	return err == nil && port != ""
}

func (e *endpoint) dialConn(ctx context.Context) (net.Conn, error) {
	if e.tlsDialer != nil {
		return e.tlsDialer.DialTLSContext(ctx, e.serverAddr)
	}
	return e.dialer.DialContext(ctx, N.NetworkTCP, e.serverAddr)
}

// httpClient is one pooled connection (h2) or connection group (h1).
type httpClient struct {
	dialConn func(ctx context.Context) (net.Conn, error)
	// inFlight counts the bytes of POSTs being sent, postCap is how large a POST this
	// connection is trusted with: it starts small and doubles on every success, like a
	// congestion window, so that a cold connection cannot hold back the whole upload
	inFlight   atomic.Int64
	postCap    atomic.Int64
	ready      atomic.Bool
	warm       atomic.Pointer[warmDial]
	download   *http.Client
	upload     *http.Client
	transports []interface{ CloseIdleConnections() }
	closed     atomic.Bool
}

func (h *httpClient) isClosed() bool {
	return h.closed.Load()
}

func (h *httpClient) isReady() bool {
	return h.ready.Load()
}

func (h *httpClient) outstanding() int64 {
	return h.inFlight.Load()
}

const initialPostCap = 128 << 10

type warmDial struct {
	done chan struct{}
	conn net.Conn
	err  error
}

func (h *httpClient) warmUp() {
	warm := &warmDial{done: make(chan struct{})}
	if !h.warm.CompareAndSwap(nil, warm) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), C.TCPConnectTimeout)
		defer cancel()
		warm.conn, warm.err = h.dialConn(ctx)
		close(warm.done)
		if warm.err == nil {
			h.ready.Store(true)
		}
	}()
}

// dial hands out the pre-dialed connection when there is one.
func (h *httpClient) dial(ctx context.Context) (net.Conn, error) {
	if warm := h.warm.Swap(nil); warm != nil {
		select {
		case <-warm.done:
		case <-ctx.Done():
			go func() {
				<-warm.done
				if warm.conn != nil {
					warm.conn.Close()
				}
			}()
			return nil, ctx.Err()
		}
		if warm.err == nil {
			return warm.conn, nil
		}
	}
	conn, err := h.dialConn(ctx)
	if err == nil {
		h.ready.Store(true)
	}
	return conn, err
}

func (h *httpClient) Close() error {
	if warm := h.warm.Swap(nil); warm != nil {
		go func() {
			<-warm.done
			if warm.conn != nil {
				warm.conn.Close()
			}
		}()
	}
	for _, transport := range h.transports {
		transport.CloseIdleConnections()
	}
	return nil
}

func (c *endpoint) newHTTPClient() xmuxConn {
	h := &httpClient{dialConn: c.dialConn}
	h.postCap.Store(initialPostCap)
	if c.http2 {
		keepAlive := time.Duration(c.config.xmux.hKeepAlivePeriod) * time.Second
		if keepAlive == 0 {
			keepAlive = h2KeepAlivePeriod
		} else if keepAlive < 0 {
			keepAlive = 0
		}
		// the receive windows bound the download speed of a stream to window/RTT, the h2
		// defaults (4 MiB per stream) are too small for long fat links
		t1 := &http.Transport{
			HTTP2: &http.HTTP2Config{
				MaxReceiveBufferPerStream:     h2StreamWindow,
				MaxReceiveBufferPerConnection: h2ConnectionWindow,
			},
		}
		transport, err := http2.ConfigureTransports(t1)
		if err != nil {
			panic(err) // only fails when configured twice
		}
		transport.DialTLSContext = func(ctx context.Context, network, addr string, cfg *tls.STDConfig) (net.Conn, error) {
			return h.dial(ctx)
		}
		transport.IdleConnTimeout = idleConnTimeout
		transport.ReadIdleTimeout = keepAlive
		stdClient := &http.Client{Transport: transport}
		h.download = stdClient
		h.upload = stdClient
		h.transports = []interface{ CloseIdleConnections() }{transport}
		return h
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return h.dial(ctx)
	}
	newTransport := func(keepAlives bool) *http.Transport {
		transport := &http.Transport{
			DialContext:       dial,
			IdleConnTimeout:   idleConnTimeout,
			DisableKeepAlives: !keepAlives,
			// the custom dialer already speaks TLS, never negotiate h2 here
			TLSNextProto: make(map[string]func(string, *tls.STDConn) http.RoundTripper),
		}
		if c.tlsDialer != nil {
			transport.DialTLSContext = dial
		}
		return transport
	}
	// chunked transfer download with KeepAlives is buggy with a custom dial
	// context, so the long-lived GET gets its own transport without keep-alives
	download := newTransport(false)
	upload := newTransport(true)
	h.download = &http.Client{Transport: download}
	h.upload = &http.Client{Transport: upload}
	h.transports = []interface{ CloseIdleConnections() }{download, upload}
	return h
}

func (c *endpoint) urlFor(path string) string {
	requestURL := url.URL{
		Scheme:   c.scheme,
		Host:     c.requestHost,
		Path:     path,
		RawQuery: c.config.normalizedQuery(),
	}
	return requestURL.String()
}

func (h *httpClient) openStream(ctx context.Context, cfg *config, requestURL string, sessionID string, body io.Reader, uploadOnly bool) (io.ReadCloser, net.Addr, net.Addr, error) {
	gotConn := make(chan struct{})
	var gotConnOnce sync.Once
	var remoteAddr, localAddr net.Addr
	markGotConn := func() { gotConnOnce.Do(func() { close(gotConn) }) }
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			remoteAddr = info.Conn.RemoteAddr()
			localAddr = info.Conn.LocalAddr()
			markGotConn()
		},
	}
	method := http.MethodGet
	if body != nil {
		method = cfg.uplinkHTTPMethod
	}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.WithoutCancel(ctx), trace), method, requestURL, body)
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.fillStreamRequest(request, sessionID)

	reader := newWaitReadCloser()
	go func() {
		response, err := h.download.Do(request)
		if err != nil {
			if !uploadOnly {
				h.closed.Store(true)
			}
			markGotConn()
			err = E.Cause(err, method, " ", requestURL)
			closeBody(body, err)
			reader.set(nil, err)
			return
		}
		if response.StatusCode != http.StatusOK || uploadOnly {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = E.New("xhttp: unexpected status: ", response.Status)
				closeBody(body, err)
				reader.set(nil, err)
			} else {
				closeBody(body, nil)
				reader.set(nil, io.EOF)
			}
			return
		}
		reader.set(response.Body, nil)
	}()

	select {
	case <-gotConn:
	case <-ctx.Done():
		reader.Close()
		return nil, nil, nil, ctx.Err()
	}
	return reader, remoteAddr, localAddr, nil
}

func closeBody(body io.Reader, cause error) {
	if pipeReader, isPipe := body.(interface{ CloseWithError(error) error }); isPipe && cause != nil {
		// writers see the real failure instead of "closed pipe"
		pipeReader.CloseWithError(cause)
		return
	}
	if closer, isCloser := body.(io.Closer); isCloser {
		closer.Close()
	}
}

func (h *httpClient) postPacket(ctx context.Context, cfg *config, requestURL string, sessionID string, seq string, payload []byte) error {
	request, err := http.NewRequestWithContext(context.WithoutCancel(ctx), cfg.uplinkHTTPMethod, requestURL, nil)
	if err != nil {
		return err
	}
	cfg.fillPacketRequest(request, sessionID, seq, payload)
	response, err := h.upload.Do(request)
	if err != nil {
		h.closed.Store(true)
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return E.New("bad status code: ", response.Status)
	}
	return nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	up, down := c.up, c.down
	cfg := up.config
	sessionID := ""
	if c.mode != modeStreamOne {
		sessionID = cfg.generateSessionID()
	}
	upMux := up.xmux.getClient()
	upMux.addRunning()
	downMux := upMux
	if down != up {
		downMux = down.xmux.getClient()
		downMux.addRunning()
	}
	upClient := upMux.conn.(*httpClient)
	downClient := downMux.conn.(*httpClient)

	keepSession := adapter.KeepSessionFromContext(ctx)
	var closed atomic.Bool
	onClose := func() {
		if !closed.CompareAndSwap(false, true) {
			return
		}
		upMux.doneRunning()
		if downMux != upMux {
			downMux.doneRunning()
		}
		if c.closeIdle.Load() && !keepSession {
			c.CloseIdleConnections()
		}
	}

	upURL := up.urlFor(cfg.normalizedPath())

	if c.mode == modeStreamOne {
		upMux.leftRequests.Add(-1)
		pipeReader, pipeWriter := newBufferedPipe(pipeBufferSize)
		reader, remoteAddr, localAddr, err := upClient.openStream(ctx, cfg, upURL, "", pipeReader, false)
		if err != nil {
			onClose()
			return nil, err
		}
		return &splitConn{
			writer:     pipeWriter,
			reader:     reader,
			remoteAddr: remoteAddr,
			localAddr:  localAddr,
			onClose:    onClose,
		}, nil
	}

	// stream-up and packet-up: download over a GET, upload over POSTs
	downMux.leftRequests.Add(-1)
	downURL := down.urlFor(down.config.normalizedPath())
	reader, remoteAddr, localAddr, err := downClient.openStream(ctx, down.config, downURL, sessionID, nil, false)
	if err != nil {
		onClose()
		return nil, err
	}
	conn := &splitConn{
		reader:     reader,
		remoteAddr: remoteAddr,
		localAddr:  localAddr,
		onClose:    onClose,
	}

	if c.mode == modeStreamUp {
		upMux.leftRequests.Add(-1)
		pipeReader, pipeWriter := newBufferedPipe(pipeBufferSize)
		_, _, _, err = upClient.openStream(ctx, cfg, upURL, sessionID, pipeReader, true)
		if err != nil {
			reader.Close()
			onClose()
			return nil, err
		}
		conn.writer = pipeWriter
		return conn, nil
	}

	maxUploadSize := int(cfg.maxEachPostBytes().rand())
	if maxUploadSize <= 0 {
		reader.Close()
		onClose()
		return nil, E.New("sc_max_each_post_bytes should be bigger than 0")
	}
	buffer := newUploadBuffer(maxUploadSize)
	uploadDone := make(chan struct{})
	conn.writer = &packetWriter{uploadBuffer: buffer, uploadDone: uploadDone}
	// closing the conn only closes the writer first, the download side follows
	// once the pending POSTs went out
	conn.reader = &delayedCloser{ReadCloser: reader, wait: uploadDone}
	go up.uploadLoop(ctx, upMux, upClient, buffer, maxUploadSize, upURL, sessionID, reader, uploadDone)
	return conn, nil
}

type delayedCloser struct {
	io.ReadCloser
	wait <-chan struct{}
}

func (d *delayedCloser) Close() error {
	go func() {
		select {
		case <-d.wait:
		case <-time.After(5 * time.Second):
		}
		d.ReadCloser.Close()
	}()
	return nil
}

type packetWriter struct {
	*uploadBuffer
	uploadDone chan struct{}
}

func (c *endpoint) uploadLoop(ctx context.Context, mux *xmuxClient, hc *httpClient, buffer *uploadBuffer, maxUploadSize int, requestURL string, sessionID string, download io.Closer, done chan struct{}) {
	defer close(done)
	var (
		seq       int64
		lastWrite time.Time
		wg        sync.WaitGroup
	)
	defer wg.Wait()
	var rotated []*xmuxClient
	inflight := make(chan struct{}, maxStripedPosts)
	stripedBefore := false
	defer func() {
		for _, client := range rotated {
			client.doneRunning()
		}
	}()
	minInterval := c.config.minPostsIntervalMs()
	for {
		// spread the POSTs over the established connections: each has its own flow control
		// window and congestion state, one connection is capped at window / RTT. A connection
		// that has not proven itself yet only gets small POSTs.
		targetMux := c.xmux.uploadClient(mux)
		striped := targetMux != mux
		target := hc
		size := maxUploadSize
		if striped {
			target = targetMux.conn.(*httpClient)
			size = int(min(int64(size), target.postCap.Load()))
		}
		chunk, ok := buffer.take(size)
		if !ok {
			return
		}
		seqStr := strconv.FormatInt(seq, 10)
		seq++
		if seq == 2 {
			// the upload is more than a single POST: bring up more connections to spread it over
			c.xmux.grow()
		}

		if minInterval.From > 0 {
			time.Sleep(time.Duration(minInterval.rand())*time.Millisecond - time.Since(lastWrite))
		}
		lastWrite = time.Now()

		if striped {
			targetMux.leftRequests.Add(-1)
		} else if mux.leftRequests.Add(-1) <= 0 || (!mux.unreusableAt.IsZero() && lastWrite.After(mux.unreusableAt)) {
			// the pooled connection reached its request budget, continue on a fresh one
			next := c.xmux.getClient()
			hc = next.conn.(*httpClient)
			next.addRunning()
			rotated = append(rotated, next)
			mux = next
			target = hc
		}

		wrote := make(chan struct{})
		var wroteOnce sync.Once
		markWrote := func() { wroteOnce.Do(func() { close(wrote) }) }
		postCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			WroteRequest: func(httptrace.WroteRequestInfo) { markWrote() },
		})
		limited := striped || stripedBefore
		if limited {
			// bounded number of POSTs in flight instead of waiting for each write: a connection
			// whose window is full must not hold back the others. The server reorders by seq.
			inflight <- struct{}{}
		}
		stripedBefore = striped
		wg.Add(1)
		target.inFlight.Add(int64(len(chunk)))
		go func(target *httpClient, chunk []byte, seqStr string, limited bool) {
			defer wg.Done()
			if limited {
				defer func() { <-inflight }()
			}
			err := target.postPacket(postCtx, c.config, requestURL, sessionID, seqStr, chunk)
			target.inFlight.Add(-int64(len(chunk)))
			markWrote()
			if err != nil {
				buffer.abort(err)
				download.Close()
				return
			}
			if current := target.postCap.Load(); current < int64(maxUploadSize) {
				target.postCap.CompareAndSwap(current, min(current*2, int64(maxUploadSize)))
			}
		}(target, chunk, seqStr, limited)
		if !limited {
			<-wrote
		}
	}
}

func (c *Client) CloseIdleConnections() {
	c.up.xmux.closeIdle()
	if c.down != c.up {
		c.down.xmux.closeIdle()
	}
}

func (c *Client) SetKeepIdleConnections(keep bool) {
	c.closeIdle.Store(!keep)
	if !keep {
		c.CloseIdleConnections()
	}
}

func (c *Client) Close() error {
	c.up.xmux.Close()
	if c.down != c.up {
		c.down.xmux.Close()
	}
	return nil
}
