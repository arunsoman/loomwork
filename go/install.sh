#!/usr/bin/env bash
# loomwork install script — one-command install.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
#
# Downloads the latest single-binary release for your platform and installs
# it to /usr/local/bin (or ~/.local/bin if no sudo).
set -e

REPO="arunsoman/loomwork"
INSTALL_DIR="/usr/local/bin"
INSTALL_PATH="${INSTALL_DIR}/loomwork"

# Detect platform
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

echo "Detecting latest release..."
# In production this hits GitHub releases API. For dev, use local binary.
if [ -f "/tmp/loomwork-${OS}-${ARCH}" ]; then
  echo "Using local build from /tmp/loomwork-${OS}-${ARCH}"
  cp "/tmp/loomwork-${OS}-${ARCH}" /tmp/loomwork
else
  echo "Downloading loomwork latest ${OS}/${ARCH}..."
  # In production: curl -fsSL "https://github.com/${REPO}/releases/latest/download/loomwork-${OS}-${ARCH}" -o /tmp/loomwork
  echo "(Production: would download from GitHub releases)"
  exit 1
fi

chmod +x /tmp/loomwork

# Install
if [ -w "${INSTALL_DIR}" ]; then
  mv /tmp/loomwork "${INSTALL_PATH}"
else
  echo "No write access to ${INSTALL_DIR}, installing to ~/.local/bin"
  mkdir -p ~/.local/bin
  mv /tmp/loomwork ~/.local/bin/loomwork
  INSTALL_PATH="$HOME/.local/bin/loomwork"
fi

echo "✓ Installed: ${INSTALL_PATH}"
echo ""
echo "Next steps:"
echo "  1. Make sure Ollama is running:   ollama pull llama3.2"
echo "  2. Scaffold your first agent:     loomwork init my-agent"
echo "  3. Ask it a question:             cd my-agent && loomwork ask \"hello\""
echo "  4. Package it:                    loomwork package --out my-agent.aci"
echo ""
echo "Spec: https://loomwork.dev  |  License: Apache-2.0"
