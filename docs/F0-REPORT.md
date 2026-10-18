# F0-REPORT — laporan otomatis F0

Format sesuai `docs/HANDOFF-F0.md` §"Laporan pagi". Mesin: workstation (Kali, linux/amd64), **bukan** mesin referensi Q14. Toolchain: `go version go1.26.8 linux/amd64`. Python hermes: `Python 3.14.7` sistem, `HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1`, hermes `868b9f244f` (2026-10-17 04:38:45 −0700).

## Status

| # | Deliverable F0 (ROADMAP) | Status | Catatan |
|---|---|---|---|
| 7 | CI skeleton: lint, test -race, size-check, LOC, deps allowlist, tidy, verify, govulncheck, go-licenses, meta-test | selesai (mekanisme) | Meta-test terbaru @ `f917511`: **11/11 gate DETECTED**. `loc-ratio` diuji pada pohon sintetis dengan rasio 1,00; gate repo nyata tetap merah (TS-03). Di HEAD, `lint` dan `loc` **merah karena `internal/ctxdb`**: wrapcheck 19, unparam 2, unused 2; LOC 2.591 > 2.500. Untuk dua gate itu, meta-test memakai baseline pohon scaffold dan menandainya HEAD_RED. Worker persistence sudah diberi tahu. Workflow GitHub belum pernah dijalankan di Actions (tanpa remote). |
| 1 | `bench/budget` (P1, o200k_base offline) | selesai | tokenizer = tiktoken 0.14.0 persis (id token & jumlah); 13/13 angka gate BASELINE dalam ±5%; job CI `bench-budget`. `TestFixedOverhead` sisi Damn-Bot menunggu `internal/assemble` (F1). |
| 4 | Baseline hermes tanpa LLM | sebagian | Selesai: token overhead & schema bytes (#1). Probe manual (bukan artefak final): cold start CLI hermes 2,7–4,0 s per perintah (`--version` 4.041 ms, `config show` 2.834 ms, `sessions list` 3.241 ms, `status` 7.989 ms, `prompt-size` 6.752 ms); `chat` di PTY tanpa jaringan: output pertama ≈ 4,8 s, settle ≈ 6,0 s, RSS ≈ 131 MB (1 run). Belum: install size/time, baris config, langkah setup. |
| 5 | `bench/speed` | sebagian | `bench/speed/hermes_driver.py` ditulis (mode `prepare`/`inproc`/`pty`, dijalankan di `unshare -rn` = tanpa jaringan); runner Go (parse FEATURE-PARITY 854 baris → measured / alasan) **belum ditulis**, driver belum dijalankan penuh. Smoke LLM 10 task: **menunggu user**. |
| 6 | Korpus code tools C1–C3 + baseline `rg`/hermes | belum | `rg` lokal hanya 13.0.0 (`/usr/lib/code-oss/.../rg`), oracle spec 15.1.0 belum tersedia. |
| 2 | `bench/h2h` head-to-head berbasis LLM | menunggu user | butuh kredensial/biaya |
| 3 | Eval set E1–E10 + manifest | belum | di luar alokasi worker scaffold/bench |
| 8 | Persistence spike `internal/ctxdb` | sebagian | DDL 0001–0008 (verbatim SPEC §11), store/mode/lease+fencing/purge 4 fase/gc/trash/journal recovery/barrier/migrasi/restore + 86 subtest `TestScenario/S001..S086`, `TestKill9Chaos`, `TestMultiProcess`: **semua lulus** tanpa `-race` (exit 0). Belum: (a) `-race` lihat Bukti; (b) LOC non-test 2.688 > 2.500 (E-19, TS-P10); (c) `make lint` belum dibersihkan (wrapcheck/check-blank dll.); (d) protokol gateway worker + konsumen approval ditulis sebagai implementasi referensi di `_test.go` (TS-P09). |
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
| loc-ratio | pohon sintetis `internal/metaratio` 101:101 (baseline lulus) + 2.000 baris test | `LOC ratio` | DETECTED |
| deps-check | require langsung `github.com/stretchr/testify` | `tidak ada di deps.allow` | DETECTED |
| vuln | `gopkg.in/yaml.v2@v2.2.2` + `yaml.Unmarshal` | `GO-YYYY-NNNN` | DETECTED |
| licenses | modul lokal `example.com/metagpl` (LICENSE = GPL-3) di-link ke `cmd/damnbot` | `GPL` | DETECTED |
| tidy-check (`go mod tidy -diff`, E-46) | require `github.com/BurntSushi/toml` yang tidak dipakai | `BurntSushi/toml` | DETECTED |
| mod-verify (`go mod verify`, E-47) | 1 file `goleak@v1.3.0` dirusak di `GOMODCACHE` terisolasi `$work/modcache` | `has been modified` | DETECTED |

Run kedua: `scripts/ci-meta-test.sh` @ `f917511`, exit 1 (karena HEAD_RED), 40,4 s. Hasil: lint dan loc DETECTED (baseline scaffold; HEAD_RED); sembilan gate lain DETECTED. Pada run pertama (@ `3520e0f`), loc-ratio masih BASELINE_RED; tidy dan verify belum dicakup. Job `supply-chain` sekarang memanggil `make tidy-check` dan `make mod-verify`.

Belum dicakup meta-test: build-matrix 8 target, fuzz, coverage, deadcode, analyzer khusus `tools/analyzers` (belum ada; F1).

### bench/budget (#1)

Commit: `ea6e99f`. Modul terpisah `bench/go.mod` (`github.com/damnbot/damnbot/bench`, **0 dependency**). Isi:

- `bench/budget/o200k`: BPE o200k_base pure Go (pola pre-tokenisasi tiktoken dengan kelas Unicode `White_Space` eksplisit; `\s+(?!\S)` diganti pemindai manual karena RE2 tanpa lookahead). Rank di-embed gzip `o200k_base.tiktoken.gz` (1.692.645 B). Sumber: `https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken` (diunduh sekali 2026-10-18; tidak ada salinan lokal sebelumnya), SHA-256 file asli `446a9538cb6c348e3516120d7c08b09f57c36495e2acfffe59a5bf8b0cfb1a2d` (= `expected_hash` tiktoken) diverifikasi saat load.
- Golden `o200k/testdata/golden.json`: dibangkitkan `gen_golden.py` dengan tiktoken 0.14.0 di venv `/tmp/f0venv` (bukan dependency repo): 35 string sulit (Unicode, CJK, emoji ZWJ, kontraksi, CRLF, NBSP/U+3000, U+2028) → id token; 14 file snapshot → jumlah token.
- `bench/budget/hermes_extract.py`: ekstraksi offline meniru `hermes prompt-size` (`hermes_cli/prompt_size.py:_build_inspection_agent`, kredensial dummy, tanpa jaringan/LLM) + `tools.skills_sync.sync_skills()` (skill bawaan, seperti start `hermes`). Snapshot di `bench/budget/testdata/hermes-868b9f244f/{default,no_context_files}/`; repo hermes tetap bersih (`git status --short` kosong sebelum/sesudah).
- `bench/budget` (`Measure`, `LoadSnapshot`, `Compare`), `TestHermesBaselineReproduced`, `TestSnapshotInternallyConsistent`; artefak `bench/budget/results/hermes-868b9f244f.json` (`go run ./budget/cmd/budget`).

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `cd ~/Dokumen/hermes-agent && HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1 python3 …/bench/budget/hermes_extract.py /tmp/f0_extract` | 0 | 19 schema; tier stable/context/volatile 12.561/13.646/7.302 B |
| idem `--skip-context-files` → `/tmp/f0_extract_noctx` | 0 | context 1.582 B (snapshot git workspace saja) |
| `TIKTOKEN_CACHE_DIR=/tmp/f0_tok /tmp/f0venv/bin/python bench/budget/o200k/testdata/gen_golden.py` | 0 | tiktoken 0.14.0 |
| `cd bench && go vet ./... && go test -count=1 -race -v ./budget/...` | 0 | `TestEncodeMatchesTiktoken`, `TestCountMatchesTiktokenFiles` (14/14 file identik), `TestHermesBaselineReproduced`, `TestSnapshotInternallyConsistent` PASS |
| `make bench-budget` | 0 | |
| `cd bench && go run ./budget/cmd/budget -snapshot budget/testdata/hermes-868b9f244f > budget/results/hermes-868b9f244f.json` | 0 | `all_gates_ok: true` |

Reproduksi BASELINE-hermes.md (satuan "Bytes" BASELINE = karakter, lihat TS-06; token BASELINE = ceil(char/4)):

| Metrik | BASELINE | Harness | Δ | Gate ±5% |
|---|---:|---:|---:|---|
| stable (char) | 12.481 | 12.481 | 0,00% | ya |
| context (char) | 13.413 | 13.478 | +0,48% | ya |
| volatile (char) | 7.289 | 7.274 | −0,21% | ya |
| token est stable/context/volatile | 3.121/3.354/1.823 | 3.121/3.370/1.819 | 0,00/+0,48/−0,22% | ya |
| schema bytes (`json.dumps(get_tool_definitions())`) | 37.758 (§2) / 37.765 (§7) | 37.759 | +0,00% / −0,02% | ya |
| schema token est | 9.440 | 9.440 | 0,00% | ya |
| jumlah schema | 19 | 19 | persis | ya |
| `delegate_task` schema bytes | 4.671 | 4.671 | 0,00% | ya |
| **overhead per call tanpa AGENTS.md** (est) | 14.500 | 14.773 | +1,88% | ya |
| overhead per call dengan AGENTS.md (est; ceil(33.183/4)+9.440) | 17.736 | 17.750 | +0,08% | ya |
| prompt floor tanpa AGENTS.md (char) | 20.240 | 21.331 | +5,39% | informatif (TS-08) |

Token o200k_base nyata (belum ada di BASELINE): system prompt 7.716 (stable 2.613, context 3.410, volatile 1.693), tanpa AGENTS.md 4.692; schema `get_tool_definitions` 9.127, `agent.tools` (yang dikirim AIAgent; `tool_search` lebih ringkas) 8.908. **Overhead per call o200k: 13.819 (tanpa AGENTS.md) – 16.843 (dengan AGENTS.md)**; via `agent.tools`: 13.600 – 16.624.

## Skenario persistence

Semua skenario = subtest `go test ./internal/ctxdb -run 'TestScenario/S0NN'` (DB SQLite temp nyata, jam/proses diinjeksi; proses OS nyata untuk #26, #37, #63, #62, #74, #85). Status = hasil `go test -count=1 ./internal/ctxdb/` @ `0f5f91d` (exit 0).

| # | Subtest | Status | # | Subtest | Status |
|---:|---|---|---:|---|---|
| 1 | S001_SigstopTakeoverThenSigcont | lulus | 44 | S044_StartStampComparedVerbatim | lulus |
| 2 | S002_StaleHolderWakesAfterLabel | lulus | 45 | S045_ReapplyPhase0Bounded | lulus |
| 3 | S003_SleepAndWallClockStep | lulus | 46 | S046_WakeBeforeOwnerHeartbeat | lulus |
| 4 | S004_RebootMidTurn | lulus | 47 | S047_DeleteSessionsBlobsRejected | lulus |
| 5 | S005_TwoRecoverersSameJournal | lulus | 48 | S048_FreeJSONSessionIdResidue | lulus (residu terdokumentasi diverifikasi) |
| 6 | S006_RecoverySkipsPurgingSession | lulus | 49 | S049_MigrationFencesOldBinary | lulus |
| 7 | S007_CrashAtEveryAppendStep | lulus | 50 | S050_MCPServeMessagesSendUnderGuards | lulus |
| 8 | S008_ForkFromLabelledOrDuringPhase0 | lulus | 51 | S051_SigtstpReleasesLocks | lulus |
| 9 | S009_UndoInPurgingSession | lulus (bagian DB; residu jendela file = caller) | 52 | S052_WrongScopeOrIdentity | lulus |
| 10 | S010_InboundToLabelledSession | lulus | 53 | S053_TwoResponders | lulus |
| 11 | S011_OutboxSkipsLabelledSession | lulus | 54 | S054_ConsumerCrashBeforeCommit | lulus |
| 12 | S012_CronKanbanAutoWritesRejected | lulus | 55 | S055_RemoteEditNeverExecuted | lulus |
| 13 | S013_ACPHoldsSessionExcluded | lulus | 56 | S056_SigtstpLongStopFenced | lulus |
| 14 | S014_TwoPurgesConcurrent | lulus | 57 | S057_OldBinaryOpensDuringMigration | lulus |
| 15 | S015_LargePurgeLockBudget | lulus tanpa `-race` (20k event: max tx 81 ms, duty 19,6%); gagal di bawah `-race` (max tx 1,03 s) | 58 | S058_UpgradeMigratesWhileOldServeRuns | lulus |
| 16 | S016_RestoreWhileServeAndTUIRun | lulus | 59 | S059_EditToDangerousClass | lulus |
| 17 | S017_AutoRestoreBlockedWriterTimeout | lulus | 60 | S060_InvalidRowDeniedConsumed | lulus |
| 18 | S018_RestoreClosesOpenJournals | lulus | 61 | S061_PurgeAfterWakeOrCtrlZ | lulus |
| 19 | S019_RestoreBeforePurgeReapplies | lulus | 62 | S062_CtrlZDuringExecToolGroup | lulus (Linux) |
| 20 | S020_SalvageModeRestoreGuards | lulus | 63 | S063_DbWriteStalledReport | lulus (pelapor = referensi test) |
| 21 | S021_GCWriterPurgeTrashRace | lulus | 64 | S064_CancelBeforeDelivery | lulus |
| 22 | S022_TUIOpenForDaysIdle | lulus | 65 | S065_ReplayKeepsRequestID | lulus |
| 23 | S023_PIDReuseAndNamespace | lulus | 66 | S066_TokenRevokedBeforeConsume | lulus |
| 24 | S024_PurgeOwnerDiesTakeover | lulus | 67 | S067_OldServeRestartedSchemaNewer | lulus |
| 25 | S025_ShortProcessPurgePending | lulus | 68 | S068_RetryWithoutKeyTwoRequests | lulus (residu terdokumentasi diverifikasi) |
| 26 | S026_RestorerKilledMarkerStale | lulus (proses nyata kill -9) | 69 | S069_QueuedInboundSurvivesRestart | lulus |
| 27 | S027_WallStepThenSecondProcess | lulus | 70 | S070_SpoolIngestThenWorker | lulus |
| 28 | S028_MaintModeFailClosedAndAbort | lulus (NULL lewat driver terpisah; abort = `runtime.Goexit`, bukan panic — E-24) | 71 | S071_MigrationRefusedKeepsOldServe | lulus |
| 29 | S029_CrossSessionEventRef | lulus | 72 | S072_SpoolTruncatedLineNotAcked | lulus |
| 30 | S030_InboxShortLeaseImportBatch | lulus | 73 | S073_SameKeyDifferentArgs | lulus |
| 31 | S031_RestoreCancelsRunningTurn | lulus | 74 | S074_OrphanStoppedToolGroupKilled | lulus (Linux, pgid nyata) |
| 32 | S032_TUIKilledServeScanner | lulus | 75 | S075_CrashBetweenPollerAndWorker | lulus (100 iterasi) |
| 33 | S033_SigstopAfterToolPendingCommit | lulus | 76 | S076_WorkerVsPurgeLabel | lulus |
| 34 | S034_AutoRefRemovedByPurge | lulus | 77 | S077_InboundUpdateGuard | lulus |
| 35 | S035_TwoRestoresConcurrent | lulus | 78 | S078_ServeMigratorForcesAfter10Min | lulus |
| 36 | S036_PurgePausesForBarrier | lulus | 79 | S079_InvalidPayloadNotSilent | lulus dengan perbaikan AF-01 (usulan ADR) |
| 37 | S037_SigstopInsideWriteTx | lulus (proses nyata SIGSTOP; `db_busy` typed) | 80 | S080_CrashAfterWorkerCommitAnswered | lulus dengan perbaikan AF-02 |
| 38 | S038_DiskFullLedgerAndBatch | lulus (`max_page_count` → SQLITE_FULL) | 81 | S081_PerChatOrderInsertion | lulus dengan perbaikan AF-03 (urut `rowid`) |
| 39 | S039_LeaseFromPreviousBootNoBootID | lulus | 82 | S082_ChatSessionBusyNotice | lulus dengan perbaikan AF-04 |
| 40 | S040_TimeNamespaceMismatchFencingHolds | lulus (deteksi + fencing) | 83 | S083_PurgeInboundAttachment | lulus dengan perbaikan AF-05 |
| 41 | S041_ResponseAfterTurnEnd | lulus | 84 | S084_SpoolDuringDrainBarrier | lulus dengan perbaikan AF-06 |
| 42 | S042_ForeignDataDirPurgesLabelled | lulus | 85 | S085_LeaderDeadNohupChildKilled | lulus dengan perbaikan AF-07 (scan anggota pgid) |
| 43 | S043_StaleAnswerForPreviousRequest | lulus | 86 | S086_MigratorRestartsAccumulate | lulus dengan perbaikan AF-08 (`run/migrate_blocked.json`) |

Ringkas: 86/86 ditulis; 86 lulus tanpa `-race`; di bawah `-race` #15 gagal (budget waktu absolut tidak bermakna dengan instrumentasi race). `TestKill9Chaos` (15 ronde SIGKILL proses anak nyata saat menulis; cek integrity_check, foreign_key_check, hash-chain, I9, purge tertunda selesai) dan `TestMultiProcess` (4 proses OS, 150 iterasi, sesi bersama + purge + gc; 0 `db_busy`, jumlah event cocok) lulus.

### Persistence spike (#8) — Bukti

Commit: `ad458b6` (DDL), `529c947` (core), `7d9f025` (purge), `1e2c128` (restore + tes), `0f5f91d` (langkah purge sebagai SQL bernama `queries.sql`).

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `go vet ./internal/ctxdb/` | 0 | |
| `go test -count=1 ./internal/ctxdb/` | 0 | `ok … 20.7s` — TestScenario (86 subtest), TestKill9Chaos, TestMultiProcess, TestSmoke* |
| `go test -count=1 -race -v ./internal/ctxdb/` @ `1e2c128` | 1 | TestKill9Chaos PASS (18,2 s), TestMultiProcess PASS (26,3 s), 0 DATA RACE, 0 goleak; TestScenario FAIL hanya S015 (`lock budget: max 1.029s`), total 522 s |
| `go test -count=1 -race ./internal/ctxdb/` @ `0f5f91d` | lihat baris berikut | (diisi setelah run selesai) |
| `scc --no-gen` non-test `internal/ctxdb` | — | 2.688 LOC > budget 2.500 |

## Temuan spec

| ID | Temuan | Asumsi yang dipakai | Usulan ADR |
|---|---|---|---|
| TS-01 | Path modul Go tidak ditetapkan di SPEC/docs (hanya `ghcr.io/damnbot/damnbot`). | `github.com/damnbot/damnbot` | ADR: tetapkan path modul + vanity import bila perlu. |
| TS-02 | P13 menulis "MB" tanpa basis (10^6 vs 2^20). | Konservatif: 1 MB = 1.000.000 B (`scripts/size-check.sh`, override `FULL_MAX`/`MINIMAL_MAX`). | ADR errata: tulis budget dalam byte atau MiB. |
| TS-03 | E-70 (rasio test:source 0,8–1,2) sebagai gate `loc-budget` mustahil dipenuhi di awal (scaffold 121:29; spike persistence dominan test). Gate merah permanen sampai F1 berjalan, sehingga meta-test gate ini tidak bermakna. | Gate tetap ditegakkan (`make loc-ratio`, step terpisah di job `loc-budget`); tidak dilemahkan. | ADR: E-70 berlaku bila Go non-test ≥ 5.000 LOC (atau mulai akhir F1); di bawah itu laporan saja. |
| TS-04 | E-39: tag "tidak pernah dipasang lewat `GOFLAGS`", tetapi `go-licenses check` v2.0.1 tidak punya flag `-tags` (`--help` tanpa opsi tag). | Target `licenses` memakai `GOFLAGS=-tags=goolm` (tanpa `-tags` lain di command line, jadi tidak ada penimpaan). | ADR errata E-39: pengecualian bernama untuk go-licenses. |
| TS-05 | E-39 "tag `goolm` wajib di semua job" + build `CGO_ENABLED=0`, sedangkan `go test -race` (E-55) butuh cgo. | Job `test` memakai `CGO_ENABLED=1` hanya untuk `-race`; build rilis/size tetap `CGO_ENABLED=0`. | ADR errata: nyatakan eksplisit bahwa `-race` = pengecualian CGO. |
| TS-06 | BASELINE-hermes.md §2 kolom "Bytes" sebenarnya jumlah **karakter** Python (`len(str)`): tier stable = 12.481 karakter persis, byte UTF-8 = 12.561. | Harness membandingkan karakter; byte UTF-8 juga dilaporkan. | ADR errata BASELINE: ganti judul kolom jadi "Char". |
| TS-07 | Token di BASELINE §2 dan SCORECARD §1.1 ("14.500–17.700 tok") = estimasi ceil(char/4), bukan o200k_base, padahal SCORECARD/P1 mengukur dengan o200k. o200k nyata: 13.819–16.843 (schema dihitung sebagai teks JSON). | Gate reproduksi F0 memakai metode BASELINE (char/4); angka o200k dilaporkan terpisah sebagai baseline P1/SCORECARD. | ADR: baseline SCORECARD [I] Fixed overhead = 13.819–16.843 tok o200k (target ≤ 0,35× → ≤ 4.836 tok vs 5.075 dari angka lama); tetapkan juga bahwa schema dihitung sebagai JSON teks (format internal provider tidak teramati). |
| TS-08 | "Floor tanpa AGENTS.md (prompt 20.240 B)" tidak menjelaskan cara ukur; sisa tier context tanpa AGENTS.md = snapshot git workspace (branch, 3 commit terakhir, path) yang bergantung mesin/commit → +5,39% di sini. Angka overhead (gabungan schema) tetap +1,88%. | Baris ini informatif, bukan gate. | ADR errata BASELINE §2: definisikan floor = `build_system_prompt_parts` dengan `skip_context_files=True` di cwd root hermes `868b9f244f`, angka 21.331 char. |
| TS-P01 | AF-01..AF-08 (REVIEW-1.0) terbuka di spec frozen. | Spike menerapkan usulan auditor apa adanya: `ON CONFLICT DO NOTHING` (bukan `OR IGNORE`); `turn_start`+journal di transaksi worker; urutan antrean per chat = `rowid`; balasan `__system__` sekali saat sesi chat dipegang proses lain; langkah 11 purge menghapus `blob_refs` owner `gateway_inbound`; spool untuk semua inbound tanpa redelivery; kill pgid berdasar anggota hidup (`/proc`); timer migrator di `run/migrate_blocked.json`. | ADR errata AF-01..AF-08 sesuai usulan REVIEW (semua lulus test #79–#86). |
| TS-P02 | Tidak ada yang ditetapkan membuat baris `sessions('__system__')`, padahal FK `events`/`blob_refs` memerlukannya. | Setiap writer saat open: `INSERT … ON CONFLICT DO NOTHING`. | ADR: baris `__system__` di `0001_core.sql` (atau aturan open). |
| TS-P03 | `dmb_maint()`/`dmb_lease_token()` per koneksi; modernc mendaftarkan fungsi per-`Driver`, bukan per koneksi; `SQLITE_INNOCUOUS` tidak dapat diset. | Satu `sqlite.Driver` privat per Store (writer + reader); trigger berjalan karena `trusted_schema` default ON. Commit/rollback hook tersedia (`HookRegisterer`) — [INFERENCE SPEC §11.11.3] terverifikasi. | ADR: catat mekanisme ini; tambah `PRAGMA trusted_schema` eksplisit. |
| TS-P04 | Validator payload "kunci lain berisi hex 64 → ditolak" bertentangan dengan `approval_request.preview_sha`/`args_sha` dan `payload_sha` carrier. | Daftar tertutup kunci non-ref: `preview_sha`, `args_sha`, `payload_sha`. | ADR errata §11.6 konvensi kunci. |
| TS-P05 | Pemetaan RAISE → kode tertutup hanya mungkin dengan membaca teks error SQLite (bertentangan dgn "klasifikasi hanya errors.Is"). | Satu fungsi `classify`: kode `SQLITE_CONSTRAINT_TRIGGER` (1811) + kecocokan **persis** teks RAISE. | ADR: kecualikan `ctxdb.classify` dari `nostringclassify`. |
| TS-P06 | Busy handler SQLite tidak adil: penulis yang terus menulis tanpa jeda dapat membuat penulis lain kehabisan `busy_timeout` (direproduksi: purge mendapat SQLITE_BUSY saat writer lain menulis berturut-turut). P19 ("0 `SQLITE_BUSY` ke user") bergantung pada laju penulis. | Test #15 memakai laju realistis (1 event/10 ms). | ADR: retry `BEGIN IMMEDIATE` berjitter di writer goroutine (netx/retry) atau antrean writer lintas proses. |
| TS-P07 | "dmb_maint panic → reset": E-24 melarang `recover` di `internal/`, jadi panic mematikan proses; yang bisa diuji adalah abort goroutine. | Reset mode di `defer` + commit/rollback hook; diuji dengan `runtime.Goexit`. | Errata teks TestMaintResetOnPanic. |
| TS-P08 | Budget waktu I11 (≤ 200 ms/tx) tidak bermakna di bawah `-race` (instrumentasi 5–10×), sedangkan E-55 menjalankan semua test dengan `-race`. | Test tetap ketat → merah di `-race` (jujur, tidak di-skip). | ADR: gate kinerja (I11, BenchmarkAppend*) dijalankan tanpa `-race` di job terpisah. |
| TS-P09 | E-06 melarang modul gateway mengimpor ctxdb, tapi protokol poller/worker/outbox/approval (SELF-HEALING §5.1, §11.11.10) memerlukan transaksi ctxdb; lokasi produksinya tidak ditetapkan. | Spike: implementasi referensi di `gateway_ref_test.go`/`approval_ref_test.go` di atas `Tx` publik (guard DB tetap menegakkan invarian). | ADR: tetapkan lokasi (ctxdb vs `modules/host` storage) + budget. |
| TS-P10 | Budget E-19 `ctxdb` 2.500 LOC terlalu kecil untuk cakupan §11 (purge 31 langkah, gc, barrier, restore, migrasi, recovery). Terukur 2.688 walau langkah purge sudah dipindah ke `queries.sql` (dikecualikan budget) dan protokol gateway/approval ke test. | Dilaporkan; gate `loc` merah. | ADR: budget `ctxdb` 3.200 (atau pecah `ctxdb/maint` 700). |

## Blocker

| Item | Butuh keputusan user |
|---|---|
| Smoke LLM 10 task, `bench/h2h` LLM | Kredensial + izin biaya (menunggu user). |
| Workflow GitHub Actions | Belum ada remote; `ci.yml` hanya divalidasi lewat Makefile lokal yang sama. |
