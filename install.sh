#!/bin/sh
set -e

REPO="your-github-user/copilot-watcher"
BINARY="copilot-watcher"
INSTALL_DIR="${HOME}/.local/bin"

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" && exit 1 ;;
esac

# Get latest release URL
LATEST_URL="https://github.com/${REPO}/releases/latest/download/${BINARY}_${OS}_${ARCH}.tar.gz"

echo "Installing ${BINARY} from ${LATEST_URL} ..."

mkdir -p "$INSTALL_DIR"
curl -sSL "$LATEST_URL" | tar -xz -C "$INSTALL_DIR" "$BINARY"
chmod +x "${INSTALL_DIR}/${BINARY}"

echo ""
echo "Installed to ${INSTALL_DIR}/${BINARY}"
echo "Make sure ${INSTALL_DIR} is in your PATH."
echo ""
echo "Usage:"
echo "  ${BINARY}           # Select from running Copilot CLI sessions"
