#!/usr/bin/env bash
# F3 baseline harness (ADR-083): Damn-Bot TUI first token, build time, and
# honestly blocked/unavailable dimensions. Run from anywhere.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
sha=$(git rev-parse --short=10 HEAD)
dirty=$(git status --porcelain | wc -l | tr -d ' ')
out=${OUT:-"$root/bench/f3/results/damnbot-$sha-F3.json"}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-f3.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
go build -tags 'goolm,bench' -o "$tmp/damnbot" ./cmd/damnbot
cd "$root/bench"
go run ./f3/cmd/f3measure \
	-bin "$tmp/damnbot" \
	-out "$out" \
	-repo "$root" \
	-commit "$sha" \
	-dirty "$dirty" \
	-runs "${RUNS:-20}" \
	-build-runs "${BUILD_RUNS:-3}"
