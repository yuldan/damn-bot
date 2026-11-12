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
PERF_BENCH ?= ^(BenchmarkShellSpawn|BenchmarkShellSessionExec|BenchmarkClassify|BenchmarkIterationOverhead|BenchmarkCodetools|BenchmarkAppend|BenchmarkDLPScan|BenchmarkSummarize|BenchmarkWebFirstToken)
# Paket pemilik benchmark PERF_BENCH (ADR-270). Paket lain tidak memberi benchmark ke gate ini, tetapi
# TestMain-nya bisa berat (internal/e2e 33–43 s di runner) dan dulu berjalan di antara benchmark.
PERF_PKGS ?= ./internal/codetools ./internal/exec ./internal/exec/classify ./internal/ctxdb ./internal/netx ./internal/web
FUZZTIME ?= 60s
SHARD ?= 1/1
# Korpus code tools + oracle rg (CODE-TOOLS §10.1, bench/corpora.lock), dibangun `make corpora`.
CORPORA_DIR ?= /tmp/damnbot-corpora
CORPORA ?= C1 C2 C3 rg
CORPUS_ENV = DAMNBOT_REQUIRE_CORPORA=1 DAMNBOT_RG=$(CORPORA_DIR)/rg DAMNBOT_CORPUS_C1=$(CORPORA_DIR)/C1 \
	DAMNBOT_CORPUS_C3=$(CORPORA_DIR)/C3 $(if $(filter C2,$(CORPORA)),DAMNBOT_CORPUS_C2=$(CORPORA_DIR)/C2)

.PHONY: all build build-minimal-all build-matrix lint vet-custom test test-windows-evidence test-timing archtest vet-cross size-check loc loc-ratio chaos \
	deps-check tidy-check mod-verify vuln licenses release-test check meta-test bench-budget fuzz coverage deadcode \
	flake perf perf-f4 ct09-nightly pr-meta phase-acceptance corpora bench-parity bench-codetools \
	sqlite-engine sqlite-engine-cross skill-refs prices-gate e2e-docker image-test release ci-required ci-required-test deadcode-linux agent-guard-test

all: check

build:
	CGO_ENABLED=0 go build $(GOTAGS) ./...

# E-39: tag `minimal` wajib mengompilasi SELURUH paket, bukan hanya `cmd/damnbot` (yang dibangun
# `size-check`). Paket yang memakai simbol full-only tanpa `//go:build !minimal` dulu lolos gate ukuran.
build-minimal-all:
	CGO_ENABLED=0 go build -tags "$(TAGSCSV),minimal" ./...

# E-39/E-65: CGO_ENABLED=0 untuk 8 target kanonik.
build-matrix:
	@set -e; for p in $(TARGETS); do \
		echo "go build GOOS=$${p%/*} GOARCH=$${p#*/}"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build $(GOTAGS) ./...; \
	done

# GAP-REL-04 (ADR-132/ADR-144): the embedded SQLite engine must satisfy REL-04
# on every E-39 target. Targets with a hosted runner run the probe; the two
# without one (linux/arm, android/arm64) cross-compile the test binary until
# QEMU execution lands at F9 (ADR-073).
sqlite-engine:
	CGO_ENABLED=0 go test $(GOTAGS) -count=1 -run '^TestSQLiteEngineVersion$$' ./internal/ctxdb

sqlite-engine-cross:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go test $(GOTAGS) -c -o $(BIN)/sqlite-engine-$(GOOS)-$(GOARCH).test ./internal/ctxdb

# GAP-SEC-26 (ADR-133/ADR-144): builtin skills carry no dangling
# requires/skill:// links or script paths.
skill-refs:
	go test $(GOTAGS) -count=1 -run '^TestBuiltinSkillRefs$$' ./internal/skills

# GAP-COST-01 (ADR-125 butir 4): the embedded registry must not ship a price
# whose Until has passed; an unpriced route later fails a monetary cap closed.
# The failure names every stale entry (version, base_url, model, until).
prices-gate:
	go test $(GOTAGS) -count=1 -run '^TestPricingTableNotExpired$$' ./internal/provider/registry

# GAP-INF-03/OPS-030 (ADR-033 Q49): flake Nix untuk paket default (CGO=0, `go build -tags goolm`)
# dan devShell. Parse statis tidak membutuhkan Nix; `nix flake check` dijalankan sebagai bukti bila
# binary Nix tersedia (job CI `nix`). Target ini tidak menambah dependency Go.
nix-check:
	go test $(GOTAGS) -count=1 -run '^TestNixFlakeParse$$' ./internal/archtest
	@if command -v nix >/dev/null 2>&1; then nix flake check; else echo "nix-check: nix tidak tersedia; parse statis lulus"; fi

