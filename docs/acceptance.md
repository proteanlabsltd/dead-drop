# Release acceptance

Automated tests do not substitute for the following checks.

- Ubuntu 24.04 amd64 and Debian 12 arm64: install the release via `scripts/install.sh`; systemd service is active, runs as configured user, and binds only its Tailscale IPs.
- A second Linux host discovers the service, uploads and downloads a file with matching SHA-256.
- Curl uploads 2 GB over the real tailnet with matching SHA-256.
- Mac discovers each host within 30 seconds; browse, mkdir, file and folder upload/download work.
- Interrupt a 1 GB transfer at 40%, reconnect, and verify automatic completion, matching SHA-256 and one final file.
- Pause, resume, retry and cancel; cancel removes only the remote partial file.
- An unlisted user and a non-tailnet connection cannot access any endpoint. The UI names the login to add to `allow_users`.
- Tailscale quit/relaunch and ten-minute Mac sleep/wake recover without an app restart.
- All traversal and concurrent symlink-swap tests pass on Linux.
- Developer ID signed and notarized DMG opens on a clean Mac without a Gatekeeper warning; repeat transfers using this release build.
- Sparkle appcast is signed, uses the bundled public key, and updates a previous installation.
- Check binary/formula name availability before tagging `v0.1.1`.

## Release procedure

Set a GitHub origin for the intended repository. Run `make test`, staticcheck and `make build-mac`, plus Linux CI. Install `create-dmg`. Configure `DEVELOPER_ID_IDENTITY`, `NOTARY_PROFILE` (using `xcrun notarytool store-credentials`), and `SPARKLE_PUBLIC_KEY`. Run `scripts/release-mac.sh`. Never put private keys or credentials in source control.

After acceptance, push tag `v0.1.1` to trigger the Linux release workflow, then attach the notarized DMG and signed appcast from `dist` to the same GitHub release. An unsigned/debug build is not a shippable macOS release.
