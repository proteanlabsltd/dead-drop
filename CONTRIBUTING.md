# Contributing

Bug reports and focused pull requests are welcome. Include reproduction steps, platform versions, expected behavior, and relevant logs with private paths and tailnet identities removed.

## Build and test

The daemon requires Go 1.25 or newer. The macOS app requires macOS 14 or newer and Xcode 16 or newer. GoReleaser is needed only for release validation and packaging.

```sh
git clone https://github.com/proteanlabsltd/dead-drop.git
cd dead-drop
make test-go
make build-daemon
./build/deaddrop version
```

On macOS:

```sh
make test-mac
make build-mac
open "build/DerivedData/Build/Products/Release/Dead Drop.app"
```

The app builds unsigned for local development. The checked-in Xcode project allows builds without XcodeGen. After changing `mac/project.yml`, regenerate it with `xcodegen generate --spec mac/project.yml` and commit both files.

## Repository layout

- `daemon/cmd/deaddrop`: CLI entry point.
- `daemon/internal`: HTTP API, transfer client, configuration, filesystem confinement, installation, server, and Tailscale authorization.
- `mac/DeadDrop`: SwiftUI menu bar application.
- `mac/DeadDropKit`: client, discovery, transfers, and Swift tests.
- `scripts`: Linux installation and macOS release packaging.
- `docs`: protocol and release acceptance.

Keep changes focused, format Go with `gofmt`, and add regression tests for behavior changes. Run `make test` and `make build-mac` on macOS; Linux-only contributors can run `make test-go`. CI also runs staticcheck and Linux cross-compilation.

## Live integration tests

Swift integration tests are skipped by default. To run them, set `DEADDROP_DISCOVERY_HOST` to a daemon's Tailscale IP, and set `DEADDROP_TEST_HOST` and `DEADDROP_TEST_ROOT` to an authorized daemon and a disposable exposed directory. Run `make test-mac`. The round-trip test leaves a uniquely named `kit-*` directory on the remote host; remove it there afterward.

## Releases

Update the daemon version, installer and macOS release script defaults, `mac/project.yml`, and release documentation together. Increment `CURRENT_PROJECT_VERSION` for Sparkle updates and regenerate the Xcode project. The Settings screen reads the bundle version. GoReleaser injects the release tag's version into Linux binaries.

Follow [release acceptance](docs/acceptance.md) before tagging. Keep credentials, signing keys, generated artifacts, and personal configuration out of commits.

Contributions are provided under the repository's [MIT license](LICENSE).
