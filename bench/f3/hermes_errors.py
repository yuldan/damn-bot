#!/usr/bin/env python3
"""Capture a small supplementary set of Hermes error surfaces for F3.

This does NOT try to mirror the 60-case Damn-Bot informative-error suite. It
runs eight common failure modes against the pinned Hermes checkout, stores the
raw (path-sanitized) stdout+stderr, and leaves ratification to human raters.

Run:
  bench/f3/hermes_errors.py --hermes ... --out bench/f3/results/hermes-errors-868b9f244f.json \
      --home ~/.cache/damnbot/f3-hermes-errors
"""

from __future__ import annotations

import argparse
import http.server
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import threading
import time


BASE_CONFIG = """\
model:
  provider: {provider}
  default: "{model}"
  base_url: "{base_url}"
  api_key: "{api_key}"
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


class Model404(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), Handler404)


class Handler404(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args) -> None:
        pass

    def do_GET(self) -> None:  # noqa: N802
        self.send_response(404)
        self.send_header("content-length", "0")
        self.send_header("connection", "close")
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802
        n = int(self.headers.get("content-length", "0") or 0)
        self.rfile.read(n)
        body = json.dumps(
            {
                "error": {
                    "message": "The model 'definitely-not-a-model' does not exist",
                    "type": "invalid_request_error",
                    "code": "model_not_found",
                }
            }
        ).encode()
        self.send_response(404)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.send_header("connection", "close")
        self.end_headers()
        self.wfile.write(body)


def clean_env(home: str) -> dict:
    env = {}
    for k, v in os.environ.items():
        u = k.upper()
        if any(s in u for s in ("API_KEY", "TOKEN", "SECRET")) or u.startswith(
            ("OPENAI_", "OPENROUTER_", "ANTHROPIC_", "HERMES_")
        ):
            continue
        if u in {"HOME", "PYTHONPATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"}:
            continue
        env[k] = v
    env.update(
        {
            "HOME": home,
            "HERMES_HOME": home,
            "XDG_CONFIG_HOME": os.path.join(home, ".config"),
            "XDG_CACHE_HOME": os.path.join(home, ".cache"),
            "XDG_DATA_HOME": os.path.join(home, ".local", "share"),
            "XDG_STATE_HOME": os.path.join(home, ".local", "state"),
            "TMPDIR": os.path.join(home, "tmp"),
            "TERM": "dumb",
            "NO_COLOR": "1",
            "LANG": "C.UTF-8",
            "PYTHONDONTWRITEBYTECODE": "1",
            "PYTHONUNBUFFERED": "1",
            "PATH": "/usr/local/bin:/usr/bin:/bin",
        }
    )
    for d in ("XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "TMPDIR"):
        os.makedirs(env[d], exist_ok=True)
    return env


def kill_home(home: str) -> None:
    want = ("HERMES_HOME=" + home).encode()
    for name in os.listdir("/proc"):
        if not name.isdigit():
            continue
        try:
            env = open(f"/proc/{name}/environ", "rb").read().split(b"\0")
        except OSError:
            continue
        if want in env:
            try:
                os.kill(int(name), signal.SIGKILL)
            except OSError:
                pass


def sanitize(text: str, home: str) -> str:
    text = text.replace(home, "$HERMES_HOME")
    text = re.sub(r"/(?:home|Users)/[^/\s\"']+", "$HOME", text)
    text = re.sub(r"\b(session_id|Session):\s*[0-9a-f-]{8,}", r"\1: <redacted>", text)
    return text


CASES = [
    {
        "id": "config-missing",
        "group": "config",
        "what": "no config.yaml in a fresh HERMES_HOME",
        "config": None,
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "config-malformed",
        "group": "config",
        "what": "config.yaml is not valid YAML",
        "config": "model: [unclosed\n",
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "credentials-missing",
        "group": "credentials",
        "what": "openrouter selected with no API key anywhere",
        "config": BASE_CONFIG.format(
            provider="openrouter", model="openrouter/anthropic/claude-sonnet-4.5", base_url="", api_key=""
        ),
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "provider-unknown",
        "group": "model",
        "what": "--provider names a provider that does not exist",
        "config": BASE_CONFIG.format(
            provider="custom", model="gpt-fake", base_url="http://127.0.0.1:9/v1", api_key="f3-bench"
        ),
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--provider", "not-a-provider", "--max-turns", "1"],
    },
    {
        "id": "model-unknown",
        "group": "model",
        "what": "custom provider answers 404 model_not_found",
        "config": BASE_CONFIG.format(
            provider="custom", model="definitely-not-a-model", base_url="http://127.0.0.1:9/v1", api_key="f3-bench"
        ),
        "fake_404": True,
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "net-refused",
        "group": "network",
        "what": "custom provider points at a closed loopback port",
        "config": BASE_CONFIG.format(
            provider="custom", model="gpt-fake", base_url="http://127.0.0.1:9/v1", api_key="f3-bench"
        ),
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "net-dns",
        "group": "network",
        "what": "custom provider host does not resolve",
        "config": BASE_CONFIG.format(
            provider="custom", model="gpt-fake", base_url="http://f3-no-such-host.invalid/v1", api_key="f3-bench"
        ),
        "args": ["chat", "-q", "hello", "-Q", "--ignore-rules", "--max-turns", "1"],
    },
    {
        "id": "cli-unknown-flag",
        "group": "cli",
        "what": "chat subcommand rejects an unknown flag",
        "config": None,
        "args": ["chat", "--definitely-not-a-hermes-flag"],
    },
]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--hermes", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--home", required=True)
    ap.add_argument("--timeout", type=float, default=150.0)
    args = ap.parse_args()

    root = os.path.abspath(args.home)
    shutil.rmtree(root, ignore_errors=True)
    os.makedirs(root)
    results = []
    for case in CASES:
        home = os.path.join(root, case["id"])
        os.makedirs(home)
        env = clean_env(home)
        if case["config"] is not None:
            path = os.path.join(home, "config.yaml")
            with open(path, "w", encoding="utf-8") as f:
                f.write(case["config"])
            os.chmod(path, case.get("chmod", 0o600))
        server = None
        if case.get("fake_404"):
            server = Model404()
            threading.Thread(target=server.serve_forever, daemon=True).start()
            env["F3_BASE_URL_OVERRIDE"] = f"http://127.0.0.1:{server.server_address[1]}/v1"
            cfg_path = os.path.join(home, "config.yaml")
            with open(cfg_path, encoding="utf-8") as f:
                cfg_text = f.read()
            cfg_text = cfg_text.replace("http://127.0.0.1:9/v1", env["F3_BASE_URL_OVERRIDE"])
            with open(cfg_path, "w", encoding="utf-8") as f:
                f.write(cfg_text)
            os.chmod(cfg_path, 0o600)
        t0 = time.perf_counter()
        timed_out = False
        try:
            proc = subprocess.run(
                [args.hermes, *case["args"]],
                cwd=home,
                env=env,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                timeout=args.timeout,
            )
            rc = proc.returncode
            raw = proc.stdout
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            rc = -1
            raw = exc.stdout or b""
        elapsed = time.perf_counter() - t0
        if server is not None:
            server.shutdown()
        kill_home(home)
        text = sanitize(raw.decode("utf-8", "replace"), home)
        logs = ""
        if not text.strip() or timed_out:
            for name in ("errors.log", "gateway.log"):
                p = os.path.join(home, "logs", name)
                if os.path.exists(p):
                    with open(p, encoding="utf-8", errors="replace") as f:
                        logs += f"\n--- {name} (tail) ---\n" + f.read()[-4000:]
            logs = sanitize(logs, home)
        results.append(
            {
                "id": case["id"],
                "group": case["group"],
                "what": case["what"],
                "argv": ["hermes", *case["args"]],
                "exit_code": rc,
                "timed_out": timed_out,
                "duration_s": round(elapsed, 3),
                "output": text,
                "gateway_log_tail": logs,
            }
        )
        print(f"{case['id']}: rc={rc} timed_out={timed_out} {elapsed:.1f}s", file=sys.stderr)

    art = {
        "meta": {
            "phase": "F3",
            "date": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "hermes_commit": "868b9f244f",
            "note": "supplementary common-failure capture; NOT the 60-case Damn-Bot suite and NOT a ratification",
            "sanitized": "HERMES_HOME/HOME/source-prefix replaced with placeholders; session ids redacted; no credentials used",
            "timeouts": "timed_out=true means no user-facing error before the deadline; gateway_log_tail is the raw evidence",
        },
        "cases": results,
        "ratification": "pending human raters; these outputs are evidence, not a score",
    }
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(art, f, indent=1)
        f.write("\n")
    print(f"wrote {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
