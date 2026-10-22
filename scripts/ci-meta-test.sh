#!/usr/bin/env bash
# Meta-test gate CI (ROADMAP F0 acceptance, F1 #0, ADR-073): setiap gate harus GAGAL pada satu
# pelanggaran sintetis. Untuk tiap gate: (1) salin HEAD ke direktori temp, jalankan gate → baseline
# harus lulus; (2) salinan baru + suntik pelanggaran → gate harus gagal DAN output memuat penanda alasan
# (agar kegagalan karena sebab lain, mis. error build, tidak dihitung "terdeteksi").
# Gate test/race/timing/fuzz/coverage/flake/perf/phase-acceptance diuji pada pohon minimal
# ./internal/metaviolation/... (ADR-073: durasi O(1) terhadap ukuran repo).
# Pemakaian: scripts/ci-meta-test.sh [gate...]   (default: semua). Exit 0 hanya bila 100% DETECTED.
set -uo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d /tmp/damnbot-meta.XXXXXX)
# File modul di GOMODCACHE terisolasi read-only: beri izin tulis sebelum dihapus.
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
# Semua gate memakai toolchain yang dipilih go.mod (ADR-051) secara lokal, sehingga GOMODCACHE
# terisolasi gate mod-verify tidak mengunduh ulang toolchain.
PATH="$(cd "$root" && go env GOROOT)/bin:$PATH"
export GOTOOLCHAIN=local
GATES=(
	lint depguard lint-anchor vet-custom test race timing vet-cross build-matrix fuzz coverage deadcode flake perf
	size-check loc loc-unmapped loc-ratio loc-ratio-testonly deps-check vuln licenses tidy-check mod-verify bench-budget
	sqlite-engine skill-refs
	phase-file phase-acceptance
	arch-importgraph arch-filelength arch-globals arch-goleak arch-budgetsum arch-codeowners arch-nolint
	arch-workflows arch-mpl
	pr-title pr-changelog adrgate adrgate-spec pr-size spec-guard
)
[[ $# -gt 0 ]] && GATES=("$@")
MINI=./internal/metaviolation/...

# fresh <dir>: ekspor HEAD (bukan worktree) agar hasil deterministik dan sama dengan CI.
fresh() {
	rm -rf "$1" && mkdir -p "$1"
	git -C "$root" archive HEAD | tar -x -C "$1"
}

# make <target> [VAR=…] per gate.
target() {
	case $1 in
	depguard | lint-anchor) echo lint ;;
	race) echo "test PKGS=$MINI" ;;
	test | flake | perf | phase-acceptance) echo "$1 PKGS=$MINI" ;;
	timing) echo "test-timing PKGS=$MINI" ;;
	fuzz) echo "fuzz PKGS=$MINI FUZZTIME=5s" ;;
	coverage) echo "coverage PKGS=$MINI COVERPKG=$MINI" ;;
	loc-unmapped) echo loc ;;
	loc-ratio-testonly) echo loc-ratio ;;
	phase-file) echo "test PKGS=./internal/testutil/phase/..." ;;
	arch-mpl) echo licenses ;;
	arch-*) echo archtest ;;
	pr-* | adrgate* | spec-guard) echo pr-meta ;;
	*) echo "$1" ;;
	esac
}

