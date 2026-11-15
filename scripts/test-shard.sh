#!/usr/bin/env bash
# Deterministic test sharding for the CI `test` matrix and the nightly flake job
# (ADR-324 amendment 2026-11-15).
#
# Tests are selected by name, not by package: the heavy packages
# (`internal/e2e`, `cmd/damnbot`) are exactly the ones whose wall clock cannot
# be reduced by package-level parallelism, because Go runs the tests of one
# package sequentially. The script enumerates every top-level `Test*` from the
# tree, gives it the measured package duration divided by its test count (with
# explicit weights for the known long tests), and assigns the list to buckets
# with an LPT (longest processing time) pass. The assignment depends only on
# the tree and the checked-in weights, so every shard sees the same split.
#
# `<i>/<n>` selects one bucket. Each bucket runs one `go test` invocation whose
# `-run` contains every test assigned to it, so `-shuffle=on` and the package
# parallelism of `go test` are unchanged. No test is skipped: the union of all
# buckets is the complete inventory, and `ci-required` consumes the aggregate
# `test` matrix result.
#
# `make test` remains the single-process local gate. `make test-shard` is the
# CI packaging of the same tests; `make flake` uses the same inventory for the
# 20-count nightly run. When PKGS is set explicitly (meta-test, focused local
# runs), sharding is bypassed and the command behaves like the original target.
#
# macOS runners ship bash 3.2, so this file avoids associative arrays and
# mapfile; the split itself lives in awk.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

MODE=${1:-}
SHARD=${2:-1/1}
if [[ $MODE != test && $MODE != flake ]]; then
	echo "usage: scripts/test-shard.sh <test|flake> <i>/<n>" >&2
	exit 2
fi
if ! [[ $SHARD =~ ^([0-9]+)/([0-9]+)$ ]]; then
	echo "test-shard: shard harus <i>/<n> (dapat: $SHARD)" >&2
	exit 2
fi
INDEX=${BASH_REMATCH[1]}
TOTAL=${BASH_REMATCH[2]}
if ((INDEX < 1 || INDEX > TOTAL)); then
	echo "test-shard: shard $INDEX di luar 1..$TOTAL" >&2
	exit 2
fi

PKGS=${PKGS:-./...}
TAGSCSV=${TAGSCSV:-goolm}
RACE=${RACE:-}
TEST_TIMEOUT=${TEST_TIMEOUT:-20m}
FLAKE_COUNT=${FLAKE_COUNT:-20}
FLAKE_WAVES=${FLAKE_WAVES:-10}
WEIGHTS=${SHARD_WEIGHTS:-$ROOT/scripts/test-shard.weights}

# Plan-only and inventory-fixture runs must reach the shard split even when
# PKGS is narrowed: the `push: main` smoke tier runs `make test PKGS="…"` and
# TestTestShardPartition feeds a fixture with SHARD_PLAN_ONLY=1. Evaluating
# them before the focused-PKGS shortcut below keeps that space-separated list
# from reaching `go test` as one package argument ("matched no packages").
if [[ -n ${SHARD_INVENTORY:-} || ${SHARD_PLAN_ONLY:-0} == 1 ]]; then
	PKGS=./...
fi

flake_waves() {
	if ((FLAKE_COUNT % FLAKE_WAVES != 0)); then
		echo "FLAKE_COUNT ($FLAKE_COUNT) harus habis dibagi FLAKE_WAVES ($FLAKE_WAVES)" >&2
		exit 2
	fi
	local n=$((FLAKE_COUNT / FLAKE_WAVES)) w
	for w in $(seq 1 "$FLAKE_WAVES"); do
		echo "flake wave $w/$FLAKE_WAVES: -count=$n -shuffle=on"
		# shellcheck disable=SC2086 # RACE is a flag list ("-race" or empty).
		go test -tags "$TAGSCSV" -count="$n" -shuffle=on "$@"
	done
}

# Focused runs (meta-test) keep the original single invocation: the shard
# inventory is the whole module and would either be empty or irrelevant.
if [[ $PKGS != "./..." ]]; then
	if [[ $MODE == flake ]]; then
		flake_waves "$PKGS"
	else
		# shellcheck disable=SC2086 # RACE is a flag list.
		go test -tags "$TAGSCSV" $RACE -shuffle=on -timeout "$TEST_TIMEOUT" -skip '^TestScenario$' "$PKGS"
		# shellcheck disable=SC2086
		go test -tags "$TAGSCSV" $RACE -shuffle=on -timeout "$TEST_TIMEOUT" -run '^TestScenario$' ./internal/ctxdb
	fi
	exit 0
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/damnbot-shard.XXXXXX")
trap 'rm -rf "$work"' EXIT

# Inventory: "<import path>\t<test name>" for every top-level Test function.
# go list supplies the package set for the current OS/arch; the source scan
# includes platform-specific files, and a name that is absent from the current
# build is simply unmatched by -run (never dropped from the inventory of the
# OS that has it). SHARD_INVENTORY lets the archtest regression feed a fixture
# without walking the module.
if [[ -n ${SHARD_INVENTORY:-} ]]; then
	cp "$SHARD_INVENTORY" "$work/inventory"
