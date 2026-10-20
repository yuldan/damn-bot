# REVIEW-F1-SEC7 — Verifikasi Putaran 3 (#78–#82)

**Status: lengkap — TL7-12..15 tertutup; verdict: approve untuk keenam PR.** Review independen ketiga atas stack `internal/tools`. Reviewer tidak menulis kode yang direview; kode produk tidak diubah. Tanggal: 2026-10-20.

Head yang direview (semua branch belum di-merge; base berantai, `f1-tools-base` = `a94a7f4`):

| PR | Branch | Head | Base |
|---|---|---|---|
| #78 | `f1-tools-1` | `d24c57c` | `a94a7f4` |
| #79 | `f1-tools-2` | `0a52bde` | `d24c57c` |
| #105 | `f1-tools-2b` | `77c3bd3` | `0a52bde` |
| #80 | `f1-tools-3` | `f813034` | `77c3bd3` |
| #81 | `f1-tools-4` | `509db53` | `f813034` |
| #82 | `f1-tools-5` | `f69ec20` | `509db53` |

Metode: `git fetch origin '+refs/heads/*:refs/remotes/origin/*' --prune`; worktree terpisah `~/.cache/damnbot/wt-review`; setiap head di-checkout bergantian untuk gate arsitektur; tes penuh dijalankan pada tip akhir `f69ec20`. Probe tambahan ditulis sebagai test sementara di paket `internal/tools` (nama `TestZZRound3*`), dijalankan, lalu **dihapus** sebelum commit dokumen ini. Batas runtime: `GOMAXPROCS=2`, `GOFLAGS=-p=2`, `ulimit -v 5242880`, `nice -n 10`.

## Verdict per PR

| PR | Isi | Head | Verdict |
|---|---|---|---|
| #78 | registry, closed error codes, approval types + resolusi konflik §2.4 | `d24c57c` | **approve** |
| #79 | SPEC §4.3 dispatch pipeline, approval, journal, spill | `0a52bde` | **approve** |
| #105 | `Prepare`/`DispatchPrepared` split, edge `tools → boundary`, approval convenience | `77c3bd3` | **approve** |
| #80 | file, todo, clarify, spill store | `f813034` | **approve** |
| #81 | shell tool + classifier-driven approval | `509db53` | **approve** |
| #82 | ADR-109 + F1 report rows | `f69ec20` | **approve** |

Ringkasan: **tiga BLOCKER dan satu MINOR Putaran 2 (TL7-12..15) tertutup** dan tidak ada temuan baru yang memblokir. TL7-1..11 tetap tertutup. Status `mergeStateStatus` GitHub saat snapshot review (`gh pr view`): #78–#81 `UNSTABLE`, #82 `CLEAN`; matriks CI penuh tidak ditunggu (lihat "Tidak dicakup").

## Verifikasi TL7-12..15

### TL7-14 (BLOCKER docs) — conflict marker dan angka budget

Perintah (semua head + `origin/main`):

```
$ for h in d24c57c 0a52bde 77c3bd3 f813034 509db53 f69ec20; do
    git grep -q '^<<<<<<<\|^>>>>>>>\|^=======$' $h -- '*.md' '*.go'; echo "$h exit=$?"
  done
d24c57c exit=1
0a52bde exit=1
77c3bd3 exit=1
f813034 exit=1
509db53 exit=1
f69ec20 exit=1
$ git grep -q '^<<<<<<<\|^>>>>>>>\|^=======$' origin/main -- '*.md' '*.go'; echo "exit=$?"
exit=1
```

`exit=1` = tidak ada match. Tidak ada conflict marker di `.md` maupun `.go` pada head mana pun.

Angka §2.4 dibandingkan dengan `loc.budget` pada **setiap** head:

| Sumber | tools | codetools | exec | exec/classify |
|---|---:|---:|---:|---:|
| `docs/ENGINEERING.md` §2.4 (baris tabel) | 1.800 | 4.800 (ADR-105) | 3.400 (ADR-081, ADR-106) | 1.600 (ADR-081, ADR-106) |
| `loc.budget` | 1800 | 4800 | 3400 | 1600 |

`git show <head>:loc.budget | grep -E '^(tools|codetools|exec|exec/classify) '` menghasilkan keempat baris yang sama di keenam head. Prosa §2.4 juga cocok: menyebut ADR-106 butir 10 memindahkan 100 baris (`exec` 3.500 → 3.400, `exec/classify` 1.500 → 1.600) tanpa mengubah total, ADR-105 menaikkan `codetools` 4.500 → 4.800, dan total core tetap 34.200. `TestBudgetTableSum` lulus:

```
$ go test ./internal/archtest -run 'TestBudgetTableSum' -count=1 -v
--- PASS: TestBudgetTableSum (0.00s)
ok  	github.com/damnbot/damnbot/internal/archtest	0.004s
EXIT:0
```

