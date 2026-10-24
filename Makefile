# Damn-Bot — titik masuk build/gate (ENGINEERING §5, E-39). Setiap job CI memanggil target di sini
# (GAP-INF-05), sehingga `make meta-test` menguji perintah yang sama dengan CI.
# Tag build: satu daftar TAGS; target lain hanya menambah (TAGS += …). Tidak pernah lewat GOFLAGS
# (pengecualian: go-licenses tidak punya flag -tags, lihat target licenses).
TAGS ?= goolm
# Paket yang diuji gate test/race/timing/fuzz/coverage/flake/perf; meta-test memakai pohon minimal
# ./internal/metaviolation/... (ADR-073).
PKGS ?= ./...

empty :=
space := $(empty) $(empty)
comma := ,
TAGSCSV = $(subst $(space),$(comma),$(strip $(TAGS)))
GOTAGS = -tags "$(TAGSCSV)"

# Tool dev dipin di tools/go.mod (E-45: di luar budget go.mod utama). Tool yang dijalankan dengan
# GOOS lain (deadcode, vettool) dibangun native ke .bin/ dulu.
GOTOOL := go tool -modfile=tools/go.mod
BIN := $(CURDIR)/.bin
LICENSES_ALLOWED := MIT,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,MPL-2.0
# E-39: 8 target rilis kanonik.
TARGETS := linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 android/arm64
# Job perf (ADR-073, ADR-077, NEW-38): benchmark dengan budget waktu absolut.
PERF_BENCH ?= ^(BenchmarkShellSpawn|BenchmarkShellSessionExec|BenchmarkClassify|BenchmarkIterationOverhead|BenchmarkCodetools|BenchmarkAppend|BenchmarkDLPScan|BenchmarkSummarize)
FUZZTIME ?= 60s
SHARD ?= 1/1
# Korpus code tools + oracle rg (CODE-TOOLS §10.1, bench/corpora.lock), dibangun `make corpora`.
CORPORA_DIR ?= /tmp/damnbot-corpora
CORPORA ?= C1 C2 C3 rg
CORPUS_ENV = DAMNBOT_REQUIRE_CORPORA=1 DAMNBOT_RG=$(CORPORA_DIR)/rg DAMNBOT_CORPUS_C1=$(CORPORA_DIR)/C1 \
	DAMNBOT_CORPUS_C3=$(CORPORA_DIR)/C3 $(if $(filter C2,$(CORPORA)),DAMNBOT_CORPUS_C2=$(CORPORA_DIR)/C2)

.PHONY: all build build-matrix lint vet-custom test test-timing archtest vet-cross size-check loc loc-ratio \
	deps-check tidy-check mod-verify vuln licenses check meta-test bench-budget fuzz coverage deadcode \
	flake perf ct09-nightly pr-meta phase-acceptance corpora bench-parity bench-codetools

all: check

build:
	CGO_ENABLED=0 go build $(GOTAGS) ./...

# E-39/E-65: CGO_ENABLED=0 untuk 8 target kanonik.
build-matrix:
	@set -e; for p in $(TARGETS); do \
		echo "go build GOOS=$${p%/*} GOARCH=$${p#*/}"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build $(GOTAGS) ./...; \
	done

lint:
	$(GOTOOL) golangci-lint run --build-tags "$(TAGSCSV)" ./...

# Analyzer khusus (ENGINEERING §5.1; kode di tools/analyzers, ADR-094): test analysistest + go vet -vettool.
vet-custom:
	cd tools && go test -count=1 ./analyzers/...
	cd tools && go build -o $(BIN)/damnbot-vet ./analyzers/cmd/damnbot-vet
	go vet $(GOTAGS) -vettool=$(BIN)/damnbot-vet ./...

# RACE kosong hanya untuk target tanpa race detector Go (windows/arm64, matrix E-55; ADR-094).
RACE ?= -race
# ADR-115: satu binary paket berbagi satu -timeout; TestScenario (86 skenario
# serial, ~172 s di runner sehat; 447 s di runner lambat) dan sisa paket
# saling memakan anggaran 10 menit. Dua invokasi dengan flag dan -timeout
# identik: sisa paket (tanpa TestScenario) dan skenario saja. Bukan skip —
# test yang sama berjalan di invokasi kedua; hanya anggaran waktunya yang
# terpisah.
TEST_TIMEOUT ?= 10m
SCENARIO_PKGS ?= ./internal/ctxdb
test:
	go test $(GOTAGS) $(RACE) -shuffle=on -timeout $(TEST_TIMEOUT) -skip '^TestScenario$$' $(PKGS)