else
	go list -f '{{.Dir}}|{{.ImportPath}}' ./... >"$work/packages"
	while IFS='|' read -r dir imp; do
		[[ -n $dir && -d $dir ]] || continue
		for f in "$dir"/*_test.go; do
			[[ -f $f ]] || continue
			sed -nE 's/^func (Test[A-Za-z0-9_]+)\(.*/\1/p' "$f" | while read -r name; do
				printf '%s\t%s\n' "$imp" "$name"
			done
		done
	done <"$work/packages" | sort -u >"$work/inventory"
fi

if [[ ! -s $work/inventory ]]; then
	echo "test-shard: tidak ada tes ditemukan" >&2
	exit 1
fi

# Weighted inventory. Package durations count once; explicit heavy tests are
# removed from the package average before it is split over the rest. The heavy
# list mirrors the durations observed in run 43675538911 and the nightly flake
# logs (installed archive tamper, manifest, chaos probe, scenario).
awk -F'\t' -v OFS='\t' -v weights="$WEIGHTS" '
	function rel(p) {
		sub(/^github\.com\/damnbot\/damnbot\//, "", p)
		return p == "github.com/damnbot/damnbot" ? "." : p
	}
	BEGIN {
		while ((getline line < weights) > 0) {
			if (line == "" || substr(line, 1, 1) == "#") continue
			split(line, p, "\t"); w[p[1]] = p[2] + 0
		}
		close(weights)
		nheavy = split("internal/e2e/TestInstallerRejectsTamper=188 internal/e2e/TestInstallerManifest=56 internal/e2e/TestChaosF4=58 internal/ctxdb/TestScenario=97", hv, " ")
		for (i = 1; i <= nheavy; i++) { split(hv[i], q, "="); hw[q[1]] = q[2] + 0 }
	}
	{ rows[++n] = $0; count[rel($1)]++; if ((rel($1) "/" $2) in hw) heavy_count[rel($1)]++ }
	END {
		for (pkg in count) {
			base = (pkg in w) ? w[pkg] : count[pkg]
			rest = count[pkg] - heavy_count[pkg]
			avg[pkg] = (rest > 0) ? base / rest : 0
		}
		for (i = 1; i <= n; i++) {
			split(rows[i], f, "\t")
			key = rel(f[1]) "/" f[2]
			print (key in hw) ? hw[key] : avg[rel(f[1])], f[1], f[2]
		}
	}
' "$work/inventory" >"$work/weighted"

# LPT: longest first, ties by package/name so the split is stable across runs.
LC_ALL=C sort -t "$(printf '\t')" -k1,1nr -k2,2 -k3,3 "$work/weighted" >"$work/sorted"
awk -F'\t' -v OFS='\t' -v n="$TOTAL" -v outdir="$work" '
	BEGIN { for (b = 1; b <= n; b++) sum[b] = 0 }
	{
		best = 1
		for (b = 2; b <= n; b++) if (sum[b] < sum[best]) best = b
		sum[best] += $1
		print $2, $3 > (outdir "/bucket-" best)
	}
	END { for (b = 1; b <= n; b++) printf "shard %d estimasi %.0fs\n", b, sum[b] > (outdir "/weights") }
' "$work/sorted"
sort -t ' ' -k3,3n "$work/weights"

bucket=$work/bucket-$INDEX
if [[ ! -s $bucket ]]; then
	echo "test-shard: shard $INDEX/$TOTAL tidak mendapat tes (naikkan n?)" >&2
	exit 1
fi

pkgs=()
while IFS= read -r p; do pkgs+=("$p"); done < <(cut -f1 "$bucket" | sort -u)
tests=()
while IFS= read -r t; do tests+=("$t"); done < <(cut -f2 "$bucket" | sort -u)
regex="^($(
	IFS='|'
	echo "${tests[*]}"
))$"

scenario=false
for t in "${tests[@]}"; do [[ $t == TestScenario ]] && scenario=true; done

echo "shard $INDEX/$TOTAL: ${#tests[@]} tes di ${#pkgs[@]} paket"
if [[ ${SHARD_PLAN_ONLY:-0} == 1 ]]; then
	cat "$bucket" >&2
	echo "regex ${#regex} byte, ${#pkgs[@]} paket"
	exit 0
fi
if [[ $MODE == test ]]; then
	# TestScenario keeps its own invocation and budget (ADR-115), exactly as
	# `make test` does; the shard that owns it runs both.
	# shellcheck disable=SC2086 # RACE is a flag list.
	go test -tags "$TAGSCSV" $RACE -shuffle=on -timeout "$TEST_TIMEOUT" \
		-skip '^TestScenario$' -run "$regex" "${pkgs[@]}"
	if [[ $scenario == true ]]; then
		# shellcheck disable=SC2086
		go test -tags "$TAGSCSV" $RACE -shuffle=on -timeout "$TEST_TIMEOUT" \
			-run '^TestScenario$' ./internal/ctxdb
	fi
else
	flake_waves -run "$regex" "${pkgs[@]}"
fi
