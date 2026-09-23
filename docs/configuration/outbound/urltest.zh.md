### 结构

```json
{
  "type": "urltest",
  "tag": "auto",
  
  "outbound_set": [],
  "outbounds": [
    "proxy-a",
    "proxy-b",
    "proxy-c"
  ],
  "url": "",
  "interval": "",
  "tolerance": 50,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### 字段

#### outbound_set

用于测试的[出站集合](/zh/configuration/outbound-set/)标签列表，也接受单个标签。

成员按集合和源文件顺序追加到 `outbounds` 后。重复的成员标签只添加一次。

#### outbounds

==当 `outbound_set` 为空时必填==

用于测试的出站标签列表。

#### url

用于测试的链接。默认使用 `https://www.gstatic.com/generate_204`。

#### interval

测试间隔。 默认使用 `3m`。

#### tolerance

以毫秒为单位的测试容差。 默认使用 `50`。

#### idle_timeout

空闲超时。默认使用 `30m`。

#### interrupt_exist_connections

当选定的出站发生更改时，中断现有连接。

仅入站连接受此设置影响，内部连接将始终被中断。
