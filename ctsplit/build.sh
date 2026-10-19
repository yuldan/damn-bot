#!/usr/bin/env bash
# build.sh FROM TO: rebuild stages FROM..TO (FROM-1 branch must exist, or FROM=1).
set -euo pipefail
cd /tmp/ct-split2
F=$1 T=$2
if ((F == 1)); then git checkout -q --detach 339d92a; else git checkout -q --detach "f1-ct-$(printf %02d $((F - 1)))"; fi
for ((k = F; k <= T; k++)); do /tmp/ctsplit/mk.sh "$k"; done
