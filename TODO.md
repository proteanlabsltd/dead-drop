# Release readiness

Source version: 0.1.1. These checks remain before distributing a verified release.

- [ ] Configure Developer ID signing, notarization, and the Sparkle signing key.
- [ ] Build and verify a signed, notarized macOS DMG and signed appcast.
- [ ] Validate fresh Ubuntu 24.04 amd64 and Debian 12 arm64 installations.
- [ ] Complete a 2 GB cross-machine transfer with matching SHA-256.
- [ ] Interrupt a 1 GB transfer, reconnect, and verify resume without duplicates.
- [ ] Verify cancellation cleanup when the network is unavailable.
- [ ] Verify Tailscale restart, sleep/wake recovery, and forbidden-user feedback.
- [ ] Complete visual acceptance, clean-Mac Gatekeeper, and Sparkle update checks.
- [ ] Publish release assets and verify the installation links.

See [release acceptance](docs/acceptance.md) for the full procedure. Automated tests and unsigned builds do not replace these checks.
