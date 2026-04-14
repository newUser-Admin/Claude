# Persistent Update Software

A lightweight daemon that continuously monitors and (optionally) applies
software updates for `apt`, `pip`, and `npm` packages.

## Files

| File | Purpose |
|---|---|
| `updater_daemon.py` | Daemon process — runs the update loop |
| `updater_cli.py` | CLI management tool |
| `updater_config.json` | Runtime configuration |
| `updater.service` | systemd unit file |
| `updater.log` | Rolling log (auto-created) |
| `updater_state.json` | Persisted run history (auto-created) |
| `updater.pid` | PID file while daemon is running (auto-created) |

---

## Quick Start

### Run once (foreground, no daemon)
```bash
python3 updater_daemon.py --run-once
```

### Start the background daemon
```bash
python3 updater_cli.py start
```

### Check status
```bash
python3 updater_cli.py status
```

### Stop the daemon
```bash
python3 updater_cli.py stop
```

---

## CLI Reference

```
python3 updater_cli.py <command>

Commands:
  status              Show daemon status and last update summary
  start               Start the daemon in the background
  stop                Stop the daemon gracefully (SIGTERM)
  restart             Stop then start
  run-once            Run one update cycle, print JSON result, exit
  config show         Print current configuration as JSON
  config set KEY VAL  Update a config value (supports dot notation)
  history [N]         Show last N update cycles (default 10)
  logs [N]            Tail last N lines of the log file (default 40)
```

### Config keys

| Key | Default | Description |
|---|---|---|
| `interval_seconds` | `3600` | Seconds between update checks |
| `auto_apply` | `false` | Actually install updates (false = dry-run only) |
| `sources.apt` | `true` | Check apt (Debian/Ubuntu) packages |
| `sources.pip` | `true` | Check pip (Python) packages |
| `sources.npm` | `false` | Check npm global packages |
| `notify_only` | `true` | Only log; do not act |
| `log_level` | `"INFO"` | Logging verbosity (DEBUG/INFO/WARNING/ERROR) |

### Examples

```bash
# Enable auto-apply
python3 updater_cli.py config set auto_apply true

# Check every 6 hours
python3 updater_cli.py config set interval_seconds 21600

# Enable npm checking
python3 updater_cli.py config set sources.npm true

# View last 5 history entries
python3 updater_cli.py history 5

# Tail 100 log lines
python3 updater_cli.py logs 100
```

---

## systemd Installation

```bash
# Copy the unit file
sudo cp updater.service /etc/systemd/system/

# Reload and enable
sudo systemctl daemon-reload
sudo systemctl enable updater
sudo systemctl start updater

# Check journal logs
sudo journalctl -u updater -f
```

---

## Signals

| Signal | Effect |
|---|---|
| `SIGTERM` | Graceful shutdown |
| `SIGINT` | Graceful shutdown (Ctrl-C) |
| `SIGHUP` | Reload `updater_config.json` without restart |