# marker: regex yang wajib muncul di output gate saat pelanggaran terdeteksi.
marker() {
	case $1 in
	lint) echo 'forbidigo' ;;
	depguard) echo "database/sql.*E-04.*\(depguard\)" ;;
	lint-anchor) echo 'internal/tools/cmd/x\.go.*E-31' ;;
	vet-custom) echo 'nohttpclient: E-05' ;;
	test) echo 'TestMetaViolation' ;;
	race) echo 'WARNING: DATA RACE' ;;
	timing) echo 'timing budget meta tx' ;;
	vet-cross) echo 'undefined: undefinedMetaCross' ;;
	build-matrix) echo 'undefined: undefinedMetaAndroid' ;;
	fuzz) echo 'FUZZ GAGAL .*FuzzMeta' ;;
	coverage) echo 'COVERAGE GAGAL .*metaviolation' ;;
	deadcode) echo 'DEADCODE GAGAL internal/metaviolation/v\.go:[0-9]+: Dead' ;;
	flake) echo 'TestMetaFlake' ;;
	perf) echo 'timing budget meta perf' ;;
	size-check) echo 'SIZE GAGAL full' ;;
	loc) echo 'LOC GAGAL boundary' ;;
	loc-unmapped) echo 'LOC GAGAL unmapped internal/metaunmapped' ;;
	loc-ratio | loc-ratio-testonly) echo 'LOC GAGAL ratio' ;;
	deps-check) echo 'tidak ada di deps.allow' ;;
	vuln) echo 'GO-[0-9]{4}-[0-9]+' ;;
	licenses) echo 'GPL' ;;
	tidy-check) echo 'example.com/meta/tidy' ;;
	mod-verify) echo 'has been modified' ;;
	bench-budget) echo 'TestMetaBench' ;;
	sqlite-engine) echo 'does not satisfy REL-04' ;;
	skill-refs) echo 'SKILL-REFS GAGAL meta/SKILL\.md' ;;
	phase-file) echo 'PHASE GAGAL' ;;
	phase-acceptance) echo 'PHASE GAGAL META-1: phase acceptance' ;;
	arch-importgraph) echo 'E-01 edge ilegal: internal/loop → internal/ctxdb' ;;
	arch-filelength) echo 'E-14/E-71 file terlalu panjang: internal/metaviolation/long\.go' ;;
	arch-globals) echo 'E-10 global di luar allowlist: internal/metaviolation/g\.go:Meta' ;;
	arch-goleak) echo 'E-29 goleak: paket internal/metaviolation' ;;
	arch-budgetsum) echo 'Σ loc\.budget core = [0-9]+ > 47000' ;;
	arch-codeowners) echo 'E-79 CODEOWNERS: direktori internal/metaviolation' ;;
	arch-nolint) echo 'E-52 nolint tanpa linter/alasan: internal/metaviolation/n\.go' ;;
	arch-workflows) echo 'AUD-070 action tidak dipin SHA: \.github/workflows/meta-violation\.yml' ;;
	arch-mpl) echo 'E-40 MPL-2\.0 di luar deps\.mpl: example\.com/metampl' ;;
	pr-title) echo 'PR-META GAGAL title' ;;
	pr-changelog) echo 'PR-META GAGAL changelog' ;;
	adrgate) echo 'PR-META GAGAL adrgate: deps.allow' ;;
	adrgate-spec) echo 'PR-META GAGAL adrgate: SPEC §3\.2\.4' ;;
	pr-size) echo 'PR-META GAGAL size' ;;
	spec-guard) echo 'PR-META GAGAL spec-guard: docs/ROADMAP\.md' ;;
	esac
}

# gofile <path> <isi>: tulis satu file Go (direktori dibuat).
gofile() { mkdir -p "$(dirname "$1")" && cat >"$1"; }

# metaheavy <dir>: paket berat test (1 baris kode : 2.000 baris test) untuk gate loc-ratio-testonly.
metaheavy() {
	local pkg
	pkg=$(basename "$1")
	printf 'package %s\n\n// Heavy is the only source line.\nfunc Heavy() int { return 1 }\n' "$pkg" | gofile "$1/meta_heavy.go"
	{
		printf 'package %s\n\nimport "testing"\n\nfunc TestMetaHeavy(t *testing.T) {\n\tx := 0\n' "$pkg"
		for _ in $(seq 2000); do printf '\tx++\n'; done
		printf '\tif Heavy() != 1 {\n\t\tt.Fatal(x)\n\t}\n}\n'
	} | gofile "$1/meta_heavy_test.go"
}

# pr_commit <dir>: commit semua perubahan worktree salinan (identitas sintetis).
pr_commit() { git -C "$1" add -A && git -C "$1" -c user.name=meta -c user.email=meta@example.invalid commit -qm "$2" --allow-empty; }

