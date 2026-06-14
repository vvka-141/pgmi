#!/bin/bash
set -e

# pgmi installer for Linux and macOS
# Downloads the requested release, verifies its SHA256 against checksums.txt, then installs.
# Usage: curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash
# Or with specific version: curl -sSL ... | PGMI_VERSION=v0.10.0 bash
# Windows: use scripts/install.ps1 instead (irm .../install.ps1 | iex)

REPO="vvka-141/pgmi"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${PGMI_VERSION:-latest}"

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)
            echo "Error: Unsupported architecture: $ARCH"
            exit 1
            ;;
    esac

    case "$OS" in
        linux)  OS="linux" ;;
        darwin) OS="darwin" ;;
        *)
            echo "Error: Unsupported operating system: $OS"
            echo "Windows users: use scripts/install.ps1 instead"
            exit 1
            ;;
    esac

    echo "Detected platform: ${OS}/${ARCH}"
}

get_latest_version() {
    if [ "$VERSION" = "latest" ]; then
        VERSION=$(curl -sS "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
        if [ -z "$VERSION" ]; then
            echo "Error: Failed to fetch latest version"
            exit 1
        fi
    fi
    echo "Installing pgmi ${VERSION}"
}

verify_checksum() {
    local FILE="$1"
    local CHECKSUMS="$2"
    local EXPECTED ACTUAL

    EXPECTED=$(grep " ${FILE}\$" "$CHECKSUMS" | awk '{print $1}' | head -n1)
    if [ -z "$EXPECTED" ]; then
        echo "Error: no checksum entry for ${FILE} in checksums.txt"
        exit 1
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        ACTUAL=$(sha256sum "$FILE" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        ACTUAL=$(shasum -a 256 "$FILE" | awk '{print $1}')
    else
        echo "Error: need sha256sum or shasum to verify the download"
        exit 1
    fi

    if [ "$ACTUAL" != "$EXPECTED" ]; then
        echo "Error: checksum mismatch for ${FILE}"
        echo "  expected: ${EXPECTED}"
        echo "  actual:   ${ACTUAL}"
        exit 1
    fi
    echo "Checksum verified."
}

install_pgmi() {
    local FILENAME="pgmi_${VERSION#v}_${OS}_${ARCH}.tar.gz"
    local BASE="https://github.com/${REPO}/releases/download/${VERSION}"

    echo "Downloading ${BASE}/${FILENAME}..."

    local TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    if ! curl -sSL -o "${TMP_DIR}/${FILENAME}" "${BASE}/${FILENAME}"; then
        echo "Error: Failed to download ${BASE}/${FILENAME}"
        exit 1
    fi

    if ! curl -sSL -o "${TMP_DIR}/checksums.txt" "${BASE}/checksums.txt"; then
        echo "Error: Failed to download ${BASE}/checksums.txt"
        exit 1
    fi

    cd "$TMP_DIR"
    echo "Verifying SHA256 checksum..."
    verify_checksum "$FILENAME" "checksums.txt"

    echo "Extracting..."
    tar -xzf "$FILENAME"

    echo "Installing to ${INSTALL_DIR}..."

    if [ -w "$INSTALL_DIR" ]; then
        mv pgmi "${INSTALL_DIR}/"
    else
        echo "Note: Requires sudo to install to ${INSTALL_DIR}"
        sudo mv pgmi "${INSTALL_DIR}/"
    fi

    chmod +x "${INSTALL_DIR}/pgmi"
}

verify_install() {
    if command -v pgmi &> /dev/null; then
        echo ""
        pgmi --version
    else
        echo ""
        echo "Installed to ${INSTALL_DIR}. Add it to PATH if not already there:"
        echo "  export PATH=\"\$PATH:${INSTALL_DIR}\""
    fi
}

main() {
    echo "pgmi installer"
    echo "=============="
    detect_platform
    get_latest_version
    install_pgmi
    verify_install
}

main
