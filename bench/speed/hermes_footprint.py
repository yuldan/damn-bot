"""Footprint install hermes TANPA instalasi ulang (SCORECARD §1.4 "Ukuran install", ADR-032).

Mengukur dari lingkungan yang sudah ada (read-only):
  - checkout: Σ ukuran file ter-track git (`git ls-files -z`, tanpa .git) + node_modules bila ada;
  - dependensi Python: closure `dependencies` pyproject.toml (marker dievaluasi untuk interpreter ini)
    lewat importlib.metadata, Σ ukuran file setiap distribusi terpasang (dist.files).
Extras/optional tidak dihitung (instalasi default). Output JSON ke stdout.

Pemakaian (dari root hermes-agent): python3 <repo>/bench/speed/hermes_footprint.py
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import tomllib
from importlib import metadata

from packaging.requirements import Requirement


def _norm(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def _closure(roots: list[Requirement]) -> tuple[dict[str, metadata.Distribution], list[str]]:
    seen: dict[str, metadata.Distribution] = {}
    missing: list[str] = []
    stack = [r for r in roots if r.marker is None or r.marker.evaluate()]
    while stack:
        req = stack.pop()
        key = _norm(req.name)
        if key in seen or key in missing:
            continue
        try:
            dist = metadata.distribution(req.name)
        except metadata.PackageNotFoundError:
            missing.append(key)
            continue
        seen[key] = dist
        for raw in dist.requires or []:
            sub = Requirement(raw)
            # Hanya dependensi wajib: marker tanpa `extra` yang lolos evaluasi.
            if sub.marker is None or sub.marker.evaluate({"extra": ""}):
                stack.append(sub)
    return seen, missing


def _dist_bytes(dist: metadata.Distribution) -> int:
    total = 0
    for f in dist.files or []:
        try:
            total += os.path.getsize(dist.locate_file(f))
        except OSError:
            pass
    return total


def main() -> int:
    with open("pyproject.toml", "rb") as f:
        deps = [Requirement(d) for d in tomllib.load(f)["project"]["dependencies"]]
    dists, missing = _closure(deps)
    per = {k: _dist_bytes(d) for k, d in sorted(dists.items())}
    files = subprocess.run(["git", "ls-files", "-z"], capture_output=True, check=True).stdout.split(b"\0")
    tracked = sum(os.path.getsize(p) for p in files if p and os.path.isfile(p))
    node = 0
    for root, _, names in os.walk("node_modules"):
        node += sum(os.path.getsize(os.path.join(root, n)) for n in names if os.path.isfile(os.path.join(root, n)))
    out = {
        "python": sys.version.split()[0],
        "checkout_tracked_bytes": tracked,
        "checkout_tracked_files": sum(1 for p in files if p),
        "node_modules_bytes": node,
        "python_deps_count": len(per),
        "python_deps_bytes": sum(per.values()),
        "python_deps_missing": missing,
        "python_deps_top10": sorted(per.items(), key=lambda kv: -kv[1])[:10],
        "total_bytes": tracked + node + sum(per.values()),
    }
    json.dump(out, sys.stdout, indent=1)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
