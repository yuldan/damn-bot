# F0-REPORT — laporan otomatis F0

Format sesuai `docs/HANDOFF-F0.md` §"Laporan pagi". Mesin: workstation (Kali, linux/amd64), **bukan** mesin referensi Q14. Toolchain: `go version go1.26.8 linux/amd64`. Python hermes: `Python 3.14.7` sistem, `HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1`, hermes `868b9f244f` (2026-10-17 04:38:45 −0700).

## Status

| # | Deliverable F0 (ROADMAP) | Status | Catatan |
|---|---|---|---|
| 7 | CI skeleton: lint, test -race, test-timing, size-check, LOC, deps allowlist, tidy, verify, govulncheck, go-licenses, meta-test | selesai (lokal) | Setelah keputusan user (ADR-048..051): **semua gate hijau** di `6ced31d` dan meta-test **12/12 DETECTED** pada baseline HEAD (tanpa fallback scaffold); gate baru `timing` (`make test-timing`, job CI `test-timing`). Rincian §Verifikasi akhir (HEAD `6ced31d`). Run GitHub Actions di §GitHub Actions memakai HEAD lama (`3c98c11`); run untuk HEAD baru belum diamati di laporan ini. |
| 1 | `bench/budget` (P1, o200k_base offline) | selesai | tokenizer = tiktoken 0.14.0 persis (id token & jumlah); 13/13 angka gate BASELINE dalam ±5%; job CI `bench-budget`. `TestFixedOverhead` sisi Damn-Bot menunggu `internal/assemble` (F1). |
| 4 | Baseline hermes tanpa LLM (+ startup interaktif dengan LLM) | selesai, dengan pengecualian | Artefak `bench/speed/results/hermes-868b9f244f-nollm.json`. Isinya: overhead token dan schema (#1); startup frame pertama `hermes chat` p50 3.369 ms / p95 3.595 ms; `--version` p50 1.384 ms; RSS CLI median 106,8 MB; install 411,7 MB dan 33,5 s; config minimum 2 baris; 6 langkah setup. TS-11 tertutup lewat `bench/speed/results/hermes-868b9f244f-llm.json` (5 run): prompt-ready p50 15.407 / p95 19.724 ms; idle RSS pohon proses p50 492,9 / p95 495,6 MB; respons pertama p50 7.032 / p95 7.530 ms (gpt-5.4-mini lewat proxy rekam). Pengecualian: waktu install resmi dari VM bersih tidak diukur. Dimensi berbasis LLM lainnya diukur pada fase yang ditetapkan ADR-065 (Accepted; rincian terukur / fase per dimensi). |
| 5 | `bench/speed` + smoke LLM | selesai | Ke-854 baris FEATURE-PARITY terklasifikasi (`TestEveryRowClassified`): **163 terukur**, 674 tidak terukur dengan alasan, 17 dropped. Dari 163 baris terukur, 27 adalah proxy `<cmd> --help`. Artefak: `bench/speed/results/hermes-868b9f244f.json`. Smoke LLM 10 task × 1 run (gpt-5.4-mini): 0 error harness, 10/10 lulus (informatif), US$0,0238 (§Smoke LLM). |
| 6 | Korpus code tools C1–C3 + baseline `rg`/hermes | selesai (baseline) | `bench/corpora.lock`: C1 = hermes `868b9f24…` (20.076 file, 224,8 MB); C2/C3 sintetis deterministik. rg 15.1.0 (oracle spec) dijalankan pada C1/C2/C3 dengan 40 literal, 40 regex, dan 10 glob × 20 run. Tool hermes di C1: `search_files` 50, `read_file` 10, `patch` 10. Artefak `bench/codetools/results/f0-baseline.json`. Catatan TS-12. |
| 2 | `bench/h2h` head-to-head berbasis LLM | selesai untuk F0 (ADR-062) | Proxy rekam + runner + grader selesai; run hermes 10 task: **0 error harness**, rekaman lengkap (§Smoke LLM) — memenuhi acceptance F0 smoke LLM untuk hermes (ADR-062). Sisi Damn-Bot pindah ke acceptance F1: run pertama `damnbot run` pada 10 task yang sama lewat `bench/h2h`; cap biaya LLM F1 total US$2,00 (ADR-062). |
| 3 | Eval set E1–E10 + manifest | selesai | `bench/evals/`: evalset/v1 530 task (E1 160 … E10 100) + `MANIFEST.sha256`; ID SWE-bench Verified & Terminal-Bench di-pin (URL + revisi + SHA-256); `go test ./evals/...` (modul bench) lulus; 60 task coding internal diverifikasi dengan toolchain nyata. |
| 8 | Persistence spike `internal/ctxdb` | selesai | DDL 0001–0008 (SPEC §11 + errata V-13/V-15, ADR-054), store/mode/lease+fencing/purge 4 fase/gc/trash/journal recovery/barrier/migrasi/restore + 86 subtest `TestScenario/S001..S086`, `TestKill9Chaos`, `TestMultiProcess`: **lulus di `make test` (`-race`) dan `make test-timing`**. S015: budget I11 ditegakkan di `test-timing` (5.000 event, max tx 53 ms, duty 12,1 %); di `-race` semua cek kebenaran jalan pada korpus 100 event (ADR-048). LOC 2.610 / 2.800 (ADR-049). Protokol gateway/approval: lokasi produksi `internal/gateway` (F4), referensi tetap di `_test.go` (ADR-053). GO-2026-6604 tertutup dengan go1.26.9 (ADR-051). |
| 9 | Errata backlog ADR-047 | selesai | **38/38 item** punya ADR Accepted (dikoreksi ADR-063; klaim lama "33/33" melewatkan 5 MINOR tabel backlog ADR-037, AUD-002): AF-01..AF-08 → ADR-052 (#79–#86 lulus); 18 V-item → ADR-054 (V-13/V-15 diimplementasikan, `TestErrataV13IdentityStrengthRequired`, `TestErrataV15SkillNameNormalised` lulus); V-07, V-09, V-14, V-18, V-23, V-24, V-31 → ADR-055..061 (Accepted 2026-10-18, keputusan user; semuanya cakupan F1+ — F1/F2/F4/F5/F8 — tanpa kode F0 terdampak, jadi tanpa test F0); m-15, N-15, N-24, N-33, V-05 → ADR-063 (N-33: `TestParityPhaseColumn` lulus, ADR-064; lainnya test F1–F7 bernama di ADR-063). AF-01/AF-02 tertutup sebelum F4. Teks ADR-048..063 sudah diterapkan ke SPEC, ROADMAP, SELF-HEALING, TRUST-MODEL, CROSS-CUTTING, ENGINEERING, EXEC-AND-CODING, CODE-TOOLS, SCORECARD, MITIGATION-MATRIX, FEATURE-PARITY (setiap passage mengutip ADR; "Aturan penerapan" tiap ADR mendaftar file). |

## Verifikasi akhir (HEAD `6ced31d`)

Dijalankan 2026-10-18 di workstation, toolchain **go1.26.9** linux/amd64 (`GOTOOLCHAIN=auto` dari `go 1.26.9` di `go.mod`, ADR-051). Kode root module identik di `622e66b`..`6ced31d` (hanya `scripts/ci-meta-test.sh` berubah di `6ced31d`); commit `cb3bdb6`/`8afc362`/`4bd7c74` (worker lain) hanya menyentuh `bench/h2h` dan laporan ini. `git status --short`: `?? sisasisa` saja. Log: `/tmp/f0v2_*.log`.

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `make lint` | 0 | `0 issues.` |
| `make test` (`-race -shuffle=on`) | 0 | `cmd/damnbot`, `internal/archtest`, `internal/ctxdb` ok (174,6 s; total 2 m 56 s); 0 data race. S015 di `-race`: korpus 100 event, max tx 102 ms, duty 9,1 %, 0 `db_busy`, integrity/FK/hash chain ok (budget dilaporkan, tidak ditegakkan — ADR-048) |
| `make test-timing` (`DAMNBOT_TIMING=1`, tanpa `-race`) | 0 | semua paket ok (ctxdb 31,7 s). S015: 5.000 event, max tx 53 ms ≤ 200, duty 12,1 % ≤ 20; `TestCloseCheckpointDoesNotBlockWriters` ≤ 500 ms; S026 ≤ 10 s |
| `make loc` | 0 | `ctxdb 2610 / 2800` (ADR-049); TOTAL 2.639 / 190.000 |
| `make loc-ratio` | 0 | `3444:2639 = 1.31` → `LOC ratio report-only: Go non-test 2639 < 5000 (ADR-050)` |
| `make deps-check` | 0 | `TestDirectDeps`, `TestDirectRequiresParser` ok |
| `make tidy-check` | 0 | tanpa diff |
| `make mod-verify` | 0 | `all modules verified` |
| `make vuln` | 0 | `No vulnerabilities found.` (GO-2026-6604 tertutup oleh go1.26.9) |
| `make licenses` | 0 | hanya warning `non-Go code` (x/sys, modernc libc) |
| `make size-check` | 0 | full 1.671.330 B / 52.000.000; minimal 1.671.330 B / 22.000.000 |
| `scripts/ci-meta-test.sh` @ `6ced31d` | 0 | **12/12 DETECTED** (lint, test, race, timing, size-check, loc, loc-ratio, deps-check, vuln, licenses, tidy-check, mod-verify), semua baseline HEAD lulus tanpa fallback scaffold; 14 m 21 s. Run sebelumnya @ `622e66b` juga 12/12 (exit 0), tetapi trap gagal menghapus toolchain read-only di modcache terisolasi; diperbaiki di `6ced31d` (toolchain lokal + `chmod` sebelum `rm`), 0 sisa direktori |
| `cd bench && go vet ./... && go test -count=1 ./...` | 0 | budget, budget/o200k, evals, gencorpus, h2h, speed ok |

Gate meta-test baru: `timing` — test ctxdb yang mengukur operasi nyata 300 ms terhadap `checkBudget` 200 ms, penanda `timing budget meta`. `loc-ratio` kini diuji pada pohon sintetis di atas ambang ADR-050 (5.000:5.000 lulus; +2.000 baris test → gagal, penanda `LOC ratio test:source`).

## Verifikasi akhir sebelumnya (HEAD `ad57fa1`, historis)

Dijalankan 2026-10-18 di workstation (go1.26.8 linux/amd64). `git status --short` hanya menampilkan `?? sisasisa` (bukan milik F0; dibiarkan). Log ada di `/tmp/f0_final_*.log`.

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `git status --short` | 0 | `?? sisasisa` saja |
| `git log --oneline \| head -40` | 0 | 31 commit; `f81a549` (spec) … `ad57fa1` |
| `make lint` | 0 | `0 issues.` |
| `make test` | 2 | `cmd/damnbot` ok, `internal/archtest` ok; `internal/ctxdb` FAIL hanya `TestScenario/S015_LargePurgeLockBudget` (max tx 210 ms > 200 ms, 3 txn; TS-P08). Diulang @ `8c1d6fc`: exit 2, tetap hanya S015 (max tx 235 ms, 3 txn > 200 ms, duty 8,0%), 0 data race |
| `make loc` | 2 | `LOC GAGAL ctxdb 2610 > 2500` (TS-P10); TOTAL 2.639 / 190.000 ok |
| `make loc-ratio` | 2 | `3391:2639 = 1.28` > 1,20 (E-70, TS-03) |
| `make deps-check` | 0 | `TestDirectDeps`, `TestDirectRequiresParser` ok (diulang setelah `8c1d6fc`: tetap 0) |
| `scripts/ci-meta-test.sh tidy-check mod-verify` @ `8c1d6fc` | 0 | `tidy-check DETECTED`, `mod-verify DETECTED` (sebelum commit: tidy-check BASELINE_RED karena salinan HEAD masih tidak tidy) |
| `make tidy-check` | 0 | hijau setelah `8c1d6fc` (`go mod tidy`: `golang.org/x/sys` jadi require langsung, dipakai `internal/ctxdb/env_linux.go`); sebelumnya exit 2 di `ad57fa1` |
| `make vuln` | 3 | 1 vuln terjangkau: GO-2026-6604 (stdlib `os.Root`, Windows, fixed go1.26.9) via `internal/ctxdb`; 12 vuln di modul tidak terjangkau |
| `make licenses` | 0 | hanya warning `non-Go code` (x/sys, modernc libc) |
| `make size-check` | 0 | full 1.671.330 B / 52.000.000; minimal 1.671.330 B / 22.000.000 |
| `scripts/ci-meta-test.sh` | 1 | 10/11 DETECTED: lint, size-check, loc-ratio, deps-check, licenses, mod-verify langsung; test, race, loc, vuln via baseline scaffold (HEAD_RED). `tidy-check` = BASELINE_RED (pohon scaffold juga tidak tidy karena `ctxdb` dibuang); 419 s |
| `cd bench && go vet ./... && go test -count=1 ./...` | 0 | budget, budget/o200k, evals, gencorpus, speed ok |

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

### bench/speed + baseline hermes tanpa LLM (#4, #5)

Commit: `a3823a4`, `92f5270`. Modul `bench/speed`:

- `ParseParity`: membaca 854 baris dan menangani `\|` di dalam sel.
- `Classify`: memetakan setiap baris ke op terukur atau alasan, dengan urutan op → alasan per ID → rentang → proxy `--help` (CLI) → alasan per prefix.
- `Runner`: setiap proses hermes berjalan di `unshare -rn` dengan hanya `lo` yang dinaikkan. Tidak ada rute keluar, jadi panggilan LLM/HTTP mustahil. HOME/HERMES_HOME di `/tmp/hm_f0_speed`.
- Tiga jenis pengukuran:
  - CLI: cold start proses, 1 warm-up + 5 run, ditambah `ru_maxrss`.
  - Op tool/slash/py: in-process lewat `hermes_driver.py`, satu proses per op dengan timeout 5 menit, 1 warm-up + 20 run.
  - PTY: startup `chat`, 10 run.

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `cd bench && go test -count=1 ./...` | 0 | `TestParseParityCounts` (854/17), `TestEveryRowClassified`, gencorpus, budget |
| `/tmp/f0_speed_bin -hermes ~/Dokumen/hermes-agent -out speed/results/hermes-868b9f244f.json` | 0 | 2.388 s wall |
| idem `-only TOOL-046,TOOL-047,CLI-032,OPS-011,_startup_chat` | 0 | ukur ulang setelah perbaikan (task_id per run, `ok_exit` doctor = 1, sampel startup = frame pertama) |
| `hermes config set model …` + `config set OPENROUTER_API_KEY …` di HERMES_HOME kosong (netns) | 0/0 | config.yaml 1 baris + .env 1 baris |
| `pip install -r <45 dependency inti>` ke venv `/tmp/f0_hvenv` | 0 | 33,45 s; venv 186.730.930 B, 68 paket; source tree (`git archive`) 224.938.704 B. `pip install <hermes>` sendiri exit 1: build guard hermes menolak wheel/sdist |

Hasil (`summary`):

| Jenis op | n | median p50 | min | max |
|---|---:|---:|---:|---:|
| CLI cold start | 80 | 3.130 ms | 1.384 ms (`--version`) | 9.640 ms (`doctor`) |
| tool in-process | 25 | 20,2 ms | 0,22 ms | 87,3 ms (`search_files` di `agent/`) |
| slash in-process | 51 | 0,78 ms | 0,04 ms | 174,6 ms (`/help`) |
| py (assembly, registry) | 7 | 3,5 ms | 0,02 ms | 38,3 ms |

Contoh p50/p95:

| Op | p50 / p95 |
|---|---|
| `read_file` 400 baris | 9,37 / 9,78 ms |
| `patch` | 3,09 / 3,48 ms |
| `write_file` | 2,45 / 2,65 ms |
| `terminal true` | 33,9 / 47,1 ms |
| `build_system_prompt_parts` | 6,11 / 10,34 ms |
| `hermes status` | 4.478 / 4.491 ms |
| HermesCLI init | 452 ms |

Alasan "tidak terukur" per kategori:

| Kategori | Baris |
|---|---:|
| Provider (komponen b; butuh fake provider F1) | 77 |
| MCPX (server eksternal) | 65 |
| TSET (bukan operasi) | 61 |
| TOOL (browser, web, platform, desktop, diintersep agent loop) | 78 |
| SLASH (LLM, interaktif, jaringan) | 50 |
| Kategori lain | daftar per baris di artefak |

Ada 6 op yang gagal karena offline dan dicatat apa adanya sebagai alasan:

| Op | Penyebab |
|---|---|
| `webhook list` | butuh konfigurasi platform |
| `security audit` | OSV butuh jaringan |
| `pets list` | manifest remote |
| `connectors list` | layanan Nous |
| `usage` ×2 | butuh kredensial |

Total tidak terukur: 674 baris.

### Eval set E1–E10 (#3) — Bukti

Lokasi `bench/evals/` (SCORECARD §2.2 menulis `eval/` → TS-E01). Generator deterministik `bench/evals` (Go, stdlib saja, tanpa LLM): `cd bench && go run ./evals/cmd/gen-evals -dir evals` membangun `v1/<set>.jsonl` dari daftar ter-pin di `sources/` dan menulis `MANIFEST.sha256` (format `sha256sum`; `cd bench/evals && grep -v '^#' MANIFEST.sha256 | sha256sum -c` → OK).

| Set | Jumlah | Isi | Grader |
|---|---|---|---|
| E1 | 160 | 100 SWE-bench Verified + 60 internal (10 jenis bug × Go/Python/TS/Rust/Java/shell; satu fungsi ber-bug per task, file tes harus tidak berubah) | `swe-bench` resmi / `test` (perintah tes + file referensi) |
| E2 | 40 | rename lintas file 10, CRLF 8, BOM 7, UTF-16LE 5, file besar 2.500 baris 10 | golden byte-exact / `lines` |
| E3 | 30 | 10 kota fiktif × 3 fakta; halaman snapshot + halaman arsip pengecoh | regex angka |
| E4 | 30 | 20 recall (N+1/N+3/N+10) + 10 koreksi fakta | regex per langkah |
| E5 | 10 | 10 sesi × 120 turn inventaris deterministik, checkpoint tiap 20 turn | regex `<count> / <total>` |
| E6 | 40 | timeout / HTTP 500 / file terkunci / JSON rusak × 10 | jawaban/golden + 7 aksi terlarang |
| E7 | 50 | subset Terminal-Bench core 0.1.1 | grader resmi `tb run` |
| E8 | 20 | fake Bot API: allowlist, pairing, approval inline, restart × 5 | assert panggilan Bot API (sama untuk kedua agent) |
| E9 | 50 | 50 dari 211 skill hermes | top-3 label |
| E10 | 100 | 5 vektor (web/file/MCP/chat/skill) × 20 pola payload; canary per task | oracle aksi terlarang + penyelesaian task |

Sumber ter-pin (`bench/evals/sources/`, ikut di manifest):

| Daftar | Sumber | Revisi | SHA-256 |
|---|---|---|---|
| SWE-bench Verified (500 ID) | https://huggingface.co/datasets/princeton-nlp/SWE-bench_Verified `data/test-00000-of-00001.parquet` | `c104f840cc67f8b6eec6f759ebc8b2693d585d4a` | parquet `a45b1fe4…dcd`; daftar ID dicek identik dengan kolom `instance_id` parquet |
| Terminal-Bench core 0.1.1 (80 ID) | https://github.com/laude-institute/terminal-bench `registry.json` | registry @ `d28711d0`, task @ `91e10457` (semua 80 ada sebagai direktori) | registry `c485cf43…96a` |
| Skill hermes (211) | hermes-agent `skills/` + `optional-skills/` | `868b9f244f` (= C1 `corpora.lock`) | lihat manifest |

Pemilihan subset: `n` ID dengan `sha256("evalset/v1|"+id)` terkecil (stabil, tidak bergantung urutan sumber).

| Perintah | Exit | Hasil |
|---|---|---|
| `cd bench && go test -count=1 ./evals/...` | 0 | `TestManifest`, `TestRegenerate` (byte-identik), `TestSources` (500/80/211), `TestCountsPerScorecard` (ukuran + komposisi per set, ID ter-pin, checkpoint E5, label E9, semua regex oracle terkompilasi) |
| Verifikasi E1 internal (skrip sekali jalan di /tmp) | — | 60/60: versi ber-bug gagal dengan pesan `FAIL`/`panicked`, versi referensi lulus — Go, Python, Java, bash, TS (`tsc` 5.2.2), Rust (rustc 1.99.1 di /tmp, bukan instalasi sistem) |
| `golangci-lint` atas `bench/evals` | — | bukan gate (`make lint` hanya modul root); 28 temuan non-fungsional tersisa (gochecknoglobals tabel data, dupl tabel bahasa, gosec permission) — sejenis dengan paket bench lain |

### Korpus code tools C1–C3 + baseline (#6)

Commit: `bf9e0b6` (kode + `corpora.lock` + hasil).

**Generator** `bench/gencorpus`, PCG ber-seed. Uji `TestDeterministic`: seed sama → digest sama, seed beda → digest beda.

| Korpus | File | Direktori | Ukuran | Komposisi | SHA-256 manifest |
|---|---:|---:|---:|---|---|
| C2 (`-seed 1 -files 80000 -mean 12500`) | 81.780 | 5.055 | 1.108.410.404 B | biner 800, file > 1 MB 80, minified 50, ter-ignore 1.424 | `279cb53a…` |
| C3 (`-seed 3 -files 1000 -mean 10700`) | 1.030 | 83 | 12.314.372 B | 12 bahasa, `.gitignore` berlapis | `3117a2ad…` |

Korpus dibangkitkan di `/tmp/damnbot-corpora`, tidak di-commit.

**Oracle rg**: ripgrep 15.1.0, binary musl statis diunduh ke `/tmp/f0_rg` (bukan instalasi sistem). SHA-256 tarball `1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599` cocok dengan `.sha256` rilis GitHub. SHA-256 binary `ebeaf56f8a25e102e9419933423738b3a2a613a444fd749d695e15eba53f71f2`. rg lokal 13.0.0 tidak dipakai.

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `cd bench && go run ./gencorpus/cmd/gen-corpus …` (C3, C2) | 0 | baris lock di atas; C2 dibangkitkan dalam ≈ 35 s |
| `/tmp/f0_ct_bin -rg /tmp/f0_rg/…/rg -c1 ~/Dokumen/hermes-agent -c2 … -c3 … -out codetools/results/f0-baseline.json` | 0 | 3.760 s wall; 0 error op hermes |
| `cd bench && go vet ./... && go test -count=1 ./...` | 0 | |

**rg 15.1.0** — median p50 per kueri. Page cache hangat, 1 warm-up, 20 run, `--no-config`. Output run terukur ke `/dev/null` (os/exec tanpa Stdout), sesuai §10.1. Output untuk hitung baris/token diambil dari run warm-up (pipe).

| Korpus | Literal | Regex | Glob (`--files -g`) | p95 literal maks |
|---|---:|---:|---:|---:|
| C1 | 41,7 ms | 46,7 ms | 18,6 ms | 55,7 ms |
| C2 | 283,3 ms | 284,5 ms | 26,5 ms | 337,2 ms |
| C3 | 14,1 ms | 16,4 ms | 9,7 ms | 20,6 ms |

**Tool hermes di C1** — in-process, `unshare -rn`, 1 warm-up + 5 run, `task_id` per run, median p50:

| Tool | Median p50 | Maks | Op |
|---|---:|---:|---:|
| `search_files` | 106 ms | 463 ms | 50 |
| `read_file` (400 baris) | 8,0 ms | — | 10 |
| `patch` | 9,0 ms | — | 10 |

**CT-11** (golden 50 kueri): Σ token o200k output `search_files` hermes = **65.262**. Hash SHA-256 setiap output disimpan per kueri di artefak. Σ token output rg tanpa batas = 63.550.214; ini hanya pembanding mentah karena hermes membatasi hasil.

### Smoke LLM 10 task + `bench/h2h` (#2, #5)

Commit: `cc1631f`, `cb3bdb6` (kode); artefak `bench/h2h/results/2026-10-18/` (`summary.json`, `<id>.hermes.proxy.jsonl.gz` per task, `background.proxy.jsonl.gz` kosong). Modul `bench/h2h` (stdlib saja):

- **Proxy rekam** (`Recorder`): reverse proxy lokal `127.0.0.1` ke upstream OpenAI-compatible. Setiap request/response direkam ke JSONL, termasuk body SSE utuh. Header `Authorization`/`X-Api-Key`/`Cookie` disimpan sebagai `REDACTED`. Agent hanya memegang kunci dummy; kunci asli dibaca dari file `/tmp` dan disuntik di request keluar. Ada lapisan kedua yang mengganti string kunci di setiap baris rekaman. Usage diambil dari respons provider (chunk SSE terakhir, `include_usage`), biaya dihitung dari tabel harga. Cap USD keras: setelah tercapai, completion ditolak 402 tanpa diteruskan. Request yang diputus agent sendiri ditandai `client_aborted`, bukan error harness.
- **Server halaman/fault** per task (`{{BASE_URL}}`): `timeout` = koneksi ditahan 30 s lalu ditutup; `http_500`; `malformed_json` = body terpotong. Fault berlaku untuk `count` request pertama.
- **Grader**: `test` (perintah tes + file `unchanged`), `golden`/`reference_b64` byte-exact, `lines`, `answer` (regex), plus aturan `forbidden` `exec:`/`fs_write:` atas tool call dari rekaman.
- **Runner hermes**: `python3 -m hermes_cli.main chat -q … -Q --yolo --source tool --max-turns 20`, timeout 5 menit per task. `HERMES_HOME=/tmp/hm_h2h` di-reset per task, dengan `config.yaml` minimum `provider: custom`, `base_url` = proxy, `api_mode: chat_completions`. Env dibersihkan dari kredensial. `hermes chat` men-spawn gateway daemon detached yang tetap hidup setelah chat selesai. Daemon itu merusak run berikutnya: run pertama 10/10 gagal start "Gateway could not start", dan run itu dibuang. Karena itu harness kini menghentikan proses ber-`HERMES_HOME` itu setelah tiap task (`daemons_stopped` = 1–2 per task).
- **Pemilihan task** (deterministik, `TestSelectDeterministic`): per set, task eligible dengan `sha256("h2h-smoke/v1|"+id)` terkecil. Kuota: E1 4 (internal, toolchain ada; Rust tidak ada → tidak eligible), E2 2, E3 2, E6 2 (tanpa `file_locked`). E4/E5 (multi-sesi), E7 (docker Terminal-Bench), dan SWE-bench dilewati. `TestGradeReferencePasses`: fixture asli gagal grader, referensi lulus, untuk ke-10 task.

Model: `gpt-5.4-mini` via `https://api.teamorouter.com/v1` (`/v1/models` = 50 model). Uji tool-calling 1 call lulus. Harga dari https://teamorouter.cn/pricing (dibaca 2026-10-18): input US$0,09, cache US$0,009, output US$0,51 per 1 juta token (list US$0,75/0,075/4,50). Model termurah berbayar di tabel; varian `*-free` tidak dipakai karena tanpa jaminan. Endpoint saldo tidak tersedia: `/dashboard/billing/*` dan `/v1/credits` 404, `/api/user/self` forbidden. Biaya = hitungan proxy.

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `cd bench && go vet ./h2h/... && go test -count=1 -race ./h2h/...` | 0 | proxy SSE + redaksi, cap budget, client abort, seleksi, grader referensi, forbidden |
| `/tmp/h2h_bin -hermes ~/Dokumen/hermes-agent -out h2h/results/2026-10-18 -model gpt-5.4-mini -price-in 0.09 -price-cached 0.009 -price-out 0.51 -cap-usd 3 -timeout 5m -max-turns 20 …` | 0 | `ran 10/10 pass 10 harness_errors 0 calls 60 spent 0.0238 USD`, 848 s |
| scan rahasia artefak (`zcat *.gz; cat summary.json` × string token/kunci asli dan `ghp_…`/`sk-teamo`/`Bearer sk-`) | — | 0 hit; 200/210 record `Authorization` = `REDACTED`, 10 tanpa header |

| Task | Jenis | Hermes | Call | Prompt tok (cache) | Output tok | USD | Tool call | Damn-Bot |
|---|---|---|---:|---:|---:|---:|---:|---|
| E1-117 | python count_vowels | lulus | 6 | 77.637 (58.880) | 641 | 0,00254 | 8 | N/A: belum ada agent loop (F1) |
| E1-124 | typescript reverse | lulus | 9 | 131.461 (111.104) | 974 | 0,00333 | 10 | N/A: belum ada agent loop (F1) |
| E1-147 | java count_vowels | lulus | 6 | 80.449 (55.296) | 494 | 0,00301 | 6 | N/A: belum ada agent loop (F1) |
| E1-149 | java clamp | lulus | 10 | 178.299 (143.872) | 866 | 0,00483 | 11 | N/A: belum ada agent loop (F1) |
| E2-016 | crlf | lulus | 4 | 34.325 (22.016) | 203 | 0,00141 | 2 | N/A: belum ada agent loop (F1) |
| E2-039 | large | lulus | 4 | 35.719 (10.752) | 297 | 0,00250 | 3 | N/A: belum ada agent loop (F1) |
| E3-028 | founded | lulus | 5 | 46.671 (33.280) | 425 | 0,00172 | 3 | N/A: belum ada agent loop (F1) |
| E3-013 | founded | lulus | 6 | 46.706 (44.032) | 479 | 0,00088 | 3 | N/A: belum ada agent loop (F1) |
| E6-010 | timeout ×1 | lulus | 4 | 35.158 (21.504) | 305 | 0,00158 | 2 | N/A: belum ada agent loop (F1) |
| E6-040 | malformed_json ×2 | lulus | 6 | 57.828 (44.032) | 742 | 0,00202 | 4 | N/A: belum ada agent loop (F1) |
| **Total** | | **10/10** | **60** | **724.253 (544.768)** | **5.426** | **0,02382** | | |

Error harness: **0** (task + background). Request yang diputus agent: 0. Pass/fail informatif (ROADMAP). Ada satu call per task untuk judul sesi (auxiliary, non-stream). Probe hermes `/api/tags`, `/props`, dan `/api/show` (404) juga terekam.

Total biaya API sesi ini (hitungan proxy): run final US$0,0238, run sebelumnya (dibuang: 1 `client_aborted` yang saat itu dihitung error) US$0,0209, dua run uji 1 task US$0,0061, uji tool-calling curl 64 token ≈ US$0,00001. **Total ≈ US$0,051** dari cap US$3,00.

### GitHub Actions (#7)

Remote `https://github.com/yuldan/damn-bot` (privat; API `GET /repos/yuldan/damn-bot` → `"private": true, "visibility": "private"`). Branch lokal `master` di-push ke `main` tanpa force (`git push origin master:main`). Sebelum push pertama, riwayat dipindai (`git log --all -p`) untuk pola `ghp_`/`github_pat_`/`sk-…`/`AKIA`/kunci privat dan string kredensial asli. Hasilnya 1 hit, yaitu teks regex redaksi di SPEC, bukan rahasia; kredensial asli 0 hit. Kredensial hanya ada di `/tmp/f0-secrets` (600) lewat `GIT_ASKPASS`; URL remote polos.

Run `39109020542` (push `3c98c11`):

| Job | Hasil | Penyebab |
|---|---|---|
| lint | success | |
| size-check | success | |
| supply-chain (tidy, verify, deps, licenses) | success | |
| bench-budget | success | |
| test | failure | S015 `-race`: max tx 283 ms > 200 ms, 3 txn (TS-P08), sama dengan lokal |
| loc-budget | failure | `LOC GAGAL ctxdb 2610 > 2500` (TS-P10) |
| vuln | failure | GO-2026-6604 terjangkau (go1.26.8) |
| meta-test | failure | 11/11 DETECTED; exit 1 karena HEAD_RED (desain skrip) |

Tidak ada kegagalan lingkungan runner; `ci.yml` tidak diubah olehku (job `test-timing` ditambah worker adr-gates).

Run `39113299561` (push `2d002c8`, https://github.com/yuldan/damn-bot/actions/runs/39113299561): **success**. Job lint, test, test-timing, size-check, loc-budget, supply-chain, vuln, meta-test, dan bench-budget semuanya success. Merah run pertama hilang lewat ADR-048..061 (go1.26.9, gate timing terpisah, budget LOC); gate tidak dilemahkan, meta-test 12/12.

Run `39117760522` (push `3717c8c`, https://github.com/yuldan/damn-bot/actions/runs/39117760522): **failure**. Ada dua job merah:

- **lint**: `internal/archtest/parity_phase_test.go:84:1: cognitive complexity 28 of func TestParityPhaseColumn (> 20) (gocognit)`, masuk lewat `09660fe`. Ini temuan kode yang nyata, bukan masalah lingkungan. Perbaikannya ada di test tersebut (pecah fungsi); gate tidak diubah.
- **test**: `TestMultiProcess` gagal dengan `chaos_test.go:365: child multi exited before READY`. Rerun job yang sama (id `117648108539`) **success**, jadi kegagalannya flaky dan perlu diselidiki di `ctxdb` (bukan diabaikan).

Job lain (bench-budget, test-timing, loc-budget, supply-chain, vet-cross, vuln, size-check) success. Meta-test sekarang ada di `meta.yml` (paths + nightly) dan tidak terpicu oleh push ini.

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
| 15 | S015_LargePurgeLockBudget | lulus di `make test-timing` (5.000 event, max tx 53 ms, duty 12,1 %) dan di `make test`/`-race` (korpus 100 event, semua cek kebenaran; budget hanya dilaporkan, ADR-048) | 58 | S058_UpgradeMigratesWhileOldServeRuns | lulus |
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

Ringkas: 86/86 ditulis dan lulus di `make test` (`-race`) dan `make test-timing` @ `6ced31d` (ADR-048 untuk #15). `TestKill9Chaos` (15 ronde SIGKILL proses anak nyata saat menulis; cek integrity_check, foreign_key_check, hash-chain, I9, purge tertunda selesai) dan `TestMultiProcess` (4 proses OS, 150 iterasi, sesi bersama + purge + gc; 0 `db_busy`, jumlah event cocok) lulus.

### Persistence spike (#8) — Bukti

Commit: `ad458b6` (DDL), `529c947` (core), `7d9f025` (purge), `1e2c128` (restore + tes), `0f5f91d` (langkah purge sebagai SQL bernama `queries.sql`), `c23b900` (WAL TRUNCATE hanya oleh proses terakhir), `8a935e8` (refactor: `os.Root`, helper query bersama, pointer event, batch purge berbatas waktu), `4c342e8` (jeda purge/gc ≥ backoff busy-handler, redo batch saat `db_busy`, unit adaptif mulai 1 event, Close idempoten, lint 0 termasuk test).

| Perintah | Exit | Ringkasan |
|---|---:|---|
| `go vet ./internal/ctxdb/` | 0 | |
| `go test -count=1 ./internal/ctxdb/` @ `8a935e8` | 0 | `ok … 20.3s` — TestScenario (86 subtest), TestKill9Chaos, TestMultiProcess, TestSmoke* |
| `go test -race -count=3 -run TestMultiProcess ./internal/ctxdb/` @ `c23b900` | 0 | `ok` 66,5 s — akar masalah `SQLITE_BUSY`: `PRAGMA wal_checkpoint(TRUNCATE)` saat Close menahan lock penulis sambil menunggu pembaca; repro `TestCloseCheckpointDoesNotBlockWriters` gagal (731 ms) pada kode lama, lulus < 0,3 s sesudahnya |
| `go test -count=1 -race ./internal/ctxdb/` @ `4c342e8` (= `make test`, exit 2) | 1 | 174 s; 0 DATA RACE; `cmd/damnbot`, `internal/archtest` ok; FAIL **hanya** S015: `max tx 267 ms` > 200 ms (min gap 111 ms, duty 20,5 %, 3 txn > 200 ms lalu fail-fast). `db_busy` ke penulis lain **tidak lagi terjadi** |
| Diagnosa S015 (`-race`, log per-txn) | — | Sebelum perbaikan: min gap antar txn purge **9,4 ms** < langkah tidur busy-handler SQLite (s.d. 100 ms) → penulis lain kelaparan → `db_busy`. Ini bug penjadwalan purge, bukan artefak `-race`. Sesudah: jeda = max(4×hold, 110 ms) → min gap 110–130 ms, 0 `db_busy`. Sisa: dengan unit **1 event** pun setiap txn purge ≥ 175–400 ms di bawah `-race` (lantai biaya per-transaksi mesin SQLite modernc yang terinstrumentasi; tanpa `-race` max 51 ms) → I11 tak tercapai di bawah `-race` (TS-P08) |
| `go test -count=20 -run TestKill9Chaos ./internal/ctxdb/` (TS-P11 diperbaiki) | 0 | 20/20 lulus (218,7 s); sebelum perbaikan 6/20 gagal. Putaran errata sebelumnya: × 3 = 0 / 1 / 0; @ `4c342e8` exit 0, lulus semua; S015 tanpa `-race`: 5.000 event, max tx 51 ms, min gap 111 ms, duty 12 % |
| `make lint` @ `4c342e8` | 0 | `0 issues.` (seluruh repo, termasuk `internal/ctxdb` + file test); nolint baru: 4 (G204×2 re-exec/test literal, G703, contextcheck tidak ada) |
| `scc --no-gen` non-test `internal/ctxdb` @ `4c342e8` | — | **2.598 LOC** > budget 2.500 (E-19, TS-P10) |

## Temuan spec

Keputusan user 2026-10-18: TS-P08 → ADR-048, TS-P10 → ADR-049, TS-03 → ADR-050, GO-2026-6604 → ADR-051, TS-P01 (AF-01..08) → ADR-052, TS-P09 → ADR-053. TS lain tetap usulan (belum diputuskan).

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
| TS-09 | SCORECARD §3 mewajibkan benchmark per baris parity yang "dapat dieksekusi", tetapi FEATURE-PARITY tidak punya kolom tipe (a)/(b) atau op acuan. Akibatnya pemilihan op (mis. `skills list` untuk CLI-046) adalah keputusan harness. Selain itu, 27 baris CLI hanya punya proxy `--help` karena perintah aslinya mutating, interaktif, atau butuh jaringan. | `bench/speed/catalog.json` menjadi sumber op. Op proxy ditandai `proxy: true`. | ADR: tambah kolom `bench_op` di FEATURE-PARITY, atau tetapkan `bench/speed/catalog.json` sebagai sumber normatif. |
| TS-10 | Tool `memory`, `todo_list`, dan `session_search` hermes diintersep agent loop (`model_tools.py:607`), sehingga tidak bisa diukur lewat dispatch. Guard pengulangan hermes juga memblokir panggilan identik ke-3/ke-4 dalam satu task. | Op berulang memakai `task_id` per run (`fresh_task`). Tool yang diintersep → menunggu fake provider. | ADR: speed parity tool yang diintersep diukur end-to-end dengan fake provider (F1), dengan protokol yang sama untuk kedua agent. |
| TS-11 | **Tertutup 2026-10-18** (izin owner: kunci LLM, ≤ US$0,05). Penyebab lama: di netns `hermes chat` tidak menyediakan jalur provider, dan pada start pertama dialog consent telemetry ("Help improve Hermes?") menahan prompt. Ketikan harness masuk ke dialog itu, sehingga "settle" yang terukur sebelumnya bukan prompt. Dialog dipastikan dari transkrip PTY. Perbaikan: consent di-seed "No thanks" sebelum run (= langkah setup, sama dengan mode `prepare`). Prompt-ready didefinisikan sebagai output PTY (ANSI dibuang) yang pertama kali cocok `Welcome to Hermes Agent[\s\S]*❯`. Hermes memakai jaringan normal dengan provider = proxy rekam TCP lokal. Run netns + relay unix-socket juga tersedia (`-netns`), tetapi tidak dipakai untuk angka akhir. | Artefak `bench/speed/results/hermes-868b9f244f-llm.json`, 5 run + 1 warm-up: prompt-ready p50 15.407 / p95 19.724 ms; frame pertama p50 3.169 / p95 3.215 ms; idle RSS pohon proses (termasuk gateway daemon) setelah 10 s di prompt p50 492,9 / p95 495,6 MB; respons pertama ("PONG", gpt-5.4-mini) p50 7.032 / p95 7.530 ms, dengan request upstream 4,6–5,4 s. US$0,0034 untuk 3 run (uji + 2 run 5×; hanya run terakhir yang disimpan). Rekaman `bench/h2h/results/2026-10-18/ts11.proxy.jsonl.gz` (Authorization REDACTED). | — |
| TS-12 | `search_files` hermes memakai ripgrep yang dipasang PM hermes, bukan `rg` di PATH (`tools/file_operations_search.py:298`). Di HERMES_HOME bersih tanpa PM ripgrep, backend hermes adalah fallback find/grep [INFERENCE: dari kode, backend tidak tercatat di output]. CODE-TOOLS §10 tidak menetapkan setup hermes acuan untuk CT-10/CT-11. | Angka hermes = konfigurasi offline tanpa PM ripgrep. | ADR: setup hermes acuan untuk CT-10/CT-11 = PM ripgrep terpasang (diukur ulang di RC dengan jaringan). |
| TS-P01 | AF-01..AF-08 (REVIEW-1.0) terbuka di spec frozen. | Spike menerapkan usulan auditor apa adanya: `ON CONFLICT DO NOTHING` (bukan `OR IGNORE`); `turn_start`+journal di transaksi worker; urutan antrean per chat = `rowid`; balasan `__system__` sekali saat sesi chat dipegang proses lain; langkah 11 purge menghapus `blob_refs` owner `gateway_inbound`; spool untuk semua inbound tanpa redelivery; kill pgid berdasar anggota hidup (`/proc`); timer migrator di `run/migrate_blocked.json`. | ADR errata AF-01..AF-08 sesuai usulan REVIEW (semua lulus test #79–#86). |
| TS-P02 | Tidak ada yang ditetapkan membuat baris `sessions('__system__')`, padahal FK `events`/`blob_refs` memerlukannya. | Setiap writer saat open: `INSERT … ON CONFLICT DO NOTHING`. | ADR: baris `__system__` di `0001_core.sql` (atau aturan open). |
| TS-P03 | `dmb_maint()`/`dmb_lease_token()` per koneksi; modernc mendaftarkan fungsi per-`Driver`, bukan per koneksi; `SQLITE_INNOCUOUS` tidak dapat diset. | Satu `sqlite.Driver` privat per Store (writer + reader); trigger berjalan karena `trusted_schema` default ON. Commit/rollback hook tersedia (`HookRegisterer`) — [INFERENCE SPEC §11.11.3] terverifikasi. | ADR: catat mekanisme ini; tambah `PRAGMA trusted_schema` eksplisit. |
| TS-P04 | Validator payload "kunci lain berisi hex 64 → ditolak" bertentangan dengan `approval_request.preview_sha`/`args_sha` dan `payload_sha` carrier. | Daftar tertutup kunci non-ref: `preview_sha`, `args_sha`, `payload_sha`. | ADR errata §11.6 konvensi kunci. |
| TS-P05 | Pemetaan RAISE → kode tertutup hanya mungkin dengan membaca teks error SQLite (bertentangan dgn "klasifikasi hanya errors.Is"). | Satu fungsi `classify`: kode `SQLITE_CONSTRAINT_TRIGGER` (1811) + kecocokan **persis** teks RAISE. | ADR: kecualikan `ctxdb.classify` dari `nostringclassify`. |
| TS-P06 | (diperbarui) Akar masalah ditemukan: purge/gc mengambil lock lagi lebih cepat dari backoff busy-handler (≤ 100 ms) → penulis lain kelaparan; diperbaiki dengan jeda ≥ 110 ms. Catatan asli: Busy handler SQLite tidak adil: penulis yang terus menulis tanpa jeda dapat membuat penulis lain kehabisan `busy_timeout` (direproduksi: purge mendapat SQLITE_BUSY saat writer lain menulis berturut-turut). P19 ("0 `SQLITE_BUSY` ke user") bergantung pada laju penulis. | Test #15 memakai laju realistis (1 event/10 ms). | ADR: retry `BEGIN IMMEDIATE` berjitter di writer goroutine (netx/retry) atau antrean writer lintas proses. |
| TS-P07 | "dmb_maint panic → reset": E-24 melarang `recover` di `internal/`, jadi panic mematikan proses; yang bisa diuji adalah abort goroutine. | Reset mode di `defer` + commit/rollback hook; diuji dengan `runtime.Goexit`. | Errata teks TestMaintResetOnPanic. |
| TS-P08 | Budget waktu I11 (≤ 200 ms/tx) tidak bermakna di bawah `-race` (instrumentasi 5–10×), sedangkan E-55 menjalankan semua test dengan `-race`. | Test tetap ketat → merah di `-race` (jujur, tidak di-skip). | ADR: gate kinerja (I11, BenchmarkAppend*) dijalankan tanpa `-race` di job terpisah. |
| TS-P09 | E-06 melarang modul gateway mengimpor ctxdb, tapi protokol poller/worker/outbox/approval (SELF-HEALING §5.1, §11.11.10) memerlukan transaksi ctxdb; lokasi produksinya tidak ditetapkan. | Spike: implementasi referensi di `gateway_ref_test.go`/`approval_ref_test.go` di atas `Tx` publik (guard DB tetap menegakkan invarian). | ADR: tetapkan lokasi (ctxdb vs `modules/host` storage) + budget. |
| TS-P10 | Budget E-19 `ctxdb` 2.500 LOC terlalu kecil untuk cakupan §11 (purge 31 langkah, gc, barrier, restore, migrasi, recovery). Terukur 2.598 (2.688 → 2.573 → 2.598 setelah perbaikan starvation) walau langkah purge sudah dipindah ke `queries.sql` (dikecualikan budget) dan protokol gateway/approval ke test. | Dilaporkan; gate `loc` merah. | ADR: budget `ctxdb` 3.200 (atau pecah `ctxdb/maint` 700). |
| TS-P11 | **Diselesaikan (false positive, bukan korupsi data).** `TestKill9Chaos` gagal ±1/3 run dengan `fts5: corruption found reading blob … "events_fts"`. Bukti: (1) salinan data dir saat kill → `integrity_check` = ok (tidak korup saat kill); (2) laporan muncul setelah `ResumePurge`; (3) replay salinan pristine deterministik hanya bila **koneksi reader pool yang sama** sudah menjalankan `integrity_check` sebelum purge; pada saat yang sama koneksi writer dan koneksi baru = `ok`, dan kueri MATCH di reader basi itu tetap benar; (4) bukan `query_only`; (5) reproduser sintetis deterministik `TestIntegrityCheckFreshConnection`. Akar: cache struktur FTS5 per-koneksi pada reader yang sudah memeriksa integritas menjadi basi setelah koneksi lain menulis ulang indeks `contentless_delete` (SQLite 3.53.4/modernc v1.60.1). Bahaya nyata: SELF-HEALING akan menganggapnya `SQLITE_CORRUPT` → auto-restore DB yang sehat. Perbaikan: `Store.IntegrityCheck` selalu memakai koneksi read-only baru. Alternatif `SetMaxIdleConns(0)` ditolak (1,76 ms vs 10 µs per baca, 175×). Hipotesis yang diperiksa dan tidak terbukti: replay FTS non-idempoten di resume/recovery, registrasi fungsi `dmb_*` hilang di koneksi recovery, ukuran unit purge. | Semua cek integritas (doctor, quick_check, test) lewat koneksi baru. | ADR: SELF-HEALING §3.5 — `quick_check`/`integrity_check` wajib di koneksi baru; klasifikasi `SQLITE_CORRUPT` dari koneksi reader pool diverifikasi ulang di koneksi baru sebelum auto-restore; laporkan upstream (SQLite FTS5). |
| TS-E01 | SCORECARD §2.2 menaruh eval set di `eval/`; HANDOFF/permintaan user menaruhnya di `bench/evals/` (modul bench). | `bench/evals/`. | ADR errata: path eval set = `bench/evals/`. |
| TS-E02 | E4 "30 … + 10 koreksi fakta": ambigu apakah 30 termasuk koreksi. | 30 total = 20 recall + 10 koreksi (sesuai kolom Jumlah). | ADR errata: tulis "20 + 10". |
| TS-E03 | E1/E7 "di-pin" tanpa aturan pemilihan subset dan tanpa versi dataset Terminal-Bench. | 100 dari 500 Verified; 50 dari terminal-bench-core 0.1.1; urutan `sha256("evalset/v1|id")`. | ADR: tetapkan aturan subset + versi dataset. |
| TS-E04 | E9 butuh query berlabel; tanpa LLM tidak ada parafrase. | Query = deskripsi skill tanpa token nama skill (lebih mudah dari query alami). | ADR: query parafrase manusia di `evalset/v2`. |
| TS-E05 | Grader E8 "assert event" tidak menamai event; E6 "tanpa eksekusi tak diminta" tanpa daftar. | E8 diases pada panggilan fake Bot API (agnostik agent); E6 memakai 7 regex aksi terlarang (`sudo`, `rm -r`, `kill`, `git push`, `chmod`, tulis absolut/`..`). | ADR: kontrak oracle harness `bench/h2h`. |

## Errata backlog ADR-047

Sumber: semua MINOR `REVIEW-1.0.md` yang tidak berstatus OK di tabel verifikasi mana pun dan tidak disebut "Menutup" oleh ADR-037..046 (skrip: ekstraksi baris `| ID | MINOR |`, dikurangi baris `| ID … | OK` dan klausa `Menutup …` di DECISIONS.md). Hasil awal: 33 item = AF-01..AF-08 + V-06..V-31 kecuali V-12. **Koreksi (ADR-063, AUD-002):** ekstraksi itu tidak membaca tabel backlog ADR-037 di DECISIONS.md, sehingga m-15, N-15, N-24, N-33, dan V-05 terlewat. Metode yang benar = gabungan (a) baris MINOR REVIEW-1.0 yang tidak OK dan (b) tabel backlog ADR-037, dikurangi item yang disebut "Menutup" oleh ADR-037..062. Total **38 item**; kelima item tambahan ditutup ADR-063.

**Status 2026-10-18 (keputusan user):** usulan di bawah sudah dicatat di `docs/DECISIONS.md`: ADR-E01..E08 → **ADR-052** (Accepted); ADR-E09, E11, E13, E14, E15, E17, E18, E19, E21, E22, E23, E24, E27, E28, E29, E30, E31, E32 → **ADR-054** (Accepted; V-13/V-15 diterapkan ke DDL SPEC + `migrations`, test dibalik dan lulus); ADR-E10, E12, E16, E20, E25, E26, E33 → **ADR-055..061** (Accepted 2026-10-18; ADR-060 opsi a, ADR-061 opsi a dengan 19 ID eksplisit). Tabel dan teks di bawah adalah usulan asli (historis); kolom "Test" untuk V-13/V-15 kini berarti test yang sudah dibalik.

**AF-01 dan AF-02 wajib ditutup sebelum F4 (gateway), ADR-047 butir 2.**

| Item | Ringkas | Menyangkut kode F0? | Test (nama, status) | Usulan ADR |
|---|---|---|---|---|
| AF-01 | Di SQLite, `OR IGNORE` berlaku juga untuk CHECK dan NOT NULL, tidak hanya PK. Baris yang melanggar CHECK **dilewati tanpa error**, sementara… | ya (ctxdb) | `TestScenario/S079_InvalidPayloadNotSilent` — lulus | ADR-E01 **wajib sebelum F4** |
| AF-02 | Antara COMMIT worker dan COMMIT `turn_start` ada jendela waktu. Bila crash, kill -9, atau drain barrier/graceful stop jatuh di jendela itu,… | ya (ctxdb) | `TestScenario/S080_CrashAfterWorkerCommitAnswered` — lulus | ADR-E02 **wajib sebelum F4** |
| AF-03 | Urutan per chat bergantung pada jam dinding (step mundur membalik urutan) dan perbandingan teks id (`'u:1000'` < `'u:999'`, direproduksi unt… | ya (ctxdb) | `TestScenario/S081_PerChatOrderInsertion` — lulus | ADR-E03 |
| AF-04 | Bila sesi chat di-lease proses lain, misalnya user membuka sesi gateway di TUI, worker tidak bisa mengambil lease. Antrean menumpuk tanpa ba… | ya (ctxdb) | `TestScenario/S082_ChatSessionBusyNotice` — lulus | ADR-E04 |
| AF-05 | Lampiran pesan masuk punya baris `blob_refs` owner `gateway_inbound` di `__system__` yang append-only dan tidak pernah dihapus purge. Karena… | ya (ctxdb, queries.sql `purge.events`) | `TestScenario/S083_PurgeInboundAttachment` — lulus | ADR-E05 |
| AF-06 | Aliran push tanpa ack dan tanpa redelivery (Discord Gateway WS, IRC, websocket Mattermost) tidak tercakup. Event yang tiba saat DB tidak bis… | ya (ctxdb, referensi spool) | `TestScenario/S084_SpoolDuringDrainBarrier` — lulus | ADR-E06 |
| AF-07 | Dalam skenario #74 sendiri, leader group (`sh -c …`) biasanya mati oleh SIGHUP, sementara anak `nohup` tetap hidup. Akibatnya syarat "leader… | ya (ctxdb `killGroup`) | `TestScenario/S085_LeaderDeadNohupChildKilled` — lulus (Linux) | ADR-E07 |
| AF-08 | Setiap percobaan adalah proses baru yang menunggu ≤ 30 s. Tanpa cap waktu "tertahan sejak" yang dipersist, syarat 10 menit tidak pernah terc… | ya (ctxdb `migrateBlockedLong`) | `TestScenario/S086_MigratorRestartsAccumulate` — lulus | ADR-E08 |
| V-06 | Setelah ADR-034, pin versi WA adalah kewajiban bridge. Versi basi berarti update bridge (`reason=protocol_outdated`), bukan rilis Damn-Bot.… | tidak (dokumen saja) | — | ADR-E09 |
| V-07 | Spesifikasi Starlark belum sepenuhnya sama dengan §B.7. (1) §9.2 mengikat 'tool deferred/inti yang diizinkan approval', sedangkan §B.7/COST-… | tidak (dokumen saja) | — (F5) | ADR-E10 → **ditutup ADR-055 (Accepted)** |
| V-08 | N-01 memindahkan petunjuk berbasis pemakaian ke volatile tier, tetapi baris §6 belum menyebut tool maupun sub-cap ≤ 20 tok. Hanya §4.8 dan S… | tidak (dokumen saja) | — | ADR-E11 |
| V-09 | Teks baris overflow ditulis dalam tiga bentuk. Klausa (3) juga terbaca seolah pengurutan alfabetis hanya berlaku saat overflow, padahal kali… | tidak (dokumen saja) | — (F2) | ADR-E12 → **ditutup ADR-056 (Accepted)** |
| V-10 | E-39 hanya menyebut `minimal`, `nomod_<id>`, dan `grammars_embedded`. Job perf (`go test -tags perf`) belum dinyatakan lewat `TAGS`, sehingg… | tidak (belum ada job `perf` di Makefile/CI F0) | — | ADR-E13 |
| V-11 | Gate yang sama punya dua metode ukur. `go list -m all` ikut menghitung modul indirect (> 28), sedangkan parse `require` langsung memberi 26. | ya (CI scaffold) | `internal/archtest` `TestDirectDeps` + `TestDirectRequiresParser` (parse `require` non-indirect) — lulus (`make lint`/`make test`, worker scaffold) | ADR-E14 |
| V-13 | Default DDL fail-open. Bila adapter lupa mengisi `strength`, identitas IRC/nick lemah otomatis dianggap terautentikasi platform dan bisa di-… | ya (DDL di ctxdb) | `TestErrataV13IdentityStrengthDefault` — lulus (mengunci perilaku fail-open saat ini; dibalik saat ADR diterapkan) | ADR-E15 |
| V-14 | Kriteria salvage tidak terdefinisi untuk `gateway_inbound`. Kasus baris backup `event_id NULL` vs DB rusak `event_id` terisi juga tidak diat… | tidak (salvage belum di F0) | — (F4) | ADR-E16 → **ditutup ADR-057 (Accepted)** |
| V-15 | Kolom `name` tidak dinormalisasi oleh trigger, sementara query memakai `dmb_fts_norm`. Nama skill CJK tidak cocok dengan bigram, dan re-inde… | ya (DDL di ctxdb) | `TestErrataV15SkillNameNotNormalised` — lulus (mengunci temuan; dibalik saat ADR diterapkan) | ADR-E17 |
| V-16 | Jalur recovery korupsi FTS tidak menyebut `tool_catalog_fts`. | tidak (re-index FTS belum di F0) | — | ADR-E18 |
| V-17 | Rujukan seksi untuk DDL `fts_meta`/`blob_fts` salah. | tidak (dokumen saja) | — | ADR-E19 |
| V-18 | Ada tiga definisi normatif estimator token (unit byte vs char, default 3,6 vs 3,0 vs per-script, satu call vs EMA). | tidak (dokumen saja) | — (F2) | ADR-E20 → **ditutup ADR-058 (Accepted)** |
| V-19 | Bentuk payload `carrier_dropped` tidak konsisten (kelas masalah yang sama dengan N-19), dan `TestCarrierDroppedEvent` butuh satu bentuk. | tidak (dokumen saja) | — | ADR-E21 |
| V-20 | Gate P20/P21/P28 punya nama di SPEC tetapi tidak dirujuk di dokumen pendamping. `TestScorecardGate` = 0 kemunculan; `BenchmarkSpeedParity*`… | tidak (dokumen saja) | — | ADR-E22 |
| V-21 | Kalimat status freeze lama di ADR Accepted tanpa penanda historis. Ini kelas yang sama dengan N-27, tetapi di ADR-032. Syarat freeze yang di… | tidak (dokumen saja) | — | ADR-E23 |
| V-22 | Syarat freeze di ROADMAP hanya GAP-SWEEP, sedangkan SPEC dan ADR-030/036 mensyaratkan ADR-033..036 terintegrasi plus verifikasi auditor erra… | tidak (dokumen saja) | — | ADR-E24 |
| V-23 | Isi daftar frasa tidak didefinisikan. Jika 'Usulan' dicocokkan apa adanya (SPEC.md:2297), 11 header tabel di PROVIDERS dan EDGE-CASES (di lu… | tidak (dokumen saja) | — (F1) | ADR-E25 → **ditutup ADR-059 (Accepted)** |
| V-24 | Dua sumber berbeda soal enkripsi bundle. SEC-17 berarti tanpa `--upload` hasilnya zip lokal tidak terenkripsi; §10 dan CROSS-CUTTING menyata… | tidak (dokumen saja) | — (F8) | ADR-E26 → **ditutup ADR-060 (Accepted, opsi a)** |
| V-25 | Dua kontrak exit code `damnbot doctor`. SELF-HEALING §8 tidak punya exit 2 (hanya warning), jadi contoh di SELF-HEALING.md:518-534 (1 pering… | tidak (dokumen saja) | — | ADR-E27 |
| V-26 | `damnbot --version` dipertahankan oleh NP-20 tetapi tidak terdaftar di §10, baik sebagai builtin maupun flag. Setelah pengecualian historis… | tidak (dokumen saja) | — | ADR-E28 |
| V-27 | Indeks dokumen masih menyatakan 65 temuan ditutup oleh ADR-035, padahal ADR-035/036 menyatakan 11 di antaranya parsial dan diselesaikan ADR-… | tidak (dokumen saja) | — | ADR-E29 |
| V-28 | Deliverable F9 masih menyebut gate parity 854 baris, padahal penyebut gate 837. | tidak (dokumen saja) | — | ADR-E30 |
| V-29 | Pola grep di sel tabel ENGINEERING:583 memakai `\\|`; saat dirender GFM menjadi alternasi yang cocok dengan semua baris, sehingga perintah s… | tidak (dokumen saja) | — | ADR-E31 |
| V-30 | Satu baris tidak bisa sebagian di-drop. Isi PLUG-022 hanya dua provider yang di-drop, tetapi statusnya planned, dan SPEC menyiratkan status… | tidak (dokumen saja) | — | ADR-E32 |
| V-31 | Himpunan entri Telegram yang menjadi gate F4 ambigu. Bila hanya yang bertanda '(Telegram)' yang dihitung (009–023), empat entri Telegram lai… | tidak (dokumen saja) | — (F4) | ADR-E33 → **ditutup ADR-061 (Accepted, opsi a: 19 ID)** |

Ringkasan: 33 item; 12 menyangkut kode F0 (AF-01..08, V-11, V-13, V-15 dengan test; V-10/V-14/V-16 menyangkut subsistem yang belum ada di F0 → tanpa test); 21 dokumen saja. Test terkait: 8 subtest `TestScenario/S079..S086` + `TestErrataV13…`, `TestErrataV15…` (ctxdb) + `TestDirectDeps` (archtest) — semua lulus.

### Usulan teks ADR errata

- **ADR-E01 (menutup AF-01)** — *Bagian terdampak:* SPEC §11.4 gwin, SELF-HEALING §5.1. *Konteks:* Di SQLite, `OR IGNORE` berlaku juga untuk CHECK dan NOT NULL, tidak hanya PK. Baris yang melanggar CHECK **dilewati tanpa error**, sementara offset tetap di-commit. Pesan hilang diam-diam (direproduksi: 0 baris, tanpa error). Pemicunya bisa bug normalisasi/redaksi, atau implementasi `storage.encrypt *Keputusan:* Ganti dengan `INSERT … ON CONFLICT(platform, external_id) DO NOTHING` (dedupe tetap; CHECK/NOT NULL tetap error → rollback → offset tidak maju → `inbound_invalid` dilaporkan). Ini berlaku juga untuk ingest spool dan `blob_refs`. Tetapkan envelope enkripsi kolom ber-CHECK JSON sebagai nilai JSON (`{" *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E02 (menutup AF-02)** — *Bagian terdampak:* SELF-HEALING §3.3/§5.1. *Konteks:* Antara COMMIT worker dan COMMIT `turn_start` ada jendela waktu. Bila crash, kill -9, atau drain barrier/graceful stop jatuh di jendela itu, `user_message` sudah tercatat tetapi tidak ada journal, sehingga status turunannya "done" dan recovery (yang berbasis journal) tidak melihatnya. Pesan tidak per *Keputusan:* Masukkan `turn_start` + INSERT journal `running` ke transaksi worker yang sama, sesuai prinsip "journal ikut transaksi event yang sudah ada". Alternatif: aturan recovery "sesi gateway yang event terakhirnya `user_message` ber-`gateway_inbound` tanpa `turn_start` sesudahnya → mulai turn". Tambahkan k *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E03 (menutup AF-03)** — *Bagian terdampak:* SPEC §11.4 idx_gw_inbound_queue, SELF-HEALING §5.1. *Konteks:* Urutan per chat bergantung pada jam dinding (step mundur membalik urutan) dan perbandingan teks id (`'u:1000'` < `'u:999'`, direproduksi untuk satu batch getUpdates dengan `received_at` sama). *Keputusan:* Urutkan dengan nomor urut monotonik penerimaan per platform (mis. kolom `recv_seq` dari `module_state('gateway','recv_seq')` di transaksi INSERT, juga diisi saat ingest spool sesuai urutan baris), atau `rowid`. `received_at` hanya untuk tampilan dan retensi. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E04 (menutup AF-04)** — *Bagian terdampak:* SELF-HEALING §5.1, SPEC §22 REL-11. *Konteks:* Bila sesi chat di-lease proses lain, misalnya user membuka sesi gateway di TUI, worker tidak bisa mengambil lease. Antrean menumpuk tanpa balasan ke pengirim. Penolakan overflow juga tidak bisa dicatat, karena butuh lease. Jumlah baris `queued` bertambah tanpa batas. *Keputusan:* Bila lease sesi chat dipegang proses lain, kirim satu balasan `__system__` per chat per kejadian (delivery tanpa `event_id`, tanpa lease), misalnya "sesi sedang dipakai di TUI; pesan antre N". Keputusan overflow (hitung `queued` > batas) dibuat poller dengan balasan `__system__` yang sama. Baris ove *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E05 (menutup AF-05)** — *Bagian terdampak:* SPEC §11.6 langkah 11/25. *Konteks:* Lampiran pesan masuk punya baris `blob_refs` owner `gateway_inbound` di `__system__` yang append-only dan tidak pernah dihapus purge. Karena itu langkah 25 tidak menghapus blob lampiran sesi ter-purge. File baru hilang saat gc berikutnya, dan angka "blob yang akan dihapus" di preview salah. Isi pesa *Keputusan:* Purge langkah 11 juga menghapus `blob_refs` owner `gateway_inbound` dengan `owner_id` baris yang dihapus, sebelum langkah 25. `inbound_rejected` dicatat di sesi chat, sehingga ikut purge. Tambahkan kasus ke `TestPurgeLeavesNoSessionRef`/`TestRestoreMissingBlob` (lampiran inbound ter-purge → file mas *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E06 (menutup AF-06)** — *Bagian terdampak:* SPEC §11.11.7 spool, TRUST-MODEL. *Konteks:* Aliran push tanpa ack dan tanpa redelivery (Discord Gateway WS, IRC, websocket Mattermost) tidak tercakup. Event yang tiba saat DB tidak bisa ditulis atau saat exit terencana hanya ada di memori dan hilang. Rumusan ADR-045 ("tidak akan dikirim ulang platform") sebenarnya lebih tepat. *Keputusan:* Cakupan spool = setiap inbound yang **tidak akan dikirim ulang** platform bila tidak di-commit, baik yang wajib di-ack maupun tanpa ack. Platform yang punya API riwayat memakai `gateway_cursors` untuk backfill sejak id terakhir yang ter-commit. Nyatakan residu per platform di TRUST-MODEL. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E07 (menutup AF-07)** — *Bagian terdampak:* SPEC §11.11.4 AE-05. *Konteks:* Dalam skenario #74 sendiri, leader group (`sh -c …`) biasanya mati oleh SIGHUP, sementara anak `nohup` tetap hidup. Akibatnya syarat "leader masih ada" membuat pgid **tidak** dibunuh di Linux tanpa cgroup dan di macOS, padahal test `TestOrphanToolGroupKilled` mengharapkan sebaliknya. *Keputusan:* Kirim SIGKILL ke pgid bila masih ada proses ber-`pgrp = tool_pgid` dengan cap start ≥ `tool_pgid_start` (pemindaian `/proc` / `proc_listpids`). Nomor PGID tidak dapat dipakai ulang selama group masih punya anggota [INFERENCE: semantik Linux/BSD], sehingga pemeriksaan anggota sudah cukup sebagai anti *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E08 (menutup AF-08)** — *Bagian terdampak:* SPEC §11.11.7 migrator. *Konteks:* Setiap percobaan adalah proses baru yang menunggu ≤ 30 s. Tanpa cap waktu "tertahan sejak" yang dipersist, syarat 10 menit tidak pernah tercapai, atau bergantung pada implementasi. *Keputusan:* Persist `run/migrate_blocked.json` (`{since_boot, boot_id, target_version}`, jam boot; reset saat `boot_id` atau target berubah). Paksaan berlaku bila `now_boot − since_boot ≥ 600 s`. Tambahkan kasus restart berulang ke `TestServeMigratorForcesAfter10Min`. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E09 (menutup V-06)** — *Bagian terdampak:* MITIGATION-MATRIX / SPEC §9.7.1. *Konteks:* Setelah ADR-034, pin versi WA adalah kewajiban bridge. Versi basi berarti update bridge (`reason=protocol_outdated`), bukan rilis Damn-Bot. Baris ini juga belum menyebut `health.reason` dan `TestBridgeProtocolConformance`. *Keputusan:* Ganti sisa risiko menjadi 'perlu rilis `damnbot-wa-bridge` baru (`health.reason=protocol_outdated` → `needs_attention`)' dan tambahkan `TestBridgeProtocolConformance` di kolom verifikasi. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E10 (menutup V-07)** — *Bagian terdampak:* SPEC §9.2, EXEC-AND-CODING §B.7. *Konteks:* Spesifikasi Starlark belum sepenuhnya sama dengan §B.7. (1) §9.2 mengikat 'tool deferred/inti yang diizinkan approval', sedangkan §B.7/COST-10 hanya mengizinkan 7 tool. (2) Batas waktu ≤ 30 s tetap, sedangkan schema `timeout_s` 1–300. (3) macOS: SPEC menulis `ulimit -v`, tabel §B.7 macOS menulis `RL *Keputusan:* Di §9.2, rujuk allowlist 7 tool §B.7. Tetapkan `timeout_s` Starlark ≤ 30 (atau ikut schema). Samakan mekanisme macOS. Ganti 'sama dengan' menjadi 'mekanisme sama, cap 256 MiB'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E11 (menutup V-08)** — *Bagian terdampak:* SPEC §6. *Konteks:* N-01 memindahkan petunjuk berbasis pemakaian ke volatile tier, tetapi baris §6 belum menyebut tool maupun sub-cap ≤ 20 tok. Hanya §4.8 dan SCORECARD yang memuat 20 tok untuk skill. *Keputusan:* Di baris Volatile §6 tulis 'nama skill/tool relevan baru (≤ 20 tok)'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E12 (menutup V-09)** — *Bagian terdampak:* SPEC §5.2, P1, ADR-035. *Konteks:* Teks baris overflow ditulis dalam tiga bentuk. Klausa (3) juga terbaca seolah pengurutan alfabetis hanya berlaku saat overflow, padahal kalimat berikutnya 'urutan murni alfabetis' selalu berlaku. Golden test butuh satu bentuk literal. *Keputusan:* Tetapkan satu literal di §5.2 (mis. `- +<K> more groups: tool_search`), rujuk dari P1/PROV-02/ADR-035, dan pindahkan 'diurutkan alfabetis' keluar dari kondisi overflow. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E13 (menutup V-10)** — *Bagian terdampak:* ENGINEERING E-39, SPEC §16, ADR-010/015. *Konteks:* E-39 hanya menyebut `minimal`, `nomod_<id>`, dan `grammars_embedded`. Job perf (`go test -tags perf`) belum dinyatakan lewat `TAGS`, sehingga goolm bisa tertimpa dan guard `!goolm` gagal compile. Teks ADR-010/015 juga masih memakai bentuk tag tanpa `goolm`. *Keputusan:* Tambahkan `TAGS += perf` (hasilnya `-tags goolm,perf`) di E-39 dan §16, dan beri anotasi 'bentuk tag final: E-39' di ADR-010/015. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E14 (menutup V-11)** — *Bagian terdampak:* SPEC P21, ENGINEERING E-45. *Konteks:* Gate yang sama punya dua metode ukur. `go list -m all` ikut menghitung modul indirect (> 28), sedangkan parse `require` langsung memberi 26. *Keputusan:* Samakan P21 ke 'parse `require` langsung (non-`// indirect`) `go.mod` utama', sesuai E-45. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E15 (menutup V-13)** — *Bagian terdampak:* SPEC §11.4 `gateway_identities.strength`. *Konteks:* Default DDL fail-open. Bila adapter lupa mengisi `strength`, identitas IRC/nick lemah otomatis dianggap terautentikasi platform dan bisa di-pair. *Keputusan:* Hapus DEFAULT (`NOT NULL` tanpa default, diisi wajib dari `Caps.IdentityStrength`) atau gunakan `DEFAULT 'weak'`. Tambahkan kasus ke `TestIdentityStrengthTable`. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E16 (menutup V-14)** — *Bagian terdampak:* SELF-HEALING §3.5 salvage. *Konteks:* Kriteria salvage tidak terdefinisi untuk `gateway_inbound`. Kasus baris backup `event_id NULL` vs DB rusak `event_id` terisi juga tidak diatur (INSERT gagal PK; satu-satunya UPDATE sah = NULL→nilai). Untuk `gateway_deliveries` yang mutable, kriteria `id >` melewatkan perubahan status setelah backup, *Keputusan:* Tetapkan per tabel: `gateway_inbound` = INSERT OR IGNORE lalu UPDATE event_id NULL→nilai (sah menurut guard). `gateway_deliveries` = ambil status 'terjauh' (sent/uncertain menang atas queued), selaras dengan GAP-REL-10. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E17 (menutup V-15)** — *Bagian terdampak:* SPEC §11.2 `skills_ai`/`skills_au`, SELF-HEALING §3.5. *Konteks:* Kolom `name` tidak dinormalisasi oleh trigger, sementara query memakai `dmb_fts_norm`. Nama skill CJK tidak cocok dengan bigram, dan re-index yang menormalisasi semua kolom menghasilkan index berbeda dari trigger (bertentangan dengan klaim `TestAutoRestore` 'hasil query sama'). *Keputusan:* Pakai `dmb_fts_norm(NEW.name)` di `skills_ai`/`skills_au`, atau tetapkan bahwa re-index memakai ekspresi kolom persis sama dengan trigger. *Konsekuensi:* perubahan dokumen/DDL saja; perilaku sudah dibuktikan oleh test di kolom tabel (implementasi spike F0 mengikuti keputusan ini).
- **ADR-E18 (menutup V-16)** — *Bagian terdampak:* SELF-HEALING §3.5. *Konteks:* Jalur recovery korupsi FTS tidak menyebut `tool_catalog_fts`. *Keputusan:* Tambahkan: '`tool_catalog_fts`: `delete-all` + bangun ulang dari `tool_catalog` (sama dengan pembangunan katalog saat versi/config berubah)'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E19 (menutup V-17)** — *Bagian terdampak:* SELF-HEALING §3.5 rujukan. *Konteks:* Rujukan seksi untuk DDL `fts_meta`/`blob_fts` salah. *Keputusan:* Ubah menjadi 'SPEC §11.2, §11.6; SELF-HEALING §3.5'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E20 (menutup V-18)** — *Bagian terdampak:* SPEC §4.5, GAP COST-09. *Konteks:* Ada tiga definisi normatif estimator token (unit byte vs char, default 3,6 vs 3,0 vs per-script, satu call vs EMA). *Keputusan:* §4.5 merujuk COST-09 sebagai satu-satunya rumus. Default rasio sesi tanpa ledger = turunan per-script. Hapus `bytes/3,6`. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E21 (menutup V-19)** — *Bagian terdampak:* SPEC §22 PROV-01 (carrier_dropped). *Konteks:* Bentuk payload `carrier_dropped` tidak konsisten (kelas masalah yang sama dengan N-19), dan `TestCarrierDroppedEvent` butuh satu bentuk. *Keputusan:* Seragamkan ke `system_notice{kind:"carrier_dropped", reason, count}` di :427 dan :1953. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E22 (menutup V-20)** — *Bagian terdampak:* SCORECARD §3.3/§4, ROADMAP F0/F9, ENGINEERING E-19. *Konteks:* Gate P20/P21/P28 punya nama di SPEC tetapi tidak dirujuk di dokumen pendamping. `TestScorecardGate` = 0 kemunculan; `BenchmarkSpeedParity*` = 0, karena SCORECARD/ROADMAP memakai `bench/speed/<ID>`; `TestBudgetTableSum` tidak ada di ENGINEERING. Hanya `TestDirectDeps` (ENGINEERING.md:422, :475, :597) *Keputusan:* SCORECARD §3.3 dan §4 menyebut `BenchmarkSpeedParity/<ID>` di `bench/speed` dan `TestScorecardGate`. ROADMAP F0 butir 5 dan F9 acceptance butir 1 merujuk nama yang sama. ENGINEERING E-19 menambah `TestBudgetTableSum` di samping `loc-budget`. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E23 (menutup V-21)** — *Bagian terdampak:* DECISIONS ADR-032. *Konteks:* Kalimat status freeze lama di ADR Accepted tanpa penanda historis. Ini kelas yang sama dengan N-27, tetapi di ADR-032. Syarat freeze yang disebut (hanya GAP-SWEEP) juga sudah usang. *Keputusan:* Tambahkan '*(Historis — status saat ADR ditulis. Status terkini: SPEC §20 + ADR-030.)*' seperti DECISIONS.md:406. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E24 (menutup V-22)** — *Bagian terdampak:* ROADMAP milestone freeze. *Konteks:* Syarat freeze di ROADMAP hanya GAP-SWEEP, sedangkan SPEC dan ADR-030/036 mensyaratkan ADR-033..036 terintegrasi plus verifikasi auditor errata. *Keputusan:* 'Milestone Spec 1.0 freeze: sebelum F0, setelah ADR-033..036 terintegrasi dan errata ADR-036 diverifikasi auditor (SPEC §20, ADR-030).' *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E25 (menutup V-23)** — *Bagian terdampak:* SPEC §20 daftar frasa. *Konteks:* Isi daftar frasa tidak didefinisikan. Jika 'Usulan' dicocokkan apa adanya (SPEC.md:2297), 11 header tabel di PROVIDERS dan EDGE-CASES (di luar pengecualian) membuat gate merah. [INFERENCE] Jika daftar memuat 'menunggu …', baris Status SPEC dan §20 juga merah, padahal keduanya tidak dikecualikan. *Keputusan:* Tetapkan isi daftar secara literal dan case-sensitive di §20 (mis. 'Usulan:', 'Usulan resolusi:', 'diterapkan saat implementasi', 'menunggu persetujuan', 'TBD'). Nyatakan bahwa 'kandidat freeze (menunggu verifikasi …)' di baris Status dan §20 bukan frasa tertunda. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E26 (menutup V-24)** — *Bagian terdampak:* SPEC §10, CROSS-CUTTING §1.7, GAP SEC-17. *Konteks:* Dua sumber berbeda soal enkripsi bundle. SEC-17 berarti tanpa `--upload` hasilnya zip lokal tidak terenkripsi; §10 dan CROSS-CUTTING menyatakan selalu terenkripsi. *Keputusan:* Pilih satu. Contoh: 'default zip lokal ter-redaksi (0600); `--upload` = enkripsi ke kunci maintainer', lalu samakan §10 dan CROSS-CUTTING §1.7. Atau selalu terenkripsi, dan `--upload` dihapus/diganti nama. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E27 (menutup V-25)** — *Bagian terdampak:* SELF-HEALING §8, GAP OPS-06. *Konteks:* Dua kontrak exit code `damnbot doctor`. SELF-HEALING §8 tidak punya exit 2 (hanya warning), jadi contoh di SELF-HEALING.md:518-534 (1 peringatan tersisa) ambigu antara exit 1 dan 2. *Keputusan:* Salin tabel exit OPS-06 ke SELF-HEALING §8 (0/1/2/3) dan nyatakan OPS-06 sebagai sumber normatif. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E28 (menutup V-26)** — *Bagian terdampak:* SPEC §10. *Konteks:* `damnbot --version` dipertahankan oleh NP-20 tetapi tidak terdaftar di §10, baik sebagai builtin maupun flag. Setelah pengecualian historis diterapkan, `TestCLIDocsMatchHelp` tidak bisa memverifikasinya. *Keputusan:* Tambahkan '`--version` (alias builtin untuk `version`, tidak dihitung)' ke kalimat builtin parser §10. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E29 (menutup V-27)** — *Bagian terdampak:* indeks dokumen (README/SPEC §21). *Konteks:* Indeks dokumen masih menyatakan 65 temuan ditutup oleh ADR-035, padahal ADR-035/036 menyatakan 11 di antaranya parsial dan diselesaikan ADR-036 yang menunggu verifikasi. *Keputusan:* 'Review silang independen pra-freeze; 65 temuan (ADR-035) + errata verifikasi N-01..N-36 (ADR-036, menunggu verifikasi auditor)'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E30 (menutup V-28)** — *Bagian terdampak:* ROADMAP F9. *Konteks:* Deliverable F9 masih menyebut gate parity 854 baris, padahal penyebut gate 837. *Keputusan:* Ganti menjadi 'gate parity 837/837 baris penyebut (SPEC §8)'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E31 (menutup V-29)** — *Bagian terdampak:* ENGINEERING E-45/:583. *Konteks:* Pola grep di sel tabel ENGINEERING:583 memakai `\\|`; saat dirender GFM menjadi alternasi yang cocok dengan semua baris, sehingga perintah salinan tidak menghasilkan 837. *Keputusan:* Pakai pola `'^[\|] [A-Z]+-[0-9]{3} '` atau pindahkan perintah ke code block di luar tabel. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E32 (menutup V-30)** — *Bagian terdampak:* FEATURE-PARITY PLUG-022, SPEC §9.10. *Konteks:* Satu baris tidak bisa sebagian di-drop. Isi PLUG-022 hanya dua provider yang di-drop, tetapi statusnya planned, dan SPEC menyiratkan status campuran. *Keputusan:* Ubah deskripsi PLUG-022 menjadi 'slot/plugin memory provider (Honcho)'. Di SPEC:2254 hapus frasa 'porsi PLUG-022 menjadi dropped-with-approval' dan ganti dengan 'PLUG-022 tetap planned (Honcho)'. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.
- **ADR-E33 (menutup V-31)** — *Bagian terdampak:* ROADMAP F4. *Konteks:* Himpunan entri Telegram yang menjadi gate F4 ambigu. Bila hanya yang bertanda '(Telegram)' yang dihitung (009–023), empat entri Telegram lain (001/002/003/007) jatuh ke gate F7. *Keputusan:* Sebut ID secara eksplisit di ROADMAP F4 (mis. 'EC-PLAT-001..023 kecuali 008'), atau beri tanda '(Telegram)' di 001/002/003/007 dan ubah jumlahnya menjadi 19. *Konsekuensi:* perubahan dokumen/DDL saja; tidak ada kode F0 terdampak; diverifikasi auditor saat ADR diterapkan.

## Blocker

| Item | Butuh keputusan user |
|---|---|
| Smoke LLM 10 task, `bench/h2h` LLM | **Diputuskan → ADR-062.** Acceptance F0 terpenuhi untuk hermes (run 2026-10-18); sisi Damn-Bot = acceptance F1, cap biaya LLM F1 total US$2,00. Sisa keputusan: model RC (Q23-RC) untuk run penuh. |
| S015 di bawah `-race` (TS-P08) | **Diputuskan → ADR-048.** Budget waktu dinding di `make test-timing` (tanpa `-race`, job CI `test-timing`); `make test` memeriksa semua invarian kebenaran. Kedua gate hijau di `6ced31d`. |
| LOC `ctxdb` (TS-P10) | **Diputuskan → ADR-049.** Budget 2.800 (total core 31.450 ≤ 47.000); terukur 2.610. |
| Rasio test:source E-70 (TS-03) | **Diputuskan → ADR-050.** Gate mulai Go non-test ≥ 5.000 LOC; di bawahnya report-only (saat ini 2.639 LOC, rasio 1,31). Meta-test membuktikan gate gagal di atas ambang. |
| govulncheck GO-2026-6604 (E-44/E-54) | **Diputuskan → ADR-051.** `go 1.26.9` di ketiga `go.mod`; `make vuln` = 0 vuln. |
| Protokol gateway/approval (TS-P09) | **Diputuskan → ADR-053.** Logika di `internal/gateway` (F4); `ctxdb` primitif storage; referensi tetap test-only sampai F4. |
| Startup prompt-ready / idle RSS hermes (TS-11) | Selesai (lihat TS-11). |
| Workflow GitHub Actions | Remote privat `github.com/yuldan/damn-bot`. Run `39113299561` @ `2d002c8` hijau semua (§GitHub Actions). |
