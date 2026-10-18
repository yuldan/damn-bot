#!/usr/bin/env bash
# Meta-test gate CI (ROADMAP F0 acceptance): setiap gate harus GAGAL pada satu pelanggaran sintetis.
# Untuk tiap gate: (1) salin HEAD ke direktori temp, jalankan gate → baseline harus lulus;
# (2) salinan baru + suntik pelanggaran → gate harus gagal DAN output memuat penanda alasan
# (agar kegagalan karena sebab lain, mis. error build, tidak dihitung "terdeteksi").
# Pemakaian: scripts/ci-meta-test.sh [gate...]   (default: semua). Exit 0 hanya bila 100% DETECTED.
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d /tmp/damnbot-meta.XXXXXX)
trap 'rm -rf "$work"' EXIT
GATES=(lint test race size-check loc loc-ratio deps-check vuln licenses)
[[ $# -gt 0 ]] && GATES=("$@")

# fresh <dir>: ekspor HEAD (bukan worktree) agar hasil deterministik dan sama dengan CI.
fresh() {
	rm -rf "$1" && mkdir -p "$1"
	git -C "$root" archive HEAD | tar -x -C "$1"
}

# target make per gate (race memakai target test yang sama: -race ada di `make test`).
target() { case $1 in race) echo test ;; *) echo "$1" ;; esac; }

# marker: regex yang wajib muncul di output gate saat pelanggaran terdeteksi.
marker() {
	case $1 in
	lint) echo 'forbidigo' ;;
	test) echo 'TestMetaViolation' ;;
	race) echo 'WARNING: DATA RACE' ;;
	size-check) echo 'SIZE GAGAL full' ;;
	loc) echo 'LOC GAGAL boundary' ;;
	loc-ratio) echo 'LOC ratio' ;;
	deps-check) echo 'tidak ada di deps.allow' ;;
	vuln) echo 'GO-[0-9]{4}-[0-9]+' ;;
	licenses) echo 'GPL' ;;
	esac
}

inject() { # <gate> <dir>
	local d=$2
	case $1 in
	lint)
		mkdir -p "$d/internal/metaviolation"
		printf 'package metaviolation\n\n// F melanggar E-23.\nfunc F() { panic("meta") }\n' >"$d/internal/metaviolation/v.go"
		;;
	test)
		mkdir -p "$d/internal/metaviolation"
		printf 'package metaviolation\n\nimport "testing"\n\nfunc TestMetaViolation(t *testing.T) { t.Fatal("pelanggaran sintetis") }\n' \
			>"$d/internal/metaviolation/v_test.go"
		;;
	race)
		# Lulus tanpa -race, gagal dengan -race: tulis bersamaan tanpa sinkronisasi.
		mkdir -p "$d/internal/metaviolation"
		cat >"$d/internal/metaviolation/race_test.go" <<'EOF'
package metaviolation

import (
	"sync"
	"testing"
)

func TestMetaRace(t *testing.T) {
	var n int
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); n++ }()
	}
	wg.Wait()
	_ = n
}
EOF
		;;
	size-check)
		# Embed 53 MB acak ke binary → full > 52 MB.
		head -c 53000000 /dev/urandom >"$d/cmd/damnbot/meta.bin"
		cat >"$d/cmd/damnbot/zz_meta_blob.go" <<'EOF'
package main

import (
	_ "embed"
	"os"
)

//go:embed meta.bin
var metaBlob []byte

func init() {
	if len(os.Args) > 1000 {
		_, _ = os.Stdout.Write(metaBlob)
	}
}
EOF
		;;
	loc)
		# internal/boundary budget 150 LOC → 200 baris kode.
		mkdir -p "$d/internal/boundary"
		{
			printf 'package boundary\n\n// Meta melanggar E-19.\nfunc Meta() int {\n\tx := 0\n'
			for _ in $(seq 200); do printf '\tx++\n'; done
			printf '\treturn x\n}\n'
		} >"$d/internal/boundary/meta.go"
		;;
	loc-ratio)
		{
			printf 'package main\n\nimport "testing"\n\nfunc TestMetaRatio(t *testing.T) {\n\tx := 0\n'
			for _ in $(seq 2000); do printf '\tx++\n'; done
			printf '\t_ = x\n}\n'
		} >"$d/cmd/damnbot/zz_meta_ratio_test.go"
		;;
	deps-check)
		# Require langsung di luar deps.allow (testify ditolak E-66).
		mkdir -p "$d/internal/metaviolation"
		printf 'package metaviolation\n\nimport _ "github.com/stretchr/testify/assert"\n' >"$d/internal/metaviolation/v_test.go"
		(cd "$d" && go get github.com/stretchr/testify@v1.9.0 >/dev/null 2>&1 && go mod tidy >/dev/null 2>&1)
		;;
	vuln)
		# gopkg.in/yaml.v2 v2.2.2: kerentanan DoS yang terjangkau lewat yaml.Unmarshal.
		mkdir -p "$d/internal/metaviolation"
		printf 'package metaviolation\n\nimport "gopkg.in/yaml.v2"\n\n// F memanggil API rentan.\nfunc F(b []byte) error { var v any; return yaml.Unmarshal(b, &v) }\n' \
			>"$d/internal/metaviolation/v.go"
		(cd "$d" && go get gopkg.in/yaml.v2@v2.2.2 >/dev/null 2>&1 && go mod tidy >/dev/null 2>&1)
		;;
	licenses)
		# Modul lokal berlisensi GPL-3.0 yang di-link ke cmd/damnbot.
		mkdir -p "$d/metagpl"
		cp /usr/share/common-licenses/GPL-3 "$d/metagpl/LICENSE"
		printf 'module example.com/metagpl\n\ngo 1.26\n' >"$d/metagpl/go.mod"
		printf 'package metagpl\n\n// V dipakai cmd/damnbot.\nconst V = "gpl"\n' >"$d/metagpl/metagpl.go"
		printf 'package main\n\nimport "example.com/metagpl"\n\nvar _ = metagpl.V\n' >"$d/cmd/damnbot/zz_meta_gpl.go"
		(cd "$d" && go mod edit -require=example.com/metagpl@v0.0.0 -replace=example.com/metagpl=./metagpl)
		;;
	esac
}

run_gate() { # <gate> <dir> <log>
	(cd "$2" && make -s "$(target "$1")") >"$3" 2>&1
}

declare -A result
fail=0
for g in "${GATES[@]}"; do
	base="$work/$g-base" viol="$work/$g-viol"
	fresh "$base"
	if ! run_gate "$g" "$base" "$work/$g-base.log"; then
		result[$g]="BASELINE_RED (gate sudah gagal tanpa pelanggaran; lihat log)"
		tail -5 "$work/$g-base.log" | sed "s/^/  [$g base] /"
		fail=1
		rm -rf "$base"
		continue
	fi
	rm -rf "$base"
	fresh "$viol"
	inject "$g" "$viol"
	if run_gate "$g" "$viol" "$work/$g-viol.log"; then
		result[$g]="MISSED (gate lulus pada pelanggaran)"
		fail=1
	elif grep -Eq "$(marker "$g")" "$work/$g-viol.log"; then
		result[$g]="DETECTED"
	else
		result[$g]="WRONG_REASON (gagal, tapi penanda '$(marker "$g")' tidak ada)"
		tail -5 "$work/$g-viol.log" | sed "s/^/  [$g viol] /"
		fail=1
	fi
	rm -rf "$viol"
done

echo "== ci-meta-test"
for g in "${GATES[@]}"; do printf '%-12s %s\n' "$g" "${result[$g]}"; done
exit $fail
