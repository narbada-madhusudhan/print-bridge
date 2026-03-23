#!/bin/bash
# NME Print Bridge — One-line installer
# Usage: curl -sL https://raw.githubusercontent.com/narbada-madhusudhan/nme-print-bridge/main/install.sh | bash
#
# This script:
# 1. Detects your OS and architecture
# 2. Downloads the latest release from GitHub
# 3. Installs to ~/Applications/ (Mac/Linux) or %LOCALAPPDATA% (Windows)
# 4. Sets up auto-start on login
# 5. Starts the service immediately

set -e

REPO="narbada-madhusudhan/nme-print-bridge"
INSTALL_DIR="$HOME/Applications"
BINARY="$INSTALL_DIR/nme-print-bridge"

# Handle --uninstall flag
if [ "${1:-}" = "--uninstall" ] || [ "${1:-}" = "uninstall" ]; then
  echo ""
  echo "  Uninstalling NME Print Bridge..."
  if [ -f "$BINARY" ]; then
    "$BINARY" --uninstall 2>/dev/null || true
    rm -f "$BINARY"
    echo "  ✓ Uninstalled"
  else
    echo "  Not installed at $BINARY"
  fi
  echo ""
  exit 0
fi

echo ""
echo "  ╔═══════════════════════════════════════╗"
echo "  ║     NME Print Bridge — Installer      ║"
echo "  ╚═══════════════════════════════════════╝"
echo ""

# Stop existing installation if upgrading
if [ -f "$BINARY" ]; then
  echo "  → Stopping existing installation..."
  "$BINARY" --uninstall 2>/dev/null || true
  rm -f "$BINARY"
fi

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
  darwin) PLATFORM="mac" ;;
  linux)  PLATFORM="linux" ;;
  *)
    echo "  ✗ Unsupported OS: $OS"
    echo "  For Windows, download from:"
    echo "  https://github.com/$REPO/releases/latest"
    exit 1
    ;;
esac

case "$ARCH" in
  arm64|aarch64) SUFFIX="${PLATFORM}-arm64" ;;
  x86_64|amd64)  SUFFIX="${PLATFORM}-amd64" ;;
  *)
    echo "  ✗ Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

BINARY_NAME="print-bridge-${SUFFIX}"
DOWNLOAD_URL="https://github.com/$REPO/releases/latest/download/$BINARY_NAME"

echo "  OS:   $OS ($ARCH)"
echo "  File: $BINARY_NAME"
echo ""

# Download
echo "  → Downloading latest release..."
mkdir -p "$INSTALL_DIR"
curl -sL "$DOWNLOAD_URL" -o "$INSTALL_DIR/nme-print-bridge"

if [ ! -s "$INSTALL_DIR/nme-print-bridge" ]; then
  echo "  ✗ Download failed"
  exit 1
fi

chmod +x "$INSTALL_DIR/nme-print-bridge"

# Remove macOS quarantine (prevents Gatekeeper warning)
if [ "$PLATFORM" = "mac" ]; then
  xattr -cr "$INSTALL_DIR/nme-print-bridge" 2>/dev/null || true
fi

echo "  ✓ Downloaded to $INSTALL_DIR/nme-print-bridge"

# Install auto-start
echo "  → Setting up auto-start..."
"$INSTALL_DIR/nme-print-bridge" --install

echo ""
echo "  ╔═══════════════════════════════════════╗"
echo "  ║  ✓ Installation complete!             ║"
echo "  ║                                       ║"
echo "  ║  NME Print Bridge is now running      ║"
echo "  ║  and will start automatically on      ║"
echo "  ║  login.                               ║"
echo "  ║                                       ║"
echo "  ║  Status: http://localhost:9120        ║"
echo "  ║                                       ║"
echo "  ║  To uninstall:                        ║"
echo "  ║  ~/Applications/nme-print-bridge      ║"
echo "  ║    --uninstall                        ║"
echo "  ╚═══════════════════════════════════════╝"
echo ""
