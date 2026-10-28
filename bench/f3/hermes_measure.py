#!/usr/bin/env python3
"""Measure the Hermes side of F3 with the same shape as cmd/f3measure.

This is the Hermes counterpart of bench/f3/cmd/f3measure. It uses a real PTY
and a timestamped loopback OpenAI-compatible SSE server:

  T0 = the instant immediately before the fake provider writes the first
       ``content`` frame that carries ``F3TOKEN`` for the benchmark prompt.
  T1 = the instant ``F3TOKEN`` becomes visible in the PTY.

Two surfaces are measured for the same dimension: the classic prompt_toolkit
REPL (``hermes chat --cli``) and the real Ink TUI (``hermes --tui``, React+Ink
over the Python ``tui_gateway``). The TUI needs a Node build (``tui_dist`` or
``ui-tui/dist``); ``--surface tui`` fails with the build recipe when it is
absent rather than inventing numbers.

The script also measures process startup (first PTY byte, first painted frame,
and the prompt-ready marker) and, with --build-runs, a clean editable install
into a fresh venv and an incremental reinstall in that same venv. With
``--tui-build-runs`` it times the TUI install/bundle commands instead.

Everything lives under ~/.cache/damnbot: the pinned interpreter, the venv, the
private HERMES_HOME, pip's cache, and temporary trees. No network is required
for the first-token runs (the fake provider is loopback); the clean install
does use PyPI by definition.
"""

from __future__ import annotations

import argparse
import http.server
import json
import os
import pty
import re
import select
import shutil
import signal
import subprocess
import sys
import threading
import time
import fcntl
import termios
import struct

TOKEN = "F3TOKEN"
PROMPT = "f3-prompt"
FAKE_MODEL = "openai/gpt-fake"
READY_MARKER = b"Welcome to Hermes Agent! Type your message"
# Ink paints a chrome frame with this status line first (before the gateway
# answers); the intro message with the tool list appears once the composer is
# usable. Both are plain text in the TUI bundle (en locale, NO_COLOR-safe).
TUI_FRAME_MARKER = b"summoning hermes"
TUI_READY_MARKER = b"Available Tools"

CONFIG = """\
model:
  provider: custom
  default: "{model}"
  base_url: "{base_url}"
  api_key: "f3-bench"
  api_mode: chat_completions
agent:
  max_turns: 1
display:
  interface: cli
telemetry:
  shared_metrics:
    enabled: false
    send: false
    offer_version: 2
"""


class FakeProvider(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), Handler)
        self.first = 0.0
        self.requests = 0
        self.lock = threading.Lock()

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.server_address[1]}/v1"


class Handler(http.server.BaseHTTPRequestHandler):
    server: FakeProvider
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args) -> None:  # keep the measurement output clean
        pass

    def _body(self) -> bytes:
        n = int(self.headers.get("content-length", "0") or 0)
        return self.rfile.read(n) if n else b""

    def do_GET(self) -> None:  # noqa: N802 - stdlib hook
        self.send_response(404)
        self.send_header("content-length", "0")
        self.send_header("connection", "close")
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802 - stdlib hook
        body = self._body()
        try:
            payload = json.loads(body or b"{}")
        except ValueError:
            payload = {}
        with self.server.lock:
            self.server.requests += 1
        stream = payload.get("stream") is True
        has_prompt = any(
            isinstance(m, dict) and PROMPT in str(m.get("content", ""))
            for m in payload.get("messages", [])
        )
        # The main turn is the streaming completion that carries the prompt.
        # Title generation and metadata probes are answered separately so they
        # can never become T0 or render F3TOKEN.
        main = stream and has_prompt
        if main:
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("cache-control", "no-cache")
            self.send_header("connection", "close")
            self.end_headers()
            with self.server.lock:
                if not self.server.first:
                    self.server.first = time.perf_counter()
            frames = [
                '{"id":"f3","object":"chat.completion.chunk","created":0,"model":"%s","choices":[{"index":0,"delta":{"role":"assistant","content":"%s"},"finish_reason":null}]}' % (FAKE_MODEL, TOKEN),
                '{"id":"f3","object":"chat.completion.chunk","created":0,"model":"%s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}' % FAKE_MODEL,
            ]
            for frame in frames:
                self.wfile.write(("data: " + frame + "\n\n").encode())
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            return
        out = json.dumps(
            {
                "id": "aux",
                "object": "chat.completion",
                "created": 0,
                "model": FAKE_MODEL,
                "choices": [
                    {"index": 0, "message": {"role": "assistant", "content": "F3 session"}, "finish_reason": "stop"}
                ],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            }
        ).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(out)))
        self.send_header("connection", "close")
        self.end_headers()
        self.wfile.write(out)


