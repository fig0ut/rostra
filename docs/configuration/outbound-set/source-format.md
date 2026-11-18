### Structure

```json
{
  "version": 1,
  "outbounds": []
}
```

### Fields

#### version

==Required==

Version of outbound-set. Only `1` is supported.

#### outbounds

==Required==

Nonempty list of [Outbound](/configuration/outbound/) definitions.

Each outbound must have a unique, nonempty `tag`. Selector and URLTest outbounds are not supported as members.

Members are registered with a set prefix. See [Member tags](../#member-tags) for details.

### Example

`proxies.json`:

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
