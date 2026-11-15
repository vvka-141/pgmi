#!/usr/bin/env bash
# Runs package.sh and licenses.sh on THIRD-PARTY-LICENSES files with
# and without a licence template placeholder.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
mkdir -p "$work/rust/release" "$work/bin/x86_64-unknown-linux-musl"
cp "$here/package.sh" "$here/placeholders.sh" "$work/rust/release/"
echo "x86_64-unknown-linux-musl linux amd64" > "$work/rust/release/targets.txt"
: > "$work/bin/x86_64-unknown-linux-musl/pgmi"
: > "$work/LICENSE"; : > "$work/README.md"
fail=0
check() {
  local want="$1" line="$2"
  printf 'MIT License\n\n%s\n\nPermission is hereby granted\n' "$line" > "$work/THIRD-PARTY-LICENSES"
  local got=0
  "$work/rust/release/package.sh" 0.0.0 "$work/bin" "$work/out" >/dev/null 2>&1 || got=$?
  if [[ "$got" != "$want" ]]; then
    echo "placeholders-test: package.sh exit $got, want $want, for: $line" >&2; fail=1
  fi
}
check 1 'Copyright (c) <year> <copyright holders>'
check 1 'Copyright (c) <year> <owner>'
check 1 'Copyright <year> <owner>'
check 1 '    Copyright (c) <YEAR> <OWNER>'
check 0 'Copyright (c) 2017 Pierre Chifflier'
check 0 'Copyright (c) 2020 Jane Doe <jane@example.com>'
mkdir -p "$work/fakebin"
cat > "$work/fakebin/cargo" <<'FAKE'
#!/usr/bin/env bash
while [[ $# -gt 0 && "$1" != -o ]]; do shift; done
cp "$FIXTURE" "$2"
FAKE
chmod +x "$work/fakebin/cargo"
cp "$here/licenses.sh" "$work/rust/release/"
generate() {
  local want="$1" crate="$2" line="$3"
  printf 'MIT License\n\nUsed by:\n  %s 1.0.0\n\n%s\n\nPermission is hereby granted\n' "$crate" "$line" > "$work/fixture"
  local got=0
  FIXTURE="$work/fixture" PATH="$work/fakebin:$PATH" "$work/rust/release/licenses.sh" "$work/generated" >/dev/null 2>"$work/err" || got=$?
  if [[ "$got" != "$want" ]]; then
    echo "placeholders-test: licenses.sh exit $got, want $want, for $crate: $line" >&2; fail=1
  elif [[ "$want" != 0 ]] && ! grep -qF "$crate has no copyright notice" "$work/err"; then
    echo "placeholders-test: licenses.sh did not name $crate" >&2; fail=1
  fi
}
generate 0 p12-keystore 'Copyright (c) <year> <copyright holders>'
generate 1 some-crate 'Copyright (c) <year> <copyright holders>'
generate 1 p12-keystore 'Copyright (c) <year> <owner>'
generate 1 some-crate 'Copyright <year> <owner>'
generate 0 some-crate 'Copyright (c) 2019 Jane Doe'
exit "$fail"
