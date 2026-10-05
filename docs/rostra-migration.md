# Migration to Rostra

Rostra is an independently maintained fork of sing-box. It is not affiliated with
or endorsed by SagerNet. Upstream copyright and license notices remain in place.

## Names and paths

| Item | Upstream | Rostra |
| --- | --- | --- |
| Command | `sing-box` | `rostra` |
| Go module | `github.com/sagernet/sing-box` | `github.com/fig0ut/rostra` |
| Systemd service | `sing-box.service` | `rostra.service` |
| Systemd service account | `root` | `root` |
| Packaged configuration | `/etc/sing-box` | `/etc/rostra` |
| Packaged state | `/var/lib/sing-box` | `/var/lib/rostra` |
| Local script configuration | `/usr/local/etc/sing-box` | `/usr/local/etc/rostra` |
| Container image | `ghcr.io/sagernet/sing-box` | `ghcr.io/fig0ut/rostra` |

OpenWrt packages use `/etc/config/rostra`, `/etc/rostra`, and `/usr/share/rostra`.
OpenRC uses `/etc/conf.d/rostra`; environment overrides are `ROSTRA_CONFIG` and
`ROSTRA_WORKDIR`. Shell completions are generated for `rostra`.

## Existing installations

1. Back up configuration, certificates, keys, and state before changing services.
2. Install Rostra and copy the files needed by your configuration to the new paths.
   Update absolute paths and ownership for the new service account. Package installation
   does not automatically move or delete upstream configuration or state.
3. Validate the configuration with `rostra check -C /etc/rostra`.
4. Stop the old service before starting Rostra when they share ports, TUN interfaces,
   routing rules, or other network resources. Stop the old process cleanly so it can
   remove its network rules.
5. Start `rostra.service`, check its logs, and update monitoring and automation to the new name.

The `release/local` scripts install to `/usr/local/bin` and `/usr/local/etc/rostra`;
use the appropriate configuration path when validating that installation.

Configuration fields, protocol names, and rule-set formats retain their upstream
identifiers. Existing configuration may still need changes for independently added
features or upstream deprecations.

## Development and releases

Build from this checkout with `make build` or `make install`. The command source is
`cmd/rostra`. Import this fork as `github.com/fig0ut/rostra`; dependent projects
must update their imports when moving to the new module identity.

Version numbers currently follow the inherited Git tags. `rostra version` identifies
the fork; historical changelog entries and compatibility version references refer
to upstream sing-box.

The documentation is maintained in this repository; no separately hosted Rostra docs
site is assumed. Schema URLs follow the repository's default branch, so changes become
available there after they are merged and pushed.

The Linux package workflow uploads to Gemfury only when the repository variable
`FURY_ACCOUNT` and secret `FURY_TOKEN` are configured for your own account.
Container publishing targets `ghcr.io/fig0ut/rostra`.

The graphical client submodules remain upstream projects. They are not rebranded
Rostra applications, and integration with the renamed Go module needs separate work.
Their application release jobs require an explicit `ENABLE_UPSTREAM_CLIENT_BUILDS`
repository variable set to `true`.
