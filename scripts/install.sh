#!/bin/sh
set -eu
[ "$(uname -s)" = Linux ] || { echo 'The daemon requires Linux.' >&2; exit 1; }
case "$(uname -m)" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
version=${DEADDROP_VERSION:-0.1.1}
case "$version" in *[!0-9.]*|'') echo 'Invalid version' >&2; exit 1;; esac
service_user=${SUDO_USER:-$(id -un)}
[ "$service_user" != root ] || { echo 'Run as the user whose files should be exposed (the installer will use sudo).' >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
base="https://github.com/proteanlabsltd/dead-drop/releases/download/v$version"
archive="deaddrop_${version}_linux_${arch}.tar.gz"
curl --fail --silent --show-error --location "$base/$archive" -o "$tmp/$archive"
curl --fail --silent --show-error --location "$base/checksums.txt" -o "$tmp/checksums.txt"
(cd "$tmp" && awk -v name="$archive" '$2 == name { print; found=1 } END { if (!found) exit 1 }' checksums.txt > selected-checksum && sha256sum --check selected-checksum && tar xzf "$archive" deaddrop)
sudo "$tmp/deaddrop" install --user "$service_user" "$@"
