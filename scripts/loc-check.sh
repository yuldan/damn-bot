#!/usr/bin/env bash
# Gate LOC (ENGINEERING E-19/E-61/E-70). Pemakaian:
#   scripts/loc-check.sh budget   # per paket (loc.budget) + total; exit 1 bila melebihi
#   scripts/loc-check.sh ratio    # rasio LOC test:source 0,8–1,2 (E-70); exit 1 bila di luar, tetapi hanya
#                                 # bila Go non-test ≥ 5.000 LOC (ADR-050); di bawahnya laporan saja (exit 0)
# Butuh scc (SCC env; default `go tool -modfile=tools/go.mod scc`).
# AUD-073: file Go non-test di internal/* yang tidak terpetakan ke baris loc.budget = gagal; file
# generated hanya dikecualikan bila path-nya cocok glob baris `GENERATED <glob>` di loc.budget
# (bukan berdasarkan header "Code generated" — scc dijalankan tanpa --no-gen).
set -euo pipefail

mode=${1:-budget}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
SCC=${SCC:-go tool -modfile=tools/go.mod scc}

dirs=()
for d in cmd internal tools/analyzers; do [[ -d $d ]] && dirs+=("$d"); done
mapfile -t gen < <(awk '$1 == "GENERATED" { print $2 }' loc.budget)
# CSV: Language,Provider(path),Filename,Lines,Code,...; baris yang cocok allowlist GENERATED dibuang.
csv=$($SCC --by-file -f csv --include-ext go "${dirs[@]}" | while IFS= read -r row; do
	path=${row#*,}
	path=${path%%,*}
	[[ $path == */testdata/* ]] && continue # fixture analysistest/fuzz, bukan kode
	for g in "${gen[@]}"; do
		# shellcheck disable=SC2053 # $g sengaja glob
		[[ $path == $g ]] && continue 2
	done
	printf '%s\n' "$row"
done)

case $mode in
budget)
	awk -v budget=loc.budget '
	BEGIN {
		FS = "[ \t]+"
		while ((getline line < budget) > 0) {
			if (line ~ /^#/ || line ~ /^[ \t]*$/) continue
			n = split(line, f, FS)
			if (f[1] == "GENERATED") continue
			if (f[1] == "TOTAL") { total_max = f[2]; continue }
			max[f[1]] = f[2]; used[f[1]] = 0
			for (i = 3; i <= n; i++) dirgroup[f[i]] = f[1]
		}
		FS = ","
	}
	NR == 1 { next }
	$2 ~ /_test\.go$/ { next }
	{
		path = $2; code = $5; total += code
		best = ""
		for (d in dirgroup)
			if ((index(path, d "/") == 1) && length(d) > length(best)) best = d
		if (best != "") used[dirgroup[best]] += code
		else if (path ~ /^internal\//) { d = path; sub(/\/[^\/]*$/, "", d); unmapped[d] += code }
	}
	END {
		fail = 0
		for (d in unmapped) { printf "LOC GAGAL unmapped %s (%d LOC): tidak ada baris loc.budget (AUD-073)\n", d, unmapped[d]; fail = 1 }
		for (g in max) {
			if (used[g] > max[g]) { printf "LOC GAGAL %-18s %6d > %6d\n", g, used[g], max[g]; fail = 1 }
			else if (used[g] > 0) printf "LOC ok    %-18s %6d / %6d\n", g, used[g], max[g]
		}
		if (total > total_max) { printf "LOC GAGAL TOTAL %d > %d\n", total, total_max; fail = 1 }
		else printf "LOC ok    TOTAL %d / %d\n", total, total_max
		exit fail
	}' <<<"$csv"
	;;
ratio)
	awk -F, '
	NR == 1 { next }
	$2 ~ /_test\.go$/ { test += $5; next }
	{ src += $5 }
	END {
		if (src == 0) { print "LOC ratio: 0 source LOC"; exit 1 }
		r = test / src
		printf "LOC ratio test:source = %d:%d = %.2f (target 0,80–1,20, E-70)\n", test, src, r
		bad = (r < 0.8 || r > 1.2)
		# ADR-050: gate berlaku mulai 5.000 LOC Go non-test; di bawahnya hanya dilaporkan.
		if (src < 5000) {
			printf "LOC ratio report-only: Go non-test %d < 5000 (ADR-050); %s, tidak menggagalkan gate\n", src, (bad ? "di luar target" : "dalam target")
			exit 0
		}
		exit bad ? 1 : 0
	}' <<<"$csv"
	;;
*)
	echo "pemakaian: $0 budget|ratio" >&2
	exit 2
	;;
esac
