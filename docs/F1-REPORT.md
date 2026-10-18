# F1-REPORT — Laporan fase F1

## Deliverable F1 #0

Keputusan/deviasi: ADR-094. Mesin lokal: workstation Kali linux/amd64, `go1.26.9`. Worktree kerja di `/tmp/damnbot-f1ci` (aturan HANDOFF). Repo publik sejak 2026-10-18; `main` diproteksi (PR wajib).

### Stack PR (gate ukuran E-84 ≤ 600 baris Go non-test per PR)

| PR | Isi | Baris Go non-test | Hasil |
|---|---|---:|---|
| #2 `f1-analyzers` | `tools/analyzers` (nohttpclient + vettool), pin tool dev, Makefile final, `scripts/pr-meta.sh`, `pr-meta.yml`, `testdata/docs_exclusions.txt` | 287 | hijau (ci 39137619713, meta 39137619723, pr-meta 39137619713); di-merge rebase → `45b61f1` |
| #1 `f1-ci-hardening` | semua gate lain, archtest, testutil, PHASE, ctxdb Windows, dokumen, ADR-094 | 363 | hijau, 22 job ci + meta (41/41 DETECTED) + pr-meta (ci 39139154438, meta 39139154412, pr-meta 39139154411); di-merge rebase → `ad961eb`; push main: ci 39139710761, meta 39139710788 hijau |
| #3 `f1-ci-followup` | `windows-ctxdb.yml`: `RACE` kosong untuk arm64 (ekspresi `''` falsy), laporan ini | 0 | lihat PR |

PR #1 awal (650 baris, satu PR) ditolak gate `pr-size` (642 > 600) — gate bekerja; pekerjaan dipecah, tanpa label pengecualian.

### Item → implementasi → test/meta → status