# prepare: penyiapan yang dipakai baseline DAN salinan pelanggaran (tidak melemahkan gate).
prepare() { # <gate> <dir>
	local d=$2
	case $1 in
	test | race | timing | fuzz | coverage | flake | perf | phase-acceptance)
		# Pohon minimal yang lulus dan ter-cover penuh.
		gofile "$d/internal/metaviolation/ok.go" <<'EOF'
// Package metaviolation is the minimal tree for gate meta-tests (ADR-073).
package metaviolation

// Ok is covered by TestOk.
func Ok() int { return 1 }
EOF
		gofile "$d/internal/metaviolation/ok_test.go" <<'EOF'
package metaviolation

import "testing"

func TestOk(t *testing.T) {
	if Ok() != 1 {
		t.Fatal("Ok")
	}
}
EOF
		;;
	loc-ratio | loc-ratio-testonly)
		# loc-ratio: gate hanya menggagalkan bila Go non-test ≥ 5.000 LOC (ADR-050), jadi gate diuji pada
		# pohon sintetis di atas ambang dengan rasio test:source ≈ 1,00 (5.000:5.000) — baseline harus
		# lulus, +2.000 baris test (rasio 1,40) harus gagal. Pohon repo nyata < 5.000 LOC hanya dilaporkan.
		find "$d/cmd" "$d/internal" -name '*.go' -delete
		mkdir -p "$d/internal/metaratio"
		{
			printf 'package metaratio\n\n// F = 5.000 baris kode.\nfunc F() int {\n\tx := 0\n'
			for _ in $(seq 4996); do printf '\tx++\n'; done
			printf '\treturn x\n}\n'
		} >"$d/internal/metaratio/a.go"
		{
			printf 'package metaratio\n\nimport "testing"\n\nfunc TestF(t *testing.T) {\n\tx := 0\n'
			for _ in $(seq 4993); do printf '\tx++\n'; done
			printf '\tif F() != 4996 {\n\t\tt.Fatal(x)\n\t}\n}\n'
		} >"$d/internal/metaratio/a_test.go"
		if [[ $1 == loc-ratio-testonly ]]; then
			# ADR-098: paket berat test di direktori TESTONLY (internal/archtest) tidak masuk rasio →
			# baseline tetap lulus; inject memindahkan isi yang sama ke paket yang TIDAK di-allowlist.
			metaheavy "$d/internal/archtest"
		fi
		;;
	pr-* | adrgate* | spec-guard)
		# Repo git sintetis: commit dasar = HEAD, lalu satu perubahan kontrol yang HARUS lolos cek itu
		# (membuktikan cek tidak memblok perubahan yang sah). Pelanggaran ditambahkan di atasnya.
		git -C "$d" init -q && pr_commit "$d" base
		case $1 in
		pr-title) printf 'catatan\n' >"$d/notes.txt" ;;
		pr-changelog)
			# Kontrol sah: .go non-test + entri CHANGELOG di baris 1 digit (7) dalam bagian Unreleased yang
			# berakhir di baris 2 digit — menangkap perbandingan string nomor baris di awk.
			printf '\n// meta: perubahan non-test\n' >>"$d/internal/ctxdb/errors.go"
			sed -i '6a - meta: entri changelog' "$d/CHANGELOG.md"
			;;
		adrgate) printf '\nmeta: perubahan dokumen biasa\n' >>"$d/docs/ROADMAP.md" ;;
		adrgate-spec) sed -i '/^## 1\. /a meta: perubahan §1, bukan §2/§3.2.4/§17' "$d/SPEC.md" ;;
		pr-size)
			{
				printf 'package metasize\n\nimport "testing"\n\nfunc TestBig(t *testing.T) {\n\tx := 0\n'
				for _ in $(seq 700); do printf '\tx++\n'; done
				printf '\t_ = x\n}\n'
			} | gofile "$d/internal/metasize/big_test.go"
			;;
		spec-guard) printf '\nmeta: laporan\n' >>"$d/docs/F0-REPORT.md" ;;
		esac
		printf 'ci: meta-test baseline\n' >"$d/.meta-pr-title"
		;;
	esac
}

