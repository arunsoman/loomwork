#!/bin/sh
# loomwork install script.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
#
# Downloads the latest release binary for your platform from GitHub, checks it
# against the release's SHA256SUMS, and installs it to /usr/local/bin (or
# ~/.local/bin if that is not writable).
#
# Environment:
#   LOOMWORK_VERSION   release tag to install (default: latest), e.g. v0.1.0
#   LOOMWORK_BIN_DIR   install directory (overrides the default choice)
set -eu

REPO="arunsoman/loomwork"

die() { echo "error: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS (Linux and macOS are supported)" ;;
esac

have curl || die "curl is required"

ASSET="loomwork-${OS}-${ARCH}"
if [ -n "${LOOMWORK_VERSION:-}" ]; then
  BASE="https://github.com/${REPO}/releases/download/${LOOMWORK_VERSION}"
else
  BASE="https://github.com/${REPO}/releases/latest/download"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading ${ASSET}..."
curl -fsSL "${BASE}/${ASSET}" -o "${TMP}/${ASSET}" ||
  die "could not download ${BASE}/${ASSET} (has a release been published for ${OS}/${ARCH}?)"
curl -fsSL "${BASE}/SHA256SUMS" -o "${TMP}/SHA256SUMS" ||
  die "could not download ${BASE}/SHA256SUMS"

# Verify the download against the release's checksum file.
WANT=$(grep " ${ASSET}\$" "${TMP}/SHA256SUMS" | awk '{print $1}')
[ -n "$WANT" ] || die "no checksum for ${ASSET} in SHA256SUMS"
if have sha256sum; then
  GOT=$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')
elif have shasum; then
  GOT=$(shasum -a 256 "${TMP}/${ASSET}" | awk '{print $1}')
else
  die "sha256sum or shasum is required to verify the download"
fi
[ "$WANT" = "$GOT" ] || die "checksum mismatch for ${ASSET} (expected ${WANT}, got ${GOT}); not installing"
echo "✓ Checksum verified"

chmod +x "${TMP}/${ASSET}"

if [ -n "${LOOMWORK_BIN_DIR:-}" ]; then
  INSTALL_DIR="$LOOMWORK_BIN_DIR"
elif [ -w /usr/local/bin ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
  echo "No write access to /usr/local/bin, installing to ${INSTALL_DIR}"
fi
mkdir -p "$INSTALL_DIR"
mv "${TMP}/${ASSET}" "${INSTALL_DIR}/loomwork"

echo "✓ Installed: ${INSTALL_DIR}/loomwork"
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *) echo "  Note: ${INSTALL_DIR} is not on your PATH. Add it: export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
esac
echo ""
echo "Next steps:"
echo "  1. Make sure Ollama is running:   ollama pull llama3.2"
echo "  2. Check your setup:              loomwork doctor"
echo "  3. Scaffold your first agent:     loomwork init my-agent"
echo "  4. Ask it a question:             cd my-agent && loomwork ask \"hello\" --folder ."
echo "  5. Package it:                    loomwork package --out my-agent.aci"
echo ""
echo "Source: https://github.com/${REPO}  |  License: Apache-2.0"
