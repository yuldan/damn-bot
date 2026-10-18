"""Driver baseline speed hermes TANPA LLM (bench/speed, SCORECARD §3 tipe (a)).

Dipanggil oleh `bench/speed/cmd/speed` (Go) dari root hermes-agent, di dalam `unshare -rn`
(tanpa jaringan: tidak mungkin ada panggilan LLM/HTTP), dengan HOME/HERMES_HOME di /tmp.

Mode:
  prepare <workspace>                 sinkron skill bawaan, jawab consent telemetri "No thanks",
                                      buat workspace fixture (salinan agent/*.py hermes) untuk tool file.
  inproc <jobs.json> <out.json>       jalankan op `tool` (model_tools.handle_function_call) dan
                                      `slash` (HermesCLI.process_command) in-process, warm, N run.
  pty <out.json> <runs> -- argv...    startup interaktif di PTY: waktu output pertama, waktu settle
                                      (output terakhir sebelum 3 s hening), RSS pohon proses setelah 5 s idle.
Sampel dalam milidetik (time.perf_counter_ns).
"""

from __future__ import annotations

import contextlib
import json
import os
import pty
import select
import shutil
import signal
import sys
import time

OP_TIMEOUT_S = 30


def _prepare(workspace: str) -> None:
    sys.path.insert(0, os.getcwd())
    from tools.skills_sync import sync_skills
    from hermes_cli.observability.shared_metrics_consent import save_consent

    sync_skills(quiet=True)
    save_consent(False, False)  # = user memilih "No thanks" sekali (bagian setup, bukan op yang diukur)
    shutil.rmtree(workspace, ignore_errors=True)
    shutil.copytree(os.path.join(os.getcwd(), "agent"), os.path.join(workspace, "agent"),
                    ignore=shutil.ignore_patterns("__pycache__"))


@contextlib.contextmanager
def _quiet():
    """Alihkan fd 1/2 ke /dev/null selama op (output UI hermes bukan bagian hasil)."""
    sys.stdout.flush()
    sys.stderr.flush()
    saved = os.dup(1), os.dup(2)
    devnull = os.open(os.devnull, os.O_WRONLY)
    os.dup2(devnull, 1)
    os.dup2(devnull, 2)
    try:
        yield
    finally:
        sys.stdout.flush()
        sys.stderr.flush()
        os.dup2(saved[0], 1)
        os.dup2(saved[1], 2)
        for fd in (*saved, devnull):
            os.close(fd)


class _Timeout(Exception):
    pass


def _alarm(_signum, _frame):
    raise _Timeout(f"op > {OP_TIMEOUT_S} s")


def _tool_error(raw: str) -> str | None:
    """Hasil tool hermes = string JSON; key `error` = gagal."""
    try:
        data = json.loads(raw)
    except (TypeError, ValueError):
        return None
    if isinstance(data, dict) and data.get("error"):
        return str(data["error"])[:300]
    return None


def _run_op(op: dict, call) -> dict:
    res = {"id": op["id"], "samples_ms": [], "error": None}
    signal.signal(signal.SIGALRM, _alarm)
    for i in range(op.get("warmup", 1) + op.get("runs", 20)):
        reset = op.get("reset")
        if reset:
            with open(reset["path"], "w", encoding="utf-8") as f:
                f.write(reset["content"])
        signal.alarm(OP_TIMEOUT_S)
        try:
            with _quiet():
                t0 = time.perf_counter_ns()
                err = call(i)
                dt = time.perf_counter_ns() - t0
        except BaseException as exc:  # op hermes boleh melempar apa saja; dicatat, bukan ditelan
            res["error"] = f"{type(exc).__name__}: {exc}"[:300]
            return res
        finally:
            signal.alarm(0)
        if err:
            res["error"] = err
            return res
        if i >= op.get("warmup", 1):
            res["samples_ms"].append(dt / 1e6)
    return res


