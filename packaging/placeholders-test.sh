#!/usr/bin/env bash
# Runs package.sh and licenses.sh on THIRD-PARTY-LICENSES files with
# and without a licence template placeholder.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
mkdir -p "$work/packaging" "$work/bin/x86_64-unknown-linux-musl"
cp "$here/package.sh" "$here/placeholders.sh" "$work/packaging/"
echo "x86_64-unknown-linux-musl linux amd64" > "$work/packaging/targets.txt"
: > "$work/bin/x86_64-unknown-linux-musl/pgmi"
: > "$work/LICENSE"; : > "$work/README.md"
fail=0
check() {
  local want="$1" line="$2"
  printf 'MIT License\n\n%s\n\nPermission is hereby granted\n' "$line" > "$work/THIRD-PARTY-LICENSES"
  local got=0
  "$work/packaging/package.sh" 0.0.0 "$work/bin" "$work/out" >/dev/null 2>&1 || got=$?
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
cp "$here/licenses.sh" "$work/packaging/"
mkdir -p "$work/packaging/notices" "$work/sysroot/share/doc/rust"
printf 'musl as a whole is licensed under the following standard MIT license:\nCopyright (c) 2005-2020 Rich Felker, et al.\n' > "$work/packaging/notices/musl-9.9.9-COPYRIGHT"
std="$work/sysroot/share/doc/rust/COPYRIGHT-library.html"
printf '<h1>Copyright notices for The Rust Standard Library</h1>\n<p>Copyright: The Rust Project Developers &#60;a&#62; &#34;q&#34; &amp; &#39;s&#39;</p>\n' > "$std"
cat > "$work/fakebin/rustc" <<FAKE
#!/usr/bin/env bash
case "\$1" in --print) echo "$work/sysroot" ;; *) echo "rustc 9.8.7 (fake)" ;; esac
FAKE
chmod +x "$work/fakebin/rustc"
generate() {
  local want="$1" crate="$2" line="$3"
  printf 'MIT License\n\nUsed by:\n  %s 1.0.0\n\n%s\n\nPermission is hereby granted\n' "$crate" "$line" > "$work/fixture"
  local got=0
  FIXTURE="$work/fixture" PATH="$work/fakebin:$PATH" "$work/packaging/licenses.sh" "$work/generated" >/dev/null 2>"$work/err" || got=$?
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
toolchain() {
  local what="$1"; shift
  for needed in "$@"; do
    grep -qxF -- "$needed" "$work/generated" || { echo "placeholders-test: licenses.sh output lacks the $what line: $needed" >&2; fail=1; }
  done
}
toolchain "standard library" 'The Rust standard library and its dependencies (rustc 9.8.7)' 'Copyright notices for The Rust Standard Library' "Copyright: The Rust Project Developers <a> \"q\" & 's'"
toolchain "musl" 'musl libc 9.9.9' 'Copyright (c) 2005-2020 Rich Felker, et al.'
missing() {
  local what="$1" file="$2" got=0
  mv "$file" "$file.away"
  FIXTURE="$work/fixture" PATH="$work/fakebin:$PATH" "$work/packaging/licenses.sh" "$work/generated" >/dev/null 2>"$work/err" || got=$?
  mv "$file.away" "$file"
  if [[ "$got" != 1 ]] || ! grep -q "is missing" "$work/err"; then
    echo "placeholders-test: licenses.sh exit $got without $what, want 1 naming the missing file" >&2; fail=1
  fi
}
missing "the standard library's notices" "$std"
missing "musl's notice" "$work/packaging/notices/musl-9.9.9-COPYRIGHT"
exit "$fail"
