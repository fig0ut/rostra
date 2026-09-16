### Structure

Available in inbound and outbound `transport` fields. `splithttp` is an alias for `xhttp`.

```json
{
  "type": "xhttp",
  "host": "",
  "path": "/",
  "mode": "auto",
  "http_version": "",
  "headers": {},
  "x_padding_bytes": "100-1000",
  "x_padding_obfs_mode": false,
  "x_padding_method": "repeat-x",
  "x_padding_placement": "queryInHeader",
  "x_padding_key": "x_padding",
  "x_padding_header": "X-Padding",
  "session_id_placement": "path",
  "session_id_key": "",
  "session_id_table": "Base62",
  "session_id_length": 16,
  "seq_placement": "path",
  "seq_key": "",
  "uplink_http_method": "POST",
  "uplink_data_placement": "auto",
  "uplink_data_key": "",
  "uplink_chunk_size": 0,
  "server_max_header_bytes": 262144,
  "sc_max_buffered_posts": 30,
  "no_sse_header": false,
  "no_grpc_header": false,
  "sc_max_each_post_bytes": 1000000,
  "sc_min_posts_interval_ms": 30,
  "xmux": {
    "max_connections": 3,
    "h_max_request_times": "600-900",
    "h_max_reusable_secs": "1800-3000"
  }
}
```

### Fields

Range fields accept an integer or an inclusive range such as `"100-1000"`. Omitted or all-zero ranges use the default.

#### host

HTTP host. The client defaults to the TLS server name or server address. The server verifies it if set.

#### path

HTTP request path. `/` is used by default. Must match on both sides.

#### mode

| Mode | Upload | Download |
| --- | --- | --- |
| `packet-up` | Multiple POST requests | Streaming GET |
| `stream-up` | Streaming POST | Streaming GET |
| `stream-one` | Streaming POST | Response to the same POST |

`auto` is used by default. The client uses `packet-up`, or `stream-one` with REALITY. With REALITY and `download_settings`, it uses `stream-up`.
The server accepts all modes unless a specific mode is set.

`stream-up` uploads and `stream-one` require HTTP/2 or HTTP/3. A separate download endpoint can use HTTP/1.1.
The table shows the default upload method; `uplink_http_method` can change it.

#### http_version

Available values: `1.1`, `2`, `3`.

The client defaults to HTTP/1.1 without TLS and HTTP/2 with TLS. A single TLS ALPN value of `http/1.1` or `h3` selects that version.
The server accepts HTTP/1.1 and HTTP/2 by default; setting a version restricts it to that version.

HTTP/3 requires TLS and a build with QUIC support. REALITY uses HTTP/2.

#### headers

Extra request headers on the client, response headers on the server. Use `host` to set the HTTP host.

#### x_padding_bytes

Padding size, up to 65536. `"100-1000"` is used by default. Must fit the server's accepted range.

#### x_padding_obfs_mode

Enable custom padding settings below. Disabled by default; repeated-X padding is sent in `Referer` instead.

#### x_padding_method

`repeat-x` repeats `X`; `tokenish` generates random alphanumeric padding.
Size is measured in characters for `repeat-x`, Huffman-encoded bytes for `tokenish`.

`repeat-x` is used by default.

#### x_padding_placement

| Value | Placement |
| --- | --- |
| `queryInHeader` | URL query in `x_padding_header` (default) |
| `header` | Value in `x_padding_header` |
| `cookie` | Cookie named `x_padding_key` |
| `query` | Request query parameter named `x_padding_key` |

#### x_padding_key

Padding query parameter or cookie name. `x_padding` is used by default.

#### x_padding_header

Padding header name. `X-Padding` is used by default.

#### session_id_placement / seq_placement

Session ID and packet sequence placement. Available values: `path`, `query`, `header`, `cookie`.
`path` is used by default. Must match on both sides.

#### session_id_key / seq_key

Names used for the selected placement. Ignored for `path`. Must match on both sides.

Defaults: `X-Session` / `X-Seq` for headers, `x_session` / `x_seq` for query parameters and cookies.

#### session_id_table

Client session ID alphabet. UUID v4 is used when empty.

Available tables: `ALPHABET`, `alphabet`, `Alphabet`, `BASE36`, `base36`, `Base62`, `HEX`, `hex`, `number`.
A custom table may contain unique letters, digits, `-`, `.`, `_`, and `~`.

#### session_id_length

Required with `session_id_table`. Integer or range within 1–4096; the shortest length must provide at least 31 bits of randomness.

#### uplink_http_method

Upload request method. Defaults to `POST`; normalized to uppercase. `GET` requires `packet-up`.
`HEAD`, `CONNECT`, and `OPTIONS` are reserved and cannot be used for uploads.
The server identifies packet uploads by their sequence number, including uploads using `GET`.

#### uplink_data_placement

| Value | Client packet upload | Server packet decoding |
| --- | --- | --- |
| `auto` (default) | HTTP body | Header chunks, cookie chunks, then body, concatenated in that order |
| `body` | HTTP body | HTTP body |
| `header` | Base64url header chunks | Header chunks |
| `cookie` | Base64url cookie chunks | Cookie chunks |

Header and cookie encoding require `packet-up` on the client. Streaming uploads always use the body.
Encoding is compatible with Xray's unpadded base64url format. Invalid encoding, duplicate chunks, and gaps in chunk numbering are rejected. Non-final chunks must contain at least 64 encoded characters.

#### uplink_data_key

