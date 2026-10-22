#!/usr/bin/env bash
# F2 baseline harness (ADR-083, ADR-163): the "Konkurensi" and "Token skill
# per turn" dimensions measured with one harness for both agents, without an
# LLM call.
#
# Requires a hermes 868b9f244f checkout and its import deps:
#   HERMES_DIR=... HERMES_PYTHONPATH=... bench/f2/run.sh
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
bench="$root/bench"
HERMES_DIR=${HERMES_DIR:?set HERMES_DIR to the hermes 868b9f244f checkout}
HERMES_PYTHONPATH=${HERMES_PYTHONPATH:-}
tmp=$(mktemp -d /tmp/damnbot-f2.XXXXXX)
trap 'rm -rf "$tmp"' EXIT

cd "$root"
go test -tags goolm -count=1 -run '^TestConcurrencyF2$' -v ./internal/ctxdb >"$tmp/ctxdb.log" 2>&1
grep -o 'F2-CONCURRENCY: .*' "$tmp/ctxdb.log" | sed 's/^F2-CONCURRENCY: //' >"$tmp/damnbot-concurrency.json"
test -s "$tmp/damnbot-concurrency.json" || { cat "$tmp/ctxdb.log"; exit 1; }

PYTHONPATH="$HERMES_PYTHONPATH${HERMES_PYTHONPATH:+:}$HERMES_DIR" PYTHONDONTWRITEBYTECODE=1 \
	python3 "$bench/f2/hermes_concurrency.py" --hermes-dir "$HERMES_DIR" --out "$tmp/hermes-concurrency.json" \
	>"$tmp/hermes.log" 2>&1
test -s "$tmp/hermes-concurrency.json" || { cat "$tmp/hermes.log"; exit 1; }

cd "$bench"
cpu=$(sed -n 's/^Model name:[[:space:]]*//p' /proc/cpuinfo 2>/dev/null | head -1)
if [ -z "$cpu" ] && command -v lscpu >/dev/null 2>&1; then
	cpu=$(lscpu | sed -n 's/^Model name:[[:space:]]*//p' | head -1)
fi
kernel=$(uname -sr)
go run ./f2/cmd/f2measure \
	-damnbot-concurrency "$tmp/damnbot-concurrency.json" \
	-hermes-concurrency "$tmp/hermes-concurrency.json" \
	-hermes-recording h2h/results/2026-10-18/E1-117.hermes.proxy.jsonl.gz \
	-damnbot-tiers ../internal/assemble/testdata/p1/all-modules.tiers.json \
	-out f2/results/hermes-868b9f244f-F2.json \
	-date "$(date -u +%F)" -cpu "$cpu" -kernel "$kernel"
