#!/usr/bin/env bash
# uninstall.sh — Remove the persistent update daemon completely.
set -euo pipefail

SERVICE_NAME="updater"
INSTALL_DIR="/opt/updater"
SYSTEMD_DIR="/etc/systemd/system"
LINK="/usr/local/bin/updater"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RESET='\033[0m'
info()  { echo -e "${GREEN}[INFO]${RESET}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${RESET}  $*"; }

[[ $EUID -ne 0 ]] && { echo -e "${RED}[ERROR]${RESET} Run as root: sudo $0" >&2; exit 1; }

# Stop and disable units
for unit in "${SERVICE_NAME}.timer" "${SERVICE_NAME}.service"; do
    if systemctl is-active --quiet "$unit" 2>/dev/null; then
        info "Stopping $unit …"
        systemctl stop "$unit"
    fi
    if systemctl is-enabled --quiet "$unit" 2>/dev/null; then
        info "Disabling $unit …"
        systemctl disable "$unit"
    fi
    [[ -f "$SYSTEMD_DIR/$unit" ]] && rm -f "$SYSTEMD_DIR/$unit" && info "Removed $SYSTEMD_DIR/$unit"
done

systemctl daemon-reload

# Remove install dir
if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    info "Removed $INSTALL_DIR"
fi

# Remove symlink
[[ -L "$LINK" || -f "$LINK" ]] && rm -f "$LINK" && info "Removed $LINK"

echo
echo -e "${GREEN}Uninstall complete.${RESET}"
