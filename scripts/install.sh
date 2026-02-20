#!/bin/bash
set -e

# pgmi installer for Linux and macOS
# Usage: curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash
# Or with specific version: PGMI_VERSION=v1.0.0 curl -sSL ... | bash
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

install_pgmi() {
    local FILENAME="pgmi_${VERSION#v}_${OS}_${ARCH}.tar.gz"
    local URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

    echo "Downloading ${URL}..."

    local TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    if ! curl -sSL -o "${TMP_DIR}/${FILENAME}" "$URL"; then
        echo "Error: Failed to download ${URL}"
        exit 1
    fi

    echo "Extracting..."
    cd "$TMP_DIR"
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
        echo "pgmi installed successfully!"
        pgmi --version
    else
        echo ""
        echo "Installation complete. You may need to add ${INSTALL_DIR} to your PATH."
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