inject() { # <gate> <dir>
	local d=$2
	case $1 in
	lint)
		printf 'package metaviolation\n\n// F melanggar E-23.\nfunc F() { panic("meta") }\n' | gofile "$d/internal/metaviolation/v.go"
		;;
	depguard)
		# AUD-072: rule depguard sqlite (E-04) di luar internal/ctxdb.
		printf 'package metaviolation\n\nimport _ "database/sql" // melanggar E-04\n' | gofile "$d/internal/metaviolation/v.go"
		;;
	lint-anchor)
		# AUD-067: exclusion `^cmd/` tidak boleh meloloskan internal/tools/cmd/.
		printf 'package cmd\n\nimport "os"\n\n// Quit melanggar E-31.\nfunc Quit() { os.Exit(3) }\n' | gofile "$d/internal/tools/cmd/x.go"
		;;
	vet-custom)
		gofile "$d/internal/metaviolation/v.go" <<'EOF'
// Package metaviolation is a synthetic E-05 violation for ci-meta-test.
package metaviolation

import "net/http"

// Client violates E-05: http.Client constructed outside internal/netx.
var Client = &http.Client{}
EOF
		;;
	test)
		printf 'package metaviolation\n\nimport "testing"\n\nfunc TestMetaViolation(t *testing.T) { t.Fatal("pelanggaran sintetis") }\n' |
			gofile "$d/internal/metaviolation/v_test.go"
		;;
	vet-cross)
		# ADR-073: file khusus windows yang tidak compile; build linux tetap hijau, `make vet-cross` gagal.
		printf 'package metaviolation\n\nvar _ = undefinedMetaCross\n' | gofile "$d/internal/metaviolation/v_windows.go"
		;;
	build-matrix)
		# E-39: target android/arm64 hanya ada di build-matrix (vet-cross tidak mencakupnya).
		printf 'package metaviolation\n\nvar _ = undefinedMetaAndroid\n' | gofile "$d/internal/metaviolation/v_android.go"
		;;
	timing)
		# Operasi nyata 300 ms terhadap budget 200 ms lewat helper tunggal internal/testutil/timing
		# (ADR-048, ADR-073): harus gagal di `make test-timing` (di `make test`/-race hanya dilaporkan).
		gofile "$d/internal/metaviolation/timing_test.go" <<'EOF'
package metaviolation

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

func TestMetaTiming(t *testing.T) {
	start := time.Now()
	for time.Since(start) < 300*time.Millisecond { // busy wait: real wall time over budget
	}
	timing.Check(t, "meta tx", time.Since(start), 200*time.Millisecond)
}
EOF
		;;
	race)
		# Lulus tanpa -race, gagal dengan -race: tulis bersamaan tanpa sinkronisasi.
		gofile "$d/internal/metaviolation/race_test.go" <<'EOF'
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
	fuzz)
		gofile "$d/internal/metaviolation/fuzz_test.go" <<'EOF'
package metaviolation

import "testing"

func FuzzMeta(f *testing.F) {
	f.Add("ok")
	f.Fuzz(func(t *testing.T, s string) {
		if s != "ok" {
			t.Fatalf("meta crash on %q", s)
		}
	})
}
EOF
		;;
	coverage)
		gofile "$d/internal/metaviolation/v.go" <<'EOF'
package metaviolation

// Uncovered is never called by a test (E-58 violation).
func Uncovered(n int) int {
	s := 0
	for i := range n {
		s += i
	}
	return s
}
EOF
		;;
	deadcode)
		gofile "$d/internal/metaviolation/v.go" <<'EOF'
// Package metaviolation is a synthetic E-53 violation.
package metaviolation

// Live is called by TestLive.
func Live() int { return 1 }

// Dead is never called (E-53 violation).
func Dead() int { return 2 }
EOF
		gofile "$d/internal/metaviolation/v_test.go" <<'EOF'
package metaviolation

import "testing"

func TestLive(t *testing.T) {
	if Live() != 1 {
		t.Fatal("Live")
	}
}
EOF
		;;
	flake)
		# Lulus pada run pertama, gagal pada run berikutnya di proses yang sama: hanya -count=20 menangkapnya.
		gofile "$d/internal/metaviolation/flake_test.go" <<'EOF'
