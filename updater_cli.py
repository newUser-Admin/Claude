#!/usr/bin/env python3
"""
Persistent Update Software — CLI Management Tool
Controls the running daemon, displays status, and edits configuration.

Usage:
  updater_cli.py status      — Show daemon status and last update info
  updater_cli.py start       — Start the daemon in the background
  updater_cli.py stop        — Stop the daemon gracefully
  updater_cli.py restart     — Restart the daemon
  updater_cli.py run-once    — Run a single update cycle (foreground)
  updater_cli.py config show — Print current configuration
  updater_cli.py config set KEY VALUE — Update a config value
  updater_cli.py history     — Show update history
  updater_cli.py logs [N]    — Tail last N lines of the log (default 40)
"""

import os
import sys
import json
import signal
import argparse
import subprocess
from pathlib import Path
from datetime import datetime


BASE_DIR = Path(__file__).resolve().parent
CONFIG_PATH = BASE_DIR / "updater_config.json"
LOG_PATH = BASE_DIR / "updater.log"
PID_FILE = BASE_DIR / "updater.pid"
STATE_FILE = BASE_DIR / "updater_state.json"
DAEMON_SCRIPT = BASE_DIR / "updater_daemon.py"

DEFAULT_CONFIG = {
    "interval_seconds": 3600,
    "auto_apply": False,
    "sources": {"apt": True, "pip": True, "npm": False},
    "notify_only": True,
    "log_level": "INFO",
    "max_log_bytes": 5_242_880,
    "backup_log_count": 3,
}


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _load_config() -> dict:
    if CONFIG_PATH.exists():
        with CONFIG_PATH.open() as f:
            user = json.load(f)
        cfg = {**DEFAULT_CONFIG, **user}
        if "sources" in user:
            cfg["sources"] = {**DEFAULT_CONFIG["sources"], **user["sources"]}
        return cfg
    return dict(DEFAULT_CONFIG)


def _save_config(cfg: dict) -> None:
    with CONFIG_PATH.open("w") as f:
        json.dump(cfg, f, indent=2)


def _load_state() -> dict:
    if STATE_FILE.exists():
        with STATE_FILE.open() as f:
            return json.load(f)
    return {}


def _get_pid() -> int | None:
    if PID_FILE.exists():
        try:
            pid = int(PID_FILE.read_text().strip())
            # Verify process is alive
            os.kill(pid, 0)
            return pid
        except (ValueError, ProcessLookupError, PermissionError):
            PID_FILE.unlink(missing_ok=True)
    return None


def _color(text: str, code: str) -> str:
    """Wrap text in ANSI colour if stdout is a tty."""
    if sys.stdout.isatty():
        return f"\033[{code}m{text}\033[0m"
    return text


def ok(text):  return _color(text, "32")    # green
def warn(text): return _color(text, "33")   # yellow
def err(text):  return _color(text, "31")   # red
def bold(text): return _color(text, "1")    # bold


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
def cmd_status(_args):
    pid = _get_pid()
    state = _load_state()
    cfg = _load_config()

    print(bold("=== UpdateDaemon Status ==="))
    if pid:
        print(f"  Daemon  : {ok('RUNNING')} (PID {pid})")
    else:
        print(f"  Daemon  : {warn('STOPPED')}")

    last_check = state.get("last_check", "—")
    last_apply = state.get("last_apply", "—")
    print(f"  Last check  : {last_check}")
    print(f"  Last apply  : {last_apply}")
    print(f"  Interval    : {cfg['interval_seconds']}s")
    print(f"  Auto-apply  : {cfg['auto_apply']}")
    print(f"  Sources     : {', '.join(k for k, v in cfg['sources'].items() if v)}")

    history = state.get("history", [])
    if history:
        last = history[-1]
        avail = sum(len(c.get("available", [])) for c in last.get("checks", []))
        applied = sum(len(c.get("applied", [])) for c in last.get("checks", []))
        errors = sum(len(c.get("errors", [])) for c in last.get("checks", []))
        print(f"\n  Last cycle  : {last.get('timestamp', '—')}")
        print(f"    available : {avail}")
        print(f"    applied   : {applied}")
        print(f"    errors    : {err(str(errors)) if errors else '0'}")


