# F0-REPORT — laporan otomatis F0

Format sesuai `docs/HANDOFF-F0.md` §"Laporan pagi". Mesin: workstation (Kali, linux/amd64), **bukan** mesin referensi Q14. Toolchain: `go version go1.26.8 linux/amd64`. Python hermes: `Python 3.14.7` sistem, `HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1`, hermes `868b9f244f` (2026-10-17 04:38:45 −0700).

## Status

| # | Deliverable F0 (ROADMAP) | Status | Catatan |
|---|---|---|---|
| 7 | CI skeleton: lint, test -race, size-check, LOC, deps allowlist, govulncheck, go-licenses, meta-test | sebagian | 8/9 gate terbukti gagal pada pelanggaran sintetis; gate `loc-ratio` (E-70) sudah merah di baseline (lihat Temuan spec TS-03). Workflow GitHub belum pernah dijalankan di Actions (tanpa remote). |
| 1 | `bench/budget` (P1, o200k_base offline) | belum | |
| 4 | Baseline hermes tanpa LLM | belum | |
| 5 | `bench/speed` | sebagian | smoke LLM 10 task: **menunggu user** (butuh kredensial/biaya) |
| 6 | Korpus code tools C1–C3 + baseline `rg`/hermes | belum | |
| 2 | `bench/h2h` head-to-head berbasis LLM | menunggu user | butuh kredensial/biaya |
| 3 | Eval set E1–E10 + manifest | belum | di luar alokasi worker scaffold/bench |
| 8 | Persistence spike `internal/ctxdb` | (diisi worker persistence) | |
| 9 | Errata backlog ADR-047 | belum | |

## Bukti

### Scaffold (#7)

Commit: `4e73c68` (go.mod), `3520e0f` (scaffold), `d1322ea` (meta-test).

Isi: `go.mod` (`module github.com/damnbot/damnbot`, `go 1.26.8`), `cmd/damnbot/{main.go,main_test.go,goolm_guard.go}` (guard `//go:build !goolm` sengaja gagal compile, E-39/M-04), `internal/archtest/deps_test.go` (`TestDirectDeps`, `TestDirectRequiresParser`), `deps.allow` (26 modul = 23 runtime + 3 test, ENGINEERING §4.4), `loc.budget` (tabel E-19 + TOTAL 190.000), `Makefile` (`TAGS ?= goolm`, `-tags "$(subst $(space),$(comma),$(TAGS))"`), `.golangci.yml` (v2, `linters.default: none`, 39 linter §5.1 + depguard/forbidigo rule E-03/E-04/E-23–E-25/E-30/E-31/E-34/E-38/E-66), `scripts/{size-check,loc-check,ci-meta-test}.sh`, `.github/workflows/ci.yml` (job `lint`, `test`, `size-check`, `loc-budget`, `supply-chain`, `vuln`, `meta-test`), `tools/go.mod` (`go tool`: golangci-lint v2.15.0, gofumpt v0.12.0, govulncheck v1.9.0, go-licenses/v2 v2.0.1, scc/v3 v3.7.0).

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `go tool -modfile=tools/go.mod golangci-lint config verify` | 0 | config v2 valid |
| `go build ./cmd/damnbot` (tanpa tag) | 1 | `undefined: buildRequiresTagGoolm` — guard E-39 bekerja |
| `make build` | 0 | |
| `make lint` | 0 | `0 issues.` |
| `make test` (`go test -tags "goolm" -race -shuffle=on ./...`) | 0 | `cmd/damnbot`, `internal/archtest` ok |
| `make size-check` | 0 | full 1.671.330 B / 52.000.000; minimal 1.671.330 B / 22.000.000 |
| `make loc` | 0 | TOTAL 97 / 190.000 (worktree saat itu memuat draf `ctxdb` 68 LOC) |
| `make loc-ratio` | 2 | `test:source = 121:97 = 1.25` > 1,20 (E-70) — merah jujur, lihat TS-03 |
| `make deps-check` | 0 | |
| `make vuln` | 0 | `Your code is affected by 0 vulnerabilities` (12 vuln di modul yang di-require, tidak terjangkau) |
| `make licenses` | 0 | hanya warning `non-Go code` (x/sys, modernc libc) |
| `scripts/ci-meta-test.sh` (= `make meta-test`, atas `git archive HEAD` @ `3520e0f`) | 1 | lihat tabel di bawah; 18,8 s wall (cache hangat) |

