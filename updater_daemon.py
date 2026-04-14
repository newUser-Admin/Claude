#!/usr/bin/env python3
"""
Persistent Update Software Daemon
Continuously monitors and applies updates for system packages,
pip packages, and npm packages on a configurable schedule.
"""

import os
import sys
import time
import signal
import logging
import argparse
import threading
import subprocess
import json
from datetime import datetime
from pathlib import Path


# ---------------------------------------------------------------------------
# Paths & defaults
# ---------------------------------------------------------------------------
BASE_DIR = Path(__file__).resolve().parent
CONFIG_PATH = BASE_DIR / "updater_config.json"
LOG_PATH = BASE_DIR / "updater.log"
PID_FILE = BASE_DIR / "updater.pid"
STATE_FILE = BASE_DIR / "updater_state.json"

DEFAULT_CONFIG = {
    "interval_seconds": 3600,      # check every hour
    "auto_apply": False,           # dry-run by default; set True to actually install
    "sources": {
        "apt": True,               # system packages (Debian / Ubuntu)
        "pip": True,               # Python packages
        "npm": False               # npm global packages (disabled by default)
    },
    "notify_only": True,           # just log what needs updating
    "log_level": "INFO",
    "max_log_bytes": 5_242_880,    # 5 MB
    "backup_log_count": 3
}


# ---------------------------------------------------------------------------
# Logging setup
# ---------------------------------------------------------------------------
def setup_logging(config: dict) -> logging.Logger:
    from logging.handlers import RotatingFileHandler

    logger = logging.getLogger("updater")
    logger.setLevel(getattr(logging, config.get("log_level", "INFO").upper(), logging.INFO))

    fmt = logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )

    # File handler (rotating)
    fh = RotatingFileHandler(
        LOG_PATH,
        maxBytes=config.get("max_log_bytes", 5_242_880),
        backupCount=config.get("backup_log_count", 3),
    )
    fh.setFormatter(fmt)
    logger.addHandler(fh)

    # Console handler
    ch = logging.StreamHandler(sys.stdout)
    ch.setFormatter(fmt)
    logger.addHandler(ch)

    return logger


# ---------------------------------------------------------------------------
# Config helpers
# ---------------------------------------------------------------------------
def load_config() -> dict:
    if CONFIG_PATH.exists():
        with CONFIG_PATH.open() as f:
            user_cfg = json.load(f)
        cfg = {**DEFAULT_CONFIG, **user_cfg}
        # Merge nested "sources" dict
        if "sources" in user_cfg:
            cfg["sources"] = {**DEFAULT_CONFIG["sources"], **user_cfg["sources"]}
        return cfg
    return dict(DEFAULT_CONFIG)


def save_config(config: dict) -> None:
    with CONFIG_PATH.open("w") as f:
        json.dump(config, f, indent=2)


# ---------------------------------------------------------------------------
# State persistence
# ---------------------------------------------------------------------------
def load_state() -> dict:
    if STATE_FILE.exists():
        with STATE_FILE.open() as f:
            return json.load(f)
    return {"last_check": None, "last_apply": None, "history": []}


def save_state(state: dict) -> None:
    with STATE_FILE.open("w") as f:
        json.dump(state, f, indent=2, default=str)


# ---------------------------------------------------------------------------
# Update runners
# ---------------------------------------------------------------------------
def _run(cmd: list[str], logger: logging.Logger) -> tuple[int, str, str]:
    """Run a command and return (returncode, stdout, stderr)."""
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=300,
        )
        return result.returncode, result.stdout.strip(), result.stderr.strip()
    except FileNotFoundError:
        return 127, "", f"Command not found: {cmd[0]}"
    except subprocess.TimeoutExpired:
        return 124, "", f"Command timed out: {' '.join(cmd)}"
    except Exception as exc:
        return 1, "", str(exc)


