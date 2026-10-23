package v2rayxhttp_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2rayxhttp"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type echoHandler struct {
	connections sync.WaitGroup
}

func (h *echoHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	h.connections.Add(1)
	go func() {
		defer h.connections.Done()
		io.Copy(conn, conn)
		conn.Close()
		if onClose != nil {
			onClose(nil)
		}
	}()
}

func selfSignedCertificate(t *testing.T, serverName string) (certPEM, keyPEM string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return
}

type testServer struct {
	addr    M.Socksaddr
	server  *v2rayxhttp.Server
	handler *echoHandler
	tlsConf tls.Config
}

// startServer starts an XHTTP server on loopback. With useTLS the transport is
// served over TLS (h2 negotiated through ALPN).
func startServer(t *testing.T, serverOptions option.V2RayXHTTPOptions, useTLS bool) *testServer {
	ctx := context.Background()
	logger := log.NewNOPFactory().Logger()
	var serverTLS tls.ServerConfig
	var clientTLS tls.Config
	if useTLS {
		certPEM, keyPEM := selfSignedCertificate(t, "example.org")
		var err error
		serverTLS, err = tls.NewServer(ctx, logger, option.InboundTLSOptions{
			Enabled:     true,
			ServerName:  "example.org",
			Certificate: strings.Split(certPEM, "\n"),
			Key:         strings.Split(keyPEM, "\n"),
		})
		require.NoError(t, err)
		clientTLS, err = tls.NewClient(ctx, logger, "example.org", option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "example.org",
			Certificate: strings.Split(certPEM, "\n"),
		})
		require.NoError(t, err)
	}
	handler := new(echoHandler)
	server, err := v2rayxhttp.NewServer(ctx, logger, serverOptions, serverTLS, handler)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return &testServer{
		addr:    M.SocksaddrFromNet(listener.Addr()),
		server:  server,
		handler: handler,
		tlsConf: clientTLS,
	}
}

func (s *testServer) client(t *testing.T, options option.V2RayXHTTPOptions) *v2rayxhttp.Client {
	client, err := v2rayxhttp.NewClient(context.Background(), N.SystemDialer, s.addr, options, s.tlsConf)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func echoRoundTrip(t *testing.T, client *v2rayxhttp.Client, size int) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	require.NoError(t, err)
	defer conn.Close()

	payload := make([]byte, size)
	mrand.New(mrand.NewSource(int64(size))).Read(payload)

	writeDone := make(chan error, 1)
	go func() {
		// write in uneven pieces to exercise batching
		rng := mrand.New(mrand.NewSource(1))
		for rest := payload; len(rest) > 0; {
			n := min(len(rest), 1+rng.Intn(70000))
			if _, err := conn.Write(rest[:n]); err != nil {
				writeDone <- err
				return
			}
			rest = rest[n:]
		}
		writeDone <- nil
	}()

	received := make([]byte, size)
	_, err = io.ReadFull(conn, received)
	require.NoError(t, err)
	require.NoError(t, <-writeDone)
	require.True(t, bytes.Equal(payload, received), "echo mismatch")
}

var modes = []string{"packet-up", "stream-up", "stream-one", "auto"}

func TestEchoPlain(t *testing.T) {
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			options := option.V2RayXHTTPOptions{Mode: mode, Path: "/xhttp"}
			server := startServer(t, options, false)
			client := server.client(t, options)
			echoRoundTrip(t, client, 1)
			echoRoundTrip(t, client, 4096)
			echoRoundTrip(t, client, 3<<20)
		})
	}
}

func TestEchoTLS(t *testing.T) {
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			options := option.V2RayXHTTPOptions{Mode: mode, Path: "/xhttp"}
			server := startServer(t, options, true)
			client := server.client(t, options)
			echoRoundTrip(t, client, 1)
			echoRoundTrip(t, client, 3<<20)
		})
	}
}

