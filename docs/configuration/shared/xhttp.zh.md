### 结构

可用于入站和出站的 `transport` 字段。`splithttp` 是 `xhttp` 的别名。

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

### 字段

范围字段可使用整数或包含两端的范围，例如 `"100-1000"`。省略或范围两端均为零时使用默认值。

#### host

HTTP 主机名。客户端默认使用 TLS 服务器名称或服务器地址。设置后，服务端会验证该值。

#### path

HTTP 请求路径。默认使用 `/`。客户端与服务端必须一致。

#### mode

| 模式 | 上传 | 下载 |
| --- | --- | --- |
| `packet-up` | 多个 POST 请求 | 流式 GET |
| `stream-up` | 流式 POST | 流式 GET |
| `stream-one` | 流式 POST | 同一 POST 的响应 |

默认使用 `auto`。客户端通常使用 `packet-up`，与 REALITY 配合时使用 `stream-one`；同时配置 `download_settings` 时使用 `stream-up`。
除非配置了指定模式，否则服务端接受所有模式。

`stream-up` 的上传与 `stream-one` 需要 HTTP/2 或 HTTP/3。独立下载端可以使用 HTTP/1.1。表格中列出的是默认上传方法，可通过 `uplink_http_method` 修改。

#### http_version

可用值：`1.1`、`2`、`3`。

未启用 TLS 时，客户端默认使用 HTTP/1.1；启用 TLS 时默认使用 HTTP/2。TLS ALPN 仅包含 `http/1.1` 或 `h3` 时，会选择对应版本。
服务端默认接受 HTTP/1.1 和 HTTP/2；设置版本后仅接受该版本。

HTTP/3 需要 TLS 和包含 QUIC 支持的构建。REALITY 使用 HTTP/2。

#### headers

客户端使用额外请求标头，服务端使用额外响应标头。请使用 `host` 设置 HTTP 主机名。

#### x_padding_bytes

填充大小，上限为 65536。默认使用 `"100-1000"`。客户端生成的大小必须在服务端接受的范围内。

#### x_padding_obfs_mode

启用下列自定义填充设置。默认禁用；禁用时会在 `Referer` 中发送重复的 `X` 填充。

#### x_padding_method

`repeat-x` 重复字符 `X`；`tokenish` 生成随机字母数字填充。
`repeat-x` 的大小按字符数计算，`tokenish` 的大小按 Huffman 编码后的字节数计算。

默认使用 `repeat-x`。

#### x_padding_placement

| 值 | 位置 |
| --- | --- |
| `queryInHeader` | `x_padding_header` 中的 URL 查询字符串（默认） |
| `header` | `x_padding_header` 的值 |
| `cookie` | 名称为 `x_padding_key` 的 Cookie |
| `query` | 名称为 `x_padding_key` 的请求查询参数 |

#### x_padding_key

填充查询参数或 Cookie 名称。默认使用 `x_padding`。

#### x_padding_header

填充标头名称。默认使用 `X-Padding`。

#### session_id_placement / seq_placement

会话 ID 和数据包序号的位置。可用值：`path`、`query`、`header`、`cookie`。
默认使用 `path`。客户端与服务端必须一致。

#### session_id_key / seq_key

所选位置使用的名称。选择 `path` 时忽略。客户端与服务端必须一致。

标头默认使用 `X-Session` / `X-Seq`；查询参数与 Cookie 默认使用 `x_session` / `x_seq`。

#### session_id_table

客户端会话 ID 字符表。留空时使用 UUID v4。

可用字符表：`ALPHABET`、`alphabet`、`Alphabet`、`BASE36`、`base36`、`Base62`、`HEX`、`hex`、`number`。
自定义字符表可以包含互不重复的字母、数字以及 `-`、`.`、`_`、`~`。

#### session_id_length

使用 `session_id_table` 时必填。可使用 1–4096 之间的整数或范围；最短长度必须提供至少 31 位随机性。

#### uplink_http_method

上传请求的方法，默认为 `POST`，自动转换为大写。`GET` 仅适用于 `packet-up`。
`HEAD`、`CONNECT` 和 `OPTIONS` 为保留方法，不能用于上传。服务端通过序列号识别分包上传，包括使用 `GET` 的上传。

#### uplink_data_placement

| 值 | 客户端分包上传 | 服务端解码 |
| --- | --- | --- |
| `auto`（默认） | HTTP 请求体 | 按请求头分块、Cookie 分块、请求体的顺序拼接 |
| `body` | HTTP 请求体 | HTTP 请求体 |
| `header` | Base64url 请求头分块 | 请求头分块 |
| `cookie` | Base64url Cookie 分块 | Cookie 分块 |

客户端请求头和 Cookie 编码仅用于 `packet-up`。流式上传始终使用请求体。
编码采用与 Xray 兼容的无填充 Base64url 格式。无效编码、重复分块和不连续的分块编号会被拒绝。除最后一块外，每个分块至少包含 64 个编码字符。

#### uplink_data_key

分块名称前缀。请求头使用 `<key>-0`、`<key>-1` 等；Cookie 使用 `<key>_0`、`<key>_1` 等。
`cookie` 默认使用 `x_data`，其他模式默认使用 `X-Data`。两端必须使用相同的前缀；客户端使用 `cookie`、服务端使用 `auto` 时也应显式设置为相同值。
分块名称不能与会话元数据、填充或自定义请求头及 Cookie 冲突。

