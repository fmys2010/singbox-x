// Mock environment for XHTTP+REALITY interop tests:
//   - a TLS 1.3 server on :19443 acting as the REALITY "dest" site
//   - an HTTP server on :19080: GET /blob?n=<bytes> (deterministic stream), POST /up (returns sha256 of body)
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	mrand "math/rand"
	"net/http"
	"strconv"
	"time"
)

func main() {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "www.example.com"},
		DNSNames:     []string{"www.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	destMux := http.NewServeMux()
	destMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from the real site\n") })
	dest := &http.Server{
		Addr:      "127.0.0.1:19443",
		Handler:   destMux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}},
	}
	go func() { log.Fatal(dest.ListenAndServeTLS("", "")) }()

	go func() { // UDP echo on :19090
		pc, err := net.ListenPacket("udp", "127.0.0.1:19090")
		if err != nil {
			log.Fatal(err)
		}
		b := make([]byte, 65536)
		for {
			n, addr, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			pc.WriteTo(b[:n], addr)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/blob", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
		rng := mrand.New(mrand.NewSource(n))
		h := sha256.New()
		buf := make([]byte, 32*1024)
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		for left := n; left > 0; {
			k := int64(len(buf))
			if left < k {
				k = left
			}
			rng.Read(buf[:k])
			h.Write(buf[:k])
			w.Write(buf[:k])
			left -= k
		}
		fmt.Printf("blob n=%d sha256=%s\n", n, hex.EncodeToString(h.Sum(nil)))
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		n, _ := io.Copy(h, r.Body)
		fmt.Printf("up n=%d sha256=%s\n", n, hex.EncodeToString(h.Sum(nil)))
		fmt.Fprintf(w, "%d %s\n", n, hex.EncodeToString(h.Sum(nil)))
	})
	// the same handlers over TLS, so XTLS Vision sees a TLS stream to switch to direct copy
	go func() {
		srv := &http.Server{Addr: "127.0.0.1:19081", Handler: mux, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
		log.Fatal(srv.ListenAndServeTLS("", ""))
	}()
	log.Fatal(http.ListenAndServe("127.0.0.1:19080", mux))
}
