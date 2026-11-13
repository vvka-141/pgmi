#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
banned='^(cc|cmake|bindgen|ring|aws-lc-sys|aws-lc-fips-sys|openssl-sys|openssl-src|libz-sys|zstd-sys|pkg-config|vcpkg)$'
export CC=/bin/false CXX=/bin/false AR=/bin/false
export TARGET_CC=/bin/false TARGET_CXX=/bin/false TARGET_AR=/bin/false
export HOST_CC=/bin/false HOST_CXX=/bin/false HOST_AR=/bin/false
status=0
while read -r target _; do
    found=$(cargo tree --locked --workspace --all-features --target "$target" -e normal,build \
        --prefix none --format '{p}' | awk '{print $1}' | sort -u | grep -E "$banned" || true)
    if [ -n "$found" ]; then
        echo "check-no-c: $target builds C or links native code through: $(echo "$found" | tr '\n' ' ')" >&2
        status=1
        continue
    fi
    under=${target//-/_}
    env "CC_$target=/bin/false" "CXX_$target=/bin/false" "AR_$target=/bin/false" \
        "CC_$under=/bin/false" "CXX_$under=/bin/false" "AR_$under=/bin/false" \
        cargo check --locked --release --workspace --all-features --target "$target"
done < rust/release/targets.txt
exit "$status"
