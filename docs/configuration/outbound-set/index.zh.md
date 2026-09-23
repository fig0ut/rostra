# 出站集合

出站集合包含可供[选择器](/zh/configuration/outbound/selector/)和
[URLTest](/zh/configuration/outbound/urltest/) 出站共享的出站定义。

在顶层 `outbound_set` 字段中配置出站集合。

!!! note ""

    出站集合是本分支提供的扩展功能。
    源文件在检查、启动或重新加载配置时读取。
    远程集合在启动后定时刷新。不支持本地文件监听。

### 结构

=== "内联"

    ```json
    {
      "type": "inline", // 可选
      "tag": "",
      "outbounds": [],
      "override": {}
    }
    ```

=== "本地文件"

    ```json
    {
      "type": "local",
      "tag": "", // 或 []
      "format": "source",
      "path": "",
      "override": {}
    }
    ```

=== "远程文件"

    !!! info ""

        启用 `experimental.cache_file.enabled` 时，远程出站集合将被缓存。

    ```json
    {
      "type": "remote",
      "tag": "", // 或 []
      "format": "source",
      "url": "",
      "initial_path": "",
      "http_client": "", // 或 {}
      "update_interval": "",
      "override": {}
    }
    ```

### 字段

#### type

出站集合类型，`inline`、`local` 或 `remote`。

默认使用 `inline`。

#### tag

==必填==

出站集合的标签。必须唯一、非空且不包含 `/`。

对于本地和远程出站集合，`tag` 也接受一组标签，用于定义多个共享其他选项的集合。
`path`、`url` 或 `initial_path` 中的 `{tag}` 占位符将被替换为每个标签，设置多个标签时必填。

多个标签与 `type: inline` 冲突。

#### override

覆盖每个成员的出站配置字段。

```json
{
  "override": {
    "detour": "relay"
  }
}
```

覆盖值优先于源配置。对象递归合并，数组和标量替换原值，`null` 删除字段。
显式的 `false`、`0` 和空字符串会保留。

不能覆盖 `type` 和 `tag`。所有字段必须受每个成员的出站类型支持。

源配置中的 `detour` 若指向另一个成员，将在同一集合内解析。
`override` 中的 `detour` 使用完整出站标签，例如 `relay` 或 `another-set/relay`。
缺失的依赖和循环依赖将被拒绝。

### 内联字段

#### outbounds

==必填==

非空的[出站](/zh/configuration/outbound/)定义列表。

每个出站必须有唯一且非空的 `tag`。不支持将选择器和 URLTest 出站作为成员。

### 本地或远程字段

#### format

==必填==

出站集合文件的格式。仅支持 `source`。

当 `path` 或 `url` 使用 `json` 作为扩展名时可选。

参阅[源文件格式](./source-format/)了解详情。

源文件大小上限为 16 MiB。

### 本地字段

#### path

==必填==

出站集合的文件路径，相对于配置工作目录。

### 远程字段

#### url

==必填==

出站集合的下载 URL。支持 HTTP 和 HTTPS。

下载超时为 `30s`。连接使用 [http_client](#http_client)。

#### initial_path

出站集合初始内容的文件路径。

启动时没有可用的缓存源则读取此文件。缓存或初始内容将立即使用，并在启动后于后台刷新。
两者均不可用时，初始下载必须成功才能继续启动。

!!! note ""

    如果下载客户端依赖正在下载的集合中的代理，需要提供缓存内容或 `initial_path`。
    缺少初始内容时，循环下载依赖将报错。不会将配置的客户端替换为直连。

#### http_client

用于下载出站集合的 HTTP 客户端。

接受客户端标签或内联配置。参阅 [HTTP 客户端字段](/zh/configuration/shared/http-client/)了解详情。

留空时使用 [`default_http_client`](/zh/configuration/route/#default_http_client) 指定的客户端，
未指定默认客户端时使用顶层 `http_clients` 的第一项。
未配置 HTTP 客户端时，下载使用直连。

`http_client` 控制下载连接。[`override.detour`](#override) 控制集合成员的代理连接。

初始下载可使用独立可用的出站，包括内联、本地或已加载远程集合中的成员。

#### update_interval

出站集合的更新间隔。

留空或为 `0` 时使用 `1d`。负值无效。

缓存或初始内容在启动后立即刷新。初始下载成功时，首次刷新将在此间隔后进行。
更新失败后按同一间隔重试。

### 更新

更新仅在成员、覆盖配置和依赖通过验证，并且新代理成功启动后应用。
更新失败时保留正在运行的集合及其缓存源。

选择器和 URLTest 的成员随集合刷新。若选择器当前选中的标签仍然存在，则保留该选择；
否则使用配置的 `default`（若仍可用），再回退到第一个成员。
初次加载配置时，默认成员不存在将报错；运行时刷新允许默认成员消失。

未更改的代理保留其出站实例和连接池。已更改的代理及依赖它们的集合成员将被替换。
已建立的连接保留原实例直至关闭。新连接使用更新后的配置。
用于 TUN 流转发的实例将保留至程序关闭，因为 TUN 分发器会保留已关联的端口。
选择器和 URLTest 原有的连接中断设置仍然生效。

若更新删除了仍被路由、detour、HTTP 客户端或显式分组成员引用的出站，则拒绝该更新。
新旧实例共存期间会形成循环依赖的更改也会被拒绝；此类更改需要通过重新加载配置应用。

### 成员标签

成员使用 `<集合标签>/<成员标签>` 作为出站标签。例如，`proxies` 中的 `node-a` 为 `proxies/node-a`。
路由规则、显式出站引用和选择器的 [`default`](/zh/configuration/outbound/selector/#default) 使用此标签。
标签不能与已有出站或端点冲突。

引用同一集合的分组共享该集合的出站实例。需要对同一源应用不同覆盖时，使用不同标签定义多个集合。
每个集合创建独立的出站实例。

### 缓存

启用 [`experimental.cache_file.enabled`](/zh/configuration/experimental/cache-file/#enabled) 时缓存远程源。
缓存目录为 `<cache_file.path>.outbound-set`，路径为空时使用 `cache.db.outbound-set`。

缓存源通过缓存 ID、集合标签和 URL 标识。加载缓存时应用当前覆盖配置。
缓存文件权限为 `0600`。初始下载在出站创建成功后保存；
刷新内容在新成员成功启动且更新已应用后保存。

### 示例

加载 `proxies.json` 并通过 `relay` 连接其中的代理：

```json
{
  "outbound_set": [
    {
      "type": "local",
      "tag": "proxies",
      "path": "proxies.json",
      "override": {
        "detour": "relay"
      }
    }
  ],
  "outbounds": [
    {
      "type": "selector",
      "tag": "exit",
      "outbound_set": ["proxies"],
      "default": "proxies/node-a"
    },
    {
      "type": "socks",
      "tag": "relay",
      "server": "127.0.0.1",
      "server_port": 1081
    }
  ],
  "route": {
    "final": "exit"
  }
}
```

参阅[源文件格式](./source-format/)中的 `proxies.json` 示例。

连接顺序为：客户端 → relay → 选中的代理 → 目标。