func TestPacketUpSmallPosts(t *testing.T) {
	// many small, possibly reordered POSTs
	for _, useTLS := range []bool{false, true} {
		options := option.V2RayXHTTPOptions{
			Mode:                 "packet-up",
			ScMaxEachPostBytes:   &option.XHTTPRange{From: 1000, To: 1000},
			ScMinPostsIntervalMs: &option.XHTTPRange{From: 1, To: 1},
		}
		server := startServer(t, options, useTLS)
		client := server.client(t, options)
		echoRoundTrip(t, client, 200000)
	}
}

func TestConcurrentSessions(t *testing.T) {
	for _, mode := range []string{"packet-up", "stream-up", "stream-one"} {
		for _, useTLS := range []bool{false, true} {
			options := option.V2RayXHTTPOptions{Mode: mode}
			server := startServer(t, options, useTLS)
			client := server.client(t, options)
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					echoRoundTrip(t, client, 10000+i*5000)
				}(i)
			}
			wg.Wait()
		}
	}
}

func TestPlacementVariants(t *testing.T) {
	variants := map[string]option.V2RayXHTTPOptions{
		"session-header-seq-query": {
			Mode: "packet-up", SessionIDPlacement: "header", SeqPlacement: "query",
		},
		"session-cookie-seq-header": {
			Mode: "packet-up", SessionIDPlacement: "cookie", SeqPlacement: "header",
		},
		"session-query-custom-keys": {
			Mode: "packet-up", SessionIDPlacement: "query", SessionIDKey: "sid", SeqPlacement: "query", SeqKey: "n",
		},
		"session-table": {
			Mode: "packet-up", SessionIDTable: "hex", SessionIDLength: &option.XHTTPRange{From: 16, To: 24},
		},
		"uplink-header": {
			Mode: "packet-up", UplinkDataPlacement: "header", UplinkChunkSize: &option.XHTTPRange{From: 2000, To: 2500},
			ScMaxEachPostBytes: &option.XHTTPRange{From: 4000, To: 4000},
		},
		"uplink-cookie": {
			Mode: "packet-up", UplinkDataPlacement: "cookie", UplinkChunkSize: &option.XHTTPRange{From: 2000, To: 2500},
			ScMaxEachPostBytes: &option.XHTTPRange{From: 4000, To: 4000},
		},
		"uplink-get": {
			Mode: "packet-up", UplinkHTTPMethod: "GET", UplinkDataPlacement: "header",
			ScMaxEachPostBytes: &option.XHTTPRange{From: 4000, To: 4000},
		},
		"padding-obfs-cookie": {
			Mode: "stream-up", XPaddingObfsMode: true, XPaddingPlacement: "cookie", XPaddingKey: "pk",
		},
		"padding-obfs-query": {
			Mode: "packet-up", XPaddingObfsMode: true, XPaddingPlacement: "query", XPaddingKey: "pk",
		},
		"padding-obfs-header-tokenish": {
			Mode: "packet-up", XPaddingObfsMode: true, XPaddingPlacement: "header", XPaddingHeader: "X-Foo", XPaddingMethod: "tokenish",
		},
		"padding-obfs-queryinheader": {
			Mode: "stream-one", XPaddingObfsMode: true, XPaddingPlacement: "queryInHeader", XPaddingHeader: "X-Ref", XPaddingKey: "q",
			XPaddingBytes: &option.XHTTPRange{From: 200, To: 400},
		},
		"no-grpc-no-sse": {
			Mode: "stream-up", NoGRPCHeader: true, NoSSEHeader: true,
		},
		"custom-headers-host": {
			Mode: "packet-up", Host: "front.example.com", Path: "/a/b?token=1",
			Headers: badoption.HTTPHeader{"User-Agent": {"firefox"}},
		},
	}
	for name, options := range variants {
		t.Run(name, func(t *testing.T) {
			for _, useTLS := range []bool{false, true} {
				server := startServer(t, options, useTLS)
				client := server.client(t, options)
				size := 100000
				if options.UplinkHTTPMethod == "GET" {
					size = 30000
				}
				echoRoundTrip(t, client, size)
			}
		})
	}
}

