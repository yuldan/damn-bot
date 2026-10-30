#!/usr/bin/env python3
"""Hermes side of the F4 budget baseline (ADR-065/083, ROADMAP F4).

Runs the pinned hermes 868b9f244f gateway with only the Telegram adapter
enabled, pointed at a loopback fake Bot API, then measures it with the same
/proc method as `make perf-f4`: 10 s warmup, 60 s window, process-wide
utime+stime, VmRSS and VmHWM. No LLM call is made.

The hermes gateway needs Python >= 3.14 plus the `telegram` extra; the pinned
environment is described in bench/f3/README.md. When the environment or the
gateway is unavailable the script still writes the artefact with an explicit
`n/a` status and the captured prerequisite error instead of inventing numbers.

Usage:
  HERMES_VENV=~/.cache/damnbot/hermes-venv \
  HERMES_SRC=~/.cache/damnbot/f2-inputs/hermes-src \
  python3 bench/f4/hermes_f4.py [--out bench/f4/results/hermes-868b9f244f-F4.json]
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HERMES_COMMIT = "868b9f244f"
WARMUP_S = 10.0
WINDOW_S = 60.0
CLOCK_TICKS = 100.0  # Linux USER_HZ
TOKEN = "424242:fake-telegram-token"


class FakeBotAPI:
    """The Bot API subset python-telegram-bot touches on connect and polling."""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.calls: list[str] = []
        self.updates: int = 0
        self.wake = threading.Event()
        self.started = time.monotonic()
        fake = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_args) -> None:  # keep stdout clean
                pass

            def do_POST(self) -> None:  # noqa: N802 (BaseHTTPRequestHandler API)
                size = int(self.headers.get("content-length") or 0)
                body = self.rfile.read(size)
                method = self.path.rsplit("/", 1)[-1]
                with fake.lock:
                    fake.calls.append(method)
                if method == "getUpdates":
                    with fake.lock:
                        fake.updates += 1
                    timeout = _timeout(self.path, body)
                    if timeout > 0:
                        fake.wake.wait(timeout=timeout)
                    fake.wake.clear()
                    payload: object = []
                elif method == "getMe":
                    payload = {"id": 4242, "is_bot": True, "first_name": "fake", "username": "fake_bot"}
                elif method == "getWebhookInfo":
                    payload = {"url": "", "has_custom_certificate": False, "pending_update_count": 0}
                elif method in {"sendMessage", "editMessageText"}:
                    payload = {
                        "message_id": 1,
                        "date": int(time.time()),
                        "chat": {"id": 7788, "type": "private"},
                        "text": "ok",
                    }
                else:
                    payload = True
                raw = json.dumps({"ok": True, "result": payload}).encode()
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self) -> None:  # noqa: N802
                self.send_response(200)
                self.send_header("content-length", "0")
                self.end_headers()

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def url(self, suffix: str) -> str:
        return f"http://127.0.0.1:{self.port}/{suffix}"

    def stop(self) -> None:
        self.wake.set()
        self.server.shutdown()


def _timeout(path: str, body: bytes) -> float:
    """Long-poll timeout in seconds from the query string or JSON/form body."""
    raw = None
    try:
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
        if "timeout" in query:
            raw = query["timeout"][0]
        elif body:
            payload = json.loads(body)
            if isinstance(payload, dict):
                raw = payload.get("timeout")
    except (ValueError, TypeError):
        raw = None
    try:
        return min(max(float(raw), 0.0), 30.0) if raw is not None else 30.0
    except (ValueError, TypeError):
        return 30.0


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def proc_sample(pid: int) -> dict:
    status = Path(f"/proc/{pid}/status").read_text()
    out = {}
    for line in status.splitlines():
        if line.startswith(("VmRSS:", "VmHWM:")):
            key, rest = line.split(":", 1)
            out[key] = int(rest.split()[0])
    stat = Path(f"/proc/{pid}/stat").read_text()
    fields = stat[stat.rfind(")") + 2 :].split()
    ticks = int(fields[11]) + int(fields[12])
    out["ticks"] = ticks
    out["at"] = time.monotonic()
    return out


def loadavg() -> str:
    try:
        return Path("/proc/loadavg").read_text().strip()
    except OSError:
        return ""


def cpu_model() -> str:
    try:
        for line in Path("/proc/cpuinfo").read_text().splitlines():
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return ""


def write(out: Path, artifact: dict) -> None:
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(artifact, indent=1) + "\n")


def not_measured(reason: str, **extra) -> dict:
    return {"status": "n/a", "hermes": None, "reason": reason, **extra}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="bench/f4/results/hermes-868b9f244f-F4.json")
    ap.add_argument("--src", default=os.environ.get("HERMES_SRC", ""))
    ap.add_argument("--venv", default=os.environ.get("HERMES_VENV", ""))
    args = ap.parse_args()
    root = Path(__file__).resolve().parents[2]
    out = Path(args.out)
    if not out.is_absolute():
        out = root / out
    src = Path(args.src or os.environ.get("HERMES_SRC", "")).expanduser()
    venv = Path(args.venv or os.environ.get("HERMES_VENV", "")).expanduser()

    meta = {
        "phase": "F4",
        "date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "hermes_commit": HERMES_COMMIT,
        "hermes_src": str(src),
        "python": "",
        "harness": "bench/f4/hermes_f4.py (same /proc method as make perf-f4)",
        "warmup_s": WARMUP_S,
        "window_s": WINDOW_S,
        "clock_ticks": CLOCK_TICKS,
        "cpu": cpu_model(),
        "kernel": os.uname().release,
        "loadavg_start": loadavg(),
        "loadavg_end": "",
        "host_note": "workstation measurement, not the Q14 reference machine",
        "llm_calls": 0,
    }
    artifact = {
        "meta": meta,
        "status": "n/a",
        "dimensions": {
            "cpu_idle": not_measured("gateway did not start"),
            "rss_idle": not_measured("gateway did not start"),
            "peak_rss_soak_1000_turns": not_measured("requires the 1 000-turn soak harness; not run by the idle budget harness"),
            "crash_hang_recovery_self_healing": not_measured("requires the chaos/SELF-HEALING suite; not run by the idle budget harness"),
            "foreign_gateway_access": not_measured("requires the E8 Telegram scenario suite; not run by the idle budget harness"),
        },
        "evidence": {},
    }

    python = venv / "bin" / "python"
    hermes = venv / "bin" / "hermes"
    if not python.exists() or not hermes.exists():
        artifact["evidence"]["prerequisite"] = f"missing hermes venv: {venv}"
        write(out, artifact)
        print(f"hermes F4: n/a ({artifact['evidence']['prerequisite']})", file=sys.stderr)
        return 0
    if not (src / "gateway" / "run.py").exists():
        artifact["evidence"]["prerequisite"] = f"missing hermes source: {src}"
        write(out, artifact)
        print(f"hermes F4: n/a ({artifact['evidence']['prerequisite']})", file=sys.stderr)
        return 0

    try:
        ver = subprocess.run([str(python), "-V"], capture_output=True, text=True, timeout=30)
        meta["python"] = ver.stdout.strip() or ver.stderr.strip()
    except (OSError, subprocess.SubprocessError):
        pass

    home = Path(tempfile.mkdtemp(prefix="damnbot-f4-hermes-"))
    log = home / "gateway.log"
    bot = FakeBotAPI()
    config = home / "config.yaml"
    config.write_text(
        "platforms:\n"
        "  telegram:\n"
        "    enabled: true\n"
        f'    token: "{TOKEN}"\n'
        "    extra:\n"
        f'      base_url: "{bot.url("bot")}"\n'
        f'      base_file_url: "{bot.url("file/bot")}"\n'
        "gateway:\n"
        "  on_all_adapters_down: stay_alive\n"
        "  loop_watchdog: false\n"
    )
    env = {
        **os.environ,
        "HOME": str(home),
        "HERMES_HOME": str(home),
        "XDG_CONFIG_HOME": str(home),
        "TELEGRAM_BOT_TOKEN": TOKEN,
        "PYTHONUNBUFFERED": "1",
        "PYTHONDONTWRITEBYTECODE": "1",
    }
    child = None
    try:
        with log.open("w") as err:
            child = subprocess.Popen(
                [str(python), "-m", "gateway.run", "--config", str(config)],
                cwd=str(src), env=env, stdout=err, stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        started = time.monotonic()
        deadline = started + 180
        connected = False
        while time.monotonic() < deadline:
            if child.poll() is not None:
                break
            if "Connected to Telegram" in log.read_text(errors="replace"):
                connected = True
                break
            time.sleep(0.1)
        artifact["meta"]["startup_s"] = round(time.monotonic() - started, 3)
        artifact["meta"]["telegram_connected"] = connected
        if not connected:
            tail = log.read_text(errors="replace").splitlines()[-30:]
            artifact["evidence"] = {
                "prerequisite": "hermes gateway did not reach Telegram polling",
                "exit": child.poll(),
                "stderr_tail": tail,
            }
            write(out, artifact)
            print("hermes F4: n/a (gateway did not reach polling)", file=sys.stderr)
            return 0

        time.sleep(WARMUP_S)
        first = proc_sample(child.pid)
        time.sleep(WINDOW_S)
        second = proc_sample(child.pid)
        elapsed = second["at"] - first["at"]
        cpu_pct = (second["ticks"] - first["ticks"]) / CLOCK_TICKS / elapsed * 100
        rss_mb = second.get("VmRSS", 0) / 1024
        peak_mb = second.get("VmHWM", 0) / 1024
        artifact["status"] = "measured"
        artifact["dimensions"]["cpu_idle"] = {
            "status": "measured", "agent": "hermes", "percent": round(cpu_pct, 3),
            "seconds": round((second["ticks"] - first["ticks"]) / CLOCK_TICKS, 3),
            "window_s": round(elapsed, 3),
            "method": "process-wide utime+stime delta from /proc/<pid>/stat",
        }
        artifact["dimensions"]["rss_idle"] = {
            "status": "measured", "agent": "hermes", "rss_mb": round(rss_mb, 1),
            "peak_mb": round(peak_mb, 1), "method": "VmRSS/VmHWM from /proc/<pid>/status",
        }
        artifact["evidence"] = {
            "pid": child.pid,
            "bot_api_calls": sorted(set(bot.calls)),
            "telegram_connected": connected,
            "gateway_log_tail": log.read_text(errors="replace").splitlines()[-10:],
        }
        artifact["meta"]["loadavg_end"] = loadavg()
        write(out, artifact)
        print(f"hermes F4: rss={rss_mb:.1f} MB peak={peak_mb:.1f} MB cpu={cpu_pct:.3f}%", file=sys.stderr)
        return 0
    finally:
        if child is not None and child.poll() is None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except OSError:
                child.terminate()
            try:
                child.wait(timeout=30)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except OSError:
                    child.kill()
                child.wait(timeout=10)
        bot.stop()
        shutil.rmtree(home, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
