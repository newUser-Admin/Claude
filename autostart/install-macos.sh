#!/usr/bin/env bash
# install-macos.sh
#
# Installs the Interaction Engine as a macOS LaunchAgent that starts
# automatically every time the current user logs in.
#
# Usage:
#   chmod +x autostart/install-macos.sh
#   autostart/install-macos.sh          # install / reinstall
#   autostart/install-macos.sh --remove # uninstall

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PLIST_SRC="$SCRIPT_DIR/com.interaction-engine.plist"
PLIST_LABEL="com.interaction-engine"
PLIST_DST="$HOME/Library/LaunchAgents/$PLIST_LABEL.plist"
LOG_DIR="$HOME/Library/Logs/interaction-engine"

# ── Helpers ──────────────────────────────────────────────────────────────────
die()  { echo "Error: $*" >&2; exit 1; }
info() { echo "  $*"; }

# ── Uninstall path ───────────────────────────────────────────────────────────
if [[ "${1:-}" == "--remove" ]]; then
    launchctl unload "$PLIST_DST" 2>/dev/null || true
    rm -f "$PLIST_DST"
    echo "Removed $PLIST_LABEL."
    exit 0
fi

# ── Pre-flight checks ────────────────────────────────────────────────────────
[[ "$(uname)" == "Darwin" ]] || die "This script is for macOS only."

NODE_BIN="$(command -v node 2>/dev/null || true)"
[[ -n "$NODE_BIN" ]]         || die "node not found in PATH. Install Node.js >= 18."

NODE_MAJOR=$(node -e 'process.stdout.write(process.version.replace(/v(\d+).*/,"$1"))' 2>/dev/null)
[[ "$NODE_MAJOR" -ge 18 ]]   || die "Node.js >= 18 required (found v$NODE_MAJOR)."

echo "Installing Interaction Engine LaunchAgent"
info "install dir : $INSTALL_DIR"
info "node binary : $NODE_BIN"
info "log dir     : $LOG_DIR"
info "plist       : $PLIST_DST"

# ── Install ws package if missing ────────────────────────────────────────────
if [[ ! -d "$INSTALL_DIR/node_modules/ws" ]]; then
    info "Installing ws npm package..."
    (cd "$INSTALL_DIR" && npm install ws --save 2>&1 | sed 's/^/    /')
fi

# ── Create directories ───────────────────────────────────────────────────────
mkdir -p "$LOG_DIR"
mkdir -p "$HOME/Library/LaunchAgents"

# ── Write plist (substitute placeholders) ───────────────────────────────────
sed \
    -e "s|__INSTALL_DIR__|$INSTALL_DIR|g" \
    -e "s|__NODE_BIN__|$NODE_BIN|g" \
    -e "s|__LOG_DIR__|$LOG_DIR|g" \
    "$PLIST_SRC" > "$PLIST_DST"

# ── Load (unload first in case already running) ──────────────────────────────
launchctl unload "$PLIST_DST" 2>/dev/null || true
launchctl load -w "$PLIST_DST"

echo ""
echo "Done. The engine starts automatically at login."
echo ""
echo "Useful commands:"
echo "  launchctl list | grep interaction-engine"
echo "  tail -f $LOG_DIR/engine.log"
echo "  launchctl stop  $PLIST_LABEL"
echo "  launchctl start $PLIST_LABEL"
echo "  $0 --remove"
