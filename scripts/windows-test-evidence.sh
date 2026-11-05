#!/usr/bin/env bash
# Bukti bernama tes Windows untuk acceptance (ADR-165; amandemen ADR-324).
# `make test` hanya mencetak `ok <pkg> <durasi>`, sehingga log Windows tidak membuktikan tes bernama
# dieksekusi. Skrip ini menjalankan kelima tes acceptance dan GAGAL bila salah satu tidak muncul
# sebagai `--- PASS:` (hilang, skip, atau exit non-zero). Bukan gate suite: `make test` tetap gate.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

goos=${GOOS:-$(go env GOOS)}
if [[ $goos != windows ]]; then
	echo "EVIDENCE GAGAL: target ini hanya untuk Windows (GOOS=$goos); tesnya ber-build-tag windows" >&2
	exit 1
fi

tags=${TAGSCSV:-goolm}
race=${RACE:-}
names=(
	TestF5BackgroundWindowsJobObject
	TestShellBackgroundWindowsCapability
	TestPathLockDistinctFilesSameContentWindows
	TestPathLockSameFileTwoPathsWindows
	TestIdentifyHandleFailureWindows
)
pkgs=(./internal/exec ./internal/tools ./internal/codetools)

run="^($(IFS='|'; echo "${names[*]}"))$"
args=(-tags "$tags" -count=1 -v -timeout 10m -run "$run")
# RACE mengikuti matrix (windows/arm64 tidak punya race detector); kosong = tanpa -race.
[[ -n $race ]] && args+=("$race")

out=$(mktemp)
trap 'rm -f "$out"' EXIT

set +e
go test "${args[@]}" "${pkgs[@]}" 2>&1 | tee "$out"
rc=${PIPESTATUS[0]}
set -e

fail=0
for n in "${names[@]}"; do
	if grep -qF -- "--- PASS: $n " "$out"; then
		echo "EVIDENCE ok    $n PASS"
	elif grep -qF -- "--- SKIP: $n " "$out"; then
		echo "EVIDENCE GAGAL $n: SKIP, bukan eksekusi"
		fail=1
	else
		echo "EVIDENCE GAGAL $n: tidak muncul sebagai PASS di output go test"
		fail=1
	fi
done
if ((rc != 0)); then
	echo "EVIDENCE GAGAL: go test exit $rc"
	fail=1
fi
((fail == 0)) || exit 1
echo "EVIDENCE: kelima tes Windows bernama PASS"