#### uplink_chunk_size

客户端每个请求头或 Cookie 分块包含的编码字符数，每个分块独立取样。
请求头默认为 `3000-4000`，Cookie 默认为 `2048-3072`。显式设置且小于 64 的值会提高至 64，与 Xray 一致。最大为 32 MiB。
此选项限制单个分块，`sc_max_each_post_bytes` 限制原始的、解码后的整个上传包。
Cookie 上传会在需要时自动减小上传包，确保每个请求不超过 Go 默认的 3000 个 Cookie 上限（包括自定义 Cookie、元数据和填充）。服务端的解码后上传包上限保持不变，请求头字节数上限仍然适用。

#### server_max_header_bytes

服务端 HTTP/1.1、HTTP/2 和 HTTP/3 的请求头大小上限。默认为 262144（256 KiB），允许 1024 至 33554432（32 MiB）。
Base64 会增加约三分之一的大小，此外还有分块名称和填充。使用编码上传时，应减小 `sc_max_each_post_bytes`，或提高服务端及反向代理的请求头上限。Xray 的默认请求头上限为 8192 字节。
分块不能绕过反向代理对整个请求头或 Cookie 请求头的大小限制。

例如，客户端可以设置 `uplink_data_placement: "header"`、`uplink_http_method: "GET"`、`uplink_data_key: "payload"`、`sc_max_each_post_bytes: 32768`。
服务端应使用相同的 `uplink_data_key`，将 `uplink_data_placement` 设为 `auto` 或 `header`，并确保上传包大小上限不小于客户端。

#### download_settings

仅客户端。为 `packet-up` 或 `stream-up` 的下载 GET 配置独立端点：

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
      "xmux": { "max_connections": 2 }
    }
  }
}
```

`server`、`server_port` 和类型为 `xhttp` 或 `splithttp` 的 `transport` 为必填项。TLS 及传输设置相互独立：省略 TLS 表示明文，省略传输字段时采用正常默认值，不继承上传端的设置。
两端共用出站的拨号器及路由、套接字设置；此处不单独配置拨号器或 `detour`。
下载域名遵循正常的出站 DNS 解析器选择规则，即使上传服务器使用 IP 地址也是如此。

两端必须连接到同一个 XHTTP 服务端会话存储。使用不同的反向代理路径或主机名时，需要转发到同一个后端并保留会话 ID；两个独立入站不能拼接同一会话。
会话 ID 由上传配置生成一次，再按照两端各自的元数据设置写入请求。

下载端具有独立的 HTTP 连接池和 XMUX 计数。关闭连接、任一端失败或建连超时都会取消两个方向并释放连接池租约；空闲连接控制同时作用于两个池。
下载可独立使用 HTTP/1.1。使用 REALITY 且模式为 `auto` 时，配置此项会选择 `stream-up`。
不支持 `stream-one` 或嵌套 `download_settings`。上传编码与分包大小仍由外层传输配置控制。

#### sc_max_buffered_posts

服务端的数据包重排序窗口，范围为 1–1024。默认使用 `30`。

#### no_sse_header

仅服务端。下载响应中不发送 `Content-Type: text/event-stream`。默认禁用。

#### no_grpc_header

仅客户端。流式上传中不发送 `Content-Type: application/grpc`。默认禁用。

#### sc_max_each_post_bytes

数据包上传的最大大小，上限为 16 MiB。默认使用 `1000000`。
客户端为每个连接从范围中取值；服务端接受不超过范围上限的值。

#### sc_min_posts_interval_ms

仅客户端。数据包上传的最小间隔，单位为毫秒，上限为 60000。默认使用 `30`。

使用 Xray-core `3519dfe` 进行互操作测试时，1 ms 间隔的 HTTP/1.1 编码上传偶尔会在流水线请求完整到达服务端之前重置 TCP 连接。Xray 默认的 30 ms 间隔通过测试，HTTP/2 和 HTTP/3 在 1 ms 下也通过测试。Xray 客户端使用 HTTP/1.1 编码上传时，请保留默认间隔或使用 HTTP/2、HTTP/3。sing-box 等待上传确认的 HTTP/1.1 客户端未出现此问题。
互操作测试通过 `XHTTP_STRESS_HTTP1=1` 保留了激进间隔的复现方式。

#### xmux

客户端 HTTP 连接复用设置，与协议多路复用相互独立。

| 字段 | 描述 |
| --- | --- |
| `max_connections` | 目标连接数，上限为 1024。与 `max_concurrency` 冲突。 |
| `max_concurrency` | 每个连接的最大并发流数量。 |
| `c_max_reuse_times` | 每个连接可分配的最大流数量。 |
| `h_max_request_times` | 每个连接的最大 HTTP 请求数。 |
| `h_max_reusable_secs` | 连接可复用时间，单位为秒。 |
| `h_keep_alive_period` | 保活间隔，单位为秒。`0` 使用 30 秒；`-1` 禁用。 |

计数字段可使用整数或范围。省略 `xmux` 或使用空对象时，使用结构示例中的默认值。
在非空对象中，未设置的计数字段不受限制。