func TestServerRejectsBadRequests(t *testing.T) {
	options := option.V2RayXHTTPOptions{Path: "/secret", Host: "front.example.com"}
	server := startServer(t, options, false)
	base := "http://" + server.addr.String()

	do := func(method, path, host string, headers map[string]string) int {
		request, err := http.NewRequest(method, base+path, nil)
		require.NoError(t, err)
		if host != "" {
			request.Host = host
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		return response.StatusCode
	}

	padding := strings.Repeat("X", 300)
	require.Equal(t, http.StatusNotFound, do("GET", "/other/abc", "front.example.com", nil), "wrong path")
	require.Equal(t, http.StatusNotFound, do("GET", "/secret/abc", "evil.example.com", nil), "wrong host")
	require.Equal(t, http.StatusBadRequest, do("GET", "/secret/abc", "front.example.com", nil), "missing padding")
	require.Equal(t, http.StatusBadRequest, do("GET", "/secret/abc", "front.example.com", map[string]string{
		"Referer": "http://front.example.com/secret/abc?x_padding=" + strings.Repeat("X", 5),
	}), "short padding")
	// a valid probe with padding but an unknown POST seq must not crash anything
	status := do("POST", "/secret/abc/notanumber", "front.example.com", map[string]string{
		"Referer": "http://front.example.com/secret/abc?x_padding=" + padding,
	})
	require.Equal(t, http.StatusInternalServerError, status)
}

func TestModeEnforcement(t *testing.T) {
	// a packet-up only server refuses stream-one
	serverOptions := option.V2RayXHTTPOptions{Mode: "packet-up"}
	server := startServer(t, serverOptions, false)
	client := server.client(t, option.V2RayXHTTPOptions{Mode: "stream-one"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx)
	require.NoError(t, err)
	defer conn.Close()
	conn.Write([]byte("hello"))
	_, err = conn.Read(make([]byte, 16))
	require.Error(t, err)
}

func TestInvalidOptions(t *testing.T) {
	for name, options := range map[string]option.V2RayXHTTPOptions{
		"bad-mode":      {Mode: "nope"},
		"host-header":   {Headers: badoption.HTTPHeader{"Host": {"x"}}},
		"get-stream":    {Mode: "stream-up", UplinkHTTPMethod: "GET"},
		"header-stream": {Mode: "stream-up", UplinkDataPlacement: "header"},
		"bad-padding":   {XPaddingPlacement: "weird"},
		"xmux-conflict": {Xmux: &option.V2RayXHTTPXmuxOptions{
			MaxConcurrency: &option.XHTTPRange{From: 1, To: 2},
			MaxConnections: &option.XHTTPRange{From: 1, To: 2},
		}},
	} {
		_, err := v2rayxhttp.NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:1"), options, nil)
		require.Error(t, err, name)
	}
}

func TestOptionsJSON(t *testing.T) {
	var transport option.V2RayTransportOptions
	require.NoError(t, json.Unmarshal([]byte(`{
		"type": "xhttp",
		"host": "example.com",
		"path": "/x",
		"mode": "stream-up",
		"x_padding_bytes": "100-500",
		"sc_max_each_post_bytes": 500000,
		"session_id_length": {"from": 8, "to": 12},
		"xmux": {"max_concurrency": "16-32", "h_keep_alive_period": 30}
	}`), &transport))
	require.Equal(t, "xhttp", transport.Type)
	require.Equal(t, "stream-up", transport.XHTTPOptions.Mode)
	require.Equal(t, option.XHTTPRange{From: 100, To: 500}, *transport.XHTTPOptions.XPaddingBytes)
	require.Equal(t, option.XHTTPRange{From: 500000, To: 500000}, *transport.XHTTPOptions.ScMaxEachPostBytes)
	require.Equal(t, option.XHTTPRange{From: 8, To: 12}, *transport.XHTTPOptions.SessionIDLength)
	require.Equal(t, option.XHTTPRange{From: 16, To: 32}, *transport.XHTTPOptions.Xmux.MaxConcurrency)
	encoded, err := json.Marshal(transport)
	require.NoError(t, err)
	var decoded option.V2RayTransportOptions
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, transport, decoded)
}

type countingListener struct {
	net.Listener
	accepted chan struct{}
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return conn, err
}

func TestDownloadSettings(t *testing.T) {
	for _, mode := range []string{"auto", "packet-up", "stream-up"} {
		for _, useTLS := range []bool{false, true} {
			t.Run(mode, func(t *testing.T) {
				ctx := context.Background()
				logger := log.NewNOPFactory().Logger()
				var serverTLS tls.ServerConfig
				var clientTLS option.OutboundTLSOptionsContainer
				var mainTLS tls.Config
				if useTLS {
					certPEM, keyPEM := selfSignedCertificate(t, "example.org")
					var err error
					serverTLS, err = tls.NewServer(ctx, logger, option.InboundTLSOptions{
						Enabled: true, ServerName: "example.org",
						Certificate: strings.Split(certPEM, "\n"), Key: strings.Split(keyPEM, "\n"),
					})
					require.NoError(t, err)
					tlsOptions := option.OutboundTLSOptions{Enabled: true, ServerName: "example.org", Certificate: strings.Split(certPEM, "\n")}
					clientTLS.TLS = &tlsOptions
					mainTLS, err = tls.NewClient(ctx, logger, "example.org", tlsOptions)
					require.NoError(t, err)
				}
				serverOptions := option.V2RayXHTTPOptions{Path: "/x"}
				server, err := v2rayxhttp.NewServer(ctx, logger, serverOptions, serverTLS, new(echoHandler))
				require.NoError(t, err)
				t.Cleanup(func() { server.Close() })
				var listeners [2]*countingListener
				for i := range listeners {
					raw, err := net.Listen("tcp", "127.0.0.1:0")
					require.NoError(t, err)
					listeners[i] = &countingListener{Listener: raw, accepted: make(chan struct{}, 64)}
					go server.Serve(listeners[i])
				}

				clientOptions := option.V2RayXHTTPOptions{
					Mode: mode, Path: "/x",
					Download: &option.V2RayXHTTPDownloadOptions{
						ServerOptions:               option.ServerOptions{Server: "127.0.0.1", ServerPort: uint16(listeners[1].Addr().(*net.TCPAddr).Port)},
						OutboundTLSOptionsContainer: clientTLS,
						V2RayXHTTPOptions:           option.V2RayXHTTPOptions{Path: "/x"},
					},
				}
				client, err := v2rayxhttp.NewClient(ctx, N.SystemDialer, M.SocksaddrFromNet(listeners[0].Addr()), clientOptions, mainTLS)
				require.NoError(t, err)
				t.Cleanup(func() { client.Close() })
				echoRoundTrip(t, client, 500000)
				require.NotEmpty(t, listeners[0].accepted, "upload endpoint saw no connection")
				require.NotEmpty(t, listeners[1].accepted, "download endpoint saw no connection")
			})
		}
	}
}

func TestDownloadSettingsRejected(t *testing.T) {
	download := &option.V2RayXHTTPDownloadOptions{ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: 1}}
	_, err := v2rayxhttp.NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:1"), option.V2RayXHTTPOptions{Mode: "stream-one", Download: download}, nil)
	require.Error(t, err)
	_, err = v2rayxhttp.NewClient(context.Background(), N.SystemDialer, M.ParseSocksaddr("127.0.0.1:1"), option.V2RayXHTTPOptions{Download: &option.V2RayXHTTPDownloadOptions{}}, nil)
	require.Error(t, err)
}