def _inproc(jobs_path: str, out_path: str) -> None:
    sys.path.insert(0, os.getcwd())
    with open(jobs_path, encoding="utf-8") as f:
        ops = json.load(f)["ops"]
    results = []
    with _quiet():
        import model_tools
    cli = None
    for op in ops:
        holder: dict = {}
        if op["kind"] == "tool":
            def call(i, op=op, holder=holder):
                # fresh_task: task_id baru per run — guard pengulangan hermes ("BLOCKED: … 3 times")
                # memblokir panggilan identik dalam satu task, jadi run berulang butuh task terpisah.
                task = f"f0-speed-{i}" if op.get("fresh_task") else "f0-speed"
                raw = model_tools.handle_function_call(op["tool"], op.get("args", {}), task_id=task)
                holder["raw"] = raw
                return _tool_error(raw)
        elif op["kind"] == "slash":
            if cli is None:
                t0 = time.perf_counter_ns()
                with _quiet():
                    from cli import HermesCLI
                    cli = HermesCLI(model="gpt-4o", api_key="inspect-only", base_url="https://openrouter.ai/api/v1")
                results.append({"id": "_cli_init", "samples_ms": [(time.perf_counter_ns() - t0) / 1e6], "error": None})

            def call(_i, op=op):
                cli.process_command(op["command"])
                return None
        elif op["kind"] == "py":
            # Op Python bebas: `setup` dieksekusi sekali (di luar waktu), `code` diukur per run.
            ns: dict = {}
            try:
                with _quiet():
                    exec(op.get("setup", ""), ns)  # noqa: S102 — kode berasal dari katalog repo, bukan target
            except BaseException as exc:
                results.append({"id": op["id"], "samples_ms": [], "error": f"setup {type(exc).__name__}: {exc}"[:300]})
                continue
            code = compile(op["code"], op["id"], "exec")

            def call(_i, code=code, ns=ns):
                exec(code, ns)  # noqa: S102
                return None
        else:
            results.append({"id": op["id"], "samples_ms": [], "error": f"kind tidak dikenal: {op['kind']}"})
            continue
        res = _run_op(op, call)
        if op.get("capture") and "raw" in holder:
            res["output"] = holder["raw"]  # output tool apa adanya (untuk token CT-11)
        results.append(res)
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"results": results}, f)


def _tree_rss_kb(pid: int) -> int:
    """Σ VmRSS proses pid dan semua turunannya (/proc/*/stat ppid)."""
    children: dict[int, list[int]] = {}
    for name in os.listdir("/proc"):
        if name.isdigit():
            try:
                with open(f"/proc/{name}/stat", encoding="utf-8") as f:
                    ppid = int(f.read().rsplit(")", 1)[1].split()[1])
            except (OSError, ValueError, IndexError):
                continue
            children.setdefault(ppid, []).append(int(name))
    total, stack = 0, [pid]
    while stack:
        p = stack.pop()
        stack.extend(children.get(p, []))
        try:
            with open(f"/proc/{p}/status", encoding="utf-8") as f:
                for line in f:
                    if line.startswith("VmRSS:"):
                        total += int(line.split()[1])
        except OSError:
            pass
    return total


def _pty_once(argv: list[str]) -> dict:
    t0 = time.monotonic()
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(argv[0], argv)
    first = last = None
    deadline = t0 + 60
    while time.monotonic() < deadline:
        ready, _, _ = select.select([fd], [], [], 0.1)
        if ready:
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            if not data:
                break
            now = time.monotonic()
            first = first or now
            last = now
        elif last is not None and time.monotonic() - last >= 3.0:
            break
    # Idle 5 s setelah settle (SCORECARD "Idle RSS"); output tetap dikuras.
    idle_end = time.monotonic() + 5.0
    while time.monotonic() < idle_end:
        ready, _, _ = select.select([fd], [], [], 0.2)
        if ready:
            try:
                os.read(fd, 65536)
            except OSError:
                break
    rss = _tree_rss_kb(pid)
    with contextlib.suppress(ProcessLookupError):
        os.killpg(os.getpgid(pid), signal.SIGKILL)
    with contextlib.suppress(ChildProcessError):
        os.waitpid(pid, 0)
    os.close(fd)
    ms = lambda t: None if t is None else (t - t0) * 1000  # noqa: E731
    return {"first_output_ms": ms(first), "settle_ms": ms(last), "idle_rss_kb": rss}


def _pty(out_path: str, runs: int, argv: list[str]) -> None:
    samples = [_pty_once(argv) for _ in range(runs + 1)][1:]  # run pertama = warm-up page cache
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"argv": argv, "runs": samples}, f)


def main() -> int:
    mode = sys.argv[1]
    if not os.environ.get("HERMES_HOME", "").startswith("/tmp/"):
        print("HERMES_HOME harus di /tmp", file=sys.stderr)
        return 2
    if mode == "prepare":
        _prepare(sys.argv[2])
    elif mode == "inproc":
        _inproc(sys.argv[2], sys.argv[3])
    elif mode == "pty":
        sep = sys.argv.index("--")
        _pty(sys.argv[2], int(sys.argv[3]), sys.argv[sep + 1:])
    else:
        print(f"mode tidak dikenal: {mode}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
