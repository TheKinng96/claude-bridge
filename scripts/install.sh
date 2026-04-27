#!/bin/bash
# Claude Bridge — one-command installer for macOS
# Usage: curl -fsSL https://raw.githubusercontent.com/TheKinng96/claude-bridge/main/scripts/install.sh | bash
set -e

REPO="TheKinng96/claude-bridge"
INSTALL_DIR="$HOME/claude-bridge"
PLIST="$HOME/Library/LaunchAgents/com.claudebridge.plist"

echo "=== Claude Bridge Installer ==="

# Create install directory
mkdir -p "$INSTALL_DIR/logs"

# Detect architecture
ARCH=$(uname -m)
if [ "$ARCH" = "arm64" ]; then
    BIN="claude-bridge-arm64"
else
    BIN="claude-bridge-amd64"
fi

echo "Architecture: $ARCH → $BIN"
echo "Downloading latest binary..."

curl -fsSL "https://github.com/$REPO/releases/latest/download/$BIN" \
    -o "$INSTALL_DIR/claude-bridge"
chmod +x "$INSTALL_DIR/claude-bridge"
xattr -d com.apple.quarantine "$INSTALL_DIR/claude-bridge" 2>/dev/null || true

VERSION=$("$INSTALL_DIR/claude-bridge" --version 2>/dev/null || echo "unknown")
echo "Downloaded version: $VERSION"

# Create launchd plist
cat > "$PLIST" << EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.claudebridge</string>
    <key>ProgramArguments</key>
    <array>
        <string>$INSTALL_DIR/claude-bridge</string>
        <string>-no-tray</string>
    </array>
    <key>WorkingDirectory</key>
    <string>$INSTALL_DIR</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>$INSTALL_DIR/logs/app.log</string>
    <key>StandardErrorPath</key>
    <string>$INSTALL_DIR/logs/app.log</string>
</dict>
</plist>
EOF

# Load service (unload first if already loaded)
launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

echo ""
echo "=== Done! ==="
echo "  Dashboard:   http://127.0.0.1:10002"
echo "  Logs:        tail -f $INSTALL_DIR/logs/app.log"
echo "  Auto-update: built-in, checks every 6h, banner appears in dashboard when ready"
echo ""
echo "To stop:    launchctl unload $PLIST"
echo "To restart: launchctl unload $PLIST && launchctl load $PLIST"
