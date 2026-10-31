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
| perf (NEW-38, ADR-270) | `make perf -tags goolm,perf`, ×1,5; build dulu, lalu `PERF_PKGS` serial (`-p 1 -parallel 1`) | `perf` | hijau |
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

Catatan 2026-10-20: `required_status_checks.strict` diubah `true` → `false`. Alasan: kelaparan CI serial dengan ~25 PR bertumpuk — tiap PR menunggu seluruh matriks sebelum boleh merge, sedangkan kuota runner terbatas sehingga antrean tidak selesai. Ke-24 check wajib tetap dipertahankan; kompensasi: CI `main` pasca-merge menangkap regresi antar-PR.

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
| #17 `f1-netx-3` | gerbang DLP egress `tool` (kanonisasi percent/form ≤ 3 lapis, indeks prefix + prefilter, batas kerja fail-closed) + log DEBUG metadata via `redact`; perbaikan REVIEW-F1-SEC1 NETX-1..14 | 436 | direvisi setelah review auditor (`docs/REVIEW-F1-SEC1.md`); lihat tabel PR di #17 |

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
| GAP-SEC-16 | `dlp.check`: URL/Host/header/trailer/body sekali baca, lapisan percent/form-decode ≤ 3, secret case-insensitive raw/hex/escape URL/JSON/base64 3 alinyemen, `dlp_limit` fail-closed | `TestEgressDLP` (132 subtest lokasi × encoding, secret ber-byte reserved), `TestDLPWire`, `TestDLPBounds`, `TestSecretForms`, `BenchmarkDLPScan` (CI EPYC 7763: plain 46–53 µs, adversarial ≤ 122 µs; budget 50/150 µs di `make perf`) | selesai (#17) |
| ADR-071 butir 5 | `logRequest` DEBUG metadata, host/path ter-redaksi, request diblok = `[blocked]` | `TestLogMetadataOnly` | selesai (#17) |
| EC-PROV-009, 010, 028, 029, 030 | — | `TestEdgeCases/<ID>` di `netx` (009, 028, 030) dan `netx/retry` (010, 029), gate `phase.Item(…, "F1", "covered")` | covered |

Coverage (job `coverage`, run 39145102606): `netx` 96,9 %, `netx/retry` 99,0 %. LOC scc: `netx` 759/1.800 setelah #9 (891 dengan `f1-netx-3`). Matrix 6 runner hijau untuk kedua paket (ubuntu x64/arm, macOS arm/intel, Windows x64 `-race`, Windows arm64). Lokal: `go test -race -count=20 -shuffle=on ./internal/netx/...` hijau; golangci-lint 0 (linux/windows/darwin); `make vet-custom deadcode loc` 0.

### `internal/schema`, `internal/boundary`, `internal/redact`, `internal/sandbox` (ADR-095..098)

LOC = scc kolom Code, Go non-test (ENGINEERING E-19). Coverage = `go test -cover` paket (lokal, linux/amd64). Benchmark lokal linux/amd64 (12 thread), median 3 run. Actions = 24 check wajib per head (matrix 6 OS + gate).

| Paket | PR | LOC / budget | Test utama | Coverage | Benchmark | Actions |
|---|---|---:|---|---:|---|---|
| `schema` (ADR-095) | [#4](https://github.com/yuldan/damn-bot/pull/4), [#10](https://github.com/yuldan/damn-bot/pull/10), [#11](https://github.com/yuldan/damn-bot/pull/11) — merged | 1.126 / 1.200 | `TestCheck`, `TestJSONNoDuplicateKeys`, `TestSchemaKeywordsSupported`, `TestCompileMCPPartial`, `TestValidate`, `TestEnumEquality`, `TestJSONSchemaTestSuite` (344 kasus dieksekusi, 39 dihitung di luar subset), `TestValidateAllocs`, `FuzzJSONCheck`, `FuzzToolArgs` | 98,3% | `BenchmarkValidate` 1 KiB 1,77 µs, 0 alloc; `BenchmarkCompile` 66 µs, 557 alloc | hijau 24/24 (0bd8973, e3dfcf9, 2fc6255) |
| `boundary` (tanpa deviasi) | [#8](https://github.com/yuldan/damn-bot/pull/8) — merged | 75 / 150 | `TestRunPanic`, `TestPanicLogged`, `TestGo`, `TestGroupLimit`, `TestGroupPanicCancels`, goleak | 100% | — | hijau 24/24 (976b77b; pr-meta dijalankan ulang setelah edit deskripsi) |
| `redact` (ADR-096) | [#13](https://github.com/yuldan/damn-bot/pull/13), [#14](https://github.com/yuldan/damn-bot/pull/14) — menunggu review | 511 / 1.300 (bersama `sandbox`) | `TestRedaction`, `TestStreamRedactSplit`, `TestStreamHoldCap`, `TestEdgeCases`/`TestStreamEdgeCases` (EC-SEC-001/005/007/008), `TestSentinelGuard`, `FuzzRedactStream`, `TestFirstTokenLatency/redact` | 92,3% (a) / 98,4% (a+b) | `BenchmarkRedact` 4 KiB 16,4 µs, 250 MB/s, 0 alloc; `BenchmarkStreamRedact` 18,5 µs, 222 MB/s, 0 alloc; first token p95 ≈ 1,2 µs | lihat §"Review keamanan" |
| `sandbox` (ADR-097) | [#15](https://github.com/yuldan/damn-bot/pull/15), [#16](https://github.com/yuldan/damn-bot/pull/16) — menunggu review | 574 / 1.300 (bersama `redact`) | `TestCodeToolsSymlinkOutsideRoot`, `TestDenyPaths`, `TestSymlinkEscapeRace` (CT-26), `TestOpenFile`, `TestOpenOutsideRootVerdict`, `TestVerifyProcFd`, `TestVerifyInode`, `TestEdgeCases` (EC-WIN-010/012/013) | 91,3% (a) / 93,2% (a+b) | `BenchmarkOpenFile` ≈ 10 µs, 34 alloc | lihat §"Review keamanan" |

Gate E-70 (rasio test:source) aktif saat `schema` 3/3 melewati 5.000 LOC; paket test-only dikeluarkan lewat allowlist `TESTONLY` di `loc.budget` ([#12](https://github.com/yuldan/damn-bot/pull/12), ADR-098). Rasio setelah #11: 5.529:4.890 = 1,13; perkiraan setelah `redact` + `sandbox` + `boundary`: 7.024:6.050 = 1,16.

### `internal/config` (ADR-113)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| `f1-config-1` | struct + default SPEC §12, decode TOML ketat (key tak dikenal + path/baris), DI `Deps`, `FuzzConfigTOML` | 497 | lihat PR |
| `f1-config-2` | validasi rentang semua kunci F1, path per OS/profil, `<config_dir>`, `Clone` immutable | 310 | lihat PR |

### `internal/assemble` (ADR-114)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| `f1-assemble-1` | stable kondisional + golden per varian, katalog, volatile beku (Σ 100), `<runtime>` (≤ 40), DI `Counter`/`Scrub` | 574 | lihat PR |
| `f1-assemble-2` | context sub-cap + urutan potong SPEC §6, baca context file lewat sandbox, filter schema `shell` per fase | 504 | lihat PR |
| `f1-assemble-3` | `Build` wire `messages`+`tools`, fixture P1 `f1-core` + hook `bench/budget` | 189 | lihat PR |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| ADR-066 P1 `f1-core` | `TestFixedOverheadGolden` → `internal/assemble/testdata/p1/f1-core.{request,tiers}.json`; `bench/budget TestFixedOverhead/f1-core` mengukur o200k_base | total **2.203** (stable 341, catalog 0, context 812, volatile 92, tools 897) ≤ 4.000; tiap tier ≤ alokasi NEW-41 | selesai |
| P15 subset ADR-067/082 | stable kondisional (aturan 3/`repo_map`), volatile beku, runtime suffix, schema shell per fase | `TestStableTierGolden` (3 varian), `TestPromptNoUnavailableTools`, `TestVolatileTierCaps`, `TestPrefixStableWithinTurn`, `TestRuntimeSuffixCaps`, `TestShellSchemaVariant` | selesai |
| Keamanan prompt | `Deps.Scrub` wajib pada semua teks pemanggil; context file hanya lewat `FileReader` sandbox, `ErrUnavailable` → file dihilangkan | `TestContextScrub`, `TestContextSandboxRefusalOmitsFile` | selesai |

### Smoke LLM (ADR-062, snapshot integrasi `22cafba`)

- Tanggal: 2026-10-20 (13:10 UTC).
- Snapshot: `origin/f1-integration` `22cafba` — **bukan** `main` tergabung.
- Model: `gpt-5.4-mini` via upstream `https://api.teamorouter.com`.
- Hasil: 10 task dijalankan (`h2h-smoke/v1`, 1 run/task, timeout 300 s), 1 lulus, 9 gagal — kegagalan bersifat informasional untuk smoke, bukan gate acceptance; 0 harness error.
- Biaya: $0,09397377 untuk run ini; ledger `bench/h2h/results/f1-spend.json` 0,7294329 → 0,82340667, sisa $1,17659333 dari cap $2,00.
- Artefak: `bench/h2h/results/2026-10-20-llm-smoke/` (`summary.json`, `background.proxy.jsonl.gz`, 10 rekaman `*.damnbot.proxy.jsonl.gz`, `ORIGIN.md`). Rekaman tidak memuat key/token (pemindaian pola redaksi ADR-071 kosong; `Authorization` diredaksi).
- Rerun pada `main` setelah stack loop/tools tergabung masih menunggu; **F1 acceptance belum selesai**.

### Smoke final pada `main` tergabung (2026-10-21)

- Commit teruji: **`3a2a10d`** (Go 1.26.10). H2h 10 task: 10 ran, 1 pass, 0 harness error, tanpa 402; ledger `bench/h2h/results/f1-spend.json` **0,82340667 → 0,92200161** (sisa $1,07799839 dari cap $2,00). Artefak: `bench/h2h/results/2026-10-21-llm-smoke-final/`.
- Matriks 29 skenario penggunaan: **24 PASS / 4 FAIL / 1 SKIP**; detail, ekspektasi, dan repro di `docs/F1-LLM-SMOKE.md` + `bench/h2h/results/2026-10-21-llm-scenarios/summary.json`.
- Defect terbuka: bad-arg exit 2 tanpa pesan (`cmd/damnbot/run.go:54-59`); `write_file` create-loop dengan `tag` nol; `grep` menerima `type` yang ditolak runtime; `shell` tidak terpakai di auto/headless dan `exec.Sessions` tidak di-wire.
- Biaya langsung skenario ≈$0,00617 (estimasi harga gpt; model free dihitung 0 pada kenyataan), total ≈$0,92817 < $2,00. Belum ada perubahan kode produk.
### `internal/tools` (ADR-109)

Base PR berantai: `f1-tools-base` menggabungkan dependency yang masih direview (#67 `codetools`, #63 `exec`, #46 `classify`, #75 `assemble`/`config`); setelah dependency merge, base diganti ke `main` dan PR di-rebase berurutan.

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| [#78](https://github.com/yuldan/damn-bot/pull/78) `f1-tools-1` | tipe pipeline, daftar kode error tertutup, `Registry` + validasi `internal/schema` (0 alloc/1 KiB), `null` = opsional, tipe approval (`Inspector`, `ApprovalRequest`, `AllowSession`), interface `Spill` | 550 | `TestRegistry`, `TestRegistryRejects`, `TestErrorCodeSet`; `vet` bersih |
| [#79](https://github.com/yuldan/damn-bot/pull/79) `f1-tools-2` | pipeline SPEC §4.3: lookup → validasi → policy/taint → approval → journal → run → redaksi/paging/spill → render; `Journal` wajib 2 metode; dedupe call id; agregat 24.000; pemetaan error `codetools` lewat tipe | 568 | `TestDispatchSuccess/UnknownTool/InvalidArgs/DuplicateCallID/JournalRequired/JournalFailure/Cancellation/InvalidEffectAndCode`, `TestPrepareDoesNotApproveOrRun`; `TestDispatchAllocs` 0 alloc; `BenchmarkDispatch` 1 KiB **2,78 µs/op, 0 allocs/op** |
| [#105](https://github.com/yuldan/damn-bot/pull/105) `f1-tools-2b` | `Prepare`/`DispatchPrepared` split untuk `loop.Toolset` (policy tepat sekali), `Prepared` dengan `Decision.Paths` untuk `allow_session`, convenience `Dispatch` + approver | 103 | `TestPrepareDoesNotApproveOrRun`, `TestPrepareExposesApprovalDecision`, `TestDispatchApprovesExactlyOnce`; golden/approval/taint lama tetap hijau |
| [#80](https://github.com/yuldan/damn-bot/pull/80) `f1-tools-3` | adapter 5 tool file (`codetools` + verdict sandbox sebelum `Run`), `todo`, `clarify`, `FileSpill` (`blobs/<sha[0:2]>/<sha>`), golden 7 tool, matriks approval/taint/spill/agregat, EC-TCALL | 423 | 87,9 % coverage; `TestGolden` + `TestGoldenCoverage` (sukses + ≥ 1 error per tool), `TestOutsideReadAsks`, `TestDeniedPathNeverApproved`, `TestAllowSessionExactPath`, `TestTaintForcesApproval`, `TestSpillOversizedResult`, `TestPagedToolCapDropsAfterAggregate`, `TestEdgeCases` EC-TCALL-001..003/005..008 |
| [#81](https://github.com/yuldan/damn-bot/pull/81) `f1-tools-4` | `shell` + `classify.Decide`/`MatchAllow` (resolver binary diinjeksi), tanpa `allow_session`, headless `denied`, sesi `exec.Sessions`, ringkasan/spill `log`+`raw`, output untrusted men-taint | 376 | `TestApprovalModesShell`, `TestShellHeadlessDenied`, `TestShellAllowExact`, `TestShellAllowNonMatchAsks`, `TestShellSessionStdinRejected`, `TestShellBackgroundRejected`, `TestShellOutputUntrustedWorkspace`, golden shell |
| `f1-tools-5` | dokumen: ADR-109, tabel ini, CHANGELOG | 0 | — |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| P4 registry + error tertutup | `Registry` (schema eager, nama unik, urut byte-wise), `CodeSet` 37 kode, `closedError` fail-closed | `TestRegistry*`, `TestErrorCodeSet`, `TestInvalidEffectAndCode` | selesai |
| SPEC §4.3 pipeline | urutan tetap lookup → JSON/schema → effect → policy → taint → approval → `ToolDispatch` → `Run` → redaksi → spill → `ToolResult`; panic → `internal` | `TestDispatchSuccess`, `TestInvalidArgs`, `TestPanicBecomesInternal`, `TestJournalRequired` | selesai |
| Approval §14.1 | `RequireAuto/Ask/Deny/Outside/DeniedPath`, `allow_session` path persis, approver nil → `denied`, `Outcome.Denied` untuk `approval_denied` | matriks 5 mode × {baca, tulis, outside baca/tulis, deny_paths, allow_session, shell} (`TestWriteModeMatrix`, `TestOutside*`, `TestDeniedPathNeverApproved`, `TestAllowSessionExactPath`) | selesai |
| ADR-070 taint | `MarkTainted`/`BeginTurn`, non-read dipaksa tanya tanpa `allow_session`, headless `denied`; baca luar/untuk workspace untrusted membungkus `<untrusted>` | `TestTaintForcesApproval`, `TestTaintFromUntrustedReadResets`, `TestShellOutputUntrustedWorkspace` | selesai |
| P5/ADR-031 spill | `Spill` + `FileSpill` content-addressed (temp+fsync+rename); preview 800/400; paged 16.000 → 4.000; shell `log`/`raw` | `TestFileSpillRoundTrip`, `TestFileSpillErrors`, `TestSpillOversizedResult`, `TestSpillShortPreviewAfterAggregate`, `TestPagedToolCapDropsAfterAggregate` | selesai |
| E-68 golden | 8 tool × (sukses + ≥ 1 error), `-update`, normalisasi durasi/path | `TestGolden`, `TestGoldenCoverage` | selesai |
| E-37 dispatch | lookup + validasi 1 KiB | `TestDispatchAllocs` ≤ 2 alloc; `BenchmarkDispatch` 2,78 µs/op, 0 allocs/op (lokal `-cpu 2`) | selesai |
| EC-TCALL F1 (subset paket) | 001 args kosong, 002 id duplikat, 003 tanpa hasil yatim, 005 JSON terpotong, 006 nama sisa XML, 007 teks setelah JSON, 008 null→default | `TestEdgeCases` dengan `phase.Item(..., "F1", "covered")`; 009/012 milik `loop`/`provider`, 004/011 F5 (ADR-077) | covered |
### Review keamanan `internal/tools` (REVIEW-F1-SEC7)

Independent review of #78–#82 found 1 BLOCKER, 5 MAJOR, 3 MINOR; all fixed before merge (ADR-109 "Review keamanan").

| ID | PR | Severity | Perbaikan | Test |
|---|---|---|---|---|
| TL7-4 | #81 | BLOCKER | `shell_allow` yang berubah antara `Inspect` dan `Run` gagal `denied`; tidak ada fallback ke mode shell | `TestShellAllowTOCTOUFailsClosed` |
| TL7-1 | #79 | MAJOR | `Result.Err`/`OK` dihormati; kegagalan tool tanpa error Go dirender sebagai error | `TestToolReportedFailureIsRendered` |
| TL7-2 | #79 | MAJOR | escape penutup `<untrusted>` tidak case-sensitive | `TestUntrustedBoundaryCaseInsensitive` |
| TL7-3 | #79 | MAJOR | `Redactor` wajib di `NewDispatcher` (gagal tutup) | `TestJournalRequired` |
| TL7-5 | #81 | MAJOR | `cwd` di luar root ditolak `outside_workspace` di `Inspect` + `Run` | `TestShellCwdOutsideWorkspaceDenied` |
| TL7-6 | #81 | MAJOR | `EnvPolicy.Secrets` (scope binary) diteruskan `shell`; API `exec` final tidak punya `OneShot.SecretRefs` | `TestShellInjectsScopedSecret` |
| TL7-7 | #81 | MAJOR | `Effect` memakai `cwd` panggilan, bukan `cfg.Cwd` | `TestShellEffectUsesCallCwd` |
| TL7-8 | #81 | MAJOR | agregat inline yang habis tetap 0 (tidak direset ke budget turn) | `TestShellInlineBudgetStaysSpent` |
| TL7-9 | #80 | MINOR | `clarify` = `non_idempotent` (interaksi, bukan baca) | `TestGolden`/`TestWriteModeMatrix` |
| TL7-10 | #81 | MINOR | stdin kosong = `nil` | `TestShellEmptyStdinIsNil` |
| TL7-11 | #105 | MINOR | `DispatchPrepared` butuh otorisasi/`Approved`, menegakkan hard deny, menolak eksekusi kedua | `TestDispatchPreparedDeniesAndIsSingleUse` |

## Review keamanan

PR berikut **tidak di-merge** pelaksana; menunggu review keamanan independen (auditor). PR bagian b bertumpuk di atas bagian a (base = branch a); setelah a di-merge, base b diganti ke `main`.

| PR | Isi | Kasus ancaman yang dicakup test |
|---|---|---|
| [#13](https://github.com/yuldan/damn-bot/pull/13) `redact` a | pola SPEC §14.3 + ADR-071 tanpa `regexp`, nilai secret yang dikenal, sentinel | 0 bocor: 42 sampel gitleaks v8.28.0 + 60 sintetis × 6 kanal (plain, JSON, log, query URL, blok kode, pesan error); EC-SEC-001 (error provider menggemakan key), EC-SEC-005 (header kustom), EC-SEC-007 (Authorization di dump Go/Python/JSON), EC-SEC-008 (input patologis linear); userinfo panjang (user 300 B, password 3.000 B); secret eksplisit pendek; sentinel di field tulis (ADR-071 butir 4); 0 false positive pada ≥ 300 hash/UUID/base64 |
| [#14](https://github.com/yuldan/damn-bot/pull/14) `redact` b | penyaring stream | secret terpotong di setiap offset (2 chunk) dan 50 potongan 3-chunk per secret = output whole-blob; prefix parsial di akhir chunk; ekor tak terbatas > 4 KiB fail-closed; PEM tanpa END; fuzz `FuzzRedactStream` |
| [#15](https://github.com/yuldan/damn-bot/pull/15) `sandbox` a | verdict `Check`, `deny_paths` | symlink file/dir keluar root (termasuk komponen tengah, dangling, relatif, loop), `..`, path absolut; symlink ke path deny; `/proc/*/environ`, `/proc/*/task/*/environ`, `/proc/*/mem`, `~/.ssh`, `**/.env`, `*.pem`, look-alike, varian huruf (Windows/macOS); MSYS/UNC/long path |
| [#16](https://github.com/yuldan/damn-bot/pull/16) `sandbox` b | open anti-TOCTOU per OS | CT-26: proses lain membalik komponen direktori ↔ symlink keluar root selama 1.000 open per mekanisme (openat2, walk `O_NOFOLLOW`) = 0 file luar; symlink baru di dalam root setelah `Check` ditolak; verifikasi `/proc/self/fd` dan device+inode (macOS/BSD, sisa risiko ADR-097 butir 4); `OpenApproved` tetap menolak deny; create pada path deny tidak membuat file |
| [#22](https://github.com/yuldan/damn-bot/pull/22), [#30](https://github.com/yuldan/damn-bot/pull/30), [#31](https://github.com/yuldan/damn-bot/pull/31) `provider` (ADR-102) — **di-merge setelah CI hijau, review auditor menyusul** (input jarak jauh tidak tepercaya) | decoder delta tanpa reflect, rakitan stream, klien openai-chat (retry, breaker, fallback), `providertest` | **SSE/JSON rusak:** frame JSON tidak lengkap → `stream_broken` (retry, lalu gagal typed), stream terpotong tanpa `finish_reason` → `stream_broken`, angka di luar jangkauan/non-integer untuk token → chunk ditolak, `null` di posisi objek/array, UTF-8 rusak/surrogate tunggal → U+FFFD, kunci beda kapitalisasi diabaikan (`FuzzDeltaDecode` diferensial vs `encoding/json`, 3 crash fuzz diperbaiki + korpus; `TestDeltaDecode`; `TestHostileStream`); **delta besar:** satu event SSE > 4 MiB → `ErrEventTooLarge` (non-retry), rakitan > 16 MiB atau > 128 tool call → `ErrResponseTooLarge` (non-retry, tanpa fallback) (`TestAssemblyBounds`, `TestHostileStream`); **field injeksi tool call:** nama/argumen disimpan mentah tanpa ditafsirkan (`../../rm -rf` tetap string), fragmen dengan `id`/`index` berulang atau baru dirakit sesuai aturan EC-PROV-001 tanpa membuat call ekstra, field tak dikenal dilewati (`TestEdgeCases/EC-PROV-001`, `TestHostileStream`); **error di dalam stream 2xx** → kelas `server`, body disalin (tidak dieksekusi); **content_filter:** dikembalikan sebagai `FinishReason` bersama konten parsial + tool call untuk loop (stop `content_filtered`, ADR-072/082), tanpa retry/fallback (`TestAssemble`, `TestHostileStream`); **klasifikasi:** 429 = `rate_limited` apa pun body-nya (EC-PROV-029), `error.param` saja (bukan teks pesan) memicu pelepasan parameter (E-21); **body error** ≤ 64 KiB, tidak diredaksi di `provider` (redaksi saat log/tampil). Sisa: `providertest` hanya loopback (E-69) |
| `exec` a (ADR-105) — **tidak di-merge sampai auditor approve** | lapisan proses per OS: session/pgid sendiri (Unix, `Pdeathsig`), PTY `creack/pty` 120×40 sebagai controlling terminal, Job Object `KILL_ON_JOB_CLOSE` dengan start tersuspensi (Windows), capture head+tail | stdin `/dev/null` = EOF tanpa tty (sudo/ssh/`read -p` gagal cepat); kill grup menjangkau `sleep &` dan subshell; `trap '' TERM` tetap mati oleh KILL; tidak ada anak yang lolos dari Job sebelum assign (start tersuspensi); capture 16 MiB terbatas untuk output tak berujung (ring, tanpa geser) |

### Stack `internal/codetools` (#48–#61, #67) — review keamanan F1

Rantai 14 PR, base berantai (#48 → main, #49 → #48, …), tiap PR ≤ 600 baris Go non-test (E-84); `codetools` 4.798/4.800 scc LOC. Temuan `REVIEW-F1-SEC2` (2 BLOCKER, 7 MAJOR) ditutup di stack ini; S16-7 (`hardlinked_file`) sudah di `main` lewat PR #33. **Tahap akhir (#61/#67) tidak di-merge sebelum auditor menyetujui entri ini.**

| PR | Tahap | Kasus ancaman yang dicakup test |
|---|---|---|
| [#48](https://github.com/yuldan/damn-bot/pull/48) | walker + `OpenResolved` sandbox | swap leluhur ↔ symlink selama open (openat2 `RESOLVE_NO_SYMLINKS`, fallback walk `O_NOFOLLOW` + verifikasi `/proc/self/fd`/inode) = 0 file luar; deny dicek di path dan identitas handle; hard link = verdict `HardLinked`; FIFO/device ditolak tanpa blokir; `TestSymlinkEscapeRace`, `TestOpenResolvedSwapRace`, `TestOpenNonRegular` |
| [#49](https://github.com/yuldan/damn-bot/pull/49) | engine core + `read_file` | file biner/UTF-8, path di luar root butuh approval, tag sha256, halaman terpotong di baris utuh |
| [#50](https://github.com/yuldan/damn-bot/pull/50) | encoding | BOM, UTF-16 LE/BE (termasuk surrogate), byte invalid ditampilkan `\xNN` tanpa U+FFFD, CRLF dipertahankan byte-exact |
| [#51](https://github.com/yuldan/damn-bot/pull/51) | file besar | streaming > 8 MiB, tail 100 MiB, indeks baris async, race ukuran/mtime → `stale_read` |
| [#53](https://github.com/yuldan/damn-bot/pull/53) | ignore engine | `.gitignore`/`.ignore` symlink atau FIFO tidak dibaca; repo `.git/config` hanya Inside; config global di luar root lewat `OpenApproved` (deny menang, isi tidak ke model); `TestIgnoreSymlinkOutsideRoot`, `TestRepoExcludesFileOutsideRoot`, `TestFIFONoHang` |
| [#54](https://github.com/yuldan/damn-bot/pull/54) | glob/walker | symlink tidak diikuti, hidden/ignore/deny, nama di direktori deny tidak bocor, urutan deterministik |
| [#55](https://github.com/yuldan/damn-bot/pull/55) | matcher grep | literal fast-path + prefilter literal wajib, RE2 hanya baris kandidat, jendela 500 char |
| [#56](https://github.com/yuldan/damn-bot/pull/56) | tool grep | timeout 30 s + hasil parsial berlabel, `skipped:` dihitung, hard link ke file deny dilewati (`TestGrepSkipsHardLinked`), parity vs `rg` (CT-21) |
| [#57](https://github.com/yuldan/damn-bot/pull/57) | `write_file` baru | temp `O_CREAT|O_EXCL` + fsync + rename + fsync dir, mode umask, symlink keluar root ditolak |
| [#58](https://github.com/yuldan/damn-bot/pull/58) | overwrite + lock | lock per identitas ter-resolve (dev+inode; Windows size+mtime bila `os.FileInfo` tanpa inode), `stale_read` fail-closed, owner/xattr/mode |
| [#59](https://github.com/yuldan/damn-bot/pull/59) | `edit_file` | exact-only, `ambiguous_match`/overlap/no-op fail-closed, sentinel `[REDACTED:` ditolak, tulis atomik + read-back sha256 |
| [#60](https://github.com/yuldan/damn-bot/pull/60) | in-place hard link | snapshot callback + backup ber-fsync sebelum byte pertama, pre-alokasi, pemulihan ENOSPC/EIO, kill mid-copy menyisakan backup utuh |
| [#61](https://github.com/yuldan/damn-bot/pull/61) | dispatch + parity/perf | JSON ketat (field tak dikenal gagal), schema ≤ 200 token, CT-21 parity, benchmark CT-04..07/09 |
| [#67](https://github.com/yuldan/damn-bot/pull/67) | driver + completeness | probe CT-01..03/10/11, token golden CT-11, `TestCodeToolsIDsComplete` |

Penutupan temuan (mutasi tercatat di handoff; auditor memverifikasi):

| ID | Perbaikan | Test / bukti |
|---|---|---|
| CT27-1 BLOCKER | cabang approved `OpenResolved` membuka dari root volume tanpa symlink; deny dicek ulang lewat path handle + identitas | `TestOpenResolvedSwapRace` (2 mode × 2 cabang, `opened>0 && refused>0`); mutan `Resolve=0`/tanpa cek path tertangkap |
| CT27-2 BLOCKER | file > 64 MiB ber-hard-link: temp lengkap + snapshot/backup ber-fsync + pre-alokasi + pemulihan; ENOSPC/no-op = byte-identik | `TestInPlaceLargeFile` (65 MiB), `TestInPlaceFailuresKeepFile` |
| CT27-3 MAJOR | ignore/config workspace hanya file reguler Inside; symlink/FIFO = tidak ada; global config lewat approval | `TestIgnoreSymlinkOutsideRoot`, `TestRepoExcludesFileOutsideRoot` |
| CT27-4 MAJOR | `O_NONBLOCK` + cek handle regular; FIFO/device → `ErrNotRegular`, tanpa goroutine bocor | `TestOpenNonRegular`, `TestFIFONoHang` (goleak) |
| CT27-5 MAJOR | lock pada path ter-resolve; hard link pada dev+inode (Windows: size+mtime saat tanpa inode) | `TestPathLockAliases`, `TestEditLockAlias` (150 iterasi) |
| CT27-6 MAJOR | API sandbox dan titik kill CT-25 diuji; mutan “semua tulis in-place” gagal | `TestOpenResolvedSwapRace`, `TestAtomicWriteKill9` (200×), `TestInPlaceKill9` |
| CT27-7 MAJOR | test acceptance CT-01..03/11/21 + `TestCodeToolsIDsComplete`; job `bench-parity` + nightly `bench-codetools` | PR #61/#67 |
| CT27-8 MAJOR | `Config.Snapshot` dipanggil sebelum byte pertama; backup `.<nama>.damnbot-bak-*` ber-fsync | `TestInPlaceEdit`, `TestInPlaceKill9` |
| CT27-13 MAJOR | `OpenResolved` inside memeriksa `nlink` pada handle; walker menghitung `skipped: N hardlinked` | `TestOpenResolvedHardLinks`, `TestGrepSkipsHardLinked` |
| CT27-9..12 MINOR | mode diverifikasi pada file ter-commit; pola temp yatim ketat + `unlinkat`; residual race rename didokumentasikan (ADR-103 butir 16); Windows `owner_preserved=false` | `TestSetgidPreserved`, `TestCleanOrphanTempsPattern`, ADR-103 |

**Kinerja terukur** (host 12 thread, korpus `bench/corpora.lock`, `rg` 15.1.0 SHA-256 `ebeaf56f…f71f2`, interleaved 20 run; gate `TestGrepSpeedVsRg`):

| Korpus × jenis | Σp50 / Σp95 vs `rg` | Target | Status |
|---|---|---|---|
| C1 literal | 1,84 / 1,85 | ≤ 1,2 | ✗ |
| C1 regex | 2,68 / 2,66 | ≤ 1,5 | ✗ |
| C1 glob | 1,75 / 1,83 | ≤ 1,2 | ✗ |
| C3 literal | 1,53 / 1,53 | ≤ 1,2 | ✗ |
| C3 regex | 1,64 / 1,69 | ≤ 1,5 | ✗ |
| C3 glob | 0,24 / 0,34 | ≤ 1,2 | ✓ |
| C2 | diukur terpisah (nightly) | — | — |

CT-10 (baseline hermes durabel `bench/codetools/results/hermes-868b9f244f-f1.json`): `search_files` ×12,4, `read_file` ×15,9, `patch` ×2,2 p50 (min op ×1,6) tetapi p95 maks 10,4 ms > 6 ms → belum lulus. CT-11: 0,906× pada limit 50 dan 1,686× pada limit default; target 0,6× tetap dan status `pending:F1` (keputusan owner).

Selisih literal berasal dari biaya struktural per file (open sandbox + sniff encoding/biner + tag sha256 untuk file yang match); regex tanpa literal wajib memakai `regexp` Go (~35 MB/s/core) melawan engine `rg`. Gate tidak dilonggarkan: `TestGrepSpeedVsRg` tetap merah sampai target terpenuhi.

### `internal/budget` + `internal/ctxdb/ledgerstore` (ADR-101)

| Item | Implementasi | Test | Status |
|---|---|---|---|
| COST-09 estimator (ADR-058/067) | `budget.Count`/`Counts.Raw` (rasio per script), `Estimator` (jangkar `prompt_tokens` nyata + delta × faktor EMA 0,5–1,5, ≤ 50 sampel) | `TestTokenEstimatorCalibration` (en/id/ru/ja, o200k_base: call ≥ 2 = 0,95–1,02), `TestCalibrate`, `TestCount`, `BenchmarkCount` (≈ 90 MB/s, 0 alloc) | selesai |
| Korpus o200k | `internal/budget/testdata/calibration/{en,id,ru,ja}.txt` + `o200k.tsv` | `bench/budget` `TestCalibrationCorpusCounts` (job `bench-budget`) | selesai |
| Biaya ledger (SPEC §18.4) | `budget.Cost` (provider → tabel harga dibulatkan ke atas → unknown), `Entry.Account` | `TestCost`, `TestAccount` | selesai |
| Storage ledger (ADR-074) | `ledgerstore.Insert` (transaksi pemanggil), `Calibration`, `SessionTotals` | `TestLedgerRoundTrip`, `TestLedgerAppendOnly` (event lintas sesi + UPDATE ditolak), `cmd/damnbot` `TestLedgerTypesConvertible` | selesai |
| Guard P27 (cap 80 %/100 %) | — | — | F2 (ROADMAP F2 "budget guard") |

### `internal/provider` + `providertest` (ADR-102)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| [#22](https://github.com/yuldan/damn-bot/pull/22) | decoder delta tanpa reflect (`jscan.go`, `delta.go`), angka dibulatkan benar (fuzz `0a57b946`) | 574 | hijau; di-merge `43f2990` |
| [#30](https://github.com/yuldan/damn-bot/pull/30) | tipe, encoder openai-chat, rakitan stream | 532 | hijau; di-merge `d44bac1` |
| [#31](https://github.com/yuldan/damn-bot/pull/31) | `OpenAI` (retry, breaker per provider+credential, fallback ≤ 2, pelepasan parameter), batas rakitan, `providertest`, `retry.Policy.Wait` | 545 | lihat PR |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| E-36 decoder delta | `DeltaDecoder` (scratch ≥ frame, 0 realokasi) | `TestDeltaDecodeZeroAlloc` (10 jenis chunk), `BenchmarkDeltaDecode` (≈ 359 ns/op, 0 B/op, 0 allocs/op), `FuzzDeltaDecode` | selesai |
| P6 kelas/retry/fallback | `toError`, `OpenAI.Do` via `retry.Do`, `retry.Breaker` | `TestToError`, `TestRetryAndClasses`, `TestCircuitOpen` | selesai |
| P7 fallback terlihat | `Response.FallbackFrom`, `Config.OnFallback` | `TestEdgeCases/EC-PROV-015` (event) | selesai di `provider`; `TestFallbackVisible` = loop/`run` |
| `content_filtered` (ADR-072/082) | finish `content_filter` diteruskan apa adanya | `TestAssemble`, `TestHostileStream` | selesai di `provider` |
| E-69 `providertest` | server palsu loopback, skrip, replay fixture SSE, `FakeClock` | `TestFixtureAndLatency`, semua test klien | selesai |
| EC-PROV F1 | 001, 004..007, 011..013, 015, 016, 025 di `provider`; 009/010/028..030 di `netx` | `TestEdgeCases/<ID>` (gate PHASE) | covered |


### `internal/exec` (ADR-105)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| exec a | tipe hasil (`ExecStatus` ADR-082), lapisan proses per OS, capture | lihat PR | CI; menunggu auditor |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| Runner pipe/pty (§A.1) | `start` (`proc_unix.go`, `proc_windows.go`), `sysAttr` | `TestProcPipeExit`, `TestProcStdinPipe`, `TestProcPTY`, `TestProcJobExit` | PR a |
| Kill pohon proses (§A.4) | `terminate`/`kill` ke pgid; `TerminateJobObject` | `TestProcGroupTerminate`, `TestProcGroupKillIgnoresTrap`, `TestProcJobKillTree` | PR a |
| Capture output | `capture` head 8 MiB + ring 8 MiB | `TestCaptureHeadTail`, `TestCaptureHugeWrite`, `TestCaptureSmall` | PR a |
| Loop timeout/cancel, sweep marker, env, mode one-shot, prompt interaktif, idle, ConPTY, summarize, P24 | — | — | PR berikutnya |

### `internal/loop` + `internal/loop/event` (ADR-110)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| [#99](https://github.com/yuldan/damn-bot/pull/99) | `loop/event` + tipe/interface konsumen | 475 | lihat PR |
| [#100](https://github.com/yuldan/damn-bot/pull/100) | `Loop`, turn core, streaming, budget, stop reason, cancel | 585 | lihat PR |
| [#101](https://github.com/yuldan/damn-bot/pull/101) | rakitan tool call, render, primitif dispatch, context guard | 485 | lihat PR |
| [#102](https://github.com/yuldan/damn-bot/pull/102) | dispatch tool, approval/taint, spill, no_progress, seluruh test | 306 | lihat PR |
| [#103](https://github.com/yuldan/damn-bot/pull/103) | komentar paket + laporan F1 | 232 | lihat PR |

LOC `loop` + `loop/event` = 1.757 / 1.800 (scc, Go non-test; E-19, amandemen ADR-110 LP8-16..20). Coverage lokal `go test -cover ./internal/loop/...` = 89,7 % (ambang E-58 80 %). `go test -race` hijau; `golangci-lint` 0 issue; gate arsitektur (`TestImportGraph`, `TestFileLength`, `TestGlobalAllowlist`, `TestGoleakMain`, `TestCodeownersComplete`) hijau. Golden E-68: `testdata/turn_ordering.golden.json` (urutan update satu turn dua iterasi) dan `testdata/tool_render.golden.json` (sukses byte-identik, envelope error, spill, too_large, truncation agregat). Regresi REVIEW-F1-SEC8 putaran 4: `TestLP816..TestLP820` (pairing setiap hasil tool, grant cached, read lambat, secret terpecah di teks/argumen/penyaring, member tak-mulai).

| Item | Implementasi | Test | Status |
|---|---|---|---|
| State machine `Turn` | `steps`/`iterate`/`build`/`ask`/`classify`; satu turn aktif per sesi (`ErrTurnInProgress`) | `TestStopReasonMatrix`, `TestTurnInProgress` | selesai |
| Iterasi + stop reason | 9 `StopReason` kasar + `StopDetail` SPEC §3.5 (ADR-110) | matrix 11 baris (termasuk `still_running` dan `max_wall`) | selesai |
| Streaming | text delta, tool-call start/args/end, tool result, usage, stop | `TestGoldenTurnOrdering`, `TestCallBuilder` | selesai |
| Rakitan tool call (EC-PROV-001) | fragmen per index/id, id-less continuation, nama dirapikan | `TestEdgeCases/EC-PROV-001`, `TestCallBuilder` | covered |
| Args duplikat / id ganda | duplikat kunci JSON → `invalid_args`; id ganda → satu dispatch + `tool_rejected` | `TestNormalizeArgs`, `TestDuplicateJSONKeysRejected`, `TestEdgeCases/EC-TCALL-002` | covered |
| Approval | `Approver` + `allow_session` per `(tool, subject)`, edit divalidasi ulang, deny terminal | `TestStopReasonMatrix/approval_denied`, `TestAllowSessionCachesApproval`, `TestApprovalEditRevalidated`, `TestNoSessionNeverCached`, `TestApproverErrorIsDenial` | selesai |
| Taint (ADR-070) | flag lengket dari `ToolResult.Tainted`; non-read dipaksa tanya, `allow_session` diturunkan | `TestTaintForcesApproval` | selesai |
| Cancel + kill grace | `cancelled`/`cancelled_confirmed`, `still_running` dari grace, batas wall-clock | `TestStopReasonMatrix/cancelled*`, `TestCancelAPI`, `TestMaxWall` | selesai |
| Guard konteks + budget | compaction 75 %, stop 90 %, cap input/output | `TestContextGuard`, `TestStopReasonMatrix/max_tokens_*` | selesai |
| Spill P5 | 4.000 char/hasil, 24.000 char/turn, preview 800/400, `too_large` tanpa Spiller | `TestGoldenToolRendering` | selesai |
| no_progress COST-07 | kunci `sha256(tool|args|result)`, ambang 3 | `TestNoProgress` | selesai |
| Golden E-68 | urutan update + render model-facing | `TestGoldenTurnOrdering`, `TestGoldenToolRendering` | selesai |
| EC F1 | EC-PROV-001/015; EC-TCALL-001, 002, 003, 005, 006, 007, 008, 012 | `TestEdgeCases/<ID>` (`phase.Item` F1) | covered |
| Determinisme | `Config.Now`/`NewTurnID` disuntik, tanpa I/O/jam nyata, goroutine via `boundary` | goleak `TestMain`, `TestDefaultTurnID` | selesai |

Stop-reason matrix (ringkas):

| `StopReason` | `StopDetail` | Pemicu | Test |
|---|---|---|---|
| `end_turn` | `completed` | final non-kosong | `TestStopReasonMatrix/end_turn` |
| `end_turn` | `empty_response` | kosong setelah satu retry tanpa nudge | `TestEmptyResponseRetry` |
| `end_turn` | `no_progress` | `(tool,args,result)` identik ke-3 | `TestNoProgress` |
| `max_iterations` | `max_iterations` | iterasi 40 selesai, model masih minta tool | `TestStopReasonMatrix/max_iterations` |
| `max_tokens` | `output_truncated`, `budget_input_tokens`, `budget_output_tokens`, `context_overflow` | finish `length` tanpa tool, cap token, window > 90 % | `TestStopReasonMatrix/max_tokens_*`, `TestContextGuard` |
| `cancelled` | `cancelled`, `max_wall` | ctx/Cancel di luar dispatch atau `still_running` | `TestStopReasonMatrix/cancelled*`, `TestCancelAPI`, `TestMaxWall` |
| `cancelled_confirmed` | `cancelled` | cancel saat dispatch, semua tool `cancelled_confirmed` | `TestStopReasonMatrix/cancelled_confirmed` |
| `content_filtered` | `content_filtered` | `finish_reason=content_filter`, tool call tidak dieksekusi | `TestContentFilterStop` |
| `error` | `provider_error`, `internal` | provider/assembler/store/ledger gagal | `TestStopReasonMatrix/error`, `TestLedgerErrorStops`, `TestStoreErrorStops` |
| `approval_denied` | `approval_denied` | deny pada `Prepared.Terminal` | `TestStopReasonMatrix/approval_denied` |
| `tool_error` | `tool_error` | `Prepare`/`Dispatch` mengembalikan error Go (bukan `*ToolError`) | `TestStopReasonMatrix/tool_error`, `TestPrepareFatal` |
## Handoff f1-net (pause)

Status 2026-10-20 saat proyek di-pause. Semua kerja ada di branch remote; tidak ada yang hanya ada di `/tmp`. Tidak ada PR yang security-gated yang di-merge.

### PR terbuka

| PR | Branch (base) | Head | CI terakhir | Gate merge |
|---|---|---|---|---|
| #28 netx DLP NETX-15/16/26..31 | `f1-netx-dlp2` (main) | `17bbaad` | berjalan; head sebelumnya `8ed7e04` perf hijau (EPYC 9V74: plain 14–16 µs, pct 30–43 µs, prefix 15–24, decode 67–91; gagal hanya pr-meta size 615 dan `TestFileLength`, keduanya diperbaiki di `17bbaad`: helper dipindah ke `dlp_layer.go`, 598 baris) | auditor sudah APPROVE deteksi NETX-26 (@d2ee9d5); minta auditor re-check range-diff `d2ee9d5..17bbaad` (perf-only + split file) + oracle; merge setelah approve |
| #39 exec a: lapisan proses, PTY, Job Object, capture | `f1-exec-1` (main) | `5a9e9e8` | berjalan | auditor |
| #40 exec b: Runner, timeout/cancel/kill order, sweep | `f1-exec-2` (#39) | `6e508ff` | job `coverage` gagal (belum diselidiki; cek `TestFileLength`/coverage log) | auditor |
| #42 exec c: env allowlist, secret-ref scope, one-shot (NEW3-03/04), `NeedsTTY` | `f1-exec-3` (#40) | `afc32ff` | berjalan | auditor |
| #45 exec d: prompt interaktif, idle-timeout | `f1-exec-4` (#42) | `18ac833` | berjalan | auditor |
| #47 exec e: ringkasan §A.3 | `f1-exec-5` (#45) | `e0c5c95` | berjalan | auditor |
| #52 exec f: `SweepStale`, `TestNoOrphans` 200×, bench spawn | `f1-exec-6` (#47) | `ea6e8b5` | berjalan | auditor |
| #62 exec g: ConPTY Windows | `f1-exec-7` (#52) | `31320bf` | **gagal** test windows-latest, windows-11-arm, ubuntu-24.04-arm (ConPTY belum pernah dijalankan di Windows; arm64 belum diselidiki) | perbaiki dulu, lalu auditor |
| #63 exec h: sesi persisten | `f1-exec-8` (#62) | `a59fa64` | windows-11-arm gagal (kemungkinan dari #62), sisanya berjalan | auditor |
| #41/#43/#44/#46 exec/classify (ClassifyLead) | `f1-classify-1..4` (berantai, #41 base main) | `11e5cfc`/`affd0e9`/`fa9f41c`/`d02bf38` | semua hijau | auditor; #41 memuat transfer budget exec 3.400 / classify 1.600 (disetujui owner) |

Branch tanpa PR: `f1-classify-wip` (`29b28bc`, versi utuh classify sebelum dipecah; boleh dihapus), `f1-exec-wip` (WIP lama, digantikan `f1-exec-1..8`; boleh dihapus).

### Worktree (lokal, boleh hilang)

`/tmp/damnbot-f1net` → `f1-netx-dlp2` / `f1-handoff-net`; `/tmp/damnbot-f1exec` → `f1-exec-1..8`; `/tmp/damnbot-f1classify` → `f1-classify-*`. Semua bersih dan sama dengan remote. Pulihkan dengan `git worktree add /tmp/<dir> <branch>`.

### Cara kerja yang perlu dibuat ulang bila `/tmp` hilang

- Lint: `GOFLAGS=-modfile=tools/go.mod go build -o /tmp/golangci github.com/golangci/golangci-lint/v2/cmd/golangci-lint`, lalu `GOOS={linux,windows,darwin} /tmp/golangci run --build-tags goolm ./internal/...`. Gate lain: `make vet-custom deadcode loc`, `go test -tags goolm ./internal/archtest/`.
- Restack exec setelah `main` bergerak: untuk b = 1..8, `git rebase --onto <tip baru b-1 (b=1: origin/main)> <tip lama b-1> f1-exec-b`; konflik CHANGELOG/DECISIONS/F1-REPORT = simpan kedua sisi (hapus baris penanda konflik), lalu periksa entri CHANGELOG/ADR yang terduplikasi. Push `-f` setiap branch.
- pr-meta E-84 menghitung diff terhadap `main` terkini: PR yang tertinggal dari `main` bisa melebihi 600 baris → rebase.
- Mesin lokal terbebani; angka benchmark lokal tidak bisa dipakai. Ukur di job `perf` CI (catat model CPU di log: EPYC 7763 ≈ 1,25× lebih lambat dari 9V74).
- Skrip kredensial GitHub (`askpass`, wrapper API, watcher CI) ada di luar repo dan tidak didokumentasikan di sini; owner menyediakannya ulang.

### Langkah berikut

1. #28: tunggu CI `17bbaad` hijau; minta auditor re-review range-diff; merge (rebase) setelah approve. Keputusan terbuka NETX-16: gate pct 75 µs kini lolos dengan margin (≈ 43 µs short50 di 9V74, perkiraan ≈ 54 µs di 7763); tidak perlu mengubah target.
2. #62 ConPTY: perbaiki kegagalan Windows/arm64 (lihat log job), lalu restack #63.
3. #40 `coverage`: baca log job, perbaiki, restack c..h.
4. Serahkan stack exec a..h + classify 1..4 ke auditor (urutan merge: classify #41 dulu karena memuat `loc.budget` exec 3.400 — exec saat ini ≈ 2.330 scc).
5. Sisa F1 exec di luar stack: `TestCancelConfirmBeforeRedispatch` (loop/tools), Backend `§A.7` (hanya `local` di F1), wiring `shell` tool (tools/loop) ke `Runner`/`Sessions`/`classify`, panggilan `exec.SweepStale()` di composition root.

### Keputusan yang tercatat (ADR-105, ADR-106) dan perlu diketahui reviewer

- macOS: tanpa idle-kill (tidak ada akuntansi CPU/I/O per proses tanpa libproc); timeout tetap berlaku.
- Setelah `kill -9` damnbot, pohon proses (selain leader di Linux) dibersihkan `SweepStale` pada start berikutnya di semua Unix.
- Sesi Windows memakai pipe (bukan ConPTY); fish tidak dipakai sebagai shell sesi.
- Pengecualian E-38 `unsafe` hanya untuk `internal/exec/*_windows.go`.

## Handoff codetools (pause)

Status 2026-10-19 saat proyek dijeda. **Tidak ada PR codetools yang di-merge**: review auditor atas stack baru belum ada. Semua WIP ada di remote; `/tmp` boleh hilang.

### PR dan branch

| Item | Head | Keterangan |
|---|---|---|
| #33 S16-7 hard link | merged (`f154049`) | rebase dari `32e9a38` yang di-approve auditor; range-diff identik |
| #32 REVIEW-F1-SEC2 | merged (`8560b32`) | — |
| #27 codetools (lama) | closed | digantikan stack #48–#61 |
| Stack #48–#61 (`f1-ct-01` … `f1-ct-13`), base berantai (PR k → branch k−1) | `6aeb132`, `811a850`, `bdbe4e3`, `51614a5`, `66a7f4e`, `782c891`, `910719d`, `c6c8b88`, `5f765a2`, `1b40ddc`, `ec9ed9f`, `103e5df`, `0f38722` | base `main` `5b3b78d`; tree `f1-ct-13` = referensi `b2fec95` + perubahan main sesudahnya; setiap tahap ≤ 600 baris Go non-test (pr-meta lokal), lint/deadcode/coverage ≥ 80 % lokal ok |
| `f1-codetools-wip` | `b2fec95` | referensi lint-clean (sumber stack) |
| `f1-ct-perf2-wip` | `7cce131` | putaran perf 2 di atas `b2fec95` (437 baris Go non-test; belum masuk stack, rencananya PR ke-14) |
| `f1-ct-split-tools` (orphan) | `4209020` | builder stack (`ctsplit/build.sh 1 13`, `check.sh K`, `stages.txt`, manifest berisi judul/body/gate per PR) |
| `f1-ct-sb-wip`, `f1-ct-ign-wip`, `f1-ct-perf-wip`, `f1-codetools-wip-79b66b5` | `398e16f`, `55ba3a6`, `6911602`, `79b66b5` | riwayat cabang kerja (sudah terkandung di referensi) |

CI stack saat jeda (24 check wajib): #48–#52 dan #55 hijau. Merah:
- **#53 (`f1-ct-06`) windows-latest `TestSymlinkEscapeRace/primary`: "opened a file outside the root".** Ini test sandbox dari `main`; tahap 6 tidak menyentuh sandbox, dan tahap 1 hijau di Windows. **Prioritas pertama:** ulang job; bila berulang, ini temuan keamanan Windows (`os.Root` + final path) yang harus dianalisis sebelum merge apa pun.
- **#56–#57 `perf`:** `BenchmarkDLPScan/secrets=50/decode` 273 µs > 225 µs (netx, di luar codetools; noise runner atau regresi main).
- **#58–#61 macOS/Windows:**
  - `TestCleanOrphanTempsPattern`: fixture `.f.txt.damnbot-ABCDEFGH2345` bertabrakan dengan nama huruf kecil di FS case-insensitive. Ini bug test; buat fixture huruf besar di direktori lain.
  - `TestPathLockAliases/hard.txt` gagal di Windows: `keyOf` tidak punya file ID, sehingga lock hard link jatuh ke path. Perbaikan: kunci volume+file ID lewat `GetFileInformationByHandle` (CT27-5 belum tertutup di Windows).
- **#61 `test-timing`:** CT-05 3,23 ms > 3 ms; CT-07 54 ms > 50 ms.

### Penutupan temuan REVIEW-F1-SEC2 (#27)

| ID | Status | Tahap | Bukti |
|---|---|---|---|
| CT27-1 BLOCKER | diperbaiki | 1 | cabang approved `OpenResolved`: open tanpa symlink dari root volume + cek ulang deny path+identitas; `TestOpenResolvedSwapRace` (2 mode × 2 cabang); mutasi `Resolve=0` / tanpa cek path tertangkap |
| CT27-2 BLOCKER | diperbaiki | 12 | in-place lewat temp lengkap → snapshot + backup ber-`fsync` → pre-alokasi → salin → pulih dari backup (ADR-103 butir 13); `TestInPlaceLargeFile` (65 MiB), `TestInPlaceFailuresKeepFile` (ENOSPC/EIO/no-op/snapshot gagal = byte-identik) |
| CT27-3 MAJOR | diperbaiki | 5 | ignore/config workspace hanya Inside + reguler; `TestIgnoreSymlinkOutsideRoot`, `TestRepoExcludesFileOutsideRoot` |
| CT27-4 MAJOR | diperbaiki | 1, 5 | `ErrNotRegular`, `O_NONBLOCK`; `TestOpenNonRegular`, `TestFIFONoHang` (goleak) |
| CT27-5 MAJOR | diperbaiki Unix; **terbuka Windows** | 10 | `TestPathLockAliases` deterministik; Windows lihat di atas |
| CT27-6 MAJOR | diperbaiki | 1, 6, 10 | test sandbox + kill point CT-25 (`mid-temp`, `pre-rename`, `post-rename`); mutasi "semua tulis in-place" tertangkap |
| CT27-7 MAJOR | sebagian | 13 | `TestGrepParityRg` (CT-21, 0 selisih C1/C3), `TestGrepOutputTokens` (CT-11, gagal), `TestGrepSpeedVsRg` (CT-01..03), `TestCodeToolsIDsComplete`; job `bench-parity` (PR) dan `bench-codetools` (nightly) dengan `make corpora` |
| CT27-8 MAJOR | diperbaiki | 12 | sama dengan CT27-2; `TestInPlaceKill9`: backup utuh saat kill di tengah salinan |
| CT27-13 MAJOR | diperbaiki | 1, 8 | cek `nlink` di handle; `TestGrepSkipsHardLinked` |
| CT27-9..12 MINOR | diperbaiki (11: residual diterima ADR-103 butir 16) | 1, 9, 10 | `TestSetgidPreserved`, `TestCleanOrphanTempsPattern`, owner Windows `false` |

### Kinerja (host lokal 12 thread, load 6–13, 20 run/kueri; `bench/codetools/results/f1-perf2-4ade771.json` di `f1-ct-perf2-wip`)

| Korpus × jenis | Σp50 / Σp95 vs `rg` 15.1.0 | Target |
|---|---|---|
| C1 literal | 1,79 / 1,94 | ≤ 1,2 ✗ |
| C1 regex | 2,62 / 2,75 | ≤ 1,5 ✗ |
| C1 glob | 1,07 / 1,08 | ≤ 1,2 ✓ |
| C2 literal / regex / glob | 2,21 / 3,46 / 1,41 | ✗ |
| C3 literal / regex / glob | 1,55 / 1,62 / 0,20 | ✗ / ✗ / ✓ |

Angka putaran sebelumnya (`f1-7d0f6bd.json`, 5 run) untuk C3 literal/regex lolos (1,07 / 1,28). Selisihnya dengan tabel di atas tidak bisa dipisahkan dari load host; ukur ulang di mesin idle (Q14).

CT-10 vs hermes C1 (baseline durabel PR #25): `search_files` p50 10,6 ms (×9,9, target ≥ 10 ✗), `read_file` 0,35 ms (×20,9 ✓), `patch` p50 4,48 ms (×2,6 ✓) dengan p95 median 5,72 ms / maks 8,8 ms (target ≤ 6 ms p95 ✗ untuk maks).

Hambatan: `regexp` Go ≈ 35 MB/s per core untuk pola tanpa literal, sehingga CT-02 di C1 tidak tercapai tanpa engine regex lain. Literal: syscall 6/file vs ≈ 4 di `rg`, sha256 tag, walker channel-per-file.

### CT-11 (token grep vs hermes): menunggu keputusan owner

Hermes@868b9f244f sudah mengelompokkan match per file, sehingga estimasi CODE-TOOLS §9 (0,5–0,65×) usang. Terukur pada 50 kueri golden (o200k):
- limit sama (50): 0,906× (24,6 tok/match);
- default limit 100: 1,69×.

Simulasi perubahan format:

| Perubahan | Rasio | Informasi yang hilang |
|---|---|---|
| tanpa header | 0,895 | hitungan match/file |
| strip indentasi | 0,874 | indentasi |
| tag 6 hex | 0,894 | tag lebih lemah |
| jendela baris 120 | 0,822 | konteks baris |
| gabungan + jendela 80 | 0,738 | semua di atas |

Perubahan lossless yang tersisa (separator `--` saat context 0) sudah diterapkan. Target 0,6× tidak tercapai tanpa kehilangan informasi. `TestGrepOutputTokens` menegakkan 0,6× dan gagal; CT-11 tetap `pending:F1`.

### Langkah berikut

1. Analisis kegagalan Windows `TestSymlinkEscapeRace` di #53 (keamanan).
2. Perbaiki dua bug test/Windows (`TestCleanOrphanTempsPattern` di FS case-insensitive; lock hard link via file ID). Bangun ulang stack dengan `ctsplit/build.sh` setelah memindahkan perbaikan ke referensi. Builder memakai `/tmp/ctsplit`: clone branch `f1-ct-split-tools` ke sana.
3. Budget CT-05/CT-07 `test-timing`: optimasi read page 1 MB / outline atau ukur ulang di runner.
4. Rebase `f1-ct-perf2-wip` ke `f1-ct-13`, lalu buka PR ke-14 (body wajib menyebut ADR-103; coverage dan parity belum dijalankan ulang setelah rebase).
5. Minta review auditor untuk #48–#61 (+ PR perf). Merge berurutan dengan metode rebase; ganti base PR berikutnya ke `main` setelah tiap merge.
6. CT-01/02/10/11 belum memenuhi target. Butuh keputusan owner: engine regex, CT-11 (opsi 0,74–0,78× dengan kehilangan informasi, atau revisi target lewat ADR).

### `internal/exec` (ADR-105)

| PR | Isi | Baris Go non-test (diff) | Hasil |
|---|---|---:|---|
| exec a | tipe hasil (`ExecStatus` ADR-082), lapisan proses per OS, capture | lihat PR | CI; menunggu auditor |

| Item | Implementasi | Test | Status |
|---|---|---|---|
| Runner pipe/pty (§A.1) | `start` (`proc_unix.go`, `proc_windows.go`), `sysAttr` | `TestProcPipeExit`, `TestProcStdinPipe`, `TestProcPTY`, `TestProcJobExit` | PR a |
| Kill pohon proses (§A.4) | `terminate`/`kill` ke pgid; `TerminateJobObject` | `TestProcGroupTerminate`, `TestProcGroupKillIgnoresTrap`, `TestProcJobKillTree` | PR a |
| Capture output | `capture` head 8 MiB + ring 8 MiB | `TestCaptureHeadTail`, `TestCaptureHugeWrite`, `TestCaptureSmall` | PR a |
| Loop timeout/cancel, sweep marker, env, mode one-shot, prompt interaktif, idle, ConPTY, summarize, P24 | — | — | PR berikutnya |

## Review keamanan — exec (tindak lanjut REVIEW-F1-SEC4, 2026-10-19)

**Status: temuan ditutup di kode; CI stack belum hijau penuh saat entri ini ditulis. Tidak ada PR exec/classify yang di-merge.** Auditor diminta memverifikasi perbaikan di bawah sebelum merge.

| Temuan | Perbaikan | Bukti/test |
|---|---|---|
| EX39-1 (MAJOR, hang PTY) | Master PTY di-`dup` setelah `File.Fd` terakhir, `O_NONBLOCK`, di-wrap `os.NewFile` (poller). `pty.Setsize` memakai slave, bukan master. Close kini membatalkan `Read` yang tertunda; `Run`/`Close` kembali walau proses lepas (`env -i setsid`) memegang slave. | `TestProcPTYCloseUnblocksRead`, `TestRunPTYEscapeeDoesNotHang` (leader exit + timeout) |
| EX39-2 (MINOR) | Leader suspended di-`Kill` sebelum `Wait` saat attach/resume gagal. | jalur `proc_windows.go` |
| EX39-3/EX40-1 (MINOR, docs) | A.4 diamandemen ke ADR-105 (reaper per pid, tanpa subreaper); residu `env -i`+setsid, sudo, `systemd-run --user`, `at`/cron, WMI/schtasks dicatat di TRUST-MODEL §3. | docs |
| EX42-1 (MAJOR, secret) | Secret cocok berdasarkan identitas binary (path absolut + `os.SameFile` dev/inode + size + mtime), hanya argv0 **tanpa separator**; binary di dalam `OneShot.Root` dikecualikan. Bukan lagi base name. | `TestSecretRefScope` (./env, PATH workspace, argv0 absolut → 0 secret) |
| EX42-2/EX62-1 (MAJOR) | argv0 ber-separator di-resolve sekali ke path absolut+clean; `Dir` relatif dibuat absolut sebelum spawn; ConPTY tidak lagi me-resolve terhadap cwd damnbot. | `TestLookPath`, `Prepare` argv0 absolut |
| EX42-3 (MINOR) | Mode (b) menolak C0 (selain tab), DEL, CR, NUL. | `TestSecretRefScope` (NUL/CR) |
| EX42-4 (MINOR) | Nama GAP-SEC-02/`env_allow`/proxy persis di Unix; lipat huruf hanya Windows. | `TestEnvAllowlist` (Path/Home/Lang/Http_Proxy) |
| EX45-1 (MAJOR, prompt) | `Result.Prompt` melewati `redact.Redactor` (pola + secret sesi via `SetRedactor`) dan hanya menyisakan rune printable, `\n`, `\t`; dipakai juga oleh sesi persisten. | `TestPromptRedactedAndSanitized`, `TestSessionPrompt` |
| EX47-1 (MINOR) | Escape tanpa terminator dibatasi 4 KiB; sisa output tetap terlihat; C0 (kecuali tab), DEL, C1 dibuang dari ringkasan. | `TestSummarizeUnterminatedEscape`, `TestSummarizePipeline/controls` |
| EX47-2 (MINOR) | Blob `Raw` memakai bentuk strip-ANSI yang di-redaksi ulang bila redaksi raw berbeda (secret terpisah SGR). | `TestSummarizeSplitSecretNotLeaked` |
| EX52-1 (MINOR) | A.10 mencatat `TestNoOrphans` hanya mengukur proses bermarker. | docs |
| EX62-1 (ConPTY output hilang) | Handle kreasi ConPTY ditutup setelah `CreateProcess` (bukan setelah `CreatePseudoConsole`); sebelumnya child kehilangan std handle console sehingga output perintah hilang. | `TestProcConPTY`, `TestRunConPTYWindows`, `TestRunConPTYPromptWindows` (Windows CI) |
| SEC5-1 (BLOCKER) | `Sessions.Exec` menolak `stdin` non-kosong (`ErrSessionStdin`): byte yang tidak dikonsumsi perintah tidak lagi dieksekusi shell sebagai perintah baru tanpa approval. | `TestSessionStdinRejected` |
| SEC5-2 (MAJOR) | Sentinelsesi `cmd` memakai `!cd!` dengan `/v:on` (delayed expansion) alih-alih `%cd%`, sehingga cwd ber-`&`/`|`/`>` tidak menjalankan perintah. | `TestCmdSentinelDelayedExpansion` |
| #63 sesi Windows/macOS | `${LASTEXITCODE}` (bukan `$LASTEXITCODE:`); frame protokol `\x1eDB:…\x1e` dibuang dari output sesi (frame `again` yang telat tidak lagi bocor ke perintah berikutnya). | `TestSessionWindows`, `TestStripFrames`, `TestSessionOutputFramesStripped` |

Yang **belum** dicakup: eksekusi runtime ConPTY/sesi Windows diverifikasi lewat CI (bukan mesin lokal); `TestNoOrphans` 200× dan benchmark spawn dibaca dari job CI; stack classify #41/#43/#44/#46 masih menunggu review auditor. Sisa F1 di luar stack tetap seperti daftar handoff di atas.

## Handoff codetools 2 (budget pause)

Status 2026-10-19. Branch stack ada di remote; `/tmp/damnbot-ct` (stack) dan `/tmp/damnbot-docs` (dokumen) boleh hilang.

### Head dan kondisi stack

| PR | Branch | Head | Kondisi |
|---|---|---|---|
| #48 | `f1-ct-01` | `9a61f9b` | CI berjalan (antrean runner); belum merge — `main` strict up-to-date |
| #49–#60 | `f1-ct-02` … `f1-ct-12` | chain hasil rebase di atas `main` `2046c24` | menunggu CI; tidak ada kegagalan yang diketahui |
| #61 | `f1-ct-13` | `c1150c9` | dispatch + parity + benchmark CT-04..07/09; codetools 4.800/4.800 scc LOC |
| #67 | `f1-ct-14` | `70e613b` | driver + `TestCodeToolsIDsComplete` + nightly |
| #68 | `f1-report-ct-security` | `c1adc4e` | entri Review keamanan (dokumen ini) |

Final stages #61/#67 **belum boleh merge** sebelum auditor menyetujui #68.

### Restack setelah `main` bergerak

Branch berantai (`base PR k = branch k−1`) dan `main` memakai strict status check. Untuk setiap merge, rebase branch berikutnya ke `main` baru:

```sh
git fetch origin main
git checkout f1-ct-NN
git rebase --onto origin/main <old-head-NN-1>   # atau cherry-pick commit tahap NN
git push --force-with-lease origin f1-ct-NN
```

Setelah #48 merge (rebase), branch #49 harus direbase ke `main`, lalu #50, dst. Jangan gunakan merge commit (metode merge owner: `gh pr merge <n> --rebase --delete-branch`).

### Sisa pekerjaan

1. Tunggu CI #48; merge berurutan. Tiap kali `main` bergerak, rebase PR berikutnya (di atas).
2. #61/#67: CI `perf` dan `test-timing` lulus lokal dengan warm-up `measure()`; `GrepToolC3` gagal 1× saat host menanggung benchmark C2 — ulangi di runner idle. `edit 1 MB` p95 di runner bersama pernah 104 ms (rata-rata, bukan ekor); pantau.
3. Kinerja grep belum memenuhi target: C1 literal 1,84× (target 1,2), C1 regex 2,68× (1,5), C3 literal 1,53×, C3 regex 1,64×; C3 glob 0,24× lulus. C2 sedang diukur (`/tmp/ct-c2.json` bila selesai). Gate `TestGrepSpeedVsRg` sengaja tetap merah; jangan longgarkan.
4. CT-10: search ×12,4 dan read ×15,9 lulus; patch ×2,2 p50 tetapi p95 maks 10,4 ms > 6 ms → belum lulus. CT-11 0,906× (limit 50) / 1,686× (default); target 0,6× tetap `pending:F1`.
5. CT27-9: repair mode macOS ada di `readBack` (bukan `ensureMode`); Windows hard-link lock memakai creation time NTFS (stabil, dibagi semua hard link). `codetools` tepat 4.800/4.800 — tambahan baris produksi harus disertai pemangkasan.
