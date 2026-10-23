package vlessenc_test

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"testing"

	"github.com/sagernet/sing-box/common/vlessenc"

	"github.com/stretchr/testify/require"
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func exchange(t *testing.T, client *vlessenc.ClientInstance, server *vlessenc.ServerInstance, size int) {
	// real sockets: the handshake relies on buffered transports, net.Pipe would deadlock
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	clientRaw, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	serverRaw := <-accepted
	defer clientRaw.Close()
	defer serverRaw.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, err := server.Handshake(serverRaw, nil)
		if err != nil {
			serverDone <- err
			return
		}
		// echo
		_, err = io.Copy(conn, conn)
		serverDone <- err
	}()
	conn, err := client.Handshake(clientRaw)
	require.NoError(t, err)
	payload := make([]byte, size)
	rand.Read(payload)
	go conn.Write(payload)
	received := make([]byte, size)
	_, err = io.ReadFull(conn, received)
	require.NoError(t, err)
	require.Equal(t, payload, received)
	clientRaw.Close()
}

func TestHandshake(t *testing.T) {
	x25519Key, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	seed := make([]byte, mlkem.SeedSize)
	rand.Read(seed)
	mlkemKey, err := mlkem.NewDecapsulationKey768(seed)
	require.NoError(t, err)

	keys := map[string][2]string{
		"x25519": {b64(x25519Key.PublicKey().Bytes()), b64(x25519Key.Bytes())},
		"mlkem":  {b64(mlkemKey.EncapsulationKey().Bytes()), b64(seed)},
	}
	for name, key := range keys {
		for _, mode := range []string{"native", "xorpub", "random"} {
			for _, rtt := range []string{"1rtt", "0rtt"} {
				t.Run(name+"/"+mode+"/"+rtt, func(t *testing.T) {
					server, err := vlessenc.NewServer("mlkem768x25519plus." + mode + ".600s." + key[1])
					require.NoError(t, err)
					defer server.Close()
					client, err := vlessenc.NewClient("mlkem768x25519plus." + mode + "." + rtt + "." + key[0])
					require.NoError(t, err)
					// the first connection does the full handshake, later ones reuse the 0-RTT ticket
					for i := 0; i < 3; i++ {
						exchange(t, client, server, 100000)
					}
				})
			}
		}
	}
}

func TestParse(t *testing.T) {
	_, err := vlessenc.NewClient("none")
	require.Error(t, err)
	_, err = vlessenc.NewClient("mlkem768x25519plus.native.0rtt.short")
	require.Error(t, err)
	_, err = vlessenc.NewServer("mlkem768x25519plus.native.600s.short")
	require.Error(t, err)
	// padding spec before the key
	x25519Key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	client, err := vlessenc.NewClient("mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333." + b64(x25519Key.PublicKey().Bytes()))
	require.NoError(t, err)
	require.Len(t, client.PaddingLens, 2)
}