# Docker-positive execute_code e2e (F5 slice 5). The workflow builds the
# fixture image and exports DAMNBOT_E2E_DOCKER_IMAGE; the test is compiled
# only under e2e_docker and stays skipOrFail-hard when CI has the image.
e2e-docker:
	go test -tags "$(TAGSCSV),e2e_docker" -count=1 -timeout 10m -run '^TestExecuteCodeDockerPositive$$' ./internal/e2e

# GAP-INF-03/GAP-SEC-22 (ADR-033 Q35/Q49): build the shipped Dockerfile and measure the image
# (size ≤ binary + 2 MB, UID 65532, no shell/compiler, healthcheck). Requires a Docker daemon;
# DAMNBOT_REQUIRE_DOCKER=1 makes a missing daemon fail instead of skip, so the CI job is a gate.
image-test:
	DAMNBOT_REQUIRE_DOCKER=1 go test -tags "$(TAGSCSV),e2e_docker" -count=1 -timeout 15m -run '^TestDockerImageSize$$' ./internal/archtest

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
# saling memakan anggaran. Dua invokasi dengan flag dan -timeout
# identik: sisa paket (tanpa TestScenario) dan skenario saja. Bukan skip —
# test yang sama berjalan di invokasi kedua; hanya anggaran waktunya yang
# terpisah.
# ADR-229: anggaran 10m → 20m sebagai hygiene runner, bukan pelonggaran gate.
# Paket terberat (cmd/damnbot, internal/ctxdb) terukur 57–99,4% dari 600 s di
# run hijau dan sudah panic 600 s di windows-latest/macos-15-intel; variasi
# 1,7× pada SHA yang sama. Tidak ada tes, asersi, -race, atau cakupan berubah.
# ADR-324 amandemen: default 20m untuk lokal/Linux; job `test` di CI mengekspor
# TEST_TIMEOUT=30m untuk runner macOS (variabel environment menimpa `?=`).
TEST_TIMEOUT ?= 20m
SCENARIO_PKGS ?= ./internal/ctxdb
test:
	go test $(GOTAGS) $(RACE) -shuffle=on -timeout $(TEST_TIMEOUT) -skip '^TestScenario$$' $(PKGS)
ifeq ($(PKGS),./...)
	go test $(GOTAGS) $(RACE) -shuffle=on -timeout $(TEST_TIMEOUT) -run '^TestScenario$$' $(SCENARIO_PKGS)
endif

# Bukti bernama untuk acceptance ledger (ADR-165; amandemen ADR-324): `make test` hanya mencetak
# ringkasan paket (`ok <pkg> <durasi>`), sehingga log Windows tidak membuktikan tes bernama dijalankan
# dan bukan skip. `scripts/windows-test-evidence.sh` menjalankan kelima tes, mencetak namanya, dan
# GAGAL bila ada yang hilang/SKIP. Bukan gate suite; `make test` tetap gate, `RACE` ikut matrix.
test-windows-evidence:
	TAGSCSV="$(TAGSCSV)" RACE="$(RACE)" scripts/windows-test-evidence.sh

# ADR-048 (TS-P08): asersi budget waktu dinding (I11 ≤ 200 ms/txn dll.) hanya ditegakkan di build
# tanpa -race, lewat satu helper internal/testutil/timing (ADR-073, NEW-35). Suite yang sama juga
# berjalan di `make test` (-race) dengan semua cek kebenaran; di sana hanya asersi waktunya yang
# dilewati. ADR-073: benchmark ikut dijalankan sekali (-benchtime=1x) agar benchmark yang memakai
# helper budget tidak lulus kosong; budget absolut benchmark ditegakkan di job `perf`.
test-timing:
	DAMNBOT_TIMING=1 go test $(GOTAGS) -count=1 -shuffle=on -timeout $(TEST_TIMEOUT) -bench=. -benchtime=1x $(PKGS)

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

# ADR-324 amandemen: gate E-53 murah untuk SETIAP tier yang mengubah kode (termasuk PR), hanya
# GOOS linux; gate 3-GOOS penuh tetap target `deadcode` di tier `full`.
deadcode-linux:
	go build -modfile=tools/go.mod -o $(BIN)/deadcode golang.org/x/tools/cmd/deadcode
	DEADCODE_GOOS=linux TAGS="$(TAGSCSV)" PKGS="$(PKGS)" DEADCODE=$(BIN)/deadcode scripts/deadcode.sh

