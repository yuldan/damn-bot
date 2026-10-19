#!/usr/bin/env bash
# check.sh K: run all gates for branch f1-ct-K; prints a summary line per gate.
set -uo pipefail
K=$1
NN=$(printf %02d "$K")
S=/tmp/ctsplit
W=/tmp/ct-split2
cd "$W"
if ((K == 1)); then BASE=339d92a; else BASE=f1-ct-$(printf %02d $((K - 1))); fi
HEADREF=f1-ct-$NN
LOG=$S/logs/$K
mkdir -p "$LOG"
res() { echo "$1: $2" | tee -a "$LOG/summary"; }
: >"$LOG/summary"

BASE=$BASE HEAD=$HEADREF PR_TITLE="$(cat $S/st/$K.title)" PR_BODY="$(cat $S/st/$K.body)" scripts/pr-meta.sh >"$LOG/prmeta" 2>&1 &&
	res prmeta "ok $(grep -o 'size: [0-9]*' "$LOG/prmeta")" || res prmeta "FAIL $(tr '\n' ' ' <"$LOG/prmeta")"

pkgs=$(git diff --name-only "$BASE" "$HEADREF" -- '*.go' | grep -v '^bench/' | xargs -r -n1 dirname | sort -u | sed 's|^|./|')
C=/tmp/ctcheck/$K
rm -rf "$C" && mkdir -p "$C" && git archive "$HEADREF" | tar -x -C "$C"
cd "$C"
for os in linux darwin windows; do
	GOOS=$os go vet -tags goolm ./... >"$LOG/vet.$os" 2>&1 && res "vet-$os" ok || res "vet-$os" "FAIL"
done

go test -race -count=1 -tags goolm $pkgs ./internal/archtest >"$LOG/test" 2>&1 && res test "ok ($(echo $pkgs | tr '\n' ' '))" || res test "FAIL"

make deadcode >"$LOG/deadcode" 2>&1 && res deadcode ok || res deadcode "FAIL $(grep GAGAL "$LOG/deadcode" | tr '\n' ' ')"

if [[ -d internal/codetools ]]; then
	TAGS=goolm PKGS=./internal/codetools/... COVERPKG=./internal/codetools/... scripts/coverage.sh >"$LOG/cov" 2>&1
	res coverage "rc=$? $(grep -iE 'total|coverage' "$LOG/cov" | tail -2 | tr '\n' ' ')"
fi

make lint >"$LOG/lint" 2>&1 && res lint ok || res lint "FAIL $(grep -cE '^[^ ]+\.go:[0-9]+' "$LOG/lint") issues"

SCC="go tool -modfile=tools/go.mod scc" scripts/loc-check.sh budget >"$LOG/loc" 2>&1 && res loc "ok $(grep -E 'codetools|sandbox' "$LOG/loc" | tr -s ' ' | tr '\n' ';')" || res loc "FAIL $(tr '\n' ' ' <"$LOG/loc")"

go mod tidy -diff >"$LOG/tidy" 2>&1 && res tidy ok || res tidy FAIL
(cd bench && go mod tidy -diff) >>"$LOG/tidy" 2>&1 && res tidy-bench ok || res tidy-bench FAIL