package metaviolation

import "testing"

var runs int //nolint:gochecknoglobals // meta-test: state lintas run -count

func TestMetaFlake(t *testing.T) {
	if runs++; runs > 1 {
		t.Fatalf("flaky on run %d", runs)
	}
}
EOF
		;;
	perf)
		# Benchmark ber-tag perf (NEW-38) 3 ms/op terhadap budget 1 ms (×1,5 = 1,5 ms, ADR-077).
		gofile "$d/internal/metaviolation/perf_test.go" <<'EOF'
//go:build perf

package metaviolation

import (
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

func BenchmarkClassify(b *testing.B) {
	start := time.Now()
	for b.Loop() {
		for t := time.Now(); time.Since(t) < 3*time.Millisecond; { // busy wait: real wall time
		}
	}
	timing.Check(b, "meta perf", time.Since(start)/time.Duration(b.N), time.Millisecond)
}
EOF
		;;
	size-check)
		# Embed 53 MB acak ke binary → full > 52 MB.
		head -c 53000000 /dev/urandom >"$d/cmd/damnbot/meta.bin"
		gofile "$d/cmd/damnbot/zz_meta_blob.go" <<'EOF'
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
		{
			printf 'package boundary\n\n// Meta melanggar E-19.\nfunc Meta() int {\n\tx := 0\n'
			for _ in $(seq 200); do printf '\tx++\n'; done
			printf '\treturn x\n}\n'
		} | gofile "$d/internal/boundary/meta.go"
		;;
	loc-unmapped)
		# AUD-073: direktori internal/* tanpa baris loc.budget.
		printf 'package metaunmapped\n\n// F tidak terpetakan.\nfunc F() int { return 1 }\n' | gofile "$d/internal/metaunmapped/f.go"
		;;
	loc-ratio-testonly)
		rm -rf "$d/internal/archtest/meta_heavy.go" "$d/internal/archtest/meta_heavy_test.go"
		metaheavy "$d/internal/metaheavy"
		;;
	loc-ratio)
		{
			printf 'package metaratio\n\nimport "testing"\n\nfunc TestMetaRatio(t *testing.T) {\n\tx := 0\n'
			for _ in $(seq 2000); do printf '\tx++\n'; done
			printf '\t_ = x\n}\n'
		} | gofile "$d/internal/metaratio/zz_meta_ratio_test.go"
		;;
	deps-check)
		# Require langsung di luar deps.allow (testify ditolak E-66).
		printf 'package metaviolation\n\nimport _ "github.com/stretchr/testify/assert"\n' | gofile "$d/internal/metaviolation/v_test.go"
		(cd "$d" && go get github.com/stretchr/testify@v1.9.0 >/dev/null 2>&1 && go mod tidy >/dev/null 2>&1)
		;;
	vuln)
		# gopkg.in/yaml.v2 v2.2.2: kerentanan DoS yang terjangkau lewat yaml.Unmarshal.
		printf 'package metaviolation\n\nimport "gopkg.in/yaml.v2"\n\n// F memanggil API rentan.\nfunc F(b []byte) error { var v any; return yaml.Unmarshal(b, &v) }\n' |
			gofile "$d/internal/metaviolation/v.go"
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
		# Require modul lokal yang tidak diimpor -> `go mod tidy -diff` harus
		# menampilkan diff (E-46). Modul ini sengaja TIDAK ada di go.mod asli,
		# sehingga gate tidak bisa lolos hanya karena dependency nyata.
		gofile "$d/metatidy/meta.go" <<'GOEOF'
package metatidy

