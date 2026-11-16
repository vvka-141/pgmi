#!/usr/bin/env bash
# Usage: licenses.sh <out-file>
# Writes the third-party licence notices (cargo about generate, about.toml),
# then the notices of what the toolchain links into every binary and
# cargo-about cannot see: the Rust standard library with its dependencies
# (the toolchain's own COPYRIGHT-library.html, as text) and musl libc, which
# is linked statically into the Linux binaries (notices/musl-*-COPYRIGHT,
# musl's own file for the version the pinned toolchain bundles).
# cargo-about fills the MIT text of a crate that publishes no licence file
# with the SPDX placeholder "<year> <copyright holders>". Of the crates
# below, asn1-rs-impl has a notice in its repository that cargo-about cannot
# fetch (its repository address ends in .git), written out here; the others
# publish no MIT copyright notice in their archive or repository, and the
# line says so. Any other crate under the placeholder fails the run: give it
# a clarify entry in about.toml, or add it here once its notice is known.
set -euo pipefail
out="${1:?output file}"
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/../.." && pwd)"
without_notice="asn1-rs-impl objc2-core-foundation objc2-system-configuration p12-keystore"
cd "$root"
cargo about generate --locked --fail -m rust/pgmi/Cargo.toml about.hbs -o "$out"
placeholder='Copyright (c) <year> <copyright holders>'
under="$(awk -v p="$placeholder" '
  /^Used by:$/ { n = 0; listing = 1; next }
  listing && /^  / { names[n++] = $1; next }
  { listing = 0 }
  tolower($0) ~ /^[ \t]*copyright.*<(year|owner|copyright holders?)>/ {
    for (i = 0; i < n; i++) print names[i], ($0 == p ? "mit" : "other")
  }' "$out" | sort -u)"
while read -r crate kind; do
  [[ -z "$crate" ]] && continue
  case "$kind: $without_notice " in
    "mit:"*" $crate "*) ;;
    *) echo "licenses.sh: $crate has no copyright notice in $out: add a clarify entry to about.toml" >&2; exit 1 ;;
  esac
done <<< "$under"
notices='Copyright (c) 2017 Pierre Chifflier (asn1-rs-impl)\
Copyright (c) the authors of the other crates listed above, which publish no MIT copyright notice'
sed -i "s/^$placeholder\$/$notices/" "$out"
std="$(rustc --print sysroot)/share/doc/rust/COPYRIGHT-library.html"
musl="$(echo "$here"/notices/musl-*-COPYRIGHT)"
for f in "$std" "$musl"; do
  [[ -s "$f" ]] || { echo "licenses.sh: $f is missing: the toolchain's notices cannot be added to $out" >&2; exit 1; }
done
rule="================================================================================"
{
  printf '%s\nThe Rust standard library and its dependencies (rustc %s)\n\nLinked into every pgmi binary. pgmi uses the standard library under the MIT\nlicence. The text below is COPYRIGHT-library.html of that toolchain.\n\n' "$rule" "$(rustc --version | cut -d' ' -f2)"
  sed -E -e 's/<[^>]*>//g' -e 's/&#34;/"/g; s/&#39;/'"'"'/g; s/&#60;/</g; s/&#62;/>/g; s/&amp;/\&/g' -e 's/[[:space:]]+$//' "$std" | cat -s
  printf '\n%s\nmusl libc %s\n\nLinked statically into the Linux binaries (the *-unknown-linux-musl targets).\n\n' "$rule" "$(basename "$musl" | cut -d- -f2)"
  cat "$musl"
} >> "$out"
grep -q '^Copyright notices for The Rust Standard Library$' "$out" || { echo "licenses.sh: $std did not give the standard library's notices" >&2; exit 1; }
if grep -q '&#\?[a-z0-9]*;' <(sed -n "/^The Rust standard library and its dependencies/,\$p" "$out"); then
  echo "licenses.sh: an HTML entity is left in the standard library's notices in $out" >&2; exit 1
fi
"$here/placeholders.sh" "$out"