| Item | Implementasi | Kasus meta-test / test | Status |
|---|---|---|---|
| E-55 matrix 6 runner | `ci.yml` job `test` (ubuntu x64/arm, macOS arm/intel, Windows x64/arm; arm64 Windows tanpa `-race`) | `test`, `race` | hijau di windows-ctxdb; matrix penuh di PR #1 |
| ctxdb Windows (ADR-090) | `dirsync_windows.go` (fsync dir no-op), `MaintenancePending` tutup `os.Root` (bug produksi), `kill9` toleran, `rawIntegrity` tutup DB sebelum hapus, s057 tutup root store manual | probe `openUnder` di setiap `newEnv` (Linux) + suite Windows | hijau (bukti di bawah) |
| NEW3-01 | `unlockFile` Windows: `UnlockFileEx` sebelum `Close` | `TestFileLockSemantics` | PASS di Windows |
| Lint Windows ctxdb | s063 prekondisi baca; sesi takeover "S" | lint GOOS=windows 0 issues | selesai |
| PHASE CRLF | `.gitattributes` `PHASE text eol=lf` + `Parse` toleran CRLF; `readPhase` hanya file reguler (FS case-insensitive) | `TestParse` (kasus CRLF), `TestPhaseGitattributes`, `TestPhaseFile` | PASS Windows/macOS |
| build-matrix E-39, fuzz E-57, coverage E-58 (+AUD-059), deadcode E-53 | Makefile + `scripts/{fuzz,coverage,deadcode}.sh` | `build-matrix`, `fuzz`, `coverage`, `deadcode` | hijau |
| flake nightly, vuln harian (AUD-071) | `nightly.yml` (flake dilewati bila 0 commit/24 jam — keputusan direktur, ADR-094 butir 4) | `flake`, `vuln` | terpasang |
| perf (NEW-38) | `make perf -tags goolm,perf`, ×1,5 | `perf` | hijau |
| helper timing (NEW-35) | `internal/testutil/timing` (42/80 LOC) | `timing` | selesai |
| Gate arsitektur (NEW-36) + E-79 + AUD-069/070 | `internal/archtest` (TestImportGraph, TestFileLength, TestGlobalAllowlist, TestGoleakMain, TestBudgetTableSum, TestCodeownersComplete, TestNolintDensity, TestWorkflowsPinned, TestMPLIsolation), CODEOWNERS, deps.mpl, Dependabot | `arch-*` (9) | hijau |
| nohttpclient E-05 | `tools/analyzers` | `vet-custom` | merged (#2) |
| pr-meta / adrgate (+NEW2-26) / spec-guard (AUD-056) | `scripts/pr-meta.sh` | `pr-title`, `pr-changelog`, `adrgate`, `adrgate-spec`, `pr-size`, `spec-guard` | merged (#2) |
| PHASE + phase-acceptance (NEW2-22) | `internal/testutil/phase`, job kondisional | `phase-file`, `phase-acceptance` | hijau |
| AUD-057/067/072/073/074/083 | CROSS-CUTTING §7; exclusion dijangkar; meta bench-budget/depguard; loc-check unmapped + GENERATED; 3 modul tidy/verify/vuln + job `size`; TS-11 ke `results/ts11/` | `lint-anchor`, `depguard`, `bench-budget`, `loc-unmapped` | selesai |
| NEW2-07 | `bench-budget` = `cd bench && go test ./...`; `TestNoLLMBaselineCoverage` menyematkan peta 27 dimensi → fase (mutasi fase "Kode mati" F1→F2 → FAIL, diverifikasi) | `bench-budget` | selesai |
| NEW-32/33/34 | ROADMAP F1 nama gate; ENGINEERING §5 = ROADMAP; baris loc.budget infra | — | selesai |
| ADR-018(b) header asal per file + `TestAttributionHeaders` | ADR-099 (paket F1, bukan F1 #0): sidecar `ORIGIN.md` per direktori (sumber, commit, lisensi, SHA-256 byte-persis), himpunan file = glob `docs/ATTRIBUTION.md`, `.gitattributes` `-text` | `TestAttributionHeaders`, `TestAttributionChecker` (7 kelas pelanggaran) | selesai (PR `f1-attribution`) |

### Bukti Windows (workflow `windows-ctxdb`, run 39136866098, windows-latest, `-race`)

```
ok  github.com/damnbot/damnbot/internal/ctxdb  459.680s
--- PASS: TestFileLockSemantics
--- PASS: TestPlatformNAList
--- PASS: TestKill9Chaos (27.87s)
--- PASS: TestMultiProcess (66.93s)
--- PASS: TestPhaseFile
    --- PASS: TestScenario/S017_AutoRestoreBlockedWriterTimeout
    --- PASS: TestScenario/S023_PIDReuseAndNamespace
    --- PASS: TestScenario/S026_RestorerKilledMarkerStale
    --- PASS: TestScenario/S044_StartStampComparedVerbatim
    --- PASS: TestScenario/S057_OldBinaryOpensDuringMigration
    --- PASS: TestScenario/S071_MigrationRefusedKeepsOldServe
```

Akar masalah S017/S026/S057/S071: `MaintenancePending` (`return markerPresent(r) && r.Close() == nil`) tidak menutup `os.Root` saat marker tidak ada → handle direktori bocor → `RemoveAll` data dir gagal di Windows. Direproduksi di Linux dengan probe `/proc/self/fd` (4 skenario yang sama), lalu diperbaiki di kode produksi. `TestKill9Chaos`: DB salinan dihapus sebelum `db.Close()` (bug test).

Konfirmasi arm64 (workflow `windows-ctxdb`, `workflow_dispatch` arch=arm64, run 39140395920, windows-11-arm, tanpa `-race`): `ok internal/ctxdb 92.729s`; PASS TestFileLockSemantics, TestPlatformNAList, TestKill9Chaos, TestPhaseFile, S017, S023, S026, S044, S057, S071. Matrix penuh PR #1: `test (windows-latest, -race, 1)` `ok internal/ctxdb 290.817s`, `test (windows-11-arm, 0)` `ok internal/ctxdb 88.965s`.

### Proteksi `main`

`PATCH /repos/yuldan/damn-bot/branches/main/protection/required_status_checks` (strict = true), diverifikasi dengan GET: PR wajib, 0 approval, `enforce_admins` = false, 24 check wajib: `lint`, `test (ubuntu-latest, -race, 1)`, `test (ubuntu-24.04-arm, -race, 1)`, `test (macos-latest, -race, 1)`, `test (macos-15-intel, -race, 1)`, `test (windows-latest, -race, 1)`, `test (windows-11-arm, 0)`, `test-timing`, `vet-cross`, `build-matrix`, `fuzz (1)`, `fuzz (2)`, `fuzz (3)`, `coverage`, `deadcode`, `perf`, `size`, `loc-budget`, `supply-chain`, `vuln`, `bench-budget`, `phase-acceptance`, `meta-test`, `pr-meta`.

### Perintah lokal (exit code)

| Perintah | Exit |
|---|---|
| `golangci-lint run --build-tags goolm ./...` GOOS linux/windows/darwin | 0 / 0 / 0 |
| `go test -tags goolm ./internal/ctxdb/ ./internal/testutil/...` (dengan probe handle) | 0 |
| `make archtest vet-custom deadcode loc` | 0 |
| `cd bench && go test ./speed/ -run TestNoLLMBaselineCoverage` | 0 |
| `scripts/ci-meta-test.sh` (41 gate) | 0 (41/41 DETECTED) |

### Actions dan menit

| Run | Isi | Hasil |
|---|---|---|
| 39134464920 / 39134464924 / 39134464959 | PR #1 run 1 (matrix penuh) | merah: macOS TestPhaseFile, Windows ctxdb, meta depguard |
| 39135509305 / 39135509099 | PR #1 run 2 (matrix penuh) | merah: Windows ctxdb, pr-size |
| 39136807376, 39136866098 | `windows-ctxdb` (amd64 saja) | YAML invalid (0 job); lalu hijau |
| 39137619752 / 39137619723 / 39137619713 | PR #2 | hijau |

Perkiraan menit tertagih saat repo masih privat (durasi job × pengali Linux 1, Windows 2, macOS 10): run 1 ≈ 209, run 2 ≈ 210, windows-ctxdb ≈ 20 → ≈ 440 menit. Sejak repo publik (PR #2 dan sesudahnya) menit Actions tidak ditagih.

## Paket F1

### `internal/netx` + `internal/netx/retry` (ADR-100) dan atribusi (ADR-099)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| #6 `f1-attribution` | sidecar `ORIGIN.md` ×6 direktori, `TestAttributionHeaders` + `TestAttributionChecker`, `.gitattributes` `-text`, ADR-099 | 0 | hijau (ci 39143369064, meta 39143369055, pr-meta 39143369207); rebase → `50d45c3` |
| #7 `f1-netx-1` | `netx/retry` (kelas, `Do`, `Breaker`), parser SSE, matriks import `netx/retry`, ADR-100 | 595 | hijau (ci 39144286024, meta 39144286006, pr-meta 39144285988); rebase → `17e7811` |
| #9 `f1-netx-2` | `Network`/`Client`, penjaga dial (test/offline/SSRF), `egress_disabled`, `StatusError`, `Stream` + watchdog TTFB/idle | 452 | hijau (ci 39145102606, meta 39145102610, pr-meta 39145102612); rebase → `e8a1bac` |
| #17 `f1-netx-3` | gerbang DLP egress `tool` + log DEBUG metadata via `redact` | 172 | stacked di atas #13 (`redact`, review independen); ci 39150214565: `netx`/`netx/retry` hijau di 6 runner, merah hanya `redact` `TestEdgeCases/EC-SEC-008` di 2 runner Windows (resolusi timer, milik #13); merge setelah #13 |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| E-05 satu pembuat klien | `netx.New` → transport `infra` (proxy env) + `tool` (tanpa proxy) | analyzer `nohttpclient` (`make vet-custom`) | selesai |
| P6/E-32 satu retry | `retry.Do`: 3 attempt, 1 s/4 s ±20 %, `Retry-After` ≤ 30 s (> 30 s → berhenti), ctx berakhir → tanpa retry | `TestRetryMatrix` (14 kelas × 3 jitter, synctest), `TestRetryScenarios`, `TestRetryCancel` | selesai |
| Kelas §4.1.1 | `ClassifyStatus`, `ClassifyErr` (errors.Is/As), `Unclassified` non-retryable | `TestClassifyStatus`, `TestClassifyErr` | selesai |
| Circuit breaker §4.1 | `retry.Breaker` in-process (5/60 s → open 30 s → 1 probe) | `TestBreaker` | selesai (lintas proses COST-06 = F4) |
| E-35 SSE zero-alloc | `SSEParser` WHATWG | `TestSSEZeroAlloc`, `BenchmarkSSEParse` (139–148 ns/op, **0 B/op, 0 allocs/op**), `FuzzSSEParse` (60 s CI, 0 crash), `TestSSEParse`, `TestSSELimits` | selesai |
| E-69 | dial non-loopback ditolak di binary test (`testing.Testing()`, `ControlContext`) | `TestNoNetwork` | selesai |
| CROSS-CUTTING §4 offline, GAP-OPS-02 | `Config.Offline`, `Config.NoBackground` | `TestDialRule`, `TestEgressDisabled` | selesai (subset F1) |
| SPEC §14.4 SSRF | transport `tool`: IP publik saja | `TestDialRule` (18 alamat diblok, 5 publik) | selesai |
| SPEC §17.2 pooling | satu transport per `Network` | `TestConnReuse` (20 call → 1 koneksi TCP) | selesai |
| GAP-REL-07 | `TTFB(tok)`, idle 60 s | `TestTTFB`, `TestStream` | selesai (subset F1) |
| GAP-SEC-16 | `dlp.check` (pola + secret raw/hex/URL/base64 3 alinyemen) | `TestEgressDLP/web_fetch` (4 encoding + pola → 5/5 diblok, 0 koneksi), `TestSecretForms`, `BenchmarkEgressDLP` (≈ 15 µs) | PR #17 (menunggu #13) |
| ADR-071 butir 5 | `logRequest` DEBUG metadata, path ter-redaksi | `TestLogMetadataOnly` | PR #17 (menunggu #13) |
| EC-PROV-009, 010, 028, 029, 030 | — | `TestEdgeCases/<ID>` di `netx` (009, 028, 030) dan `netx/retry` (010, 029), gate `phase.Item(…, "F1", "covered")` | covered |

Coverage (job `coverage`, run 39145102606): `netx` 96,9 %, `netx/retry` 99,0 %. LOC scc: `netx` 759/1.800 setelah #9 (891 dengan `f1-netx-3`). Matrix 6 runner hijau untuk kedua paket (ubuntu x64/arm, macOS arm/intel, Windows x64 `-race`, Windows arm64). Lokal: `go test -race -count=20 -shuffle=on ./internal/netx/...` hijau; golangci-lint 0 (linux/windows/darwin); `make vet-custom deadcode loc` 0.
