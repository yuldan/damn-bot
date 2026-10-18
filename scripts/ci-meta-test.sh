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
GATES=(lint test race size-check loc loc-ratio deps-check vuln licenses tidy-check mod-verify)
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
	tidy-check) echo 'BurntSushi/toml' ;;
	mod-verify) echo 'has been modified' ;;
	esac
}

# prepare: penyiapan yang dipakai baseline DAN salinan pelanggaran (tidak melemahkan gate).
# loc-ratio: rasio repo nyata sedang merah (TS-03), jadi gate diuji pada pohon sintetis
# dengan rasio test:source = 1,00 (100:100) — baseline harus lulus, +2.000 baris test harus gagal.
prepare() { # <gate> <dir>
	local d=$2
	case $1 in
	loc-ratio)
		find "$d/cmd" "$d/internal" -name '*.go' -delete
		mkdir -p "$d/internal/metaratio"
		{
			printf 'package metaratio\n\n// F = 100 baris kode.\nfunc F() int {\n\tx := 0\n'
			for _ in $(seq 96); do printf '\tx++\n'; done
			printf '\treturn x\n}\n'
		} >"$d/internal/metaratio/a.go"
		{
			printf 'package metaratio\n\nimport "testing"\n\nfunc TestF(t *testing.T) {\n\tx := 0\n'
			for _ in $(seq 93); do printf '\tx++\n'; done
			printf '\tif F() != 96 {\n\t\tt.Fatal(x)\n\t}\n}\n'
		} >"$d/internal/metaratio/a_test.go"
		;;
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
			printf 'package metaratio\n\nimport "testing"\n\nfunc TestMetaRatio(t *testing.T) {\n\tx := 0\n'
			for _ in $(seq 2000); do printf '\tx++\n'; done
			printf '\t_ = x\n}\n'
		} >"$d/internal/metaratio/zz_meta_ratio_test.go"
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
	tidy-check)
		# Require yang tidak dipakai → `go mod tidy -diff` harus menampilkan diff (E-46).
		(cd "$d" && go mod edit -require=github.com/BurntSushi/toml@v1.6.0)
		;;
	mod-verify)
		# Rusak satu file modul di cache terisolasi → `go mod verify` harus gagal (E-47).
		(cd "$d" && GOMODCACHE="$work/modcache" GOFLAGS=-modcacherw go mod download >/dev/null 2>&1)
		local f
		f=$(find "$work/modcache/go.uber.org/goleak@v1.3.0" -name '*.go' | sort | head -1)
		chmod u+w "$f"
		printf '\n// meta: dirusak\n' >>"$f"
		;;
	esac
}

run_gate() { # <gate> <dir> <log>
	if [[ $1 == mod-verify ]]; then
		# Cache modul terisolasi di $work (pelanggaran = cache yang dirusak; ~/go tidak disentuh).
		(cd "$2" && GOMODCACHE="$work/modcache" GOFLAGS=-modcacherw make -s mod-verify) >"$3" 2>&1
		return
	fi
	(cd "$2" && make -s "$(target "$1")") >"$3" 2>&1
}

declare -A result
fail=0
# scaffold <dir>: buang paket internal/* selain archtest. Dipakai HANYA bila baseline HEAD merah
# (kode paket lain melanggar gate): mekanisme gate tetap diuji pada pohon yang bersih, dan status
# HEAD yang merah tetap dilaporkan (HEAD_RED) — gate nyata tidak dilemahkan.
scaffold() {
	find "$1/internal" -mindepth 1 -maxdepth 1 -type d ! -name archtest -exec rm -rf {} +
}

for g in "${GATES[@]}"; do
	base="$work/$g-base" viol="$work/$g-viol" note=""
	fresh "$base"
	prepare "$g" "$base"
	if ! run_gate "$g" "$base" "$work/$g-base.log"; then
		tail -3 "$work/$g-base.log" | sed "s/^/  [$g HEAD] /"
		scaffold "$base"
		if ! run_gate "$g" "$base" "$work/$g-base.log"; then
			result[$g]="BASELINE_RED (gagal juga pada pohon scaffold; lihat log)"
			tail -5 "$work/$g-base.log" | sed "s/^/  [$g base] /"
			fail=1
			rm -rf "$base"
			continue
		fi
		note=" (baseline scaffold; HEAD_RED)"
		fail=1
	fi
	rm -rf "$base"
	fresh "$viol"
	prepare "$g" "$viol"
	[[ -n $note ]] && scaffold "$viol"
	inject "$g" "$viol"
	if run_gate "$g" "$viol" "$work/$g-viol.log"; then
		result[$g]="MISSED (gate lulus pada pelanggaran)$note"
		fail=1
	elif grep -Eq "$(marker "$g")" "$work/$g-viol.log"; then
		result[$g]="DETECTED$note"
	else
		result[$g]="WRONG_REASON (gagal, tapi penanda '$(marker "$g")' tidak ada)$note"
		tail -5 "$work/$g-viol.log" | sed "s/^/  [$g viol] /"
		fail=1
	fi
	rm -rf "$viol"
done

echo "== ci-meta-test"
for g in "${GATES[@]}"; do printf '%-12s %s\n' "$g" "${result[$g]}"; done
exit $fail
