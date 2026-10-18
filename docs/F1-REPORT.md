# F1-REPORT — Laporan fase F1

## Deliverable F1 #0

Branch `f1-ci-hardening`, PR #1 (https://github.com/yuldan/damn-bot/pull/1). Keputusan/deviasi: ADR-094. Mesin lokal: workstation Kali linux/amd64, `go1.26.9`.

### Item → implementasi → test/meta → status

| Item | Implementasi | Test / kasus meta-test (`scripts/ci-meta-test.sh`) | Status |
|---|---|---|---|
| Matrix test E-55 (6 runner) | `ci.yml` job `test` (ubuntu x64/arm, macOS arm/intel, Windows x64/arm; arm64 Windows tanpa `-race`), `make test RACE=` | `test`, `race` | macOS: perbaikan `TestPhaseFile` (FS case-insensitive) di-push; Windows: perbaikan ctxdb di-push, menunggu run |
| ctxdb Windows ADR-090 (TestFileLockSemantics, S023, S044, TestPlatformNAList) | `dirsync_windows.go` (fsync direktori = no-op, ADR-094), `kill9` toleran anak yang sudah keluar | suite ctxdb di job `test` Windows | run pertama merah (sync dir "Access is denied", TerminateProcess); perbaikan di-push |
| NEW3-01 | `unlockFile` Windows: `UnlockFileEx` sebelum `Close` | `TestFileLockSemantics` (Windows) | diterapkan |
| Lint Windows ctxdb (SA4006 s063, unparam takeOverB/acquireBg) | prekondisi baca sebelum stall; sesi takeover tetap "S" | `lint` GOOS=windows/darwin/linux 0 issues | selesai |
| build-matrix E-39 | `make build-matrix` (8 target) | `build-matrix` | hijau |
| fuzz E-57 | `scripts/fuzz.sh`, 3 shard | `fuzz` | hijau (0 target sampai paket F1) |
| coverage E-58 + AUD-059 | `scripts/coverage.sh` | `coverage` | hijau (ctxdb 85,5 %) |
| deadcode E-53 | `scripts/deadcode.sh` (3 GOOS gabungan); `BackupPath` dihapus | `deadcode` | hijau |
| flake nightly | `nightly.yml` `make flake` (`-count=20`) | `flake` | terpasang |
| perf (NEW-38) | `make perf` `-tags goolm,perf`, ×1,5 di helper | `perf` | hijau |
| helper timing (NEW-35) | `internal/testutil/timing` (≤ 80 LOC, 42) | `timing` | selesai |
| pohon minimal meta | gate test/race/timing/fuzz/coverage/flake/perf/phase di `./internal/metaviolation/...` | — | selesai |
| Gate arsitektur (NEW-36) | `internal/archtest`: TestImportGraph, TestFileLength, TestGlobalAllowlist, TestGoleakMain, TestBudgetTableSum, TestCodeownersComplete, TestNolintDensity | `arch-importgraph`, `arch-filelength`, `arch-globals`, `arch-goleak`, `arch-budgetsum`, `arch-codeowners`, `arch-nolint` | hijau |
| nohttpclient E-05 | `tools/analyzers` + `make vet-custom` | `vet-custom` | hijau |
| pr-meta (E-80..E-84), adrgate (+NEW2-26), spec-guard (AUD-056) | `scripts/pr-meta.sh`, `pr-meta.yml`, `testdata/docs_exclusions.txt` | `pr-title`, `pr-changelog`, `adrgate`, `adrgate-spec`, `pr-size`, `spec-guard` | hijau di PR #1 |
| CODEOWNERS E-79 | `.github/CODEOWNERS` | `arch-codeowners` | selesai (penegakan review butuh branch protection — langkah manusia, ADR-094 butir 14) |
| meta workflow ADR-085 | `meta.yml` (path diperbarui ADR-094) | seluruh daftar | 41/41 DETECTED lokal; CI: 40/41 (penanda depguard diperbaiki) |
| PHASE (ADR-077/080, NEW2-22) | file `PHASE`=F1, `internal/testutil/phase`, `make phase-acceptance`, job kondisional | `phase-file`, `phase-acceptance` | hijau |
| AUD-057 | CROSS-CUTTING §7 = SPEC §16 | — | selesai |
| AUD-067 | exclusion golangci dijangkar | `lint-anchor` | selesai |
| AUD-069 | `deps.mpl` + `TestMPLIsolation` (tag `licenses`) | `arch-mpl` | selesai |
| AUD-070 | action dipin SHA, `persist-credentials: false`, Dependabot | `arch-workflows` (TestWorkflowsPinned) | selesai |
| AUD-071 | vuln harian `nightly.yml` | `vuln` | selesai |
| AUD-072 | meta `bench-budget`, `depguard` | `bench-budget`, `depguard` | selesai |
| AUD-073 | loc-check: unmapped gagal, GENERATED allowlist | `loc-unmapped`, `loc` | selesai |
| AUD-074 | (a) tidy/verify/vuln 3 modul; (b) tool dipin di tools/go.mod; (c) "39 linter" historis (f0-close: dibiarkan); (d) header F0-REPORT (f0-close, `3ce74d7`); (e) job `size` | `tidy-check`, `mod-verify`, `vuln` | selesai |
| AUD-083 | rekaman TS-11 → `bench/h2h/results/ts11/` | — | selesai |
| NEW-32/33/34 | ROADMAP F1 nama gate; ENGINEERING §5 = ROADMAP; baris loc.budget infra | `arch-budgetsum` | selesai |
| NEW2-07 (sebagian) | `bench-budget` = `cd bench && go test ./...` | `bench-budget` | sebagian (`TestNoLLMBaselineCoverage` tidak termasuk) |

### Perintah lokal (exit code)

| Perintah | Exit |
|---|---|
| `golangci-lint run --build-tags goolm ./...` GOOS linux/windows/darwin | 0 / 0 / 0 |
| `make archtest vet-custom deadcode loc` | 0 |
| `go test -tags goolm,licenses -run TestMPLIsolation ./internal/archtest` | 0 |
| `go test -tags goolm -race ./...` | 0 (169 s) |
| `scripts/ci-meta-test.sh` (41 gate) | 1 → depguard WRONG_REASON; setelah perbaikan penanda: `depguard DETECTED` (0), 40 gate lain DETECTED |

### Actions

| Run | Commit | Hasil |
|---|---|---|
| ci https://github.com/yuldan/damn-bot/actions/runs/39134464920 | `6b743a6` | gagal: test macOS ×2 (TestPhaseFile), Windows ×2 (ctxdb); 18 job lain hijau |
| meta https://github.com/yuldan/damn-bot/actions/runs/39134464924 | `6b743a6` | gagal: depguard WRONG_REASON |
| pr-meta https://github.com/yuldan/damn-bot/actions/runs/39134464959 | `6b743a6` | hijau |

Menit runner: lihat ringkasan PR/laporan akhir (diambil dari API `actions/runs/<id>/timing`).
