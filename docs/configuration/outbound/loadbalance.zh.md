### 结构

```json
{
  "type": "loadbalance",
  "tag": "balance",

  "outbound_set": [],
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "strategy": "consistent_hash",
  "hash_key": "source_ip_domain",
  "url": "",
  "interval": "",
  "idle_timeout": ""
}
```

### 字段

#### outbound_set

用于负载均衡的[出站集合](/zh/configuration/outbound-set/)标签列表，也接受单个标签。

成员按集合和源文件顺序追加到 `outbounds` 后。重复的成员标签只添加一次。

#### outbounds

==当 `outbound_set` 为空时必填==

用于负载均衡的出站标签列表。

#### strategy

负载均衡策略。默认使用 `consistent_hash`。

| 值                | 描述                                                        |
|-------------------|-------------------------------------------------------------|
| `consistent_hash` | 使用 `hash_key` 通过 Rendezvous 哈希选择成员。 |
| `round_robin`     | 依次选择可用成员，TCP 和 UDP 使用独立计数器。                 |

调整成员顺序不会改变选择。添加或移除成员只会重新分配受该成员影响的连接。

每个 TCP 连接或 UDP 会话选择一次出站，并在其生命周期内保持不变。
只有支持所请求网络的出站才会参与选择。拨号失败时直接返回错误，不会重试其他成员。
不支持 ICMP 和三层转发。

#### hash_key

==仅在 `strategy` 为 `consistent_hash` 时可用==

用于哈希的键。默认使用 `source_ip_domain`。

| 值                   | 描述                             |
|----------------------|----------------------------------|
| `source_ip_domain`   | 源 IP 地址和目标可注册域名。      |
| `source_ip_host`     | 源 IP 地址和目标完整主机名。      |
| `destination_domain` | 目标可注册域名，不包含源地址。    |

可注册域名通过公共后缀列表（Public Suffix List）确定，包括私有后缀。
例如，`login.example.co.uk` 和 `api.example.co.uk` 使用 `example.co.uk`，
而 `alice.github.io` 和 `bob.github.io` 保持独立。
无法取得可注册域名的主机名（如 `localhost`）按原主机名处理。

所有模式均忽略端口。主机名不区分大小写，忽略末尾的点，国际化域名转换为 ASCII。
主机名不可用时使用目标 IP 地址，源 IP 地址不可用时仅由目标地址确定键。

默认模式使同一客户端访问相关子域名时使用同一出站，同时允许不同客户端使用不同出站。
修改 `hash_key` 可能改变新连接的出站选择，现有连接不受影响。

#### url

用于健康检查的链接。默认使用 `https://www.gstatic.com/generate_204`。

在启动、成员变更和网络变更后执行检查。检查失败的成员不会用于新连接。
尚未检查的成员仍可参与选择。没有可用成员时，新连接将失败。

嵌套选择器的健康结果仅适用于测试时的选择。新的选择在检查完成前仍可参与负载均衡。

HTTP 检查不能验证 UDP 连通性。仅支持 UDP 的成员不执行 HTTP 检查，仍可参与选择。

#### interval

健康检查间隔。默认使用 `3m`。

不能大于 `idle_timeout`。

#### idle_timeout

空闲超时。默认使用 `30m`。

在此期间没有为连接选择出站时，暂停定期健康检查。新连接会恢复检查。

### 出站集合更新

成员随出站集合刷新。未变更的成员保留健康状态，新增或替换的成员从未检查状态开始。
更新后会重新执行健康检查。现有连接继续使用原来的出站。

### 状态

负载均衡器没有单一的选定出站。查询组状态不会选择成员或推进轮询计数器。
Clash API 返回空的 `now` 值，以及按成员标签索引的 `health` 映射，状态为 `unknown`、`healthy` 或 `unhealthy`。

守护进程的出站组响应使用本组健康检查中的成员测试时间和延迟。使用不同测试链接的组独立保存结果。
