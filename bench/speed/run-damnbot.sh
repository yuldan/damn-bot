#!/usr/bin/env bash
# P28 Damn-Bot speed artefact: build the binary, run the real probes, merge
# them with FEATURE-PARITY. Never copies a hermes number.
#
#   bench/speed/run-damnbot.sh [out-file]
#
# Default out: bench/speed/results/damnbot-<short-sha>.json
set -euo pipefail
unset GH_TOKEN

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
SHA=$(git -C "$ROOT" rev-parse --short=10 HEAD)
OUT=${1:-"$ROOT/bench/speed/results/damnbot-$SHA.json"}
TMP=$(mktemp -d /tmp/damnbot-speed-XXXXXX)
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/probes"
echo "==> build damnbot ($SHA)"
(cd "$ROOT" && go build -tags goolm -o "$TMP/damnbot" ./cmd/damnbot)

echo "==> probe e2e (93 rows)"
(cd "$ROOT" && \
  DAMNBOT_SPEED_BIN="$TMP/damnbot" DAMNBOT_SPEED_OUT="$TMP/probes" \
  go test -count=1 -timeout 30m -run '^TestSpeedP20Probe$' ./internal/e2e)

echo "==> probe tui (3 rows)"
(cd "$ROOT" && \
  DAMNBOT_SPEED_OUT="$TMP/probes" \
  go test -count=1 -timeout 5m -run '^TestSpeedP20TUIProbe$' ./internal/tui)

echo "==> merge artefact"
(cd "$ROOT/bench" && \
  go run ./speed/cmd/damnbot-speed -probes "$TMP/probes" -commit "$SHA" -out "$OUT")

echo "wrote $OUT"
