### Structure

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

### Fields

#### outbound_set

List of [outbound-set](/configuration/outbound-set/) tags to balance. Also accepts a single tag.

Members are appended after `outbounds`, in set and source order. Repeated member tags are included only once.

#### outbounds

==Required if `outbound_set` is empty==

List of outbound tags to balance.

#### strategy

The balancing strategy. `consistent_hash` will be used if empty.

| Value             | Description                                                                    |
|-------------------|--------------------------------------------------------------------------------|
| `consistent_hash` | Select by `hash_key` using rendezvous hashing. |
| `round_robin`     | Select each eligible outbound in turn, with separate counters for TCP and UDP.   |

Reordering members does not change selection. Adding or removing a member only remaps connections affected by that member.

An outbound is selected for each TCP connection or UDP association and remains selected for its lifetime.
Only outbounds supporting the requested network are eligible. Dial failures are returned without retrying another member.
ICMP and layer 3 forwarding are not supported.

#### hash_key

==Only available when `strategy` is `consistent_hash`==

The key used for hashing. `source_ip_domain` will be used if empty.

| Value                | Description                                                 |
|----------------------|-------------------------------------------------------------|
| `source_ip_domain`   | Source IP address and registrable destination domain.        |
| `source_ip_host`     | Source IP address and full destination hostname.             |
| `destination_domain` | Registrable destination domain, without the source address. |

Registrable domains are determined using the Public Suffix List, including private suffixes.
For example, `login.example.co.uk` and `api.example.co.uk` use `example.co.uk`,
while `alice.github.io` and `bob.github.io` remain separate.
Hostnames without a registrable domain, such as `localhost`, are used unchanged.

All modes ignore ports. Hostnames are case-insensitive, a trailing dot is ignored, and internationalized domain names are converted to ASCII.
When no hostname is available, the destination IP address is used. When the source IP address is unavailable, only the destination contributes to the key.

The default keeps related subdomains on the same outbound for a given client, while allowing different clients to use different outbounds.
Changing `hash_key` can change selection for new connections. Existing connections are unaffected.

#### url

The URL used for health checks. `https://www.gstatic.com/generate_204` will be used if empty.

Checks run at startup and after membership or network changes. Members with failed checks are excluded from new selections.
Members that have not been checked remain eligible. If no eligible member exists, new connections fail.

Health results for nested selectors apply only to the selection that was tested. A new selection remains eligible while it is checked.

HTTP checks do not verify UDP connectivity. UDP-only members remain eligible without an HTTP check.

#### interval

The health check interval. `3m` will be used if empty.

Must not exceed `idle_timeout`.

#### idle_timeout

The idle timeout. `30m` will be used if empty.

Periodic health checks pause after no connections have been selected for this duration. A new connection resumes checks.

### Outbound-set updates

Membership follows outbound-set refreshes. Unchanged members retain their health state; new or replaced members start unchecked.
Health checks run again after an update. Existing connections continue using their original outbound.

### Status

A load balancer has no single selected outbound. Inspecting the group does not select a member or advance round-robin counters.
The Clash API reports an empty `now` value and a `health` map keyed by member tag, with `unknown`, `healthy`, or `unhealthy` status.

Daemon group responses report each member’s test time and delay from this group’s health checks. Results are kept separate when groups use different test URLs.
