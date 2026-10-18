# F0-REPORT — laporan otomatis F0

Format sesuai `docs/HANDOFF-F0.md` §"Laporan pagi". Mesin: workstation (Kali, linux/amd64), **bukan** mesin referensi Q14. Toolchain: `go version go1.26.8 linux/amd64`. Python hermes: `Python 3.14.7` sistem, `HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1`, hermes `868b9f244f` (2026-10-17 04:38:45 −0700).

## Status

| # | Deliverable F0 (ROADMAP) | Status | Catatan |
|---|---|---|---|
| 7 | CI skeleton: lint, test -race, size-check, LOC, deps allowlist, govulncheck, go-licenses, meta-test | sebagian | 8/9 gate terbukti gagal pada pelanggaran sintetis; gate `loc-ratio` (E-70) sudah merah di baseline (lihat Temuan spec TS-03). Workflow GitHub belum pernah dijalankan di Actions (tanpa remote). |
| 1 | `bench/budget` (P1, o200k_base offline) | selesai | tokenizer = tiktoken 0.14.0 persis (id token & jumlah); 13/13 angka gate BASELINE dalam ±5%; job CI `bench-budget`. `TestFixedOverhead` sisi Damn-Bot menunggu `internal/assemble` (F1). |
| 4 | Baseline hermes tanpa LLM | sebagian | Selesai: token overhead & schema bytes (#1). Probe manual (bukan artefak final): cold start CLI hermes 2,7–4,0 s per perintah (`--version` 4.041 ms, `config show` 2.834 ms, `sessions list` 3.241 ms, `status` 7.989 ms, `prompt-size` 6.752 ms); `chat` di PTY tanpa jaringan: output pertama ≈ 4,8 s, settle ≈ 6,0 s, RSS ≈ 131 MB (1 run). Belum: install size/time, baris config, langkah setup. |
| 5 | `bench/speed` | sebagian | `bench/speed/hermes_driver.py` ditulis (mode `prepare`/`inproc`/`pty`, dijalankan di `unshare -rn` = tanpa jaringan); runner Go (parse FEATURE-PARITY 854 baris → measured / alasan) **belum ditulis**, driver belum dijalankan penuh. Smoke LLM 10 task: **menunggu user**. |
| 6 | Korpus code tools C1–C3 + baseline `rg`/hermes | belum | `rg` lokal hanya 13.0.0 (`/usr/lib/code-oss/.../rg`), oracle spec 15.1.0 belum tersedia. |
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

(diisi worker persistence)

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

## Blocker

| Item | Butuh keputusan user |
|---|---|
| Smoke LLM 10 task, `bench/h2h` LLM | Kredensial + izin biaya (menunggu user). |
| Workflow GitHub Actions | Belum ada remote; `ci.yml` hanya divalidasi lewat Makefile lokal yang sama. |
