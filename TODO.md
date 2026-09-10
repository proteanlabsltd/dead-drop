# Remaining work

## Current UI fixes

- [x] Prefer the friendly Tailscale/MagicDNS server name over the OS hostname.
- [x] Correct connection status and distinguish an online Tailscale machine from an unavailable Dead Drop daemon.
- [x] Remove excess top/bottom space from the menu bar popover.

## Release preparation

- [x] Implement daemon, CLI, Mac application, transfers, installer, CI, and release scripts.
- [x] Pass local Go race tests/vet/staticcheck, Swift/Xcode tests, and unsigned app builds.
- [x] Validate live tailnet discovery, small transfers, access rejection, and filesystem confinement.
- [x] Create the GitHub repository and upload the implementation.
- [x] Review the first GitHub Actions run and resolve any runner-specific failures (Linux and macOS passed).
- [ ] Configure a Developer ID Application identity, notarization credentials, and Sparkle signing key.
- [ ] Produce and verify the signed/notarized DMG and signed Sparkle appcast.
- [ ] Validate installation on fresh Ubuntu 24.04 amd64 and Debian 12 arm64 hosts.
- [ ] Run the complete 2 GB cross-machine upload with matching SHA-256.
- [ ] Interrupt a 1 GB transfer at roughly 40%, reconnect, and verify automatic resume, matching hash, and no duplicate file.
- [ ] Verify cancellation cleanup, including when the network is unavailable.
- [ ] Verify Tailscale quit/relaunch recovery, ten-minute sleep/wake, and second-user forbidden UX.
- [ ] Complete visual UI acceptance and clean-Mac Gatekeeper testing.
- [ ] Verify Sparkle updates from an earlier installation.
- [ ] Check the binary/formula name before tagging `v0.1.0`.
- [ ] Decide public distribution or authenticated private downloads; the repository is currently private.
- [ ] Publish Linux binaries/checksums, notarized Mac DMG, and appcast; validate install links.
- [ ] Delete the hosted implementation plan only after all required acceptance checks pass.

See [verification](docs/verification.md) for completed checks and [release acceptance](docs/acceptance.md) for the procedure. Development binaries and unsigned DMGs are not signed releases.
