### 结构

```json
{
  "type": "selector",
  "tag": "select",

  "outbound_set": [],
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "default": "proxy-c",
  "interrupt_exist_connections": false
}
```

!!! quote ""

    选择器目前只能通过 [Clash API](/zh/configuration/experimental/clash-api/) 来控制。

### 字段

#### outbound_set

用于选择的[出站集合](/zh/configuration/outbound-set/)标签列表，也接受单个标签。

成员按集合和源文件顺序追加到 `outbounds` 后。重复的成员标签只添加一次。

#### outbounds

==当 `outbound_set` 为空时必填==

用于选择的出站标签列表。

#### default

默认的出站标签。必须属于 `outbounds` 或引用的 `outbound_set`。

对于出站集合成员，使用 `<集合标签>/<成员标签>`，例如 `proxies/node-a`：

```json
{
  "type": "selector",
  "tag": "select",
  "outbound_set": ["proxies"],
  "default": "proxies/node-a"
}
```

留空时使用第一个出站。显式 `outbounds` 排在出站集合成员之前。
仅配置 `outbound_set` 时，使用第一个集合的第一个成员。

!!! note ""

    启用缓存文件时，若已保存的选择仍属于当前成员，则优先于 `default` 使用。
    否则使用 `default` 或第一个成员。

出站集合刷新后，若选中的成员消失，则使用仍可用的默认成员，否则选择第一个成员。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。
