#!/bin/bash
set -euo pipefail
: "${DEVELOPER_ID_IDENTITY:?Set Developer ID Application signing identity}"
: "${NOTARY_PROFILE:?Set a notarytool keychain profile}"
: "${SPARKLE_PUBLIC_KEY:?Set the Sparkle Ed25519 public key}"
version=${DEADDROP_VERSION:-0.1.0}
cd "$(dirname "$0")/.."
command -v create-dmg >/dev/null || { echo 'Install create-dmg with brew install create-dmg' >&2; exit 1; }
xcodebuild -project mac/DeadDrop.xcodeproj -scheme DeadDrop -configuration Release -derivedDataPath build/Release \
 CODE_SIGN_IDENTITY="$DEVELOPER_ID_IDENTITY" CODE_SIGN_STYLE=Manual ENABLE_HARDENED_RUNTIME=YES \
 MARKETING_VERSION="$version" SPARKLE_PUBLIC_KEY="$SPARKLE_PUBLIC_KEY" build
app='build/Release/Build/Products/Release/Dead Drop.app'
test -d "$app"
codesign --verify --deep --strict --verbose=2 "$app"
mkdir -p dist
zip="dist/DeadDrop-$version.zip"
ditto -c -k --keepParent "$app" "$zip"
xcrun notarytool submit "$zip" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$app"
ditto -c -k --keepParent "$app" "$zip"
dmg="dist/DeadDrop-$version.dmg"
create-dmg --volname 'Dead Drop' --window-size 540 360 --icon-size 100 --icon 'Dead Drop.app' 130 150 --app-drop-link 400 150 "$dmg" "$app"
codesign --sign "$DEVELOPER_ID_IDENTITY" "$dmg"
xcrun notarytool submit "$dmg" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$dmg"
spctl --assess --type execute --verbose=2 "$app"
# Sparkle's tool reads the private signing key from the Keychain.
generator=$(find build/Release/SourcePackages/artifacts -type f -name generate_appcast -print -quit)
[ -n "$generator" ] || { echo 'Sparkle generate_appcast tool missing' >&2; exit 1; }
"$generator" --download-url-prefix "https://github.com/protean-labs/dead-drop/releases/download/v$version/" dist
printf 'Signed, notarized release prepared in dist. Complete docs/acceptance.md before tagging.\n'