Commit resolusi diperiksa: `b8deb95` menghapus marker dan memilih angka `loc.budget` (bukan sisi `f1-classify-4` yang lama), `d24c57c` mencatatnya di `docs/DECISIONS.md` ("Catatan implementasi — resolusi `docs/ENGINEERING.md` §2.4"). **TL7-14 tertutup.**

### TL7-12 (BLOCKER gate) — edge `internal/tools → internal/boundary`

- `internal/archtest/allowed.go:52` memuat edge: `"internal/tools": append([]string{..., "internal/boundary"}, groupExec()...)`.
- `docs/ENGINEERING.md` graf §2.2.1 memuat `TOOLS --> BND` (tepat satu kemunculan mulai `77c3bd3`; tidak ada di `d24c57c`/`0a52bde` karena kedua head itu belum mengimpor `boundary`, jadi edge memang belum diperlukan).
- Impor nyata per head (`git grep -n -E 'internal/boundary' <head> -- 'internal/tools/*.go'`):
  - `d24c57c`, `0a52bde`: tidak ada output (`exit=1`).
  - `77c3bd3`, `f813034`, `509db53`, `f69ec20`: hanya `internal/tools/dispatch.go:16`.
- Tidak ada pemanggilan langsung di #79:
  ```
  $ git grep -n -E 'internal/boundary|boundary\.(Run|Go|Group)' 0a52bde -- 'internal/tools/*.go'
  exit=1
  ```
- Pemanggilan di tip akhir hanya dua, keduanya di `dispatch.go`: `:197` (`boundary.Run(ctx, "tools.prepare", ...)`) dan `:233` (`boundary.Run(ctx, "tools.dispatch", ...)`).
- Gate dijalankan di **setiap** head:
  ```
  $ for h in d24c57c 0a52bde 77c3bd3 f813034 509db53 f69ec20; do
      git checkout -q --detach $h
      go test ./internal/archtest -run 'TestImportGraph|TestGlobalAllowlist' -count=1
    done
  d24c57c EXIT:0   ok ... 0.375s
  0a52bde EXIT:0   ok ... 0.376s
  77c3bd3 EXIT:0   ok ... 0.379s
  f813034 EXIT:0   ok ... 0.369s
  509db53 EXIT:0   ok ... 0.391s
  f69ec20 EXIT:0   ok ... 0.395s
  ```
  `-v` pada tip akhir menunjukkan kedua test benar-benar berjalan: `=== RUN TestImportGraph`, `=== RUN TestGlobalAllowlist`, keduanya `PASS`.
- Perilaku wrapper setelah pindah ke #105 diuji dengan probe panic (tip akhir):
  - `TestZZRound3PreparePanicContained`: `Effect` tool panic → `Prepare` mengembalikan code `internal`, proses tidak crash; log `panic recovered goroutine=tools.prepare`.
  - `TestZZRound3DispatchPanicContained`: `Run` tool panic → `DispatchPrepared` mengembalikan outcome code `internal`; log `panic recovered goroutine=tools.dispatch`.
  - Keduanya `PASS`. **TL7-12 tertutup.**

### TL7-13 (BLOCKER gate) — direktif `nolint` dan allowlist global

- `internal/tools/golden_test.go:15`: `var updateGolden = flag.Bool(...) //nolint:gochecknoglobals // test-only update flag` — direktif di baris deklarasi.
- `internal/tools/golden_test.go:79`: `var goldenCases = map[string][]goldenCase{ //nolint:gochecknoglobals // table of golden cases` — direktif di baris deklarasi.
- `internal/archtest/globals.allow` berisi tepat **3** entri (batas 10), semuanya masih dipakai; tidak ada baris basi:
  ```
  internal/assemble/golden_helpers_test.go:update test-only golden flag
  internal/tools/golden_test.go:updateGolden test-only golden update flag
  internal/tools/golden_test.go:goldenCases table of golden cases
  ```
  `TestGlobalAllowlist` lulus di `f813034` dan semua head setelahnya (lihat perintah gate di atas; test yang sama juga lulus di `d24c57c`–`77c3bd3`). **TL7-13 tertutup.**

### TL7-15 (MINOR) — record `shell_allow` bersih dan terbatas

Kode di `internal/tools/builtins_shell.go`:

- `Inspect` hanya memanggil `remember` bila `matched && direct != nil` (`:135-136`), jadi call non-match (mode shell) tidak meninggalkan record.
- `Run` → `runOneShot` → `approvedDirect` (`:369-386`) memakai `take` (hapus record) dan menolak bila `shell_allow` muncul, hilang, atau argv berubah.
- `remember` (`:342-354`) membatasi map pada `shellRecordLimit = 32`; saat penuh, `clear(t.rec)` sehingga pertumbuhan tidak tak terbatas.
- `take` (`:357-363`) selalu `delete`, termasuk saat perbandingan argv gagal.