def cmd_start(_args):
    pid = _get_pid()
    if pid:
        print(warn(f"Daemon already running (PID {pid})"))
        return

    proc = subprocess.Popen(
        [sys.executable, str(DAEMON_SCRIPT)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    # Give the process a moment to write its PID file
    import time; time.sleep(0.5)
    new_pid = _get_pid()
    if new_pid:
        print(ok(f"Daemon started (PID {new_pid})"))
    else:
        print(ok(f"Daemon launched (PID {proc.pid}) — check {LOG_PATH} for details"))


def cmd_stop(_args):
    pid = _get_pid()
    if not pid:
        print(warn("Daemon is not running"))
        return
    try:
        os.kill(pid, signal.SIGTERM)
        # Wait up to 5 s
        import time
        for _ in range(10):
            time.sleep(0.5)
            if _get_pid() is None:
                break
        print(ok(f"Daemon stopped (was PID {pid})"))
    except ProcessLookupError:
        print(warn("Process not found — PID file stale, cleaned up"))
        PID_FILE.unlink(missing_ok=True)


def cmd_restart(args):
    cmd_stop(args)
    import time; time.sleep(1)
    cmd_start(args)


def cmd_run_once(_args):
    subprocess.run(
        [sys.executable, str(DAEMON_SCRIPT), "--run-once"],
        check=False,
    )


def cmd_config_show(_args):
    cfg = _load_config()
    print(json.dumps(cfg, indent=2))


def cmd_config_set(args):
    cfg = _load_config()
    key: str = args.key
    value_str: str = args.value

    # Support dot-notation for nested keys: sources.apt
    parts = key.split(".")
    target = cfg
    for part in parts[:-1]:
        if part not in target or not isinstance(target[part], dict):
            print(err(f"Key '{part}' not found or not a dict"))
            sys.exit(1)
        target = target[part]

    leaf = parts[-1]
    if leaf not in target:
        print(err(f"Unknown config key: {key}"))
        sys.exit(1)

    # Coerce type to match existing value
    existing = target[leaf]
    if isinstance(existing, bool):
        value = value_str.lower() in ("1", "true", "yes", "on")
    elif isinstance(existing, int):
        value = int(value_str)
    elif isinstance(existing, float):
        value = float(value_str)
    else:
        value = value_str

    target[leaf] = value
    _save_config(cfg)
    print(ok(f"Config updated: {key} = {value!r}"))

    # Reload running daemon
    pid = _get_pid()
    if pid:
        try:
            os.kill(pid, signal.SIGHUP)
            print(ok("Sent SIGHUP to daemon — config reloaded"))
        except ProcessLookupError:
            pass


def cmd_history(args):
    state = _load_state()
    history = state.get("history", [])
    if not history:
        print(warn("No update history available"))
        return

    n = getattr(args, "n", 10) or 10
    print(bold(f"=== Last {min(n, len(history))} update cycle(s) ==="))
    for cycle in history[-n:]:
        ts = cycle.get("timestamp", "?")
        checks = cycle.get("checks", [])
        avail = sum(len(c.get("available", [])) for c in checks)
        applied = sum(len(c.get("applied", [])) for c in checks)
        errors = sum(len(c.get("errors", [])) for c in checks)
        line = f"  {ts}  available={avail}  applied={applied}  errors={errors}"
        if errors:
            print(err(line))
        elif applied:
            print(ok(line))
        else:
            print(line)


def cmd_logs(args):
    n = getattr(args, "n", 40) or 40
    if not LOG_PATH.exists():
        print(warn("Log file not found"))
        return
    lines = LOG_PATH.read_text(errors="replace").splitlines()
    for line in lines[-n:]:
        print(line)


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        description="UpdateDaemon CLI",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    sub = p.add_subparsers(dest="command", metavar="COMMAND")

    sub.add_parser("status", help="Show daemon status")
    sub.add_parser("start", help="Start the daemon")
    sub.add_parser("stop", help="Stop the daemon")
    sub.add_parser("restart", help="Restart the daemon")
    sub.add_parser("run-once", help="Run one update cycle and exit")

    # config sub-commands
    cfg_p = sub.add_parser("config", help="View / edit configuration")
    cfg_sub = cfg_p.add_subparsers(dest="cfg_cmd", metavar="ACTION")
    cfg_sub.add_parser("show", help="Print config")
    set_p = cfg_sub.add_parser("set", help="Set a config value")
    set_p.add_argument("key", help="Config key (supports dot notation, e.g. sources.apt)")
    set_p.add_argument("value", help="New value")

    hist_p = sub.add_parser("history", help="Show update history")
    hist_p.add_argument("n", nargs="?", type=int, default=10, help="Number of entries (default 10)")

    logs_p = sub.add_parser("logs", help="Tail the log file")
    logs_p.add_argument("n", nargs="?", type=int, default=40, help="Number of lines (default 40)")

    return p


def main():
    parser = build_parser()
    args = parser.parse_args()

    dispatch = {
        "status": cmd_status,
        "start": cmd_start,
        "stop": cmd_stop,
        "restart": cmd_restart,
        "run-once": cmd_run_once,
        "history": cmd_history,
        "logs": cmd_logs,
    }

    if args.command is None:
        parser.print_help()
        return

    if args.command == "config":
        if not hasattr(args, "cfg_cmd") or args.cfg_cmd is None:
            cmd_config_show(args)
        elif args.cfg_cmd == "show":
            cmd_config_show(args)
        elif args.cfg_cmd == "set":
            cmd_config_set(args)
        return

    handler = dispatch.get(args.command)
    if handler:
        handler(args)
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