def check_apt(logger: logging.Logger, auto_apply: bool) -> dict:
    result = {"source": "apt", "available": [], "applied": [], "errors": []}

    # Refresh package lists
    rc, out, err = _run(["apt-get", "update", "-qq"], logger)
    if rc != 0:
        result["errors"].append(f"apt-get update failed: {err}")
        logger.warning("apt-get update failed: %s", err)
        return result

    # List upgradable packages
    rc, out, _ = _run(
        ["apt-get", "--simulate", "--just-print", "upgrade"], logger
    )
    upgradable = [
        line for line in out.splitlines() if line.startswith("Inst ")
    ]
    result["available"] = upgradable
    logger.info("apt: %d package(s) available for upgrade", len(upgradable))
    for pkg in upgradable:
        logger.debug("  %s", pkg)

    if auto_apply and upgradable:
        logger.info("apt: applying upgrades …")
        rc, out, err = _run(
            ["apt-get", "upgrade", "-y", "--quiet"], logger
        )
        if rc == 0:
            result["applied"] = upgradable
            logger.info("apt: upgrades applied successfully")
        else:
            result["errors"].append(f"apt-get upgrade failed: {err}")
            logger.error("apt: upgrade failed: %s", err)

    return result


def check_pip(logger: logging.Logger, auto_apply: bool) -> dict:
    result = {"source": "pip", "available": [], "applied": [], "errors": []}

    rc, out, err = _run(
        [sys.executable, "-m", "pip", "list", "--outdated", "--format=json"],
        logger,
    )
    if rc != 0:
        result["errors"].append(f"pip list failed: {err}")
        logger.warning("pip list --outdated failed: %s", err)
        return result

    try:
        outdated = json.loads(out) if out else []
    except json.JSONDecodeError:
        outdated = []

    result["available"] = [f"{p['name']}=={p['latest_version']}" for p in outdated]
    logger.info("pip: %d package(s) outdated", len(outdated))

    if auto_apply and outdated:
        for pkg in outdated:
            name = pkg["name"]
            latest = pkg["latest_version"]
            logger.info("pip: upgrading %s → %s", name, latest)
            rc, _, err = _run(
                [sys.executable, "-m", "pip", "install", "--upgrade", name],
                logger,
            )
            if rc == 0:
                result["applied"].append(f"{name}=={latest}")
            else:
                result["errors"].append(f"pip upgrade {name} failed: {err}")
                logger.error("pip: upgrade of %s failed: %s", name, err)

    return result


def check_npm(logger: logging.Logger, auto_apply: bool) -> dict:
    result = {"source": "npm", "available": [], "applied": [], "errors": []}

    rc, out, err = _run(["npm", "outdated", "-g", "--json"], logger)
    if rc == 127:
        result["errors"].append("npm not found")
        logger.debug("npm not found, skipping")
        return result

    try:
        outdated = json.loads(out) if out else {}
    except json.JSONDecodeError:
        outdated = {}

    result["available"] = [
        f"{name}@{info.get('latest', '?')}" for name, info in outdated.items()
    ]
    logger.info("npm: %d global package(s) outdated", len(outdated))

    if auto_apply and outdated:
        logger.info("npm: upgrading global packages …")
        rc, _, err = _run(["npm", "update", "-g"], logger)
        if rc == 0:
            result["applied"] = list(result["available"])
        else:
            result["errors"].append(f"npm update -g failed: {err}")
            logger.error("npm: global update failed: %s", err)

    return result


