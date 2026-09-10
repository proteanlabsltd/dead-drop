# Dead Drop

Browse and transfer files between your own Tailscale machines, without syncing. The Linux daemon exposes configured directories; the macOS 14+ menu bar app discovers it on your tailnet. No Dead Drop account or relay.

This is a v0.1 implementation under validation. Release URLs below become available when a verified release is published; no release has been published from this workspace.

## Install

Linux (already running Tailscale and systemd):

```sh
curl -fsSL https://raw.githubusercontent.com/protean-labs/dead-drop/main/scripts/install.sh | sh
```

Mac (download the signed DMG from the release):

```sh
open https://github.com/protean-labs/dead-drop/releases/latest
```

The installer uses sudo to install a static binary and systemd unit, then runs the daemon as your ordinary user. It preserves and validates an existing configuration when upgrading. Default exposed root: that user's home. Configure `/etc/deaddrop/config.toml`:

```toml
port = 7477
roots = ["/home/axl", "/srv"]
allow_users = ["axl@github"]
allow_tags = []
follow_symlinks_outside_root = false
```

If `allow_users` is omitted, the daemon allows the login owning the Tailscale node. A tagged server with no owner needs explicit allowed users or tags. Roots must exist. Restart `deaddrop.service` after changing configuration. The outside-root symlink setting cannot be enabled in v0.1.

## CLI

```sh
deaddrop serve --root "$HOME"
deaddrop status
deaddrop hosts
deaddrop ls vps:/home/axl
deaddrop put ./dump.sql vps:/home/axl/backups/ --overwrite
deaddrop get vps:/home/axl/dump.sql ./
deaddrop mkdir vps:/home/axl/new
```

Hosts may be MagicDNS names, IPv4, `[IPv6]`, or `host:port`. Upload and download retries resume partial files. Folders transfer recursively. CLI download refuses existing destinations. Recursive symlinks are refused to avoid cycles. The Mac app provides the host picker, directory browser, Finder file drops, downloads, transfer controls and Settings.

## Security

HTTP travels inside Tailscale's WireGuard tunnel, directly, with no HTTP proxy or redirect following. The daemon binds only the node's Tailscale addresses; it never falls back to a wildcard interface. Every request is authenticated through tailscaled whois. Any process on an allowed peer can use the service: this is a peer identity boundary, not per-process authorization. Use Tailscale ACLs and a dedicated service user to limit access further.

Filesystem operations are confined to configured roots, including symlink resolution. Uploads remain in `.deaddrop-part` files until finalized. Cancel deletes only an upload partial, never a final file. Ordinary Unix permissions also apply. Never expose `/` or run the service as root. The optional loopback development mode is unauthenticated and must only be used for local testing.

## Development

Install Go 1.25+ (required for race-safe descriptor-relative filesystem operations; see `daemon/go.mod`), Xcode 16+ and GoReleaser. Then:

```sh
make test
make build-daemon
make build-mac
goreleaser check
```

See [wire protocol](docs/protocol.md), [release acceptance](docs/acceptance.md), and the archived [implementation plan](docs/implementation-plan.txt). Build artifacts go in `build`/`dist`; no credentials are stored in this repository.
