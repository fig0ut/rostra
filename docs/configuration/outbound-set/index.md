# outbound-set

An outbound-set contains outbound definitions shared by [Selector](/configuration/outbound/selector/)
and [URLTest](/configuration/outbound/urltest/) outbounds.

Configure outbound-sets in the top-level `outbound_set` field.

!!! note ""

    Outbound-sets are an extension provided by this fork.
    Sources are loaded when checking, starting or reloading the configuration.
    Remote sets are refreshed after startup. Local file watching is not supported.

### Structure

=== "Inline"

    ```json
    {
      "type": "inline", // optional
      "tag": "",
      "outbounds": [],
      "override": {}
    }
    ```

=== "Local File"

    ```json
    {
      "type": "local",
      "tag": "", // or []
      "format": "source",
      "path": "",
      "override": {}
    }
    ```

=== "Remote File"

    !!! info ""

        Remote outbound-sets will be cached if `experimental.cache_file.enabled` is enabled.

    ```json
    {
      "type": "remote",
      "tag": "", // or []
      "format": "source",
      "url": "",
      "initial_path": "",
      "http_client": "", // or {}
      "update_interval": "",
      "override": {}
    }
    ```

### Fields

#### type

Type of outbound-set, `inline`, `local` or `remote`.

`inline` will be used if empty.

#### tag

==Required==

Tag of outbound-set. Must be unique, nonempty and must not contain `/`.

For local and remote outbound-sets, `tag` also accepts a list of tags to define multiple sets sharing other options.
The `{tag}` placeholder in `path`, `url` or `initial_path` is replaced by each tag,
and is required when multiple tags are set.

Multiple tags conflict with `type: inline`.

#### override

Outbound fields to override for every member.

```json
{
  "override": {
    "detour": "relay"
  }
}
```

Overrides take precedence over source values. Objects merge recursively, arrays and scalar values replace existing values,
and `null` removes a field. Explicit `false`, `0` and empty strings are preserved.

`type` and `tag` cannot be overridden. All fields must be supported by each member's outbound type.

A `detour` in the source that names another member is resolved within the same set.
A `detour` in `override` uses the full outbound tag, such as `relay` or `another-set/relay`.
Missing dependencies and circular dependencies are rejected.

### Inline Fields

#### outbounds

==Required==

Nonempty list of [Outbound](/configuration/outbound/) definitions.

Each outbound must have a unique, nonempty `tag`. Selector and URLTest outbounds are not supported as members.

### Local or Remote Fields

#### format

==Required==

Format of outbound-set file. Only `source` is supported.

Optional when `path` or `url` uses `json` as extension.

See [Source Format](./source-format/) for details.

Source files are limited to 16 MiB.

### Local Fields

#### path

==Required==

File path of outbound-set, relative to the configuration working directory.

### Remote Fields

#### url

==Required==

Download URL of outbound-set. HTTP and HTTPS are supported.

The download timeout is `30s`. The connection uses [http_client](#http_client).

#### initial_path

File path of the initial outbound-set content.

Read at startup when no usable cached source is available. Cached or initial content is used immediately,
then refreshed in the background after startup. Without either, the initial download must succeed before startup can continue.

!!! note ""

    If the download client depends on a proxy from the set being downloaded, provide cached content or `initial_path`.
    Without bootstrap content, a circular download dependency is an error. The configured client is never replaced with a direct connection.

#### http_client

HTTP Client for downloading outbound-set.

Accepts a client tag or inline configuration. See [HTTP Client Fields](/configuration/shared/http-client/) for details.

When empty, the client named by [`default_http_client`](/configuration/route/#default_http_client) is used,
or the first top-level `http_clients` entry when no default is specified.
If no HTTP client is configured, downloads use a direct connection.

`http_client` controls downloads. [`override.detour`](#override) controls connections through set members.

Initial downloads can use independently available outbounds, including members from inline, local or already-loaded remote sets.

#### update_interval

Update interval of outbound-set.

`1d` will be used if empty or `0`. Negative values are invalid.

Cached or initial content is refreshed immediately after startup. After a successful initial download,
the first refresh occurs after this interval. Failed updates are retried after the same interval.

### Updates

An update is applied only after its members, overrides and dependencies have been validated and new proxies have started successfully.
Failed updates retain the running set and its cached source.

Selector and URLTest membership is refreshed with the new members. A selector keeps its selected tag if it still exists;
otherwise it uses its configured `default` if available, then the first member.
A missing configured default is an error during initial configuration loading, but is allowed when members change during a refresh.

Unchanged proxies keep their instances and connection pools. Changed proxies and their dependent set members are replaced.
Established connections retain their original instances until they close. New connections use the updated configuration.
Instances used for TUN flow forwarding are retained until shutdown because TUN dispatchers keep their attached ports.
Normal selector and URLTest connection interruption settings still apply.

Updates that remove an outbound still referenced by a route, detour, HTTP client or explicit group membership are rejected.
Dependency changes that would form a cycle while old and new instances coexist are also rejected; apply those changes by reloading the configuration.

### Member tags

Members use `<set-tag>/<member-tag>` as their outbound tag. For example, `node-a` in `proxies` becomes `proxies/node-a`.
Use this tag in route rules, explicit outbound references and the selector's [`default`](/configuration/outbound/selector/#default).
Tags must not conflict with existing outbounds or endpoints.

Groups referencing the same set share its outbound instances. To apply different overrides to the same source,
define separate sets with different tags. Each set creates independent outbound instances.

### Cache

Remote sources are cached when [`experimental.cache_file.enabled`](/configuration/experimental/cache-file/#enabled) is enabled.
The cache directory is `<cache_file.path>.outbound-set`, or `cache.db.outbound-set` when the path is empty.

Cached sources are identified by cache ID, set tag and URL. Current overrides are applied when loading the cache.
Cache files use mode `0600`. Initial downloads are saved after outbound construction succeeds;
refreshes are saved after the new members have started and the update is applied.

### Example

Load `proxies.json` and connect to its proxies through `relay`:

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

See [Source Format](./source-format/) for an example of `proxies.json`.

The connection path is client → relay → selected proxy → destination.
