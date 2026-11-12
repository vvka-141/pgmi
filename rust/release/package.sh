#!/usr/bin/env bash
# Usage: package.sh <version> <bin-dir> <out-dir>
# <bin-dir>/<target>/pgmi[.exe] for every target in targets.txt. Writes the
# archives under the names the Go release used (pgmi_<version>_<os>_<arch>),
# so scripts/install.sh and install.ps1 work unchanged, plus checksums.txt.
set -euo pipefail
version="${1:?version}"; bins="$(cd "${2:?bin dir}" && pwd)"; out="${3:?out dir}"
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/../.." && pwd)"
mkdir -p "$out"; out="$(cd "$out" && pwd)"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
while read -r target os arch; do
  [[ -z "$target" ]] && continue
  exe=pgmi; [[ "$os" == windows ]] && exe=pgmi.exe
  src="$bins/$target/$exe"
  [[ -f "$src" ]] || { echo "missing $src" >&2; exit 1; }
  name="pgmi_${version}_${os}_${arch}"
  stage="$work/$name"; mkdir -p "$stage"
  cp "$src" "$stage/$exe"; chmod 0755 "$stage/$exe"
  cp "$root/LICENSE" "$root/README.md" "$stage/"
  if [[ "$os" == windows ]]; then
    (cd "$stage" && TZ=UTC touch -d @0 * && zip -qX "$out/$name.zip" "$exe" LICENSE README.md)
  else
    tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -C "$stage" -czf "$out/$name.tar.gz" "$exe" LICENSE README.md
  fi
done < "$here/targets.txt"
(cd "$out" && sha256sum pgmi_* | sort -k2 > checksums.txt)
cat "$out/checksums.txt"
