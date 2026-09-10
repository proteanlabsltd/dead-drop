# Implementation and verification — 2026-09-10

The repository was empty when implementation began. Work was split among three GPT-5.6-sol agents and the coordinating agent. The hosted plan remains available: release and manual acceptance are not complete.

## Implemented

- Static Go daemon and CLI: confined roots, Tailscale LocalAPI authorization/cache, tailnet address rebinding, all v1 endpoints, ranged reads, resumable uploads, SHA-256 verification, atomic finalization, partial cancellation, discovery, transfers, systemd installer and config.
- Swift 6 package and macOS menu bar app: automatic/manual discovery, root browser, upload/drop and downloads, recursive folders, conflict prompt, transfer queue/progress/pause/cancel/retry, settings, login item, wake/network refresh, notifications, and Sparkle integration.
- Build/test targets, GitHub Actions, Linux release configuration, installer, signing/notarization/appcast script, protocol and release documentation.

## Successful checks

- `make test`: Go race tests and vet; Swift package unit tests (live integrations are opt-in and were also run separately).
- `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...`: passed.
- `xcodebuild test -scheme DeadDropKit -destination 'platform=macOS' CODE_SIGNING_ALLOWED=NO`: passed.
- `make build-mac`: unsigned macOS application built successfully. Only the expected missing-App-Intents metadata warning remained.
- Linux test executables for jail, API, auth, server, config, client, installer and CLI ran successfully on the actual Ubuntu 26.04 host.
- Static Linux amd64 and arm64 builds; `goreleaser check`; GoReleaser snapshot archives and checksums: passed.
- Tailnet destination restrictions reject public IP/DNS results; IPv6-only peers are supported. App ATS is explicitly configured for the HTTP-over-WireGuard protocol.
- Shell syntax checks for installer and release script: passed.
- Development DMG creation and `hdiutil verify`: passed. This DMG is explicitly unsigned and is not a release artifact suitable for Gatekeeper acceptance.
- Actual Tailscale discovery located the Linux daemon on port 7477 in 0.195 seconds after the final destination restrictions.
- Live Swift integration: 1 MiB upload/read equality, partial cancellation, TransferManager upload/download roundtrip: passed, including after final transfer and cleanup changes (6.410 seconds).
- Live Go CLI: 1 MiB random payload uploaded and downloaded over the tailnet; byte comparison passed.
- Final absolute-symlink and concurrent-swap jail/API suites passed again on Linux after the resolver changes.
- Live authentication: disallowed caller received 403 for info, ls, stat, read, PUT/HEAD/DELETE write, mkdir and unknown routing. Outside-root ls/stat/read rejected with 400.
- Interrupted Mac-to-Linux transfer: retained 151,864,500 bytes, finalized at that offset, SHA-256 `e901fc0039c4ba9f1d232bb2e6021f034bc523b059971380469e6d64de620c7d` matched.
- Linux-local upload through its Tailscale address: 2,147,483,648 bytes; source and destination SHA-256 `a7c744c13cc101ed66c29f672f92455547889cc586ce6d44fe76ae824958ea51` matched. This does not substitute for the full 2 GB cross-machine acceptance test.

All temporary remote validation daemons and test files were removed after testing. No existing system services were modified.

## Required before completion and release

- Developer ID Application certificate, notarization credentials and Sparkle signing key are not configured. Only Apple Development/Distribution identities were available; no release signing or notarization was attempted.
- The implementation and remaining-work checklist are uploaded to the private [proteanlabsltd/dead-drop repository](https://github.com/proteanlabsltd/dead-drop). No release tag or release assets have been published; installer release URLs remain prospective. The original plan used a different organization name, so current module and release URLs have been corrected.
- Available remote host runs Ubuntu 26.04 amd64. Fresh Ubuntu 24.04 amd64 and Debian 12 arm64 systemd installations still need acceptance.
- The complete 1 GB Wi-Fi interruption/reconnect scenario, full 2 GB cross-machine curl upload, ten-minute sleep/wake, Tailscale quit/relaunch, second-user UX, clean-Mac Gatekeeper and Sparkle update acceptance remain outstanding.
- Native UI automation timed out; programmatic builds and client tests passed, but full visual UI acceptance is not claimed.

See [acceptance.md](acceptance.md) for the release procedure. Keep the hosted plan until every required acceptance check succeeds.

## Deliberate implementation details

Go 1.25+ is required for the standard library's descriptor-relative `os.Root` Link/Rename operations. This avoids a separate platform-specific jail dependency and confines symlink traversal during operations. Relative and absolute symlinks are resolved within configured roots with a bounded walk; final descriptor-relative operations fail closed on concurrent swaps. A 3,000-iteration symlink-swap stress test verifies that outside bytes are never read. Existing hardlinks are subject to ordinary Unix permissions: a malicious local process running as the same service user already has equivalent direct filesystem access.

Uploads use bounded 4 MiB, file-backed URLSession tasks with progress delegates. Downloads retain completed ranged chunks, so interrupted work resumes from the persisted offset without buffering a large file in memory. Final publication without overwrite uses an atomic hard link followed by partial removal; overwrite uses atomic rename.

## Repository upload and UI follow-up

Uploaded implementation and [TODO.md](../TODO.md) to the private `proteanlabsltd/dead-drop` repository. The first GitHub Actions run passed both Linux and macOS jobs. Current module, installer and release URLs use the actual organization; the archived plan is preserved as originally supplied.

The browser now prefers MagicDNS names (`athena`, `hestia`), preserves them after daemon probes, distinguishes Tailscale-offline peers from online peers whose Dead Drop daemon is unavailable, and rejects stale successful/failed refresh results. Matching manual entries no longer override Tailscale names/status. Folder loads also ignore obsolete results; redundant wake/network observers were removed.

The content pane fills the available height, pinning the header/footer to the popover edges. An offscreen 360×480 pt render confirmed the header begins at the intended 12 pt inset and the footer reaches the bottom, with no large empty top/bottom bands. This synthetic render verifies layout, not full native UI acceptance.

Swift package regression tests: 17 tests, zero failures, two expected opt-in integration skips. Local Go race tests/vet passed after the module-path correction. At diagnosis both real Linux peers were online in Tailscale, but neither accepted connections on port 7477; no daemon was installed or started as part of these UI fixes.
