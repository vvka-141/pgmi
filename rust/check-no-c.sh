#!/usr/bin/env bash
set -euo pipefail
export CC=/bin/false CXX=/bin/false AR=/bin/false
for target in x86_64-unknown-linux-musl aarch64-unknown-linux-musl x86_64-pc-windows-msvc aarch64-apple-darwin x86_64-apple-darwin; do
    cargo check --locked --release --workspace --target "$target"
done
