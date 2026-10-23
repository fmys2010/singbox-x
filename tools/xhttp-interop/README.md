# XHTTP interop harness

Mock-environment test of the sing-box XHTTP transport over VLESS + REALITY (with and without VLESS encryption and
Vision), against a real Xray-core binary acting as the peer in both directions. No external network is needed: the
REALITY "dest" site and the data server run on loopback (`helper/`).

You need an Xray-core binary (any recent release). It is only a test peer, sing-box does not use or embed it.

```bash
go build -tags with_utls,with_quic,with_gvisor -o .sb-bin/sing-box ./cmd/sing-box
(cd tools/xhttp-interop/helper && go build -o helper .)
# put an Xray-core binary at .xray-bin/xray (or .xray-bin/xray.exe), or point XRAY at it
python tools/xhttp-interop/interop.py            # all cases
python tools/xhttp-interop/interop.py obfs xmux  # name filter
```

Environment: `SINGBOX` and `XRAY` override the binary paths. Proxy variables (`NO_PROXY`, `HTTP_PROXY`, ...) are
cleared for the curl calls, otherwise `NO_PROXY=127.0.0.1` makes curl bypass the SOCKS proxy and the test passes
without touching the tunnel.

Each case starts a server and a client (sing-box or Xray), then downloads 24 MiB, uploads 12 MiB (sha256 verified on
the server side), optionally repeats over TLS (Vision direct copy) and UDP, and issues small requests through the
SOCKS inbound. Unit-level tests live in `transport/v2rayxhttp` and `common/vlessenc`
(`go test -tags with_utls ./transport/v2rayxhttp ./common/vlessenc`).