// V is unused: the synthetic require must be removed by tidy.
const V = "meta"
GOEOF
		printf 'module example.com/meta/tidy\n\ngo 1.26\n' >"$d/metatidy/go.mod"
		(cd "$d" && go mod edit -require=example.com/meta/tidy@v0.0.0 -replace=example.com/meta/tidy=./metatidy)
		;;
	mod-verify)
		# Rusak satu file modul di cache terisolasi → `go mod verify` harus gagal (E-47).
		(cd "$d" && GOMODCACHE="$work/modcache" GOFLAGS=-modcacherw go mod download >/dev/null 2>&1)
		local f
		f=$(find "$work/modcache/go.uber.org/goleak@v1.3.0" -name '*.go' | sort | head -1)
		chmod u+w "$f"
		printf '\n// meta: dirusak\n' >>"$f"
		;;
	bench-budget)
		# NEW2-07: job bench-budget = seluruh test modul bench.
		printf 'package metabench\n\nimport "testing"\n\nfunc TestMetaBench(t *testing.T) { t.Fatal("pelanggaran sintetis") }\n' |
			gofile "$d/bench/metabench/v_test.go"
		;;
	sqlite-engine)
		# REL-04: an engine older than MinSQLiteVersion must fail the probe.
		sed -i 's/MinSQLiteVersion = "3\.51\.3"/MinSQLiteVersion = "99.0.0"/' "$d/internal/ctxdb/engine.go"
		;;
	skill-refs)
		# GAP-SEC-26: a builtin skill with a dangling requires must fail the rule.
		mkdir -p "$d/skills/meta"
		cat >"$d/skills/meta/SKILL.md" <<'EOF'
---
name: meta-skill
description: meta dangling reference
requires: [missing-skill]
---
Body text for the meta fixture.
EOF
		;;
	phase-file)
		printf 'F10\n' >"$d/PHASE"
		;;
	phase-acceptance)
		# Item fase F1 masih pending: lulus di gate PR (PHASE = F1), gagal di acceptance fase (ADR-080).
		gofile "$d/internal/metaviolation/phase_test.go" <<'EOF'
package metaviolation

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

func TestMetaPhase(t *testing.T) {
	phase.Current(t).Item(t, "META-1", "F1", "pending:F1")
}
EOF
		;;
	arch-importgraph)
		# E-02: loop hanya boleh mengimpor loop/event, boundary, stdlib.
		printf 'package loop\n\nimport "github.com/damnbot/damnbot/internal/ctxdb"\n\n// ErrMeta melanggar E-02.\nvar ErrMeta = ctxdb.ErrDBBusy\n' |
			gofile "$d/internal/loop/meta.go"
		;;
	arch-filelength)
		{
			printf 'package metaviolation\n\n'
			for _ in $(seq 799); do printf '// x\n'; done
		} | gofile "$d/internal/metaviolation/long.go"
		;;
	arch-globals)
		printf 'package metaviolation\n\n// Meta melanggar E-10.\nvar Meta = map[string]int{} //nolint:gochecknoglobals // meta violation\n' |
			gofile "$d/internal/metaviolation/g.go"
		;;
	arch-goleak)
		printf 'package metaviolation\n\n// F meluncurkan goroutine tanpa goleak.\nfunc F() { go func() {}() }\n' | gofile "$d/internal/metaviolation/g.go"
		;;
	arch-budgetsum)
		printf 'metabloat         20000 internal/metabloat\n' >>"$d/loc.budget"
		;;
	arch-codeowners)
		printf 'package metaviolation\n' | gofile "$d/internal/metaviolation/doc.go"
		;;
	arch-nolint)
		printf 'package metaviolation\n\n// F melanggar E-52.\nfunc F() {} //nolint\n' | gofile "$d/internal/metaviolation/n.go"
		;;
	arch-workflows)
		printf 'name: meta\non: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n' \
			>"$d/.github/workflows/meta-violation.yml"
		;;
	arch-mpl)
		# Modul lokal MPL-2.0 di luar deps.mpl yang di-link ke cmd/damnbot (E-40, AUD-069).
		mkdir -p "$d/metampl"
		cp /usr/share/common-licenses/MPL-2.0 "$d/metampl/LICENSE"
		printf 'module example.com/metampl\n\ngo 1.26\n' >"$d/metampl/go.mod"
		printf 'package metampl\n\n// V dipakai cmd/damnbot.\nconst V = "mpl"\n' >"$d/metampl/metampl.go"
		printf 'package main\n\nimport "example.com/metampl"\n\nvar _ = metampl.V\n' >"$d/cmd/damnbot/zz_meta_mpl.go"
		(cd "$d" && go mod edit -require=example.com/metampl@v0.0.0 -replace=example.com/metampl=./metampl)
		;;
	pr-title)
		printf 'Update stuff\n' >"$d/.meta-pr-title"
		;;
	pr-changelog)
		# Perubahan .go yang sama tanpa entri CHANGELOG.
		git -C "$d" checkout -q -- CHANGELOG.md
		;;
	adrgate)
		printf '# meta: perubahan allowlist tanpa ADR\n' >>"$d/deps.allow"
		;;
	adrgate-spec)
		sed -i '/^#### 3\.2\.4 /a meta: perubahan budget tanpa ADR' "$d/SPEC.md"
		;;
	pr-size)
		{
			printf 'package metasize\n\n// Big melanggar E-84.\nfunc Big() int {\n\tx := 0\n'
			for _ in $(seq 700); do printf '\tx++\n'; done
			printf '\treturn x\n}\n'
		} | gofile "$d/internal/metasize/big.go"
		;;
	spec-guard)
		printf '\nmeta: perubahan spec tanpa ADR\n' >>"$d/docs/ROADMAP.md"
		;;
	esac
}

