#!/usr/bin/env bash
# Gate coverage (ENGINEERING E-58, ADR-078 AUD-059): coverage pernyataan per paket di bawah COVERPKG,
# diukur dari satu profil `go test -coverpkg` (test paket lain ikut menutup paket ini).
# Paket kritis ≥ 80 %, paket lain ≥ 70 %. Paket yang punya fungsi tetapi tidak ada di profil = gagal
# (tidak boleh lulus kosong). Pemakaian: PKGS=./... COVERPKG=./internal/... scripts/coverage.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
TAGS=${TAGS:-goolm}
PKGS=${PKGS:-./...}
COVERPKG=${COVERPKG:-./internal/...}
# E-58 + AUD-059: daftar paket kritis (path relatif modul, cocok persis; subpaket = "paket lain").
CRITICAL="internal/loop internal/tools internal/schema internal/provider internal/ctxdb internal/assemble internal/compaction internal/journal internal/budget internal/heal internal/codetools internal/exec"

module=$(go list -m)
profile=$(mktemp)
trap 'rm -f "$profile"' EXIT

# shellcheck disable=SC2086 # PKGS boleh berisi beberapa pola
go test -tags "$TAGS" -count=1 -covermode=set -coverpkg="$COVERPKG" -coverprofile="$profile" $PKGS

# Paket yang wajib muncul di profil: punya file Go non-test dengan deklarasi fungsi.
expected=$(go list -tags "$TAGS" -f '{{.ImportPath}} {{.Dir}}' "$COVERPKG" | while read -r ip dir; do
	if grep -lqE '^func ' $(find "$dir" -maxdepth 1 -name '*.go' ! -name '*_test.go') /dev/null 2>/dev/null; then
		echo "$ip"
	fi
done)

awk -v module="$module" -v critical="$CRITICAL" -v expected="$expected" '
BEGIN {
	n = split(critical, c, " "); for (i = 1; i <= n; i++) crit[module "/" c[i]] = 1
	n = split(expected, e, "\n"); for (i = 1; i <= n; i++) if (e[i] != "") want[e[i]] = 1
}
NR == 1 { next } # mode: set
{
	# <importpath>/<file>.go:<l.c>,<l.c> <stmts> <count>; blok yang sama bisa muncul dari beberapa binary test.
	key = $1; split($1, a, ":"); pkg = a[1]; sub(/\/[^\/]*$/, "", pkg)
	if (!(key in seen)) { seen[key] = 1; total[pkg] += $2 }
	if ($3 > 0 && !(key in hit)) { hit[key] = 1; cov[pkg] += $2 }
}
END {
	fail = 0
	for (p in want) if (!(p in total)) { printf "COVERAGE GAGAL %s: tidak ada di profil (paket tanpa test yang menjangkaunya)\n", p; fail = 1 }
	for (p in total) {
		if (total[p] == 0) continue
		pct = 100 * cov[p] / total[p]; min = (p in crit) ? 80 : 70
		if (pct < min) { printf "COVERAGE GAGAL %s: %.1f%% < %d%%\n", p, pct, min; fail = 1 }
		else printf "COVERAGE ok    %s: %.1f%% (min %d%%)\n", p, pct, min
	}
	exit fail
}' "$profile"
