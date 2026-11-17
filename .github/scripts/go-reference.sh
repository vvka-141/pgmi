#!/usr/bin/env bash
# Downloads the last Go release, 0.14.1, the reference the differential,
# bad-weather and cross-binary checksum cases compare the Rust binary with.
# Usage: go-reference.sh <output path>
set -euo pipefail
out="${1:?usage: go-reference.sh <output path>}"
archive=pgmi_0.14.1_linux_amd64.tar.gz
sha256=101f445e3222019e2516a2aaec0b86c0de59fcfbd6cc03fc1703ba1e6c61d5d0
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
curl -fsSL --retry 3 -o "$dir/$archive" "https://github.com/vvka-141/pgmi/releases/download/v0.14.1/$archive"
echo "$sha256  $dir/$archive" | sha256sum -c --quiet
tar -xzf "$dir/$archive" -C "$dir" pgmi
install -m 0755 "$dir/pgmi" "$out"
"$out" --version | head -1
