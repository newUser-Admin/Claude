#!/usr/bin/env python3
"""
Process management for the Update Daemon.

Manages only the daemon's own process and its child update workers:
  - Query PID, CPU, memory usage
  - Set scheduling priority (nice value)
  - Kill a stuck worker subprocess
  - Health checks with auto-restart
"""

import os
import sys
import signal
import subprocess
from pathlib import Path


BASE_DIR = Path(__file__).resolve().parent
PID_FILE = BASE_DIR / "updater.pid"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _get_pid() -> int | None:
    """Return the daemon PID if the process is alive, else None."""
    if not PID_FILE.exists():
        return None
    try:
        pid = int(PID_FILE.read_text().strip())
        os.kill(pid, 0)          # raises if process is gone
        return pid
    except (ValueError, ProcessLookupError, PermissionError):
        PID_FILE.unlink(missing_ok=True)
        return None


def _proc_stat(pid: int) -> dict:
    """
    Read /proc/<pid>/stat and /proc/<pid>/status to get CPU ticks and
    resident memory for the given PID.  Returns an empty dict on failure.
    """
    info: dict = {"pid": pid}
    try:
        status = Path(f"/proc/{pid}/status").read_text()
        for line in status.splitlines():
            if line.startswith("VmRSS:"):
                parts = line.split()
                info["rss_kb"] = int(parts[1])
            elif line.startswith("Threads:"):
                info["threads"] = int(line.split()[1])
            elif line.startswith("Name:"):
                info["name"] = line.split(None, 1)[1]
    except FileNotFoundError:
        return {}

    try:
        stat = Path(f"/proc/{pid}/stat").read_text().split()
        # field 14 = utime, 15 = stime (in clock ticks)
        utime = int(stat[13])
        stime = int(stat[14])
        info["cpu_ticks"] = utime + stime
        info["state"] = stat[2]   # R, S, D, Z …
    except (FileNotFoundError, IndexError, ValueError):
        pass

    return info


def _child_pids(parent_pid: int) -> list[int]:
    """Return direct child PIDs of parent_pid via /proc."""
    children = []
    try:
        for entry in Path("/proc").iterdir():
            if not entry.name.isdigit():
                continue
            try:
                stat = (entry / "stat").read_text().split()
                ppid = int(stat[3])
                if ppid == parent_pid:
                    children.append(int(entry.name))
            except (FileNotFoundError, IndexError, ValueError):
                continue
    except PermissionError:
        pass
    return children


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------
def daemon_info() -> dict:
    """
    Return resource info for the daemon and its child workers.
    Keys: pid, state, rss_kb, threads, cpu_ticks, children
    """
    pid = _get_pid()
    if pid is None:
        return {"running": False}

    info = _proc_stat(pid)
    if not info:
        return {"running": False}

    info["running"] = True
    info["children"] = []
    for cpid in _child_pids(pid):
        child = _proc_stat(cpid)
        if child:
            info["children"].append(child)

    return info


def set_priority(nice_value: int) -> bool:
    """
    Set the scheduling priority (nice value, -20 to 19) of the daemon.
    Lower values = higher priority. Requires root for negative values.
    Returns True on success.
    """
    pid = _get_pid()
    if pid is None:
        return False
    try:
        os.setpriority(os.PRIO_PROCESS, pid, nice_value)
        return True
    except (PermissionError, OSError):
        return False


def kill_worker(child_pid: int, force: bool = False) -> bool:
    """
    Send SIGTERM (or SIGKILL if force=True) to a child worker PID.
    Only acts if child_pid is actually a child of the daemon.
    """
    daemon_pid = _get_pid()
    if daemon_pid is None:
        return False

    allowed = _child_pids(daemon_pid)
    if child_pid not in allowed:
        return False

    sig = signal.SIGKILL if force else signal.SIGTERM
    try:
        os.kill(child_pid, sig)
        return True
    except (ProcessLookupError, PermissionError):
        return False


def health_check() -> dict:
    """
    Run a health check on the daemon.
    Returns: {"healthy": bool, "reason": str, "pid": int|None}
    """
    pid = _get_pid()
    if pid is None:
        return {"healthy": False, "reason": "daemon not running", "pid": None}

    info = _proc_stat(pid)
    if not info:
        return {"healthy": False, "reason": "process vanished", "pid": pid}

    state = info.get("state", "?")
    if state == "Z":
        return {"healthy": False, "reason": "zombie process", "pid": pid}
    if state == "D":
        return {
            "healthy": False,
            "reason": "process in uninterruptible sleep (D state)",
            "pid": pid,
        }

    return {"healthy": True, "reason": "ok", "pid": pid}


def format_info(info: dict) -> str:
    """Format daemon_info() output as a human-readable string."""
    if not info.get("running"):
        return "Daemon is not running."

    lines = [
        f"  PID      : {info.get('pid', '?')}",
        f"  State    : {info.get('state', '?')}",
        f"  RSS      : {info.get('rss_kb', 0):,} KB  "
        f"({info.get('rss_kb', 0) / 1024:.1f} MB)",
        f"  Threads  : {info.get('threads', '?')}",
        f"  CPU ticks: {info.get('cpu_ticks', '?')}",
    ]
    children = info.get("children", [])
    if children:
        lines.append(f"  Workers  : {len(children)}")
        for c in children:
            lines.append(
                f"    PID {c.get('pid','?'):>6}  state={c.get('state','?')}  "
                f"rss={c.get('rss_kb',0):,} KB"
            )
    else:
        lines.append("  Workers  : none")

    return "\n".join(lines)
