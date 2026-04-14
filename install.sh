#!/usr/bin/env bash
# install.sh — Install the persistent update daemon as a systemd service.
# Must be run as root (or with sudo).
set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
INSTALL_DIR="/opt/updater"
SERVICE_NAME="updater"
PYTHON=$(command -v python3 || true)
SYSTEMD_DIR="/etc/systemd/system"

# ---------------------------------------------------------------------------
# Colour helpers
# ---------------------------------------------------------------------------
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RESET='\033[0m'
info()    { echo -e "${GREEN}[INFO]${RESET}  $*"; }
warn()    { echo -e "${YELLOW}[WARN]${RESET}  $*"; }
error()   { echo -e "${RED}[ERROR]${RESET} $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------
[[ $EUID -ne 0 ]] && error "Please run as root: sudo $0"
[[ -z "$PYTHON" ]]  && error "python3 not found — install it first"
systemctl --version &>/dev/null || error "systemd not available on this system"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------------------------------------------------------------------------
# Create install directory and copy files
# ---------------------------------------------------------------------------
info "Installing files to $INSTALL_DIR …"
mkdir -p "$INSTALL_DIR"

for f in updater_daemon.py updater_cli.py updater_config.json; do
    src="$SCRIPT_DIR/$f"
    if [[ ! -f "$src" ]]; then
        error "Required file not found: $src"
    fi
    cp "$src" "$INSTALL_DIR/$f"
    info "  Copied $f"
done

chmod +x "$INSTALL_DIR/updater_daemon.py"
chmod +x "$INSTALL_DIR/updater_cli.py"

# ---------------------------------------------------------------------------
# Write the service unit with the resolved install path
# ---------------------------------------------------------------------------
info "Writing systemd service unit …"
cat > "$SYSTEMD_DIR/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Persistent Update Software Daemon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=${INSTALL_DIR}
ExecStart=${PYTHON} ${INSTALL_DIR}/updater_daemon.py
ExecReload=/bin/kill -HUP \$MAINPID
Restart=always
RestartSec=15
WatchdogSec=300
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${SERVICE_NAME}
PrivateTmp=true
ProtectSystem=full
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
EOF

# ---------------------------------------------------------------------------
# Write the watchdog timer unit
# ---------------------------------------------------------------------------
info "Writing systemd timer unit …"
cat > "$SYSTEMD_DIR/${SERVICE_NAME}.timer" <<EOF
[Unit]
Description=Watchdog timer for Persistent Update Software Daemon
Requires=${SERVICE_NAME}.service

[Timer]
OnBootSec=2min
OnUnitActiveSec=10min
Unit=${SERVICE_NAME}.service

[Install]
WantedBy=timers.target
EOF

# ---------------------------------------------------------------------------
# Create a symlink so "updater" is available on PATH
# ---------------------------------------------------------------------------
LINK="/usr/local/bin/updater"
if [[ -L "$LINK" || -f "$LINK" ]]; then
    warn "Symlink $LINK already exists — overwriting"
fi
ln -sf "$INSTALL_DIR/updater_cli.py" "$LINK"
info "CLI available as: updater (→ $LINK)"

# ---------------------------------------------------------------------------
# Enable & start
# ---------------------------------------------------------------------------
info "Reloading systemd daemon …"
systemctl daemon-reload

info "Enabling service (auto-start on boot) …"
systemctl enable "${SERVICE_NAME}.service"
systemctl enable "${SERVICE_NAME}.timer"

info "Starting service …"
systemctl start "${SERVICE_NAME}.service"
systemctl start "${SERVICE_NAME}.timer"

# ---------------------------------------------------------------------------
# Verify
# ---------------------------------------------------------------------------
sleep 1
if systemctl is-active --quiet "${SERVICE_NAME}.service"; then
    info "Service is ${GREEN}running${RESET}"
else
    warn "Service may not have started — check: journalctl -u ${SERVICE_NAME} -n 50"
fi

echo
echo -e "${GREEN}Installation complete.${RESET}"
echo
echo "Useful commands:"
echo "  updater status          — daemon status"
echo "  updater logs            — tail the log"
echo "  updater config show     — current config"
echo "  updater config set auto_apply true   — enable auto-install"
echo "  systemctl status ${SERVICE_NAME}     — systemd view"
echo "  journalctl -u ${SERVICE_NAME} -f     — live journal log"
