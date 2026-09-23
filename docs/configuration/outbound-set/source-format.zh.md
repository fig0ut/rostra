### 结构

```json
{
  "version": 1,
  "outbounds": []
}
```

### 字段

#### version

==必填==

出站集合的版本。仅支持 `1`。

#### outbounds

==必填==

非空的[出站](/zh/configuration/outbound/)定义列表。

每个出站必须有唯一且非空的 `tag`。不支持将选择器和 URLTest 出站作为成员。

成员注册时会添加集合前缀。参阅[成员标签](../#成员标签)了解详情。

### 示例

`proxies.json`：

```json
{
  "version": 1,
  "outbounds": [
    {
      "type": "socks",
      "tag": "node-a",
      "server": "127.0.0.1",
      "server_port": 1080
    }
  ]
}
```
