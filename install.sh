#!/bin/bash
set -e

# Installation script for wrklogr
# This script builds the wrklogr binary and installs it to a directory in your PATH.

echo "🔨 Building wrklogr..."
go build -o wrklogr cmd/wrklogr/main.go

# Determine installation directory
INSTALL_DIR=""

# Check if ~/go/bin exists and is in PATH (standard Go setup)
if [[ ":$PATH:" == *":$HOME/go/bin:"* ]] && [[ -d "$HOME/go/bin" ]]; then
    INSTALL_DIR="$HOME/go/bin"
    echo "📁 Installing to $INSTALL_DIR (standard Go bin directory)"
# Otherwise, try /usr/local/bin (requires sudo)
elif [[ -d "/usr/local/bin" ]]; then
    INSTALL_DIR="/usr/local/bin"
    echo "📁 Installing to $INSTALL_DIR (requires sudo)"
else
    echo "❌ Could not find a suitable installation directory."
    echo "   Please add $HOME/go/bin to your PATH or use sudo to install to /usr/local/bin"
    exit 1
fi

# Copy the binary
if [[ "$INSTALL_DIR" == "/usr/local/bin" ]]; then
    sudo cp wrklogr "$INSTALL_DIR/wrklogr"
    sudo chmod +x "$INSTALL_DIR/wrklogr"
else
    cp wrklogr "$INSTALL_DIR/wrklogr"
    chmod +x "$INSTALL_DIR/wrklogr"
fi

echo "✅ wrklogr installed successfully to $INSTALL_DIR/wrklogr"
echo ""
echo "You can now run:"
echo "  wrklogr --help"
echo ""
echo "To generate a report for a local repo:"
echo "  wrklogr report --local --local-path /path/to/repo --since 2025-03-01"