def kill_home(home: str) -> int:
    """Kill processes carrying HERMES_HOME=<home> (the detached gateway)."""
    want = ("HERMES_HOME=" + home).encode()
    pids = []
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            env = open(f"/proc/{name}/environ", "rb").read().split(b"\0")
        except OSError:
            continue
        if want in env:
            pids.append(int(name))
    for sig in (signal.SIGTERM, signal.SIGKILL):
        for pid in pids:
            try:
                os.kill(pid, sig)
            except OSError:
                pass
        time.sleep(0.2)
        pids = [p for p in pids if os.path.exists(f"/proc/{p}") and _still_home(p, want)]
    return len(pids)


def _still_home(pid: int, want: bytes) -> bool:
    try:
        return want in open(f"/proc/{pid}/environ", "rb").read().split(b"\0")
    except OSError:
        return False


def wait_gateway_gone(home: str, timeout: float = 10.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if kill_home(home) == 0:
            return
        time.sleep(0.2)


def pct(xs: list[float]) -> tuple[float, float]:
    if not xs:
        return 0.0, 0.0
    s = sorted(xs)

    def rank(p: float) -> float:
        i = int(float(len(s)) * p + 0.999999) - 1
        return s[min(max(i, 0), len(s) - 1)]

    return rank(0.50), rank(0.95)


def loadavg() -> str:
    try:
        return open("/proc/loadavg").read().strip()
    except OSError:
        return ""


def cpu_model() -> str:
    try:
        for line in open("/proc/cpuinfo"):
            if line.startswith("model name"):
                return line.split(":", 1)[1].strip()
    except OSError:
        pass
    return ""


def run_once(hermes: str, home: str, provider: FakeProvider, timeout: float) -> dict:
    """One PTY run. Returns startup and first-token deltas in milliseconds."""
    cfg = os.path.join(home, "config.yaml")
    os.makedirs(home, exist_ok=True)
    with open(cfg, "w", encoding="utf-8") as f:
        f.write(CONFIG.format(model=FAKE_MODEL, base_url=provider.base_url))
    os.chmod(cfg, 0o600)
    obj = os.path.join(home, "hermes-objects")
    os.makedirs(obj, exist_ok=True)

    env = {
        "HOME": home,
        "HERMES_HOME": home,
        "XDG_CONFIG_HOME": os.path.join(home, ".config"),
        "XDG_CACHE_HOME": os.path.join(home, ".cache"),
        "XDG_DATA_HOME": os.path.join(home, ".local", "share"),
        "XDG_STATE_HOME": os.path.join(home, ".local", "state"),
        "TMPDIR": os.path.join(home, "tmp"),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONUNBUFFERED": "1",
        "TERM": "xterm-256color",
        "COLUMNS": "120",
        "LINES": "40",
        "LANG": "C.UTF-8",
        "NO_COLOR": "1",
        "PATH": "/usr/local/bin:/usr/bin:/bin",
        "HERMES_OBJECTS_DIR": obj,
    }
    for d in (env["XDG_CONFIG_HOME"], env["XDG_CACHE_HOME"], env["XDG_DATA_HOME"], env["XDG_STATE_HOME"], env["TMPDIR"]):
        os.makedirs(d, exist_ok=True)

    argv = [hermes, "chat", "--cli", "--yolo", "--source", "tool", "--max-turns", "1"]
    with provider.lock:
        provider.first = 0.0
    t0 = time.perf_counter()
    pid, fd = pty.fork()
    if pid == 0:  # child
        try:
            os.execvpe(hermes, argv, env)
        except BaseException:
            os._exit(127)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

    first_output = 0.0
    ready = 0.0
    token_at = 0.0
    buf = b""
    deadline = time.time() + timeout
    try:
        while time.time() < deadline:
            r, _, _ = select.select([fd], [], [], 0.1)
            if not r:
                continue
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            now = time.perf_counter()
            if not first_output:
                first_output = now
            buf += chunk
            if not ready and READY_MARKER in buf and (b"Ask anything" in buf or b"\xe2\x9d\xaf" in buf):
                ready = now
                # The input widget is painted after the welcome line. Give the
                # app loop a moment, then type. The REPL ignores Enter that
                # arrives within 50 ms of the last text change (it treats it as
                # a pasted newline), so type the prompt first, pause, submit.
                time.sleep(0.8)
                os.write(fd, PROMPT.encode())
                time.sleep(0.3)
                os.write(fd, b"\r")
                time.sleep(0.4)
                os.write(fd, b"\r")
            if ready and not token_at and TOKEN.encode() in buf:
                token_at = now
                break
    finally:
        try:
            os.killpg(os.getpgid(pid), signal.SIGKILL)
        except OSError:
            try:
                os.kill(pid, signal.SIGKILL)
            except OSError:
                pass
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.waitpid(pid, 0)
        except OSError:
            pass

    if not first_output:
        raise RuntimeError(f"no PTY output: {buf[-800:]!r}")
    if not ready:
        raise RuntimeError(f"prompt not ready within {timeout}s: {buf[-1200:]!r}")
    if not token_at:
        raise RuntimeError(f"F3TOKEN not rendered: {buf[-1200:]!r}")
    if not provider.first:
        raise RuntimeError("fake provider never saw the streaming prompt request")
    return {
        "startup_first_output_ms": (first_output - t0) * 1000.0,
        "startup_prompt_ready_ms": (ready - t0) * 1000.0,
        "first_token_ms": (token_at - provider.first) * 1000.0,
        "provider_requests": provider.requests,
    }


def run_tui_once(hermes: str, home: str, provider: FakeProvider, timeout: float,
                 python: str, node: str) -> dict:
    """One Ink TUI run under a PTY. Returns startup and first-token deltas (ms).

    The TUI is ``hermes --tui``: Node runs the esbuild bundle, which spawns
    ``python -m tui_gateway.entry`` over newline-delimited JSON-RPC. The gateway
    reads the same private ``config.yaml`` as the classic REPL.
    """
    cfg = os.path.join(home, "config.yaml")
    os.makedirs(home, exist_ok=True)
    with open(cfg, "w", encoding="utf-8") as f:
        f.write(CONFIG.format(model=FAKE_MODEL, base_url=provider.base_url))
    os.chmod(cfg, 0o600)
    obj = os.path.join(home, "hermes-objects")
    os.makedirs(obj, exist_ok=True)

    env = {
        "HOME": home,
        "HERMES_HOME": home,
        # The TUI launcher resolves its runtime through PM unless these are
        # explicit; the PM-managed Node needs libatomic from sudo.
        "HERMES_PYTHON": python,
        "HERMES_NODE": node,
        "XDG_CONFIG_HOME": os.path.join(home, ".config"),
        "XDG_CACHE_HOME": os.path.join(home, ".cache"),
        "XDG_DATA_HOME": os.path.join(home, ".local", "share"),
        "XDG_STATE_HOME": os.path.join(home, ".local", "state"),
        "TMPDIR": os.path.join(home, "tmp"),
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONUNBUFFERED": "1",
        "TERM": "xterm-256color",
        "COLUMNS": "120",
        "LINES": "40",
        "LANG": "C.UTF-8",
        "NO_COLOR": "1",
        "PATH": "/usr/local/bin:/usr/bin:/bin",
        "HERMES_OBJECTS_DIR": obj,
    }
    for d in (env["XDG_CONFIG_HOME"], env["XDG_CACHE_HOME"], env["XDG_DATA_HOME"],
              env["XDG_STATE_HOME"], env["TMPDIR"]):
        os.makedirs(d, exist_ok=True)

    argv = [hermes, "--tui", "--yolo"]
    with provider.lock:
        provider.first = 0.0
    t0 = time.perf_counter()
    pid, fd = pty.fork()
    if pid == 0:  # child
        try:
            os.execvpe(hermes, argv, env)
        except BaseException:
            os._exit(127)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

    first_output = 0.0
    first_frame = 0.0
    ready = 0.0
    token_at = 0.0
    buf = b""
    deadline = time.time() + timeout
    try:
        while time.time() < deadline:
            r, _, _ = select.select([fd], [], [], 0.1)
            if not r:
                continue
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            now = time.perf_counter()
            if not first_output:
                first_output = now
            buf += chunk
            if not first_frame and TUI_FRAME_MARKER in buf:
                first_frame = now
            if not ready and TUI_READY_MARKER in buf:
                ready = now
                # The intro is painted with the composer; type once, then
                # submit. One Enter is enough (unlike the classic REPL).
                time.sleep(0.6)
                os.write(fd, PROMPT.encode())
                time.sleep(0.35)
                os.write(fd, b"\r")
            if ready and not token_at and TOKEN.encode() in buf:
                token_at = now
                break
    finally:
        try:
            os.killpg(os.getpgid(pid), signal.SIGKILL)
        except OSError:
            try:
                os.kill(pid, signal.SIGKILL)
            except OSError:
                pass
        try:
            os.close(fd)
        except OSError:
            pass
        try:
            os.waitpid(pid, 0)
        except OSError:
            pass

    if not first_output:
        raise RuntimeError(f"no PTY output: {buf[-800:]!r}")
    if not first_frame:
        raise RuntimeError(f"first TUI frame not painted within {timeout}s: {buf[-1200:]!r}")
    if not ready:
        raise RuntimeError(f"TUI composer not ready within {timeout}s: {buf[-1200:]!r}")
    if not token_at:
        raise RuntimeError(f"F3TOKEN not rendered by the TUI: {buf[-1200:]!r}")
    if not provider.first:
        raise RuntimeError("fake provider never saw the streaming prompt request")
    return {
        "startup_first_output_ms": (first_output - t0) * 1000.0,
        "startup_first_frame_ms": (first_frame - t0) * 1000.0,
        "startup_prompt_ready_ms": (ready - t0) * 1000.0,
        "first_token_ms": (token_at - provider.first) * 1000.0,
        "provider_requests": provider.requests,
    }



def build_measure(src: str, py: str, runs: int, work: str) -> dict:
    clean, inc = [], []
    for i in range(runs):
        root = os.path.join(work, f"build-{i}")
        shutil.rmtree(root, ignore_errors=True)
        os.makedirs(root)
        venv = os.path.join(root, "venv")
        env = {
            "HOME": root,
            "TMPDIR": os.path.join(root, "tmp"),
            "PIP_CACHE_DIR": os.path.join(root, "pip-cache"),
            "PIP_DISABLE_PIP_VERSION_CHECK": "1",
            "PATH": "/usr/local/bin:/usr/bin:/bin",
            "LANG": "C.UTF-8",
        }
        os.makedirs(env["TMPDIR"])
        subprocess.run([py, "-m", "venv", venv], check=True, env=env, timeout=120)
        pip = os.path.join(venv, "bin", "pip")
        t0 = time.perf_counter()
        proc = subprocess.run(
            [pip, "install", "--no-cache-dir", "--disable-pip-version-check", "-e", src],
            cwd=src,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=1800,
        )
        elapsed = time.perf_counter() - t0
        if proc.returncode != 0:
            raise RuntimeError(f"clean install failed: {proc.stdout[-2000:].decode('utf-8', 'replace')}")
        clean.append(elapsed)
        # Incremental: the same venv, rebuild only the editable wheel and
        # reinstall the console scripts; dependencies are already present.
        probe = os.path.join(src, "hermes_cli", "version_info.py")
        with open(probe, "a", encoding="utf-8") as f:
            f.write("\n# f3 incremental install probe\n")
        t0 = time.perf_counter()
        proc = subprocess.run(
            [pip, "install", "--no-cache-dir", "--disable-pip-version-check", "--no-deps", "-e", src],
            cwd=src,
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=600,
        )
        elapsed = time.perf_counter() - t0
        # Remove the probe comment so repeated runs start from the same inputs.
        with open(probe, encoding="utf-8") as f:
            text = f.read()
        text = text.replace("\n# f3 incremental install probe\n", "")
        with open(probe, "w", encoding="utf-8") as f:
            f.write(text)
        if proc.returncode != 0:
            raise RuntimeError(f"incremental install failed: {proc.stdout[-2000:].decode('utf-8', 'replace')}")
        inc.append(elapsed)
        shutil.rmtree(root, ignore_errors=True)
    cp50, cp95 = pct(clean)
    ip50, ip95 = pct(inc)
    return {
        "clean": {
            "method": "fresh `python -m venv` + `pip install --no-cache-dir -e <hermes-src>` (deps downloaded from PyPI; venv creation included)",
            "samples_s": clean,
            "p50_s": cp50,
            "p95_s": cp95,
        },
        "incremental": {
            "method": "same venv, comment appended to hermes_cli/version_info.py, `pip install --no-cache-dir --no-deps -e <hermes-src>`",
            "samples_s": inc,
            "p50_s": ip50,
            "p95_s": ip95,
        },
        "note": "Python packaging has no compile step; clean install is dominated by dependency download/wheel extraction. Measured on the workstation network, not a clean 10 Mbit/s VM.",
    }


def tui_build_measure(src: str, node: str, npm: str, runs: int, work: str) -> dict:
    """Time the two commands that produce the Ink bundle.

    A sample is ``npm ci --workspace ui-tui`` followed by ``npm run build`` in
    the same checkout. The workspace selector is deliberate: a root ``npm ci``
    also pulls apps/desktop's and tests-js's ``node-pty``, which rebuilds from
    source when a prebuild is missing and needs a C++ toolchain the TUI does
    not. ``npm run build`` writes ``ui-tui/dist``; publishing it as
    ``hermes_cli/tui_dist`` is a copy step described in the README.
    """
    os.makedirs(work, exist_ok=True)
    env = {
        "HOME": work,
        "TMPDIR": os.path.join(work, "tmp"),
        "npm_config_cache": os.path.join(work, "npm-cache"),
        "npm_config_audit": "false",
        "npm_config_fund": "false",
        "PATH": "/usr/local/bin:/usr/bin:/bin",
        "LANG": "C.UTF-8",
        "CI": "1",
    }
    os.makedirs(env["TMPDIR"], exist_ok=True)
    installs, bundles = [], []
    for i in range(runs):
        t0 = time.perf_counter()
        proc = subprocess.run(
            [npm, "ci", "--workspace", "ui-tui", "--no-audit", "--no-fund", "--loglevel=error"],
            cwd=src, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=1800,
        )
        install = time.perf_counter() - t0
        if proc.returncode != 0:
            raise RuntimeError(f"npm ci failed: {proc.stdout[-2000:].decode('utf-8', 'replace')}")
        t0 = time.perf_counter()
        proc = subprocess.run(
            [npm, "run", "build"],
            cwd=os.path.join(src, "ui-tui"), env=env,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=900,
        )
        bundle = time.perf_counter() - t0
        if proc.returncode != 0:
            raise RuntimeError(f"npm run build failed: {proc.stdout[-2000:].decode('utf-8', 'replace')}")
        installs.append(round(install, 3))
        bundles.append(round(bundle, 3))
        print(f"tui build {i + 1}/{runs}: install {install:.1f} s, bundle {bundle:.1f} s", file=sys.stderr)
    i50, i95 = pct(installs)
    b50, b95 = pct(bundles)
    return {
        "node": subprocess.run([node, "--version"], capture_output=True, text=True).stdout.strip(),
        "npm": subprocess.run([npm, "--version"], capture_output=True, text=True).stdout.strip(),
        "install": {
            "command": "npm ci --workspace ui-tui --no-audit --no-fund",
            "cwd": "$HERMES_SRC",
            "samples_s": installs,
            "p50_s": i50,
            "p95_s": i95,
        },
        "bundle": {
            "command": "npm run build  # -> scripts/build/tui.mjs -> ui-tui/dist/entry.js",
            "cwd": "$HERMES_SRC/ui-tui",
            "samples_s": bundles,
            "p50_s": b50,
            "p95_s": b95,
        },
        "publish": "cp -a ui-tui/dist hermes_cli/tui_dist && printf '{\"type\":\"module\"}\\n' > hermes_cli/tui_dist/package.json",
        "note": "npm's cache lives under the build work directory: the first sample filled it, the next two reused it. A root `npm ci` fails on this host because node-pty (apps/desktop, tests-js) has no linux-x64 prebuild and no g++ is installed; the TUI workspace does not use it.",
    }


def _reuse_cli(path: str) -> dict:
    """Carry the earlier classic-REPL numbers into the new artefact shape."""
    with open(path, encoding="utf-8") as f:
        old = json.load(f)
    dims = old.get("dimensions", {})
    if "cli_repl" in dims:
        return dims["cli_repl"]
    if "first_token_tui" not in dims:
        raise SystemExit(f"{path}: no cli_repl/first_token_tui to reuse")
    return {
        "first_token": dims["first_token_tui"],
        "startup": dims.get("startup_first_frame", {}),
        "reused_from": os.path.basename(path),
        "note": "classic-REPL numbers were measured in the earlier CLI pass over the same checkout; this pass added the Ink TUI.",
    }


def _reuse_buildtime(path: str) -> dict:
    with open(path, encoding="utf-8") as f:
        return json.load(f).get("dimensions", {}).get("buildtime", {"note": "unavailable"})


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--hermes", required=True, help="path to the hermes console script")
    ap.add_argument("--python", required=True, help="interpreter used to build throwaway venvs")
    ap.add_argument("--src", required=True, help="Hermes source checkout")
    ap.add_argument("--out", required=True, help="artefact JSON path")
    ap.add_argument("--home", required=True, help="private HERMES_HOME (under ~/.cache/damnbot)")
    ap.add_argument("--tui-home", help="private HERMES_HOME for the TUI pass (default: <home>-tui)")
    ap.add_argument("--surface", choices=("both", "cli", "tui"), default="both")
    ap.add_argument("--runs", type=int, default=20)
    ap.add_argument("--tui-runs", type=int, help="TUI samples (default: --runs)")
    ap.add_argument("--build-runs", type=int, default=3)
    ap.add_argument("--tui-build-runs", type=int, default=3)
    ap.add_argument("--skip-build", action="store_true")
    ap.add_argument("--skip-tui-build", action="store_true")
    ap.add_argument("--tui-node", default="/usr/bin/node")
    ap.add_argument("--tui-python", help="interpreter for tui_gateway (default: sibling of --hermes)")
    ap.add_argument("--reuse", help="earlier artefact whose classic-REPL/build numbers are carried over")
    ap.add_argument("--timeout", type=float, default=90.0)
    args = ap.parse_args()
    if args.surface == "both" and args.reuse:
        raise SystemExit("--reuse only makes sense with --surface cli|tui (one fresh surface)")
    args.tui_runs = args.tui_runs if args.tui_runs is not None else args.runs
    args.tui_home = args.tui_home or args.home + "-tui"
    args.tui_python = args.tui_python or os.path.join(os.path.dirname(args.hermes), "python")

    for path in (args.hermes, args.python, args.src, args.tui_node, args.tui_python):
        if not os.path.exists(path):
            raise SystemExit(f"missing: {path}")

    tui_dist = os.path.join(args.src, "hermes_cli", "tui_dist", "entry.js")
    tui_source = os.path.join(args.src, "ui-tui", "dist", "entry.js")
    if args.surface in ("both", "tui") and not (os.path.isfile(tui_dist) or os.path.isfile(tui_source)):
        raise SystemExit(
            "no TUI bundle: expected hermes_cli/tui_dist/entry.js or ui-tui/dist/entry.js. "
            "Build it with bench/f3/hermes_tui.sh (see README §Hermes side)."
        )

    loadavg_start = loadavg()
    provider = FakeProvider()
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    cli = None
    tui = None
    try:
        if args.surface in ("both", "cli"):
            wait_gateway_gone(args.home)
            shutil.rmtree(args.home, ignore_errors=True)
            os.makedirs(args.home, exist_ok=True)
            # Cold sample: starts the detached gateway and the first session.
            cold = run_once(args.hermes, args.home, provider, args.timeout)
            samples, startup_first, startup_ready = [], [], []
            for i in range(args.runs):
                r = run_once(args.hermes, args.home, provider, args.timeout)
                samples.append(round(r["first_token_ms"], 3))
                startup_first.append(round(r["startup_first_output_ms"], 3))
                startup_ready.append(round(r["startup_prompt_ready_ms"], 3))
                print(f"cli {i + 1}/{args.runs}: token {r['first_token_ms']:.1f} ms, startup {r['startup_prompt_ready_ms']:.0f} ms", file=sys.stderr)
            p50, p95 = pct(samples)
            f50, f95 = pct(startup_first)
            r50, r95 = pct(startup_ready)
            cli = {
                "first_token": {
                    "agent": "hermes",
                    "surface": "classic prompt_toolkit REPL (`hermes chat --cli`) under a PTY",
                    "method": "T0 = fake provider writes the first streaming content frame carrying F3TOKEN; T1 = F3TOKEN visible in PTY output",
                    "token": TOKEN,
                    "runs": args.runs,
                    "samples_ms": samples,
                    "p50_ms": p50,
                    "p95_ms": p95,
                    "cold_start_sample_ms": round(cold["first_token_ms"], 3),
                    "warm_gateway": True,
                    "difference_from_damnbot": "Hermes runs the turn in a detached local gateway, so T0→T1 includes a gateway hop and rich-console rendering; the Damn-Bot harness measures the in-process TUI path.",
                },
                "startup": {
                    "method": "T0 = before pty fork/exec of `hermes chat --cli`; first_output = first PTY byte; prompt_ready = 'Welcome to Hermes Agent! Type your message' seen",
                    "runs": args.runs,
                    "first_output": {"samples_ms": startup_first, "p50_ms": f50, "p95_ms": f95},
                    "prompt_ready": {"samples_ms": startup_ready, "p50_ms": r50, "p95_ms": r95},
                    "cold": {
                        "first_output_ms": round(cold["startup_first_output_ms"], 3),
                        "prompt_ready_ms": round(cold["startup_prompt_ready_ms"], 3),
                        "note": "includes starting the detached gateway; excluded from the 20 warm samples",
                    },
                },
            }
        else:
            cli = _reuse_cli(args.reuse) if args.reuse else None

        if args.surface in ("both", "tui"):
            wait_gateway_gone(args.tui_home)
            shutil.rmtree(args.tui_home, ignore_errors=True)
            os.makedirs(args.tui_home, exist_ok=True)
            cold = run_tui_once(args.hermes, args.tui_home, provider, args.timeout,
                                args.tui_python, args.tui_node)
            samples, frames, firsts, readies = [], [], [], []
            for i in range(args.tui_runs):
                r = run_tui_once(args.hermes, args.tui_home, provider, args.timeout,
                                 args.tui_python, args.tui_node)
                samples.append(round(r["first_token_ms"], 3))
                frames.append(round(r["startup_first_frame_ms"], 3))
                firsts.append(round(r["startup_first_output_ms"], 3))
                readies.append(round(r["startup_prompt_ready_ms"], 3))
                print(f"tui {i + 1}/{args.tui_runs}: token {r['first_token_ms']:.1f} ms, "
                      f"frame {r['startup_first_frame_ms']:.0f} ms, ready {r['startup_prompt_ready_ms']:.0f} ms",
                      file=sys.stderr)
            p50, p95 = pct(samples)
            ff50, ff95 = pct(frames)
            b50, b95 = pct(firsts)
            q50, q95 = pct(readies)
            tui = {
                "built": True,
                "surface": "ink",
                "command": "hermes --tui --yolo",
                "transport": "Node/Ink bundle spawns `python -m tui_gateway.entry`; newline-delimited JSON-RPC over stdio",
                "runtime_env": {
                    "HERMES_NODE": args.tui_node,
                    "HERMES_PYTHON": "$HERMES_VENV/bin/python",
                    "note": "The launcher's PM path tried to provision its own Node under HERMES_HOME and failed (libatomic.so.1 missing; sudo unavailable), so the system Node 22 from PATH is pinned explicitly. This is a setup limitation, not a TUI requirement.",
                },
                "first_token_p50_ms": p50,
                "first_token_p95_ms": p95,
                "first_frame_p50_ms": ff50,
                "first_frame_p95_ms": ff95,
                "first_token": {
                    "method": "T0 = fake provider writes the first streaming content frame carrying F3TOKEN; T1 = F3TOKEN visible in the TUI's PTY output",
                    "token": TOKEN,
                    "runs": args.tui_runs,
                    "samples_ms": samples,
                    "p50_ms": p50,
                    "p95_ms": p95,
                    "cold_start_sample_ms": round(cold["first_token_ms"], 3),
                    "note": "each sample starts a fresh Node app and Python gateway; only the OS caches are warm",
                },
                "first_frame": {
                    "method": "T0 = before pty fork/exec of `hermes --tui`; first_frame = the first painted chrome frame ('summoning hermes'); first_byte = first PTY byte (Ink's initial mouse-mode reset); prompt_ready = the intro/tool list ('Available Tools') that carries the composer",
                    "runs": args.tui_runs,
                    "samples_ms": frames,
                    "p50_ms": ff50,
                    "p95_ms": ff95,
                    "first_byte": {"samples_ms": firsts, "p50_ms": b50, "p95_ms": b95},
                    "prompt_ready": {"samples_ms": readies, "p50_ms": q50, "p95_ms": q95},
                    "cold": {
                        "first_byte_ms": round(cold["startup_first_output_ms"], 3),
                        "first_frame_ms": round(cold["startup_first_frame_ms"], 3),
                        "prompt_ready_ms": round(cold["startup_prompt_ready_ms"], 3),
                        "note": "fresh HERMES_HOME: first session and gateway; excluded from the warm samples",
                    },
                },
                "build": (
                    {"note": "skipped (--skip-tui-build)"}
                    if args.skip_tui_build
                    else tui_build_measure(args.src, args.tui_node, shutil.which("npm", path="/usr/local/bin:/usr/bin:/bin") or "npm",
                                           args.tui_build_runs, os.path.join(args.tui_home, "..", "f3-hermes-tui-build"))
                ),
                "difference_from_damnbot": "Hermes renders through Ink over a separate Python gateway process, so T0→T1 includes an extra IPC hop; Damn-Bot renders in-process.",
            }
        else:
            tui = None

        if args.reuse:
            buildtime = _reuse_buildtime(args.reuse)
        elif args.skip_build:
            buildtime = {"note": "skipped (--skip-build)"}
        else:
            buildtime = build_measure(args.src, args.python, args.build_runs,
                                      os.path.join(args.home, "..", "f3-hermes-build"))
    finally:
        provider.shutdown()
        wait_gateway_gone(args.home)
        wait_gateway_gone(args.tui_home)

    version = subprocess.run([args.hermes, "--version"], capture_output=True, text=True, timeout=60, env={
        "HOME": args.home, "HERMES_HOME": args.home, "TERM": "dumb", "NO_COLOR": "1",
        "PATH": "/usr/local/bin:/usr/bin:/bin",
    })
    version_text = (version.stdout + version.stderr).strip()

    dimensions = {
        "cli_repl": cli,
        "tui": tui,
        "buildtime": buildtime,
        "error_informatif": "supplementary: 8 common Hermes failure scenarios captured in results/hermes-errors-868b9f244f.json; not a substitute for human raters",
    }
    if cli is None:
        dimensions.pop("cli_repl")
    if tui is None:
        dimensions.pop("tui")

    art = {
        "meta": {
            "phase": "F3",
            "date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "hermes_commit": "868b9f244f",
            "hermes_version": version_text.splitlines()[0] if version_text else "unknown",
            "python": subprocess.run([args.python, "-VV"], capture_output=True, text=True).stdout.strip(),
            "python_install": "python-build-standalone cpython-3.14.10+20261026 x86_64-unknown-linux-gnu-install_only_stripped.tar.gz (sha256 4d3c16c59ed0802a3d1d34d8bce504b15ceeaff39d62dadecc6a9fa803412aed) extracted to $DAMNBOT_CACHE/py314",
            "hermes_install": "pip install -e . in $DAMNBOT_CACHE/hermes-venv (editable path; non-editable wheel/sdist refused by setup.py guard); 37 s with an empty pip cache",
            "node": subprocess.run([args.tui_node, "--version"], capture_output=True, text=True).stdout.strip(),
            "source": "$DAMNBOT_CACHE/f2-inputs/hermes-src (vendored, no .git; identity from the F0 baseline)",
            "cpu": cpu_model(),
            "kernel": f"{os.uname().sysname}/{os.uname().machine}",
            "loadavg_start": loadavg_start,
            "loadavg": loadavg(),
            "harness": "bench/f3/hermes_measure.py (PTY + timestamped loopback OpenAI SSE + turnkey/build timing)",
            "host_note": "workstation measurement, not the Q14 reference machine",
            "command": "bench/f3/hermes.sh",
        },
        "hermes": "measured",
        "hermes_detail": "classic prompt_toolkit REPL (`hermes chat --cli`) and the real Ink TUI (`hermes --tui`) under a PTY",
        "dimensions": dimensions,
    }
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(art, f, indent=1)
        f.write("\n")
    print(f"wrote {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
