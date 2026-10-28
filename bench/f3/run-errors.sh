#!/usr/bin/env bash
# Build the real damnbot binary and run the F3 informative-error suite.
# Usage: bench/f3/run-errors.sh [--timeout 120s] [--bin PATH]
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
go_bin=${GO:-go}
own_bin=0
if [[ -z ${BIN:-} ]]; then
  BIN=$(mktemp "${TMPDIR:-/tmp}/damnbot-f3-errors-bin.XXXXXX")
  own_bin=1
fi
cleanup() { ((own_bin)) && rm -f "$BIN"; }
trap cleanup EXIT

(cd "$root" && CGO_ENABLED=0 "$go_bin" build -tags goolm -o "$BIN" ./cmd/damnbot)
cd "$root/bench"
"$go_bin" run ./f3/cmd/f3errors \
  --bin "$BIN" \
  --cases f3/errors/cases.json \
  --out f3/errors \
  "$@"
