### Structure

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
  "tolerance": 0,
  "idle_timeout": "",
  "interrupt_exist_connections": false
}
```

### Fields

#### outbound_set

List of [outbound-set](/configuration/outbound-set/) tags to test. Also accepts a single tag.

Members are appended after `outbounds`, in set and source order. Repeated member tags are included only once.

#### outbounds

==Required if `outbound_set` is empty==

List of outbound tags to test.

#### url

The URL to test. `https://www.gstatic.com/generate_204` will be used if empty.

#### interval

The test interval. `3m` will be used if empty.

#### tolerance

The test tolerance in milliseconds. `50` will be used if empty.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

#### interrupt_exist_connections

Interrupt existing connections when the selected outbound has changed.

Only inbound connections are affected by this setting, internal connections will always be interrupted.
