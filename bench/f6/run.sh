#!/usr/bin/env bash
# F6 baseline harness (ADR-083, ADR-309): "Voice end-to-end" and "Image
# preflight" for Hermes and Damn-Bot, with no LLM call.
#
#   HERMES_DIR=/opt/hermes-agent HERMES_PYTHON=~/.cache/damnbot/hermes-venv/bin/python \
#     bench/f6/run.sh
#
# Optional: RUNS=5 MACHINE="Q14" OUT=bench/speed/results/... bench/f6/run.sh
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
bench="$root/bench"
HERMES_DIR=${HERMES_DIR:-/opt/hermes-agent}
HERMES_PYTHON=${HERMES_PYTHON:-python3}
RUNS=${RUNS:-5}
MACHINE=${MACHINE:-workstation (not Q14)}
OUT=${OUT:-$bench/speed/results/hermes-868b9f244f-F6.json}
tmp=$(mktemp -d /tmp/damnbot-f6.XXXXXX)
trap 'rm -rf "$tmp"' EXIT

cd "$root"
go test -tags goolm -run '^$' -bench '^BenchmarkImagePreflightStages$' -benchtime="${RUNS}x" -count=1 \
	./internal/media/preflight >"$tmp/image.bench" 2>&1
go test -tags goolm -run '^TestVoicePipelineLatency$' -count="${RUNS}" -v \
	./internal/voice >"$tmp/voice.log" 2>&1

if [[ -d "$HERMES_DIR" ]]; then
	PYTHONPATH="$HERMES_DIR${PYTHONPATH:+:$PYTHONPATH}" "$HERMES_PYTHON" \
		"$bench/f6/hermes_image.py" --hermes-dir "$HERMES_DIR" --out "$tmp/hermes-image.json" \
		--runs "$RUNS" --machine "$MACHINE" >/dev/null 2>&1 || true
	PYTHONPATH="$HERMES_DIR${PYTHONPATH:+:$PYTHONPATH}" "$HERMES_PYTHON" \
		"$bench/f6/hermes_voice.py" --hermes-dir "$HERMES_DIR" --out "$tmp/hermes-voice.json" \
		--runs "$RUNS" --machine "$MACHINE" >/dev/null 2>&1 || true
fi

hermes_commit=$(git -c safe.directory="$HERMES_DIR" -C "$HERMES_DIR" rev-parse HEAD 2>/dev/null || true)
[[ -n "$hermes_commit" ]] || hermes_commit=868b9f244f5aa3ad238e494048046fe4f29c28ba
damnbot_commit=$(git -C "$root" rev-parse --short HEAD)
damnbot_dirty=$(git -C "$root" status --porcelain | wc -l | tr -d ' ')
cpu=$(sed -n 's/^Model name:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -1)
if [[ -z "$cpu" ]] && command -v lscpu >/dev/null 2>&1; then
	cpu=$(lscpu | sed -n 's/^Model name:[[:space:]]*//p' | head -1)
fi
kernel=$(uname -sr)

cd "$bench"
go run ./f6/cmd/f6measure \
	-damnbot-image "$tmp/image.bench" \
	-damnbot-voice "$tmp/voice.log" \
	-hermes-image "$tmp/hermes-image.json" \
	-hermes-voice "$tmp/hermes-voice.json" \
	-out "$OUT" -hermes-commit "$hermes_commit" \
	-machine "$MACHINE" -cpu "$cpu" -kernel "$kernel" \
	-damnbot-commit "$damnbot_commit" -damnbot-dirty "$damnbot_dirty"