Header or cookie chunk prefix. Headers use `<key>-0`, `<key>-1`, etc.; cookies use `<key>_0`, `<key>_1`, etc.
Defaults to `x_data` for `cookie` and `X-Data` otherwise. Set the same key on both sides, including when the server uses `auto` and the client uses `cookie`.
Chunk names must not collide with session metadata, padding, or configured headers/cookies.

#### uplink_chunk_size

Client encoded characters per header or cookie chunk, sampled for each chunk.
Defaults to `3000-4000` for headers and `2048-3072` for cookies. Explicit values below 64 are raised to 64, matching Xray. Maximum: 32 MiB.
This controls individual chunks, not the total request size. `sc_max_each_post_bytes` limits the original, decoded packet.
Cookie uploads automatically use smaller packets when necessary to stay within Go's default limit of 3000 cookies per request, including configured cookies, metadata, and padding. The server's decoded packet limit is unchanged. Header byte limits still apply.

#### server_max_header_bytes

Server request header limit, across HTTP/1.1, HTTP/2, and HTTP/3. Defaults to 262144 (256 KiB); accepts 1024 through 33554432 (32 MiB).
Base64 adds about one third to the payload size, plus chunk names and padding. When using encoded uploads, reduce `sc_max_each_post_bytes` or raise the header limit on the server and any reverse proxy. Xray's default header limit is 8192 bytes.

For example, a header-encoded client can use:

```json
{
  "type": "xhttp",
  "mode": "packet-up",
  "path": "/xhttp",
  "uplink_http_method": "GET",
  "uplink_data_placement": "header",
  "uplink_data_key": "payload",
  "uplink_chunk_size": "3000-4000",
  "sc_max_each_post_bytes": 32768
}
```

Use `uplink_data_key: "payload"` on the server, with `uplink_data_placement` set to `auto` or `header`.
The server's decoded packet limit must be at least the client's packet size. Chunking does not bypass a proxy's aggregate header or Cookie-header limit.

#### download_settings

Client only. A separate endpoint for the streaming download GET in `packet-up` or `stream-up`:

```json
{
  "type": "xhttp",
  "mode": "packet-up",
  "path": "/upload",
  "download_settings": {
    "server": "download.example.com",
    "server_port": 443,
    "tls": {
      "enabled": true,
      "server_name": "download.example.com"
    },
    "transport": {
      "type": "xhttp",
      "host": "download.example.com",
      "path": "/download",
      "http_version": "2",
      "xmux": {
        "max_connections": 2
      }
    }
  }
}
```

`server`, `server_port`, and an `xhttp` or `splithttp` `transport` are required. The nested TLS and transport settings are independent: omitted TLS means plaintext, and omitted transport fields use their normal defaults, not the upload endpoint's settings.
The outbound's dialer and routing/socket settings are shared. A separate dialer/detour is not configured here.
Download hostname resolution follows the normal outbound DNS resolver rules, including when the upload server is an IP address.

Both endpoints must reach the same XHTTP server session store. Separate reverse proxy paths or hosts must route to that same backend and preserve the shared session ID; two independent inbounds cannot join the session.
Session IDs are generated once using the upload settings and encoded separately according to each endpoint's metadata settings.

The endpoints have separate HTTP connection pools and XMUX counters. Closing the connection, an endpoint failure, or an incomplete setup timeout cancels both directions and releases both pools' leases. Idle connection controls apply to both pools.
`stream-one` and nested `download_settings` are rejected. Upload encoding and packet sizing remain controlled by the outer transport.

#### sc_max_buffered_posts

Server packet reordering window, within 1–1024. `30` is used by default.

#### no_sse_header

Server only. Omit `Content-Type: text/event-stream` on downloads. Disabled by default.

#### no_grpc_header

Client only. Omit `Content-Type: application/grpc` on streaming uploads. Disabled by default.

#### sc_max_each_post_bytes

Maximum packet upload size, up to 16 MiB. `1000000` is used by default.
The client samples the range per connection; the server accepts up to its upper bound.

#### sc_min_posts_interval_ms

Client only. Minimum interval between packet uploads, in milliseconds, up to 60000. `30` is used by default.

Interoperability note: testing Xray-core commit `3519dfe` at 1 ms with encoded HTTP/1.1 uploads exposed intermittent TCP resets before all pipelined requests reached the server. Xray's default 30 ms pacing passed; HTTP/2 and HTTP/3 also passed at 1 ms. For Xray clients using encoded HTTP/1.1, retain the default pacing or use HTTP/2 or HTTP/3. This limitation was not observed with sing-box's acknowledgement-based HTTP/1.1 uploader.
The interoperability suite retains the aggressive case behind `XHTTP_STRESS_HTTP1=1`.

#### xmux

Client HTTP connection reuse settings, independent of protocol multiplexing.

| Field | Description |
| --- | --- |
| `max_connections` | Target connection count, up to 1024. Conflicts with `max_concurrency`. |
| `max_concurrency` | Maximum concurrent streams per connection. |
| `c_max_reuse_times` | Maximum stream assignments per connection. |
| `h_max_request_times` | Maximum HTTP requests per connection. |
| `h_max_reusable_secs` | Connection reuse lifetime in seconds. |
| `h_keep_alive_period` | Keepalive interval in seconds. `0` uses 30 seconds; `-1` disables it. |

Counters accept integers or ranges. An omitted or empty `xmux` uses the values shown above.
In a nonempty object, unspecified counters are unlimited.
