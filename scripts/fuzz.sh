#!/usr/bin/env bash
# Gate fuzz (ENGINEERING E-57, ADR-073): setiap target `Fuzz*` di paket PKGS dijalankan FUZZTIME
# (default 60 s), 0 crash. Input yang gagal ditulis Go ke testdata/fuzz paket itu (di-commit bersama
# perbaikannya). SHARD=i/n membagi daftar target (urut paket + nama) ke n shard paralel (job CI 3 shard).
# 0 target pada shard = lulus dengan pesan eksplisit (target fuzz bertambah bersama paket F1).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
TAGS=${TAGS:-goolm}
PKGS=${PKGS:-./...}
FUZZTIME=${FUZZTIME:-60s}
SHARD=${SHARD:-1/1}
idx=${SHARD%/*} n=${SHARD#*/}

targets=()
# shellcheck disable=SC2086 # PKGS boleh berisi beberapa pola
while read -r ip dir; do
	while read -r name; do
		targets+=("$ip $name")
	done < <(find "$dir" -maxdepth 1 -name '*_test.go' -exec sed -nE 's/^func (Fuzz[A-Za-z0-9_]*)\(.*/\1/p' {} + | sort -u)
done < <(go list -tags "$TAGS" -f '{{.ImportPath}} {{.Dir}}' $PKGS)

fail=0 ran=0
for i in "${!targets[@]}"; do
	((i % n == idx - 1)) || continue
	read -r ip name <<<"${targets[$i]}"
	ran=$((ran + 1))
	echo "== fuzz $ip $name ($FUZZTIME)"
	if ! go test -tags "$TAGS" -run '^$' -fuzz "^${name}\$" -fuzztime "$FUZZTIME" "$ip"; then
		echo "FUZZ GAGAL $ip $name"
		fail=1
	fi
done
echo "fuzz: $ran target dijalankan (shard $SHARD dari ${#targets[@]} target)"
exit $fail