# ADR-073: deteksi flake nightly, -count=20 (tanpa -race: 20× suite -race melebihi budget menit CI,
# ADR-094; race tetap di `make test` setiap PR).
# -timeout go test berlaku per binary paket dan bawaannya 10 menit tanpa peduli -count: 20 putaran
# paket lambat melewatinya dengan run yang sehat (internal/exec: corpus P24.2 ~68 s/putaran,
# internal/ctxdb ~45 s/putaran), sehingga setiap malam job panic "test timed out" dan hasil kedua
# paket itu hilang — flake tidak pernah terdeteksi. Putaran dipecah menjadi FLAKE_WAVES gelombang
# dengan batas waktu tetap 10 menit: total putaran per test tetap FLAKE_COUNT, dan hang sungguhan
# tetap dilaporkan dengan stack dalam ≤ 10 menit. Satu gelombang -count=2 memakan ≤ 3 menit per
# paket terukur (internal/exec, 4 vCPU, 2026-10-21), jadi timeout 10 menit tetap >3× jarak. Butuh
# FLAKE_COUNT habis dibagi FLAKE_WAVES.
FLAKE_COUNT ?= 20
FLAKE_WAVES ?= 10
flake:
	@set -e; \
	if [ $$(( $(FLAKE_COUNT) % $(FLAKE_WAVES) )) -ne 0 ]; then \
		echo "FLAKE_COUNT ($(FLAKE_COUNT)) harus habis dibagi FLAKE_WAVES ($(FLAKE_WAVES))" >&2; \
		exit 2; \
	fi; \
	n=$$(( $(FLAKE_COUNT) / $(FLAKE_WAVES) )); \
	for w in $$(seq 1 $(FLAKE_WAVES)); do \
		echo "flake wave $$w/$(FLAKE_WAVES): -count=$$n -shuffle=on"; \
		go test $(GOTAGS) -count=$$n -shuffle=on $(PKGS); \
	done

# ADR-073/077, NEW-38, ADR-270: budget waktu absolut benchmark, tag perf (helper timing ×1,5 di runner
# bersama). Semua test binary dibangun lebih dulu, lalu tiap paket pemilik benchmark dijalankan serial
# (-p 1 -parallel 1) sehingga tidak ada kompilasi atau paket lain di jendela ukur. Ambang, -benchtime,
# dan warmup di dalam benchmark tidak berubah.
perf: TAGS += perf
perf:
	@set -e; for p in $(PERF_PKGS); do \
		echo "perf build $$p"; \
		go test -c -o /dev/null $(GOTAGS) $$p; \
	done
	@rc=0; for p in $(PERF_PKGS); do \
		echo "perf $$p (serial)"; \
		DAMNBOT_TIMING=1 go test $(GOTAGS) -p 1 -parallel 1 -run '^(TestMeasureDurableRetryMeasuresSet2)$$' \
			-bench '$(PERF_BENCH)' -benchtime=100x $$p || rc=1; \
	done; \
	exit $$rc

# Acceptance performa F2 (SPEC §11 append p99, §11.6 recall p95 @1 juta event).
# Skala penuh, tanpa pengecilan dataset; ambang absolut memakai margin ×1,5
# runner bersama lewat timing.Check (ADR-077) dan DAMNBOT_TIMING=1.
PERF_RECALL_EVENTS ?= 1000000
perf-acceptance: TAGS += perf
perf-acceptance:
	DAMNBOT_TIMING=1 DAMNBOT_RECALL_EVENTS=$(PERF_RECALL_EVENTS) go test $(GOTAGS) -count=1 -v -timeout 0 -run '^(TestRecallP95|TestAppendP99)$$' ./internal/ctxdb

# ADR-127: backstop CT-09 nightly di main — 5 set sampel, dan run yang seluruh
# set-nya invalid (runner berdisk bising) GAGAL di sini, bukan lulus dengan
# warning seperti gate PR.
CT09_SETS ?= 5
ct09-nightly: TAGS += perf
ct09-nightly:
	DAMNBOT_TIMING=1 DAMNBOT_CT09_SETS=$(CT09_SETS) DAMNBOT_CT09_STRICT_INCONCLUSIVE=1 \
		go test $(GOTAGS) -run '^$$' -bench '^BenchmarkCodetools/Edit1MB$$' -benchtime=100x ./internal/codetools/

# F4 chaos acceptance (ROADMAP F4, SELF-HEALING §9.2): kill -9 in the middle of
# CHAOS_ROUNDS turns, network cut, full data dir, corrupt config/DB, simulated
# reboot, Telegram 429/5xx/drop and a non-idempotent tool pending at kill. Not
# part of `make test`: it starts and kills real processes and takes minutes.
# Machine limits keep the host responsive while the suite runs (GOMAXPROCS=2,
# nice 19, 8 GiB address space); the timeout is the suite's hard budget.
CHAOS_ROUNDS ?= 1000
CHAOS_TIMEOUT ?= 20m
chaos:
	@ulimit -v 8388608 2>/dev/null || true; \
	nice -n 19 env GOMAXPROCS=2 DAMNBOT_CHAOS_ROUNDS=$(CHAOS_ROUNDS) \
		go test $(GOTAGS) -count=1 -timeout $(CHAOS_TIMEOUT) -run '^TestChaosF4$$' -v ./internal/e2e

