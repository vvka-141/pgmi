#!/usr/bin/env bash
set -euo pipefail
case "${1:-go}" in
  go) go build -o pgmi ./cmd/pgmi ;;
  rust) cargo build --locked --release -p pgmi && cp target/release/pgmi pgmi ;;
  *) echo "build-pgmi.sh: implementation must be go or rust, got $1" >&2; exit 2 ;;
esac
