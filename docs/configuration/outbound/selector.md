### Structure

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

    The selector can only be controlled through the [Clash API](/configuration/experimental#clash-api-fields) currently.

### Fields

#### outbound_set

List of [outbound-set](/configuration/outbound-set/) tags to select from. Also accepts a single tag.

Members are appended after `outbounds`, in set and source order. Repeated member tags are included only once.

#### outbounds

==Required if `outbound_set` is empty==

List of outbound tags to select.

#### default

The default outbound tag. Must be a member of `outbounds` or a referenced `outbound_set`.

For an outbound-set member, use `<set-tag>/<member-tag>`, such as `proxies/node-a`:

```json
{
  "type": "selector",
  "tag": "select",
  "outbound_set": ["proxies"],
  "default": "proxies/node-a"
}
```

The first outbound will be used if empty. Explicit `outbounds` are listed before outbound-set members.
If only `outbound_set` is configured, the first member of the first set is used.

!!! note ""

    When the cache file is enabled, a saved selection takes precedence over `default` if it is still a member.
    Otherwise, `default` or the first member is used.

When an outbound-set refresh removes the selected member, the configured default is used if it remains available,
otherwise the first member is selected.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