# GAP-REL-03: 50-client chaos untuk window nightly penuh (PR memakai default singkat).
# `-timeout 40m` wajib: sebelum PR ini target memakai window 30 m dengan batas default
# `go test` 10 m, sehingga job nightly `chaos` mati karena TIMEOUT (600.093 s) dan
# window 30 m-nya tidak pernah benar-benar dieksekusi.
chaos-nightly:
	DAMNBOT_CHAOS_DURATION=30m go test $(GOTAGS) -timeout 40m -run '^TestChaos50Clients$$' -count=1 ./internal/ctxdb/
# Varian jendela panjang (>= 3x TTL) IKUT dijalankan: jendela 45 s dulu mereproduksi
# `db_busy: database is locked (5) (SQLITE_BUSY)` di main (2/2 di host sepi), yaitu
# invarian yang justru dilindungi GAP-REL-03, sehingga baris di bawah sempat dikomentari
# sebagai penahan eksplisit. Policy busy kini mendarat (PR #312), jadi flag dikembalikan:
# tesnya tetap ada, terlihat dan load-bearing - bukan dihapus, bukan di-skip.
	DAMNBOT_CHAOS_LONG=1 go test $(GOTAGS) -timeout 10m -run '^TestChaos50ClientsAcrossLeaseTTL$$' -count=1 ./internal/ctxdb/

# ROADMAP F4 + SCORECARD §1.2: RSS `serve` + Telegram + cron ≤ 90 MB dan CPU idle ≤ 0,5 %.
# Biner asli dibangun dengan tag servetest (satu Tick cron eksplisit + beban kontrol negatif, tanpa
# jejak rilis; serve sendiri hanya menjalankan satu scheduler produksi 30 s),
# fake Telegram + fake provider, warmup 10 s lalu jendela 60 s (VmRSS/VmHWM, Δ utime+stime, gauge
# /metrics). Build dinicekan, pengukuran tidak. Artefak: bench/f4/results/.
perf-f4:
	nice -n 19 go build -tags "$(TAGSCSV),servetest" -o /dev/null ./cmd/damnbot
	@ulimit -v 8388608; DAMNBOT_PERF_F4=1 GOMAXPROCS=2 \
		DAMNBOT_F4_COMMIT=$$(git rev-parse --short HEAD) \
		DAMNBOT_F4_DIRTY=$$(git status --porcelain | wc -l) \
		go test -tags "$(TAGSCSV),servetest" -count=1 -timeout 0 -run '^TestPerfF4' -v ./internal/e2e

# ENGINEERING §7.2 (E-80..E-84) + spec-guard SPEC §20; env BASE/HEAD/PR_* diisi workflow pr-meta.
pr-meta:
	scripts/pr-meta.sh

# ADR-080 butir 2: semua item fase ≤ PHASE wajib covered (dijalankan saat file PHASE berubah).
phase-acceptance:
	DAMNBOT_PHASE_ACCEPTANCE=1 go test $(GOTAGS) -count=1 -timeout $(TEST_TIMEOUT) $(PKGS)

# ADR-324 (E-78): agregat ci-required memeriksa himpunan gate per tier (pr/smoke/full); gate wajib
# harus success, gate di luar tier harus skipped. Uji logikanya dijalankan di job yang sama.
ci-required:
	scripts/ci-required.sh

ci-required-test:
	scripts/ci-required.sh --self-test

# Resource guard 60% (ENGINEERING §5.5, ADR-341): self-test 21 skenario: ambang, cleanup, flag, limit, dan flock
# tanpa menyentuh cache/state nyata (fixture GUARD_FAKE_*).
agent-guard-test:
	scripts/agent-guard-test.sh

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

# CROSS-CUTTING §1.4/§1.5: unit tools/release + sign→verify end-to-end dengan kunci uji in-process.
# Kontrol negatif (tamper artefak/checksum, kunci/passphrase/pubkey hilang) wajib gagal.
release-test:
	scripts/release-test.sh

# GAP-INF-04: satu konfigurasi untuk semua artefak rilis (6 target, dua build
# byte-identik + SBOM, SHA256SUMS, release.json). CI memakai scripts yang sama
# per target; VERSION wajib atau HEAD tepat di tag vX.Y.Z.
release:
	scripts/release-local.sh

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