Tes fokus (dijalankan dengan `-race`, `-count=1`):

```
$ go test ./internal/tools -race -count=1 -run 'TestShellAllowTOCTOUFailsClosed|TestShellAllowAppearsAfterInspectFailsClosed|TestShellInspectWithoutAllowLeavesNoRecord' -v
--- PASS: TestShellAllowTOCTOUFailsClosed (0.00s)
--- PASS: TestShellAllowAppearsAfterInspectFailsClosed (0.00s)
--- PASS: TestShellInspectWithoutAllowLeavesNoRecord (0.00s)
ok  	github.com/damnbot/damnbot/internal/tools	...
```

Probe baru (tidak di-commit):

- `TestZZRound3TL15DeniedRecordsBounded`: 500 `Inspect` match dengan `CallID` berbeda dan **tanpa** `Run` (mensimulasikan call yang ditolak/terminal setelah approval); setelah setiap iterasi `len(st.rec) <= 32`. Lulus.
- `TestZZRound3TL15NonMatchKeepsNoRecord`: `Inspect` non-match pada mode `ModeAsk`; `len(st.rec) == 0`. Lulus.
- `TestZZRound3TL4StatefulAllowMutation`: entry `shell_allow` diubah (`{"echo","hi"}` → `{"echo","bye"}`) antara `Inspect` yang auto-approve dan `Run`; hasil `denied`, `runner.calls == 0`. Lulus.
- `TestZZRound3TL4ArgvSwapped`: `Run` memakai argv berbeda untuk `CallID` yang sama; `denied`, `runner.calls == 0`. Lulus.
- `TestZZRound3TL4RecordSingleUse`: `Run` kedua untuk `CallID` yang sama setelah record dikonsumsi; `denied`, `runner.calls == 1`. Lulus.

TOCTOU tetap fail-closed pada tiga arah: match **muncul** di Run (`TestShellAllowAppearsAfterInspectFailsClosed`), match **hilang** (`TestShellAllowTOCTOUFailsClosed`), dan argv **berubah** (dua probe baru di atas). Tidak ada fallback ke `bash -c`; pada semua kasus `runner.calls` 0 (atau 1 hanya untuk Run pertama yang sah). **TL7-15 tertutup.**

## TL7-1..11 — tetap tertutup pada tip akhir

Dijalankan dengan `-race` pada `f69ec20` (semua `PASS`):

| ID | Bukti (test/probe) |
|---|---|
| TL7-1 | `TestToolReportedFailureIsRendered` (subtest `err`, `silent`) — lulus |
| TL7-2 | `TestUntrustedBoundaryCaseInsensitive` — lulus |
| TL7-3 | `TestZZRound3RedactorRequired`: `NewDispatcher` tanpa `Redactor` → `tools: redactor required` — lulus |
| TL7-4 | `TestShellAllowTOCTOUFailsClosed`, `TestShellAllowAppearsAfterInspectFailsClosed`, + tiga probe baru TL7-4 — lulus |
| TL7-5 | `TestShellCwdOutsideWorkspaceDenied` — lulus; `builtins_shell.go:122`, `:173` menolak `cwd` di luar roots |
| TL7-6 | `TestShellInjectsScopedSecret` — lulus |
| TL7-7 | `TestShellEffectUsesCallCwd` — lulus (`builtins_shell.go:89` memakai `callCwd`) |
| TL7-8 | `TestShellInlineBudgetStaysSpent` — lulus (`:264` `max(env.InlineLeft, 0)`) |
| TL7-9 | statis: `clarifyTool.Effect` = `EffectNonIdempotent` (`builtins_simple.go:104-109`) |
| TL7-10 | `TestShellEmptyStdinIsNil` — lulus |
| TL7-11 | `TestDispatchPreparedDeniesAndIsSingleUse` — lulus (`dispatch.go:250` `p.done`, `:263` `authorized`/`Approved`) |

Perintah:

```
$ go test ./internal/tools -race -count=1 -run 'TestToolReportedFailureIsRendered|TestUntrustedBoundaryCaseInsensitive|TestShellAllowTOCTOUFailsClosed|TestShellAllowAppearsAfterInspectFailsClosed|TestShellCwdOutsideWorkspaceDenied|TestShellInjectsScopedSecret|TestShellEffectUsesCallCwd|TestShellInlineBudgetStaysSpent|TestShellEmptyStdinIsNil|TestDispatchPreparedDeniesAndIsSingleUse|TestShellInspectWithoutAllowLeavesNoRecord' -v
PASS
ok  	github.com/damnbot/damnbot/internal/tools	1.106s
EXIT:0

$ go test ./internal/tools/... -race -count=1
ok  	github.com/damnbot/damnbot/internal/tools	1.383s
EXIT:0
```

