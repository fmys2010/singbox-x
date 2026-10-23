# Notice

This tree is sing-box with a native XHTTP transport and VLESS encryption. The files below were written by
reading, and in part porting, [Xray-core](https://github.com/XTLS/Xray-core) (MPL-2.0), so they follow its protocol
behaviour and keep its constants, defaults and algorithms:

| Files | Derived from Xray-core |
|---|---|
| `transport/v2rayxhttp/` (`config.go`, `padding.go`, `headers.go`, `upload_queue.go`, `xmux.go`, `server.go`, `client.go`) | `transport/internet/splithttp/`, `common/utils/browser.go` |
| `common/vlessenc/` | `proxy/vless/encryption/` |
| `protocol/vless/vision_encryption.go`, `protocol/vless/service_encryption.go` | `sagernet/sing-vmess` `vless/vision.go`, `vless/service.go` (adapted to run on the encryption layer, as in Xray's `proxy/` Vision) |

Xray-core itself is not part of this tree and is not linked or executed by sing-box. Before redistributing, check that
the licence terms of the original files (MPL-2.0 for Xray-core) are met for the derived files above.
