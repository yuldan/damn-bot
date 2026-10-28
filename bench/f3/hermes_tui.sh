#!/usr/bin/env bash
# Build the Ink TUI bundle that `hermes --tui` serves (Hermes F3).
#
# The vendored checkout ships no node_modules and no hermes_cli/tui_dist, so the
# TUI must be compiled before it can be measured. This script stops at the
# first failure and prints the exact commands and wall-clock durations.
#
# Notes:
#   * `npm ci --workspace ui-tui` is deliberate. A root `npm ci` also installs
#     apps/desktop and tests-js, whose node-pty has no linux-x64 prebuild here
#     and would need g++ (absent); the TUI workspace does not use node-pty.
#   * Node 22 (v22.23.3) and npm 10.9.9 were used for the recorded numbers.
#   * Everything stays under ~/.cache/damnbot; nothing is committed.
#
# Usage:
#   bench/f3/hermes_tui.sh
#   HERMES_SRC=/path/to/hermes bench/f3/hermes_tui.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cache=${DAMNBOT_CACHE:-$HOME/.cache/damnbot}
src=${HERMES_SRC:-$cache/f2-inputs/hermes-src}
node=${HERMES_NODE:-/usr/bin/node}
npm=${HERMES_NPM:-npm}

for p in "$src/ui-tui/package.json" "$src/scripts/build/tui.mjs"; do
	[[ -e $p ]] || { echo "hermes_tui.sh: missing $p" >&2; exit 1; }
done
command -v "$node" >/dev/null || { echo "hermes_tui.sh: no node at $node" >&2; exit 1; }
command -v "$npm" >/dev/null || { echo "hermes_tui.sh: no npm on PATH" >&2; exit 1; }

export npm_config_cache=${npm_config_cache:-$cache/npm-cache}
export npm_config_audit=false
export npm_config_fund=false

ms() { echo $(( ($2 - $1) / 1000000 )); }

t0=$(date +%s%N)
(cd "$src" && nice -n 19 "$npm" ci --workspace ui-tui --no-audit --no-fund --loglevel=error)
t1=$(date +%s%N)
echo "install: npm ci --workspace ui-tui --no-audit --no-fund ($(ms "$t0" "$t1") ms)"

t0=$(date +%s%N)
(cd "$src/ui-tui" && nice -n 19 "$npm" run build)
t1=$(date +%s%N)
echo "bundle:  cd ui-tui && npm run build ($(ms "$t0" "$t1") ms)"

# Packaging (scripts/build/agent.py plant_surfaces) copies the product's dist
# and its package.json into hermes_cli/tui_dist; the launcher loads that path.
rm -rf "$src/hermes_cli/tui_dist"
cp -a "$src/ui-tui/dist" "$src/hermes_cli/tui_dist"
printf '{"type":"module"}\n' >"$src/hermes_cli/tui_dist/package.json"
echo "bundle:  $src/hermes_cli/tui_dist/entry.js"
"$node" --version
"$npm" --version
