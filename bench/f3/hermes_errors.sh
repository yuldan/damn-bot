#!/usr/bin/env bash
# Supplementary common-failure capture for the Hermes side of F3. This is not
# the 60-case Damn-Bot suite and it does not produce a score: it stores raw,
# path-sanitized output for human raters.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
cache=${DAMNBOT_CACHE:-$HOME/.cache/damnbot}
venv=${HERMES_VENV:-$cache/hermes-venv}
home=${HERMES_ERROR_HOME:-$cache/f3-hermes-errors}
out=${OUT:-"$root/bench/f3/results/hermes-errors-868b9f244f.json"}

[[ -x $venv/bin/hermes ]] || { echo "hermes_errors.sh: missing $venv/bin/hermes" >&2; exit 1; }

exec nice -n 19 "$venv/bin/python" "$root/bench/f3/hermes_errors.py" \
	--hermes "$venv/bin/hermes" \
	--home "$home" \
	--out "$out"
