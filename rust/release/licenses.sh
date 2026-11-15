#!/usr/bin/env bash
# Usage: licenses.sh <out-file>
# Writes the third-party licence notices (cargo about generate, about.toml).
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
  $0 == p { for (i = 0; i < n; i++) print names[i] }' "$out" | sort -u)"
for crate in $under; do
  case " $without_notice " in
    *" $crate "*) ;;
    *) echo "licenses.sh: $crate has no copyright notice in $out: add a clarify entry to about.toml" >&2; exit 1 ;;
  esac
done
notices='Copyright (c) 2017 Pierre Chifflier (asn1-rs-impl)\
Copyright (c) the authors of the other crates listed above, which publish no MIT copyright notice'
sed -i "s/^$placeholder\$/$notices/" "$out"
! grep -qF '<copyright holders>' "$out"