run_gate() { # <gate> <dir> <log>
	local g=$1 d=$2 t
	case $g in
	mod-verify)
		# Cache modul terisolasi di $work (pelanggaran = cache yang dirusak; ~/go tidak disentuh).
		(cd "$d" && GOMODCACHE="$work/modcache" GOFLAGS=-modcacherw make -s mod-verify MODULES=.) >"$3" 2>&1
		return
		;;
	pr-* | adrgate* | spec-guard)
		# Satu commit "PR" di atas commit dasar; hanya cek gate ini yang dijalankan.
		local check=${g#pr-}
		[[ $g == adrgate-spec ]] && check=adrgate
		t=$(cat "$d/.meta-pr-title")
		rm -f "$d/.meta-pr-title"
		pr_commit "$d" pr
		(cd "$d" && BASE=$(git rev-list --max-parents=0 HEAD) PR_TITLE="$t" PR_BODY="meta-test" PR_LABELS="" \
			PR_APPROVALS=0 scripts/pr-meta.sh "$check") >"$3" 2>&1
		return
		;;
	esac
	# shellcheck disable=SC2046 # target() mengembalikan "<target> [VAR=…]"
	(cd "$d" && make -s $(target "$g")) >"$3" 2>&1
}

declare -A result
fail=0
# scaffold <dir>: buang paket internal/* selain archtest/testutil/metaviolation. Dipakai HANYA bila
# baseline HEAD merah (kode paket lain melanggar gate): mekanisme gate tetap diuji pada pohon yang
# bersih, dan status HEAD yang merah tetap dilaporkan (HEAD_RED) — gate nyata tidak dilemahkan.
scaffold() {
	find "$1/internal" -mindepth 1 -maxdepth 1 -type d ! -name archtest ! -name testutil ! -name metaviolation -exec rm -rf {} +
}

for g in "${GATES[@]}"; do
	base="$work/$g-base" viol="$work/$g-viol" note=""
	if [[ -z $(marker "$g") ]]; then
		result[$g]="UNKNOWN (gate tidak dikenal)"
		fail=1
		continue
	fi
	fresh "$base"
	prepare "$g" "$base"
	if ! run_gate "$g" "$base" "$work/$g-base.log"; then
		tail -3 "$work/$g-base.log" | sed "s/^/  [$g HEAD] /"
		if [[ $g == pr-* || $g == adrgate* || $g == spec-guard ]]; then
			result[$g]="BASELINE_RED (perubahan kontrol ditolak; lihat log)"
			fail=1
			rm -rf "$base"
			continue
		fi
		fresh "$base"
		prepare "$g" "$base"
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
for g in "${GATES[@]}"; do printf '%-17s %s\n' "$g" "${result[$g]}"; done
exit $fail
