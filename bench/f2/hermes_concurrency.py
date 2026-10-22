#!/usr/bin/env python3
"""Hermes side of the F2 "Konkurensi" dimension (ADR-065/ADR-083, SCORECARD §1.3).

Runs 4 processes x 50 turns on their own sessions plus 2 processes x 50 turns
contending on one shared session through Hermes' own SessionDB (hermes_state),
then checks the database integrity. No LLM call is made.

Usage:
  PYTHONPATH=<hermes-deps>:<hermes-checkout> \
    python3 hermes_concurrency.py --hermes-dir <checkout> --out <json>
"""
import argparse
import json
import multiprocessing as mp
import os
import sqlite3
import sys
import tempfile
from pathlib import Path

TURNS = 50
WORKERS = 4
SHARED_WORKERS = 2


def _worker(db: str, sid: str, shared: bool, turns: int) -> dict:
    from hermes_state import SessionDB  # imported inside the spawned process

    appends, busy, errors = 0, 0, []
    s = SessionDB(db_path=Path(db))
    s.ensure_session(sid, "cli")
    for i in range(turns):
        for role in ("user", "assistant"):
            try:
                s.append_message(sid, role, f"{'shared' if shared else 'own'} {i} {role}")
                appends += 1
            except Exception as exc:  # noqa: BLE001 - classify below
                text = str(exc).lower()
                if "database is locked" in text or "busy" in text:
                    busy += 1
                errors.append(f"{type(exc).__name__}: {str(exc)[:120]}")
    s.close()
    return {"shared": shared, "appends": appends, "busy": busy, "errors": errors[:5]}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--hermes-dir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--turns", type=int, default=TURNS)
    args = ap.parse_args()
    sys.path.insert(0, args.hermes_dir)

    db = Path(tempfile.mkdtemp(prefix="hermes-f2-")) / "state.db"
    ctx = mp.get_context("spawn")
    jobs = [(db, f"own-{i}", False, args.turns) for i in range(WORKERS)]
    jobs += [(db, "SHARED", True, args.turns) for _ in range(SHARED_WORKERS)]
    with ctx.Pool(len(jobs)) as pool:
        results = pool.starmap(_worker, jobs)

    own = sum(r["appends"] for r in results if not r["shared"])
    shared = sum(r["appends"] for r in results if r["shared"])
    busy = sum(r["busy"] for r in results)
    errors = [e for r in results for e in r["errors"]]
    con = sqlite3.connect(db)
    integrity = con.execute("PRAGMA integrity_check").fetchone()[0]
    journal = con.execute("PRAGMA journal_mode").fetchone()[0]
    shared_rows = con.execute(
        "SELECT count(*) FROM messages WHERE session_id = 'SHARED'"
    ).fetchone()[0]
    con.close()
    if shared_rows != shared:
        errors.append(f"shared rows {shared_rows} != reported {shared}")

    out = {
        "agent": "hermes",
        "workers": WORKERS,
        "shared_workers": SHARED_WORKERS,
        "turns": args.turns,
        "own_appends": own,
        "shared_appends": shared,
        "busy": busy,
        "errors": len(errors),
        "error_samples": errors[:5],
        "integrity": integrity,
        "sqlite_version": sqlite3.sqlite_version,
        "journal_mode": journal,
        "python": sys.version.split()[0],
    }
    Path(args.out).write_text(json.dumps(out) + "\n")
    print(json.dumps(out))
    return 0 if integrity == "ok" and not busy and not errors else 1


if __name__ == "__main__":
    raise SystemExit(main())
