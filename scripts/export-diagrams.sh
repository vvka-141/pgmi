#!/usr/bin/env bash
# Export every docs/diagrams/*.drawio to <name>.drawio.svg with embedded XML.
# Requires draw.io Desktop (https://get.diagrams.net/).
set -euo pipefail

cd "$(dirname "$0")/.."

find_drawio() {
    if command -v drawio >/dev/null 2>&1; then
        echo "drawio"
        return
    fi
    local candidates=(
        "$LOCALAPPDATA/Programs/draw.io/draw.io.exe"
        "/c/Program Files/draw.io/draw.io.exe"
        "/Applications/draw.io.app/Contents/MacOS/draw.io"
    )
    for c in "${candidates[@]}"; do
        if [ -x "$c" ]; then
            echo "$c"
            return
        fi
    done
    echo "error: draw.io Desktop not found; install from https://get.diagrams.net/" >&2
    exit 1
}

DRAWIO="$(find_drawio)"

shopt -s nullglob
sources=(docs/diagrams/*.drawio)
if [ ${#sources[@]} -eq 0 ]; then
    echo "no .drawio sources in docs/diagrams/"
    exit 0
fi

for src in "${sources[@]}"; do
    case "$(basename "$src")" in _*) continue ;; esac
    out="${src%.drawio}.drawio.svg"
    echo "export: $src -> $out"
    "$DRAWIO" -x -f svg -e -b 10 -o "$out" "$src"
    if [ -f "${src%.drawio}.drawio.png" ]; then
        echo "export: $src -> ${src%.drawio}.drawio.png"
        "$DRAWIO" -x -f png -b 10 -s 2 -o "${src%.drawio}.drawio.png" "$src"
    fi
done