Meta-test (1 pelanggaran sintetis per gate; baseline harus lulus, pelanggaran harus gagal **dengan** penanda alasan):

| Gate | Pelanggaran sintetis | Penanda | Hasil |
|---|---|---|---|
| lint | `panic("meta")` di `internal/metaviolation` | `forbidigo` | DETECTED |
| test | `t.Fatal` | `TestMetaViolation` | DETECTED |
| test -race | dua goroutine `n++` tanpa sinkronisasi | `WARNING: DATA RACE` | DETECTED |
| size-check | `//go:embed` 53 MB acak di `cmd/damnbot` | `SIZE GAGAL full` | DETECTED |
| loc | 200 baris kode di `internal/boundary` (budget 150) | `LOC GAGAL boundary` | DETECTED |
| loc-ratio | +2.000 baris test | `LOC ratio` | BASELINE_RED (121:29 = 4,17 di HEAD) |
| deps-check | require langsung `github.com/stretchr/testify` | `tidak ada di deps.allow` | DETECTED |
| vuln | `gopkg.in/yaml.v2@v2.2.2` + `yaml.Unmarshal` | `GO-YYYY-NNNN` | DETECTED |
| licenses | modul lokal `example.com/metagpl` (LICENSE = GPL-3) di-link ke `cmd/damnbot` | `GPL` | DETECTED |

Belum dicakup meta-test: `go mod tidy`/`go mod verify` (E-46/E-47, job `supply-chain`), build-matrix 8 target, fuzz, coverage, deadcode, analyzer khusus `tools/analyzers` (belum ada; F1).

## Skenario persistence

(diisi worker persistence)

## Temuan spec

| ID | Temuan | Asumsi yang dipakai | Usulan ADR |
|---|---|---|---|
| TS-01 | Path modul Go tidak ditetapkan di SPEC/docs (hanya `ghcr.io/damnbot/damnbot`). | `github.com/damnbot/damnbot` | ADR: tetapkan path modul + vanity import bila perlu. |
| TS-02 | P13 menulis "MB" tanpa basis (10^6 vs 2^20). | Konservatif: 1 MB = 1.000.000 B (`scripts/size-check.sh`, override `FULL_MAX`/`MINIMAL_MAX`). | ADR errata: tulis budget dalam byte atau MiB. |
| TS-03 | E-70 (rasio test:source 0,8–1,2) sebagai gate `loc-budget` mustahil dipenuhi di awal (scaffold 121:29; spike persistence dominan test). Gate merah permanen sampai F1 berjalan, sehingga meta-test gate ini tidak bermakna. | Gate tetap ditegakkan (`make loc-ratio`, step terpisah di job `loc-budget`); tidak dilemahkan. | ADR: E-70 berlaku bila Go non-test ≥ 5.000 LOC (atau mulai akhir F1); di bawah itu laporan saja. |
| TS-04 | E-39: tag "tidak pernah dipasang lewat `GOFLAGS`", tetapi `go-licenses check` v2.0.1 tidak punya flag `-tags` (`--help` tanpa opsi tag). | Target `licenses` memakai `GOFLAGS=-tags=goolm` (tanpa `-tags` lain di command line, jadi tidak ada penimpaan). | ADR errata E-39: pengecualian bernama untuk go-licenses. |
| TS-05 | E-39 "tag `goolm` wajib di semua job" + build `CGO_ENABLED=0`, sedangkan `go test -race` (E-55) butuh cgo. | Job `test` memakai `CGO_ENABLED=1` hanya untuk `-race`; build rilis/size tetap `CGO_ENABLED=0`. | ADR errata: nyatakan eksplisit bahwa `-race` = pengecualian CGO. |

## Blocker

| Item | Butuh keputusan user |
|---|---|
| Smoke LLM 10 task, `bench/h2h` LLM | Kredensial + izin biaya (menunggu user). |
| Workflow GitHub Actions | Belum ada remote; `ci.yml` hanya divalidasi lewat Makefile lokal yang sama. |
