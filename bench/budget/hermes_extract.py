"""Ekstrak system prompt (3 tier) + tool schema hermes TANPA panggilan LLM/jaringan.

Meniru `hermes prompt-size` (hermes_cli/prompt_size.py:_build_inspection_agent): kredensial dummy
memaksa jalur konstruksi langsung AIAgent, lalu `build_system_prompt_parts` / `agent.tools` dibaca offline.

Pemakaian (dari root hermes-agent, read-only; HERMES_HOME wajib di /tmp):
    HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1 \
        python3 <repo>/bench/budget/hermes_extract.py <outdir> [--model gpt-4o] [--platform cli]

Output <outdir>: stable.txt, context.txt, volatile.txt, system_prompt.txt (gabungan dikirim),
tools_agent.json (agent.tools, `json.dumps` default = payload API), tools_defs.json
(`model_tools.get_tool_definitions(quiet_mode=True)`, metode BASELINE-hermes.md §7), meta.json.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("outdir")
    ap.add_argument("--model", default="gpt-4o")
    ap.add_argument("--platform", default="cli")
    ap.add_argument("--skip-context-files", action="store_true",
                    help="tanpa AGENTS.md/file konteks (baris 'Floor tanpa AGENTS.md' BASELINE §2)")
    args = ap.parse_args()

    home = os.environ.get("HERMES_HOME", "")
    if not home.startswith("/tmp/"):
        print("HERMES_HOME harus di /tmp (repo hermes read-only)", file=sys.stderr)
        return 2
    sys.path.insert(0, os.getcwd())

    from run_agent import AIAgent  # noqa: E402  (butuh sys.path = root hermes)
    from hermes_cli.config import load_config
    from hermes_cli.tools_config import _get_platform_tools
    from agent.skill_utils import parse_config_string_list
    from agent.system_prompt import build_system_prompt, build_system_prompt_parts
    from tools.skills_sync import sync_skills
    import model_tools

    # Sesi CLI default: `hermes` menyinkronkan skill bawaan ke HERMES_HOME saat start
    # (hermes_cli/main.py:_sync_bundled_skills_for_startup); tanpa ini indeks skill tier volatile kosong.
    sync_skills(quiet=True)

    cfg = load_config()
    agent_cfg = cfg.get("agent") or {}
    agent = AIAgent(
        model=args.model,
        api_key="inspect-only", base_url="https://openrouter.ai/api/v1", quiet_mode=True,
        save_trajectories=False, platform=args.platform, skip_context_files=args.skip_context_files,
        enabled_toolsets=sorted(_get_platform_tools(cfg, args.platform)),
        disabled_toolsets=parse_config_string_list(agent_cfg.get("disabled_toolsets")) or None,
    )
    parts = build_system_prompt_parts(agent)
    full = build_system_prompt(agent)
    tools = getattr(agent, "tools", None) or []
    defs = model_tools.get_tool_definitions(quiet_mode=True)

    out = pathlib.Path(args.outdir)
    out.mkdir(parents=True, exist_ok=True)
    for name in ("stable", "context", "volatile"):
        (out / f"{name}.txt").write_text(parts.get(name, ""), encoding="utf-8")
    (out / "system_prompt.txt").write_text(full, encoding="utf-8")
    (out / "tools_agent.json").write_text(json.dumps(tools), encoding="utf-8")
    (out / "tools_defs.json").write_text(json.dumps(defs), encoding="utf-8")
    commit = subprocess.run(["git", "rev-parse", "--short=10", "HEAD"], capture_output=True, text=True).stdout.strip()
    meta = {
        "hermes_commit": commit,
        "model": args.model,
        "platform": args.platform,
        "skip_context_files": args.skip_context_files,
        "python": sys.version.split()[0],
        "tools_agent_count": len(tools),
        "tools_defs_count": len(defs),
        "tool_names_agent": [t["function"]["name"] for t in tools],
    }
    (out / "meta.json").write_text(json.dumps(meta, indent=2), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
