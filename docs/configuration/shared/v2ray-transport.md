V2Ray Transport is a set of private protocols invented by v2ray, and has contaminated the names of other protocols, such
as `trojan-grpc` in clash.

### Structure

```json
{
  "type": ""
}
```

Available transports:

* HTTP
* WebSocket
* QUIC
* gRPC
* HTTPUpgrade
* XHTTP
!!! warning "Difference from v2ray-core"

    * No TCP transport, plain HTTP is merged into the HTTP transport.
    * No mKCP transport.
    * No DomainSocket transport.

!!! note ""

    You can ignore the JSON Array [] tag when the content is only one item

### HTTP

```json
{
  "type": "http",
  "host": [],
  "path": "",
  "method": "",
  "headers": {},
  "idle_timeout": "15s",
  "ping_timeout": "15s"
}
```

!!! warning "Difference from v2ray-core"

    TLS is not enforced. If TLS is not configured, plain HTTP 1.1 is used.

#### host

List of host domain.

The client will choose randomly and the server will verify if not empty.

#### path

!!! warning

    V2Ray's documentation says that the path between the server and the client must be consistent, 
    but the actual code allows the client to add any suffix to the path.
    sing-box uses the same behavior as V2Ray, but note that the behavior does not exist in `WebSocket` and `HTTPUpgrade` transport.

Path of HTTP request.

The server will verify.

#### method

Method of HTTP request.

The server will verify if not empty.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

#### idle_timeout

In HTTP2 server:

Specifies the time until idle clients should be closed with a GOAWAY frame. PING frames are not considered as activity.

In HTTP2 client:

Specifies the period of time after which a health check will be performed using a ping frame if no frames have been
received on the connection.Please note that a ping response is considered a received frame, so if there is no other
traffic on the connection, the health check will be executed every interval. If the value is zero, no health check will
be performed.

Zero is used by default.

#### ping_timeout

In HTTP2 client:

Specifies the timeout duration after sending a PING frame, within which a response must be received.
If a response to the PING frame is not received within the specified timeout duration, the connection will be closed.
The default timeout duration is 15 seconds.

### WebSocket

```json
{
  "type": "ws",
  "path": "",
  "headers": {},
  "max_early_data": 0,
  "early_data_header_name": ""
}
```

#### path

Path of HTTP request.

The server will verify.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

#### max_early_data

Allowed payload size is in the request. Enabled if not zero.

#### early_data_header_name

Early data is sent in path instead of header by default.

To be compatible with Xray-core, set this to `Sec-WebSocket-Protocol`.

It needs to be consistent with the server.

### QUIC

```json
{
  "type": "quic"
}
```

!!! warning "Difference from v2ray-core"

    No additional encryption support:
    It's basically duplicate encryption. And Xray-core is not compatible with v2ray-core in here.

### gRPC

!!! note ""

    standard gRPC has good compatibility but poor performance and is not included by default, see [Installation](/installation/build-from-source/#build-tags).

```json
{
  "type": "grpc",
  "service_name": "TunService",
  "idle_timeout": "15s",
  "ping_timeout": "15s",
  "permit_without_stream": false
}
```

#### service_name

Service name of gRPC.

#### idle_timeout

In standard gRPC server/client:

If the transport doesn't see any activity after a duration of this time,
it pings the client to check if the connection is still active.

In default gRPC server/client:

It has the same behavior as the corresponding setting in HTTP transport.

#### ping_timeout

In standard gRPC server/client:

The timeout that after performing a keepalive check, the client will wait for activity.
If no activity is detected, the connection will be closed.

In default gRPC server/client:

It has the same behavior as the corresponding setting in HTTP transport.

#### permit_without_stream

In standard gRPC client:

If enabled, the client transport sends keepalive pings even with no active connections.
If disabled, when there are no active connections, `idle_timeout` and `ping_timeout` will be ignored and no keepalive
pings will be sent.

Disabled by default.

### HTTPUpgrade

```json
{
  "type": "httpupgrade",
  "host": "",
  "path": "",
  "headers": {}
}
```

#### host

Host domain.

The server will verify if not empty.

#### path

Path of HTTP request.

The server will verify.

#### headers

Extra headers of HTTP request.

The server will write in response if not empty.

### XHTTP

Compatible with Xray-core's XHTTP (SplitHTTP) transport: HTTP/1.1 and HTTP/2 (including over REALITY),
modes `packet-up`, `stream-up` and `stream-one`, xPadding, XMUX and the session/seq/uplink data placement options.
HTTP/3 (`alpn: h3`), `downloadSettings` (split up/down servers) and the browser dialer are not supported.

```json
{
  "type": "xhttp",
  "host": "",
  "path": "",
  "mode": "auto",
  "headers": {},
  "x_padding_bytes": "100-1000",
  "x_padding_obfs_mode": false,
  "x_padding_key": "",
  "x_padding_header": "",
  "x_padding_placement": "",
  "x_padding_method": "",
  "uplink_http_method": "",
  "session_id_placement": "",
  "session_id_key": "",
  "session_id_table": "",
  "session_id_length": "",
  "seq_placement": "",
  "seq_key": "",
  "uplink_data_placement": "",
  "uplink_data_key": "",
  "uplink_chunk_size": "",
  "no_grpc_header": false,
  "no_sse_header": false,
  "sc_max_each_post_bytes": 1000000,
  "sc_min_posts_interval_ms": 30,
  "sc_max_buffered_posts": 30,
  "sc_stream_up_server_secs": "20-80",
  "server_max_header_bytes": 8192,
  "xmux": {
    "max_concurrency": 0,
    "max_connections": 0,
    "c_max_reuse_times": 0,
    "h_max_request_times": 0,
    "h_max_reusable_secs": 0,
    "h_keep_alive_period": 0
  }
}
```

Every option has the same meaning and default as the Xray-core option of the same name in camelCase
(`x_padding_bytes` is `xPaddingBytes`, etc.). Range options accept `100`, `"100-1000"` or `{"from": 100, "to": 1000}`.
Options marked server/client below must match between both ends.

#### host

Host header. Defaults to the TLS server name, then the server address. The server verifies it if not empty.

#### path

Request path, may contain a `?query`. The server verifies it.

#### mode

`auto` (default), `packet-up`, `stream-up` or `stream-one`.

On the client `auto` selects `stream-one` over REALITY and `packet-up` otherwise (same as Xray-core).
On the server `auto` accepts every mode, any other value restricts it.

#### headers

Extra headers of HTTP request. `User-Agent` set to `chrome`, `firefox`, `safari`, `edge`, `curl` or `golang` selects
a built-in browser header profile; unset means `chrome`.

#### x_padding_*

Padding of every request (`x_padding_bytes`), by default as a `Referer` URL query. With `x_padding_obfs_mode` the
placement (`queryInHeader`, `header`, `cookie`, `query`), key, header name and method (`repeat-x`, `tokenish`)
are configurable and must match on both ends.

#### session_id_* / seq_*

Where the session id and the packet sequence number are carried (`path`, `header`, `cookie`, `query`) and how
the session id is generated.

#### uplink_*

`packet-up` only: upload HTTP method, and where the payload is carried (`body`, `header`, `cookie`, `auto`).

#### sc_*

Limits of the uplink: `sc_max_each_post_bytes` (the server value must be at least the client's),
`sc_min_posts_interval_ms`, `sc_max_buffered_posts`, `sc_stream_up_server_secs`.

#### xmux

Client connection pool, see Xray-core XMUX. Unset uses `max_connections: 3`, `h_max_request_times: 600-900`,
`h_max_reusable_secs: 1800-3000`.
