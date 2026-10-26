#!/usr/bin/env bash
# Gate LOC (ENGINEERING E-19/E-61/E-70). Pemakaian:
#   scripts/loc-check.sh budget   # per paket (loc.budget) + total; exit 1 bila melebihi
#   scripts/loc-check.sh ratio    # rasio LOC test:source 0,8–1,2 (E-70); exit 1 bila di luar, tetapi hanya
#                                 # bila Go non-test ≥ 5.000 LOC (ADR-050); di bawahnya laporan saja (exit 0).
#                                 # Direktori baris `TESTONLY <dir>` loc.budget dilaporkan terpisah dan
#                                 # tidak masuk rasio maupun ambang (ADR-098).
# Butuh scc (SCC env; default `go tool -modfile=tools/go.mod scc`).
# AUD-073: file Go non-test di internal/* yang tidak terpetakan ke baris loc.budget = gagal; file
# generated hanya dikecualikan bila path-nya cocok glob baris `GENERATED <glob>` di loc.budget
# (bukan berdasarkan header "Code generated" — scc dijalankan tanpa --no-gen).
# ADR-196: baris `MODULE <grup> <LOC> <dir...>` dicek per-baris seperti baris core, tetapi LOC-nya
# tidak masuk TOTAL maupun Σ core (kelas modul permukaan, preseden baris infra NEW-34).
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
			if (f[1] == "GENERATED" || f[1] == "TESTONLY") continue
			if (f[1] == "TOTAL") { total_max = f[2]; continue }
			# ADR-196: baris `MODULE <nama> <cap> <dir...>` menjadi grup sendiri ber-key
			# "MODULE <nama>" dengan cap per-baris, tetapi LOC-nya tidak masuk TOTAL/Σ core; baris
			# lain tetap `<nama> <cap> <dir...>`.
			if (f[1] == "MODULE") { name = "MODULE " f[2]; capn = f[3]; first = 4 }
			else                  { name = f[1]; capn = f[2]; first = 3 }
			if (name in max) { printf "LOC GAGAL baris ganda %s\n", name; begin_fail = 1; continue }
			if (capn !~ /^[0-9]+$/ || capn + 0 <= 0) { printf "LOC GAGAL cap %s tidak valid\n", name; begin_fail = 1; continue }
			max[name] = capn; used[name] = 0
			for (i = first; i <= n; i++) dirgroup[f[i]] = name
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
		fail = begin_fail
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
	awk -F, -v budget=loc.budget '
	BEGIN {
		# ADR-098: allowlist eksplisit paket test-only (bukan heuristik), agar paket baru yang
		# berat test tidak bisa lolos dari rasio diam-diam.
		while ((getline line < budget) > 0) {
			n = split(line, f, /[ \t]+/)
			if (f[1] == "TESTONLY" && n >= 2) testonly[f[2]] = 1
		}
	}
	NR == 1 { next }
	{
		ex = ""
		for (d in testonly) if (index($2, d "/") == 1) ex = d
		kind = ($2 ~ /_test\.go$/) ? "test" : "src"
		if (ex != "") { exl[ex, kind] += $5; next }
		if (kind == "test") test += $5; else src += $5
	}
	END {
		for (d in testonly)
			printf "LOC ratio dikecualikan (TESTONLY, ADR-098) %s test:source = %d:%d\n", d, exl[d, "test"], exl[d, "src"]
		if (src == 0) { print "LOC ratio: 0 source LOC"; exit 1 }
		r = test / src
		printf "LOC ratio test:source = %d:%d = %.2f (target 0,80–1,20, E-70)\n", test, src, r
		bad = (r < 0.8 || r > 1.2)
		# ADR-050: gate berlaku mulai 5.000 LOC Go non-test; di bawahnya hanya dilaporkan.
		if (src < 5000) {
			printf "LOC ratio report-only: Go non-test %d < 5000 (ADR-050); %s, tidak menggagalkan gate\n", src, (bad ? "di luar target" : "dalam target")
			exit 0
		}
		if (bad) printf "LOC GAGAL ratio %.2f di luar 0,80–1,20 (E-70)\n", r
		exit bad ? 1 : 0
	}' <<<"$csv"
	;;
*)
	echo "pemakaian: $0 budget|ratio" >&2
	exit 2
	;;
esac