ifeq ($(PKGS),./...)
	go test $(GOTAGS) $(RACE) -shuffle=on -timeout $(TEST_TIMEOUT) -run '^TestScenario$$' $(SCENARIO_PKGS)
endif

# ADR-048 (TS-P08): asersi budget waktu dinding (I11 ≤ 200 ms/txn dll.) hanya ditegakkan di build
# tanpa -race, lewat satu helper internal/testutil/timing (ADR-073, NEW-35). Suite yang sama juga
# berjalan di `make test` (-race) dengan semua cek kebenaran; di sana hanya asersi waktunya yang
# dilewati. ADR-073: benchmark ikut dijalankan sekali (-benchtime=1x) agar benchmark yang memakai
# helper budget tidak lulus kosong; budget absolut benchmark ditegakkan di job `perf`.
test-timing:
	DAMNBOT_TIMING=1 go test $(GOTAGS) -count=1 -shuffle=on -bench=. -benchtime=1x $(PKGS)

# Gate arsitektur (ADR-073 AUD-032, NEW-36): dijalankan juga di `make test`; target ini untuk meta-test.
archtest:
	go test $(GOTAGS) -count=1 ./internal/archtest

# ADR-073 (AUD-033): go vet lintas OS/arch (murah; matrix test menjalankan suite di OS nyata).
VET_CROSS := windows/amd64 windows/arm64 darwin/amd64 darwin/arm64
vet-cross:
	@set -e; for p in $(VET_CROSS); do \
		echo "go vet GOOS=$${p%/*} GOARCH=$${p#*/}"; \
		GOOS=$${p%/*} GOARCH=$${p#*/} go vet $(GOTAGS) ./...; \
	done

# E-57: setiap target Fuzz* FUZZTIME, SHARD=i/n.
fuzz:
	TAGS="$(TAGSCSV)" PKGS="$(PKGS)" FUZZTIME="$(FUZZTIME)" SHARD="$(SHARD)" scripts/fuzz.sh

# E-58 (+ AUD-059): coverage per paket internal/.
COVERPKG ?= ./internal/...
coverage:
	TAGS="$(TAGSCSV)" PKGS="$(PKGS)" COVERPKG="$(COVERPKG)" scripts/coverage.sh

# E-53: deadcode -test, GOOS linux/darwin/windows digabung.
deadcode:
	go build -modfile=tools/go.mod -o $(BIN)/deadcode golang.org/x/tools/cmd/deadcode
	TAGS="$(TAGSCSV)" PKGS="$(PKGS)" DEADCODE=$(BIN)/deadcode scripts/deadcode.sh

# ADR-073: deteksi flake nightly, -count=20 (tanpa -race: 20× suite -race melebihi budget menit CI,
# ADR-094; race tetap di `make test` setiap PR).
flake:
	go test $(GOTAGS) -count=20 -shuffle=on $(PKGS)

# ADR-073/077, NEW-38: budget waktu absolut benchmark, tag perf (helper timing ×1,5 di runner bersama).
perf: TAGS += perf
perf:
	DAMNBOT_TIMING=1 go test $(GOTAGS) -run '^(TestMeasureDurableRetryMeasuresSet2)$$' -bench '$(PERF_BENCH)' -benchtime=100x $(PKGS)

# ADR-127: backstop CT-09 nightly di main — 5 set sampel, dan run yang seluruh
# set-nya invalid (runner berdisk bising) GAGAL di sini, bukan lulus dengan
# warning seperti gate PR.
CT09_SETS ?= 5
ct09-nightly: TAGS += perf
ct09-nightly:
	DAMNBOT_TIMING=1 DAMNBOT_CT09_SETS=$(CT09_SETS) DAMNBOT_CT09_STRICT_INCONCLUSIVE=1 \
		go test $(GOTAGS) -run '^$$' -bench '^BenchmarkCodetools/Edit1MB$$' -benchtime=100x ./internal/codetools/

# GAP-REL-03: 50-client chaos untuk window nightly penuh (PR memakai default singkat).
# `-timeout 40m` wajib: sebelum PR ini target memakai window 30 m dengan batas default
# `go test` 10 m, sehingga job nightly `chaos` mati karena TIMEOUT (600.093 s) dan
# window 30 m-nya tidak pernah benar-benar dieksekusi.
chaos-nightly:
	DAMNBOT_CHAOS_DURATION=30m go test $(GOTAGS) -timeout 40m -run '^TestChaos50Clients$$' -count=1 ./internal/ctxdb/