## Size gate (E-84) per PR

Dijalankan dengan semantik CI (`scripts/pr-meta.sh`, hanya cek `size`; BASE = tip branch induk):

```
$ BASE=<base> HEAD=<head> ./scripts/pr-meta.sh size
```

| PR | BASE | HEAD | Baris Go non-test diubah | Batas | Hasil |
|---|---|---|---:|---:|---|
| #78 | `a94a7f4` | `d24c57c` | 550 | 600 | ok |
| #79 | `d24c57c` | `0a52bde` | 589 | 600 | ok |
| #105 | `0a52bde` | `77c3bd3` | 158 | 600 | ok |
| #80 | `77c3bd3` | `f813034` | 428 | 600 | ok |
| #81 | `f813034` | `509db53` | 475 | 600 | ok |
| #82 | `509db53` | `f69ec20` | 0 | 600 | ok |

Semua ≤ 600. #79 paling dekat (589), menyisakan 11 baris.

## Diperiksa dan bersih

- **Satu jalur approval:** `Dispatch` = `Prepare` → `authorize` → `DispatchPrepared`; `authorize` tidak dipanggil ulang untuk call yang sudah `authorized`/`Approved`, dan `RequireDeny`/`RequireDeniedPath` selalu diotorisasi ulang (`dispatch.go:255-270`).
- **`DispatchPrepared` single-use:** `p.done` menolak dispatch kedua; `p.authorized` mencegah approval ganda (`:250-270`).
- **Taint:** `taintGate` mematikan `allow_session` dan menaikkan `RequireAuto` → `RequireAsk`; headless menolak (`dispatch.go:378-391`).
- **Redaksi gagal tertutup:** `NewDispatcher` menolak `Redactor` nil (`dispatch.go:139-145`).
- **Escape `untrusted`:** case-insensitive, semua `</untrusted` di-escape (`dispatch.go:591-609`).
- **Spill:** content-addressed SHA-256; tidak ada path dari nama tool.
- **`shell`:** tidak pernah `allow_session`; `cwd` di luar roots ditolak di `Inspect` dan `Run`; `Effect` memakai `callCwd`; stdin kosong = `nil`.
- **Gate arsitektur:** `TestImportGraph` + `TestGlobalAllowlist` hijau di keenam head; `TestBudgetTableSum` hijau di tip akhir.
- **Tidak ada marker konflik** di `.md`/`.go` pada keenam head dan `origin/main`.

## Risiko residual (non-blocking)

1. **Record `shell_allow` dihapus massal saat penuh.** `remember` memanggil `clear` pada 32 record (`builtins_shell.go:350`). Bila 32 call match di-`Inspect` tetapi tidak pernah `Run`, record milik call yang sedang in-flight dapat ikut terhapus; `Run` berikutnya gagal `denied` (fail-closed, bukan eksekusi salah). Ini adalah trade-off anti-leak yang disengaja, bukan jalur bypass.
2. **Panic di `Run` tidak menulis baris `tool_result`.** Probe (`TestZZJournalPanicProbe`, sementara): panic di `Run` → outcome `internal`, `dispatch=1`, `result=0`. Penyebab: recovery terjadi di `DispatchPrepared` (`boundary.Run`), di luar `execute`, sehingga `record` tidak sempat dipanggil. Tidak ada crash dan outcome tetap dilaporkan ke pemanggil; pasangan baris journal `tool_dispatch`/`tool_result` untuk kasus panic ini belum lengkap dibanding ADR-109. Tidak memblokir TL7-12..15.
3. **TOCTOU binary di lapisan exec.** `MatchAllow` me-resolve argv0 dan membandingkan identitas binary saat `Run`, tetapi `exec.Prepare` melakukan `lookPath` sendiri sebelum spawn (`internal/exec/oneshot.go:56-67`). Jendela antara resolusi tools dan `execve` tidak dapat ditutup di lapisan tools; ini milik `internal/exec` dan di luar cakupan review ini.

## Tidak dicakup

- Runtime Windows/macOS (shell quoting, ConPTY, `cmd`/pwsh) — hanya statis.
- Matriks CI penuh (6 target test, lint, deadcode, coverage, loc-budget, dll.) — tidak ditunggu; verdict ini berlaku untuk head di atas dan hasil tes lokal yang dicatat.
- Integrasi end-to-end model → approval UI → tool; fuzz/bench penuh; stress konkurensi approval.
- Perbaikan setelah `f69ec20`; head baru = perlu re-review.
- `internal/exec`/`internal/exec/classify` di luar titik sentuh tools (lihat risiko residual 3).
