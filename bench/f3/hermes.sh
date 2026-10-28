#!/usr/bin/env bash
# Hermes side of the F3 baseline (ADR-083): first token in the interactive
# surface, startup, and install time. Reproducible on a workstation with the
# pinned source checkout and the private Python 3.14 prefix.
#
# Everything is isolated under ~/.cache/damnbot. The first-token runs need no
# network: they use a loopback OpenAI-compatible fake provider. The clean
# install samples do use PyPI (that is the thing being measured).
#
# Usage:
#   bench/f3/hermes.sh
#   RUNS=20 BUILD_RUNS=3 OUT=bench/f3/results/hermes-868b9f244f-F3.json bench/f3/hermes.sh
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
cache=${DAMNBOT_CACHE:-$HOME/.cache/damnbot}
hermes_src=${HERMES_SRC:-$cache/f2-inputs/hermes-src}
python=${HERMES_PYTHON:-$cache/py314/python/bin/python3.14}
venv=${HERMES_VENV:-$cache/hermes-venv}
home=${HERMES_BENCH_HOME:-$cache/f3-hermes-home}
out=${OUT:-"$root/bench/f3/results/hermes-868b9f244f-F3.json"}

for p in "$hermes_src/pyproject.toml" "$python" "$venv/bin/python" "$venv/bin/hermes"; do
	[[ -e $p ]] || { echo "hermes.sh: missing $p (see README §Hermes side)" >&2; exit 1; }
done

args=(
	--hermes "$venv/bin/hermes"
	--python "$python"
	--src "$hermes_src"
	--home "$home"
	--out "$out"
	--surface "${SURFACE:-both}"
	--runs "${RUNS:-20}"
	--tui-runs "${TUI_RUNS:-${RUNS:-20}}"
	--build-runs "${BUILD_RUNS:-3}"
	--tui-build-runs "${TUI_BUILD_RUNS:-3}"
)
[[ -n ${REUSE:-} ]] && args+=(--reuse "$REUSE")
[[ -n ${SKIP_BUILD:-} ]] && args+=(--skip-build)
[[ -n ${SKIP_TUI_BUILD:-} ]] && args+=(--skip-tui-build)

exec nice -n 19 "$venv/bin/python" "$root/bench/f3/hermes_measure.py" "${args[@]}"