# Varian jendela panjang (>= 3x TTL) SENGAJA belum dijalankan: jendela 45 s
# mereproduksi `db_busy: database is locked (5) (SQLITE_BUSY)` di main (2/2 di host
# sepi), yaitu invarian yang justru dilindungi GAP-REL-03. Tesnya tetap ada dan
# terlihat; komentari di sini adalah penahan eksplisit, bukan skip - kembalikan
# `DAMNBOT_CHAOS_LONG=1` pada baris di bawah begitu policy busy diperbaiki.
#	DAMNBOT_CHAOS_LONG=1 go test $(GOTAGS) -timeout 10m -run '^TestChaos50ClientsAcrossLeaseTTL$$' -count=1 ./internal/ctxdb/

# ENGINEERING §7.2 (E-80..E-84) + spec-guard SPEC §20; env BASE/HEAD/PR_* diisi workflow pr-meta.
pr-meta:
	scripts/pr-meta.sh

# ADR-080 butir 2: semua item fase ≤ PHASE wajib covered (dijalankan saat file PHASE berubah).
phase-acceptance:
	DAMNBOT_PHASE_ACCEPTANCE=1 go test $(GOTAGS) -count=1 $(PKGS)

size-check:
	TAGS="$(TAGSCSV)" scripts/size-check.sh

loc:
	SCC="$(GOTOOL) scc" scripts/loc-check.sh budget

loc-ratio:
	SCC="$(GOTOOL) scc" scripts/loc-check.sh ratio

deps-check:
	go test $(GOTAGS) -count=1 -run 'TestDirectDeps|TestDirectRequiresParser' ./internal/archtest

# AUD-074(a): modul utama, tools, dan bench sama-sama di-vuln/tidy/verify.
vuln:
	$(GOTOOL) govulncheck $(GOTAGS) ./...
	cd tools && go tool govulncheck ./...
	cd bench && go tool -modfile=../tools/go.mod govulncheck ./...

# go-licenses tidak punya flag -tags; satu-satunya jalur adalah GOFLAGS (tanpa -tags lain di command line,
# jadi tidak ada penimpaan E-39). TestMPLIsolation (E-40, AUD-069): modul MPL ⊆ deps.mpl.
licenses:
	GOFLAGS=-tags=$(TAGSCSV) $(GOTOOL) go-licenses check ./... --allowed_licenses=$(LICENSES_ALLOWED)
	go test -tags "$(TAGSCSV),licenses" -count=1 -run TestMPLIsolation ./internal/archtest

# E-46: go.mod/go.sum sudah tidy (go mod tidy -diff: exit ≠ 0 + diff bila tidak), ketiga modul.
tidy-check:
	go mod tidy -diff
	cd tools && go mod tidy -diff
	cd bench && go mod tidy -diff

# E-47: integritas modul di cache cocok dengan go.sum, ketiga modul (meta-test: MODULES=. dengan cache
# terisolasi).
MODULES ?= . tools bench
mod-verify:
	@set -e; for m in $(MODULES); do echo "go mod verify ($$m)"; (cd $$m && go mod verify); done

check: lint vet-custom test test-timing vet-cross build-matrix size-check loc loc-ratio deps-check tidy-check \
	mod-verify vuln licenses deadcode coverage

# Meta-test gate CI (ROADMAP F0/F1 #0): setiap gate harus gagal pada pelanggaran sintetis.
meta-test:
	scripts/ci-meta-test.sh

# Modul bench (bench/go.mod terpisah, di luar E-45), NEW2-07: seluruh test modul bench, termasuk
# harness token P1 (tokenizer o200k_base = tiktoken, reproduksi BASELINE-hermes.md ±5%).
bench-budget:
	cd bench && go test -count=1 ./...

corpora:
	CORPORA_DIR=$(CORPORA_DIR) CORPORA="$(CORPORA)" scripts/corpora.sh

# CT-21 parity vs rg di C1+C3 (job PR bench-parity). Korpus wajib ada: tanpa korpus test GAGAL,
# bukan skip (DAMNBOT_REQUIRE_CORPORA=1).
bench-parity: corpora
	$(CORPUS_ENV) go test $(GOTAGS) -count=1 -timeout 0 -run '^TestGrepParityRg$$' ./internal/codetools

# CODE-TOOLS §10 dengan korpus nyata (job nightly bench-codetools; RC = gate): CT-01/02/03 vs rg
# (TestGrepSpeedVsRg, 20 run), CT-21 parity, CT-11 token vs golden hermes (modul bench).
bench-codetools: corpora
	$(CORPUS_ENV) go test $(GOTAGS) -count=1 -timeout 0 -run '^(TestGrepParityRg|TestGrepSpeedVsRg)$$' ./internal/codetools
	cd bench && $(CORPUS_ENV) go test -count=1 -timeout 0 -run '^TestGrepOutputTokens$$' ./codetools
