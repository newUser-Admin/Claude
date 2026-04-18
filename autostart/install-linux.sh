#!/usr/bin/env bash
# install-linux.sh
#
# Installs the Interaction Engine as a systemd service that starts
# automatically on boot for the current user's system.
#
# Usage:
#   chmod +x autostart/install-linux.sh
#   sudo autostart/install-linux.sh          # install / reinstall
#   sudo autostart/install-linux.sh --remove # uninstall

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
SERVICE_SRC="$SCRIPT_DIR/interaction-engine.service"
SERVICE_DST="/etc/systemd/system/interaction-engine.service"
SERVICE_NAME="interaction-engine"

# ── Helpers ──────────────────────────────────────────────────────────────────
die()  { echo "Error: $*" >&2; exit 1; }
info() { echo "  $*"; }

# ── Uninstall path ───────────────────────────────────────────────────────────
if [[ "${1:-}" == "--remove" ]]; then
    systemctl stop    "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "$SERVICE_DST"
    systemctl daemon-reload
    echo "Removed $SERVICE_NAME."
    exit 0
fi

# ── Pre-flight checks ────────────────────────────────────────────────────────
[[ "$EUID" -eq 0 ]] || die "Run as root:  sudo $0"

command -v node >/dev/null  || die "node not found in PATH. Install Node.js >= 18."
NODE_MAJOR=$(node -e 'process.stdout.write(process.version.replace(/v(\d+).*/,"$1"))' 2>/dev/null)
[[ "$NODE_MAJOR" -ge 18 ]]  || die "Node.js >= 18 required (found v$NODE_MAJOR)."

# Run as the invoking user, not root (sudo preserves SUDO_USER).
RUN_AS="${SUDO_USER:-$(whoami)}"

echo "Installing Interaction Engine"
info "install dir : $INSTALL_DIR"
info "run as user : $RUN_AS"
info "service file: $SERVICE_DST"

# ── Install ws package if missing ────────────────────────────────────────────
if [[ ! -d "$INSTALL_DIR/node_modules/ws" ]]; then
    info "Installing ws npm package..."
    (cd "$INSTALL_DIR" && sudo -u "$RUN_AS" npm install ws --save 2>&1 | sed 's/^/    /')
fi

# ── Write service file ───────────────────────────────────────────────────────
sed \
    -e "s|__INSTALL_DIR__|$INSTALL_DIR|g" \
    -e "s|__USER__|$RUN_AS|g" \
    "$SERVICE_SRC" > "$SERVICE_DST"

chmod 644 "$SERVICE_DST"

# ── Enable and start ─────────────────────────────────────────────────────────
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
systemctl restart "$SERVICE_NAME"

echo ""
echo "Done. The engine starts automatically on boot."
echo ""
echo "Useful commands:"
echo "  sudo systemctl status   $SERVICE_NAME"
echo "  sudo journalctl -u      $SERVICE_NAME -f"
echo "  sudo systemctl restart  $SERVICE_NAME"
echo "  sudo $0 --remove"
