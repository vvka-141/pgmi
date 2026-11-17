#!/usr/bin/env bash
set -euo pipefail
cargo build --locked --release -p pgmi
cp target/release/pgmi pgmi
