#!/usr/bin/env bash
# Gate kode mati (ENGINEERING E-53, ADR-073): `deadcode -test` untuk GOOS linux, darwin, windows,
# digabung: fungsi mati = tidak terjangkau di SETIAP GOOS yang meng-compile file-nya (fungsi di file
# khusus OS cukup mati di OS itu; fungsi lintas OS yang dipakai satu OS saja tetap hidup).
# Pemakaian: DEADCODE=<bin> PKGS=./... scripts/deadcode.sh  (Makefile membangun DEADCODE native).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
TAGS=${TAGS:-goolm}
PKGS=${PKGS:-./...}
: "${DEADCODE:?DEADCODE = path binary deadcode (make deadcode membangunnya dari tools/go.mod)}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

oses=(linux darwin windows)
for os in "${oses[@]}"; do
	# Laporan: "<file>:<baris>:<kolom>: unreachable func: <nama>" → kunci "<file>:<baris>: <nama>".
	# shellcheck disable=SC2086 # PKGS boleh berisi beberapa pola
	GOOS=$os "$DEADCODE" -test -tags "$TAGS" $PKGS | sed -E 's/^\.\///; s/^([^:]+):([0-9]+):[0-9]+: unreachable func: (.*)$/\1:\2: \3/' |
		sort -u >"$work/dead.$os"
	# File yang di-compile di GOOS ini (termasuk test), path relatif root.
	# shellcheck disable=SC2086
	GOOS=$os go list -tags "$TAGS" -test -f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}{{"\n"}}{{end}}' $PKGS |
		sed "s|^$root/||" | sort -u >"$work/files.$os"
done

fail=0
while read -r key; do
	file=${key%%:*}
	dead=1
	for os in "${oses[@]}"; do
		if grep -qxF "$file" "$work/files.$os" && ! grep -qxF "$key" "$work/dead.$os"; then
			dead=0
			break
		fi
	done
	if ((dead)); then
		echo "DEADCODE GAGAL $key"
		fail=1
	fi
done < <(sort -u "$work"/dead.*)
((fail)) || echo "deadcode: 0 fungsi tak terjangkau (${oses[*]})"
exit $fail