# ---------------------------------------------------------------------------
# Core update cycle
# ---------------------------------------------------------------------------
def run_update_cycle(config: dict, logger: logging.Logger) -> dict:
    sources = config.get("sources", {})
    auto_apply = config.get("auto_apply", False)

    logger.info("=" * 60)
    logger.info("Update cycle started  (auto_apply=%s)", auto_apply)
    logger.info("=" * 60)

    cycle_results = {
        "timestamp": datetime.utcnow().isoformat(),
        "checks": [],
    }

    if sources.get("apt"):
        cycle_results["checks"].append(check_apt(logger, auto_apply))
    if sources.get("pip"):
        cycle_results["checks"].append(check_pip(logger, auto_apply))
    if sources.get("npm"):
        cycle_results["checks"].append(check_npm(logger, auto_apply))

    total_available = sum(len(c["available"]) for c in cycle_results["checks"])
    total_applied = sum(len(c["applied"]) for c in cycle_results["checks"])
    total_errors = sum(len(c["errors"]) for c in cycle_results["checks"])

    logger.info(
        "Cycle complete — available: %d  applied: %d  errors: %d",
        total_available, total_applied, total_errors,
    )

    return cycle_results


# ---------------------------------------------------------------------------
# Daemon
# ---------------------------------------------------------------------------
class UpdateDaemon:
    def __init__(self):
        self.config = load_config()
        self.logger = setup_logging(self.config)
        self.state = load_state()
        self._stop_event = threading.Event()
        self._timer: threading.Thread | None = None

    # ---- signal handling ------------------------------------------------
    def _handle_signal(self, signum, frame):
        sig_name = signal.Signals(signum).name
        self.logger.info("Received signal %s, shutting down …", sig_name)
        self.stop()

    def _register_signals(self):
        signal.signal(signal.SIGTERM, self._handle_signal)
        signal.signal(signal.SIGINT, self._handle_signal)
        signal.signal(signal.SIGHUP, self._reload_config)

    def _reload_config(self, signum, frame):
        self.logger.info("SIGHUP received — reloading configuration")
        self.config = load_config()

    # ---- PID file -------------------------------------------------------
    def _write_pid(self):
        PID_FILE.write_text(str(os.getpid()))

    def _remove_pid(self):
        try:
            PID_FILE.unlink(missing_ok=True)
        except Exception:
            pass

    # ---- main loop ------------------------------------------------------
    def _schedule_next(self):
        interval = self.config.get("interval_seconds", 3600)
        self._timer = threading.Timer(interval, self._tick)
        self._timer.daemon = True
        self._timer.start()
        self.logger.info("Next update check in %d second(s)", interval)

    def _tick(self):
        if self._stop_event.is_set():
            return
        result = run_update_cycle(self.config, self.logger)
        self.state["last_check"] = result["timestamp"]
        self.state.setdefault("history", []).append(result)
        # Keep only last 50 cycle summaries
        self.state["history"] = self.state["history"][-50:]
        save_state(self.state)
        if not self._stop_event.is_set():
            self._schedule_next()

    def start(self):
        self._register_signals()
        self._write_pid()
        self.logger.info("UpdateDaemon started (PID %d)", os.getpid())
        self.logger.info("Config: interval=%ds  auto_apply=%s  sources=%s",
                         self.config["interval_seconds"],
                         self.config["auto_apply"],
                         self.config["sources"])

        # Run first cycle immediately
        self._tick()

        # Block main thread until stop event
        self._stop_event.wait()
        self._remove_pid()
        self.logger.info("UpdateDaemon stopped")

    def stop(self):
        self._stop_event.set()
        if self._timer:
            self._timer.cancel()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------
def main():
    parser = argparse.ArgumentParser(
        description="Persistent Update Software Daemon"
    )
    parser.add_argument(
        "--run-once",
        action="store_true",
        help="Run a single update cycle and exit (no daemon loop)",
    )
    parser.add_argument(
        "--config",
        metavar="PATH",
        default=str(CONFIG_PATH),
        help=f"Path to config JSON (default: {CONFIG_PATH})",
    )
    args = parser.parse_args()

    # Allow overriding config path
    global CONFIG_PATH
    CONFIG_PATH = Path(args.config)

    if args.run_once:
        cfg = load_config()
        logger = setup_logging(cfg)
        result = run_update_cycle(cfg, logger)
        print(json.dumps(result, indent=2, default=str))
        return

    daemon = UpdateDaemon()
    daemon.start()


if __name__ == "__main__":
    main()
