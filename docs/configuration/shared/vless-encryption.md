### Structure

VLESS outbound:

```json
{
  "encryption": "mlkem768x25519plus.native.0rtt.<public-key>"
}
```

VLESS inbound:

```json
{
  "decryption": "mlkem768x25519plus.native.600s.<private-key>"
}
```

Omitted, empty, or `none` disables encryption. Leave `flow` empty when enabled.

### Fields

#### Mode

The second component selects the mode. Both sides must match.

| Mode | Description |
| --- | --- |
| `native` | No additional obfuscation. |
| `xorpub` | Obfuscate the public key exchange. |
| `random` | Obfuscate the public key exchange and record headers. |

#### Handshake

Client: `1rtt` always performs a full handshake. `0rtt` allows session resumption after the first full handshake.

Server: ticket lifetime in seconds, such as `600s`, or an inclusive range such as `300-600s`.
Values must be within 0–65535. A single value selects between half and the full value; `600-600s` fixes the lifetime.
`0s` disables resumption.

#### Key

One server key, encoded as unpadded base64url:

| Key type | Client public key | Server private key |
| --- | --- | --- |
| X25519 | 32 bytes | 32 bytes |
| ML-KEM-768 | 1184 bytes | 64-byte seed |

Relay-key chains and custom handshake padding are not supported.
