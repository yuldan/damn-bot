# HANDOFF F1 — PAUSE

Snapshots diambil langsung dari GitHub pada **2026-10-20 06:50 WIB** (2026-10-19 23:50 UTC).
`main` = `93c8fc81c51cb412cfbee91b9130c32976cc9cd0` (`93c8fc8`).

Dokumen ini adalah satu-satunya yang dibutuhkan untuk melanjutkan tanpa state lokal. Semua
commit, branch, PR, hasil check, dan verdict review ada di GitHub. Worktree di `/tmp` boleh
hilang; tidak ada pekerjaan yang hanya ada di sana. Hapus worktree lama dengan
`git worktree prune` bila perlu.

> Aturan: **jangan** menulis kode produk saat melanjutkan dari dokumen ini sebelum membaca
> §3 (urutan merge) dan §7 (review keamanan yang menahan merge).

## 1. Status ringkas

- Fase: **F1** (`PHASE` = `F1`), belum selesai. Repo publik `yuldan/damn-bot`, `main`
  diproteksi: PR wajib, 24 check wajib, 0 approval wajib, `enforce_admins=false`,
  **`required_status_checks.strict=false`** (lihat §4).
- Branch protection terverifikasi lewat API saat snapshot; daftar 24 check wajib ada di
  `docs/F1-REPORT.md` §"Proteksi `main`".
- **41 PR terbuka**, **20 check merah** tersebar di 9 PR, **4 PR konflik** (`DIRTY`:
  #71, #93, #99, #100). Tidak ada PR yang sedang di-merge.

### Sudah di `main`

| Paket | PR merged | ADR / catatan |
|---|---|---|
| F1 #0 CI (24 gate, archtest, analyzer, PHASE, ctxdb Windows, nightly) | #1, #2, #3, #5, #12 | ADR-094, ADR-086, ADR-091 |
| `internal/schema` | #4, #10, #11 | ADR-095 |
| `internal/boundary` | #8 | E-24/E-26 |
| `internal/redact` (+ prefilter) | #13, #14, #29 | ADR-096 |
| `internal/sandbox` | #15, #16, #33, #48 | ADR-097 |
| `internal/netx` + `netx/retry` + DLP egress | #7, #9, #17 | ADR-099 (atribusi), ADR-100 |
| `internal/budget` + `ctxdb/ledgerstore` | #20, #37 | ADR-101 |
| `internal/provider` + `providertest` | #22, #30, #31, #36, #38 | ADR-102 |
| `internal/codetools` ct1–ct10 (read_file, grep, glob, write_file, edit_file) | #83–#92 | ADR-103 |
| `internal/exec` **hanya** lapisan proses/PTY/Job Object/capture | #39 | ADR-105 |
| `internal/exec/classify` tahap awal (tipe API, label, wrapper, walk bash/POSIX) | #41, #44, #110 | ADR-106 |
| Dokumen + review keamanan (SEC1–SEC8, F1-REPORT, handoff sebelumnya) | #18, #21, #23, #24, #26, #32, #64, #65, #66, #69, #70, #104, #113, #114, #115, #116 | ADR-104 |
| `ci: group concurrency` (pembatalan run PR superseded) | #104 | — |

### Belum di `main`

- `internal/config` (#71, #72) dan `internal/assemble` (#73–#75) — **belum ada di `main`**;
  pustaka ini hanya hidup di branch.
- `internal/tools` (#78, #79, #105, #80, #81, #82).
- `internal/loop` (#99, #100, #101, #118, #102, #103; #107 di lineage lama).
- Sisa `internal/exec` (#40, #42, #45, #47, #52, #62, #108, #109).
- Sisa `internal/exec/classify` (#111, #112).
- Sisa `internal/codetools` ct11–ct15 (#93, #94, #95, #120, #96, #97).
- Perbaikan `netx` DLP (#28), perbaikan `ctxdb` (#106), dokumentasi VPS (#119).
- Smoke LLM Damn-Bot (belum pernah dijalankan; butuh kredensial, §8).

## 2. Tabel PR terbuka

Status check = `gh pr checks <N>` saat snapshot, format **pass/pending/fail**. `BLOCKED` =
check wajib belum hijau; `UNSTABLE` = check merah tapi mergeable; `DIRTY` = konflik dengan base.
Head = 7 karakter pertama `headRefOid`.

### Independen (base `main`)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #28 | `f1-netx-dlp2` | `7bc48aa` | main | BLOCKED | 0/0/0 | run dibatalkan concurrency — **jalankan ulang** |
| #35 | `review-f1-sec3` | `6754e99` | main | BLOCKED | 11/13/0 | — |
| #68 | `f1-report-ct-security` | `135b7e1` | main | BLOCKED | 14/0/0 | — |
| #106 | `fix/ctxdb-session-model` | `ca3f594` | main | CLEAN | 24/0/0 | — |
| #117 | `review-f1-sec8-round5` | `01fa99f` | main | BLOCKED | 8/16/0 | — |
| #119 | `docs/vps-bootstrap` | `a652440` | main | BLOCKED | 0/24/0 | — |
| #121 | `review-f1-sec7-round2` | `11bad1f` | main | BLOCKED | 0/24/0 | — |

### exec (base berantai; #40 base `main`)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #40 | `f1-exec-2` | `48941b4` | main | BLOCKED | 23/0/1 | `test (macos-15-intel, -race, 1)` |
| #42 | `f1-exec-3` | `3bb8cc8` | f1-exec-2 | CLEAN | 24/0/0 | — |
| #45 | `f1-exec-4` | `5e0dcad` | f1-exec-3 | UNSTABLE | 23/0/1 | `loc-budget` |
| #47 | `f1-exec-5` | `a367b61` | f1-exec-4 | UNSTABLE | 0/24/0 | sedang berjalan |
| #52 | `f1-exec-6` | `fbd1b92` | f1-exec-5 | UNSTABLE | 22/0/2 | `test (ubuntu-latest, -race, 1)`, `test-timing` |
| #62 | `f1-exec-7` | `e3f8eb3` | f1-exec-6 | UNSTABLE | 21/0/3 | `test-timing`, `loc-budget`, `test (macos-15-intel, -race, 1)` |
| #108 | `f1-exec-sess-a` | `ffb95b8` | f1-exec-7 | UNSTABLE | 2/20/2 | `loc-budget`, `test (ubuntu-24.04-arm, -race, 1)` |
| #109 | `f1-exec-sess-b` | `c2638d4` | f1-exec-sess-a | UNSTABLE | 0/24/0 | sedang berjalan |

### config → assemble (#71 base `main`, sisanya berantai)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #71 | `f1-config-1` | `7381e0c` | main | **DIRTY** | 23/1/0 | — (konflik) |
| #72 | `f1-config-2` | `281c098` | f1-config-1 | UNSTABLE | 20/4/0 | — |
| #73 | `f1-assemble-1` | `2d77ec6` | f1-config-2 | UNSTABLE | 21/3/0 | — |
| #74 | `f1-assemble-2` | `c5dc549` | f1-assemble-1 | UNSTABLE | 23/1/0 | — |
| #75 | `f1-assemble-3` | `6bf1cf9` | f1-assemble-2 | CLEAN | 24/0/0 | — |

### tools (#78 base `f1-tools-base`, branch yang = `f1-assemble-3` + merge; sisanya berantai)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #78 | `f1-tools-1` | `78cce28` | f1-tools-base | UNSTABLE | 17/0/8 | `coverage`, `deadcode`, `lint`, `meta-test`, `test-timing`, `test (ubuntu-latest, -race, 1)`, `test (windows-latest, -race, 1)`, `test (windows-11-arm, 0)` |
| #79 | `f1-tools-2` | `5268d07` | f1-tools-1 | UNSTABLE | 0/25/0 | sedang berjalan |
| #105 | `f1-tools-2b` | `638cbc7` | f1-tools-2 | UNSTABLE | 0/25/0 | sedang berjalan |
| #80 | `f1-tools-3` | `ff5883e` | f1-tools-2b | UNSTABLE | 0/25/0 | sedang berjalan |
| #81 | `f1-tools-4` | `58e82d9` | f1-tools-3 | UNSTABLE | 0/25/0 | sedang berjalan |
| #82 | `f1-tools-5` | `e8c5c36` | f1-tools-4 | UNSTABLE | 0/25/0 | sedang berjalan |

### loop (#99 dan #100 dua akar terpisah di atas `main`; #101 base `loop-b`; sisanya berantai)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #99 | `loop-a` | `beae644` | main | **DIRTY** | 23/0/1 | `test (macos-15-intel, -race, 1)` + konflik |
| #100 | `loop-b` | `5821828` | main | **DIRTY** | 0/0/1 | `pr-meta` + konflik |
| #101 | `loop-c` | `d641c08` | loop-b | UNSTABLE | 0/0/0 | run dibatalkan |
| #118 | `loop-d-stage` | `8d8f8f8` | loop-c | UNSTABLE | 8/16/0 | — |
| #102 | `loop-d` | `2a4c7d3` | loop-d-stage | UNSTABLE | 12/12/0 | — |
| #103 | `loop-e` | `1415068` | loop-d | UNSTABLE | 12/12/0 | — |
| #107 | `fix/ledger-event-id` | `c5aa9b3` | f1-loop-final | CLEAN | 24/0/0 | — (base **basi**, lihat §3) |

### classify (#111 base `main`)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #111 | `f1-classify-4a` | `1a7cfc9` | main | BLOCKED | 23/0/1 | `pr-meta` |
| #112 | `f1-classify-4b` | `e57c74d` | f1-classify-4a | UNSTABLE | 21/3/0 | — |

### codetools (#93 base `main`, sisanya berantai)

| PR | Branch | Head | Base | Status | pass/pending/fail | Check merah |
|---|---|---|---|---|---|---|
| #93 | `ct11-hardlink` | `f6378f0` | main | **DIRTY** | 23/1/0 | — (konflik) |
| #94 | `ct12-dispatch` | `f37e9ac` | ct11-hardlink | CLEAN | 25/0/0 | — |
| #95 | `ct13-bench` | `ceec638` | ct12-dispatch | CLEAN | 25/0/0 | — |
| #120 | `ct13b-ignore` | `811636f` | ct13-bench | UNSTABLE | 0/25/0 | sedang berjalan |
| #96 | `ct14-prefilter` | `b4491cb` | ct13b-ignore | UNSTABLE | 0/25/0 | sedang berjalan |
| #97 | `ct15-dirfd` | `e762e09` | ct14-prefilter | UNSTABLE | 0/25/0 | sedang berjalan |

Catatan topologi yang diverifikasi dari git (bukan dari tulisan PR):

- `f1-tools-base` (`a94a7f4`) = merge `f1-assemble-3` ke branch itu. Jadi **#78 tidak di atas
  `main`**; ia memuat seluruh stack config+assemble. Retarget ke `main` hanya setelah #71–#75 merge.
- #52 (`f1-exec-6`) **tidak** turun dari #47 (`f1-exec-5`): tree-nya tidak memuat
  `internal/exec/spec_unit_test.go` dan `internal/exec/run_extra_unix_test.go`. Diff PR tetap
  aditif (merge-base dipakai GitHub), tetapi CI PR tidak menjalankan dua file itu → restack dulu.
- #107 dibangun di `f1-loop-final` (`b7a4fa7`, satu PR loop lama, ADR-107). Stack loop yang
  berlaku sudah dipecah dan memakai **ADR-110**; branch ini tidak memuat stack itu. Port
  perbaikannya ke `loop-e`, jangan merge #107 apa adanya.
- `loop-b` sudah memuat isi `loop-a` (diff `event.go`/`types.go` antara keduanya kosong), jadi
  urutan #99 → #100 tetap benar dan tidak menggandakan isi.

## 3. Urutan merge yang harus dilanjutkan

Aturan keras:

- Merge **bottom-up** per stack. Satu PR per stack pada satu waktu.
- Retarget anak dulu, baru merge induk: `gh pr edit <anak> --base main`.
- Merge: **`gh pr merge <N> --squash`**. Jangan `--rebase`, jangan `--merge`. Jangan
  `--delete-branch` selama anak belum di-retarget (menghapus branch induk memutus base anak).
- `strict=false`, jadi **tidak perlu** "Update branch" untuk mengejar `main`.
- PR `DIRTY` harus di-rebase lokal dulu: `git fetch origin && git checkout <branch> &&
  git rebase origin/main`, selesaikan konflik, `git push --force-with-lease`.
- Konflik **hanya boleh** diselesaikan secara **aditif** (simpan kedua sisi) di:
  `docs/F1-REPORT.md`, `CHANGELOG.md`, `docs/DECISIONS.md`, `.github/CODEOWNERS`,
  `go.mod`/`go.sum`, `internal/archtest/allowed.go`.
  Konflik di file lain → **`git rebase --abort`**, jangan merge, laporkan ke owner.
  Setelah menyelesaikan konflik aditif, periksa entri CHANGELOG/ADR yang terduplikasi.
- Setelah setiap merge, tunggu CI `main` hijau sebelum lanjut ke PR berikutnya (kompensasi
  `strict=false`, §4).

### Urutan

1. **Dokumen verdict (membuka jalan review).** #117 (SEC8 putaran 5, verdict *approve* untuk
   #118/#102/#103) → #121 (SEC7 putaran 2). #121 mendokumentasikan blocker yang masih ada;
   merge sebagai catatan, bukan izin merge tools.
2. **Perbaikan kecil independen.** #106 (`ctxdb` Session + config sha) → #28 (netx DLP;
   run-nya dibatalkan concurrency, jalankan ulang lebih dulu) → #119 (`docs/VPS.md` +
   `deploy/`; butuh `docs/DECISIONS.md`, sudah ada di PR) → #35, #68.
3. **exec.** #40 → #42 → #45 → #47 → #52 → #62 → #108 → #109.
   Sebelum #52: restack `f1-exec-6` ke `f1-exec-5` dan pastikan dua file test exec kembali
   ada. Perbaiki `loc-budget`/`test-timing` di #45/#62/#108 (lihat §4 anggaran).
   #109 bergantung pada #108; jangan merge #109 sebelum #108 hijau.
4. **config → assemble.** #71 (rebase dulu, DIRTY) → #72 → #73 → #74 → #75.
5. **tools.** Baru setelah langkah 4 merge: `gh pr edit 78 --base main`, lalu
   #78 → #79 → #105 → #80 → #81 → #82. #79/#81 masih `changes required` (§7) — **tunggu
   perbaikan + verdict baru**.
6. **loop.** #99 (rebase, DIRTY) → #100 (rebase, DIRTY) → #101 → #118 → #102 → #103.
   Merge hanya setelah #117 (verdict approve) masuk dan blocker E-01/E-10 tools tidak
   menyentuh loop. Port #107 ke `loop-e` (ganti rujukan ADR-107 → ADR-110) sebelum #103,
   atau buka PR baru di atas `loop-e`.
7. **classify.** #111 (perbaiki `pr-meta`; cek judul/label/ADR) → #112. `#111` membawa
   `loc.budget` `exec/classify` 1.600 → 1.800.
8. **codetools.** #93 (rebase dulu, DIRTY) → #94 → #95 → #120 → #96 → #97. #120–#97
   menaikkan budget/target perf; lihat §4 sebelum menyetujui.
9. **Smoke LLM** (§8) setelah stack exec/tools/loop masuk.

Perintah yang dipakai konsisten:

```bash
gh pr edit <N> --base main
gh pr merge <N> --squash
gh pr checks <N>
git fetch origin && git log --oneline -5 origin/main
```

## 4. Keputusan & ADR penting

### Branch protection `strict=false`

`required_status_checks.strict` diubah `true` → `false` pada 2026-10-20. Alasan: kelaparan CI
serial dengan ~25 PR bertumpuk; setiap PR menunggu seluruh matriks sebelum boleh merge,
sedangkan kuota runner terbatas. 24 check wajib **tetap** dipertahankan. Kompensasi: CI
`main` pasca-merge menangkap regresi antar-PR — karena itu §3 mewajibkan menunggu `main`
hijau setelah tiap merge. Catatan ini ada di `docs/F1-REPORT.md` §"Proteksi `main`" (PR #116).
Tidak ada ADR terpisah; ini deviasi operasional yang dicatat di laporan fase.

### Anggaran LOC (`loc.budget`)

Angka di `main` dan angka yang **diusulkan** PR terbuka berbeda. Jangan mengira kenaikan sudah
berlaku.

| Grup | `main` | Diusulkan | Pembawa |
|---|---:|---:|---|
| `codetools` | 4.500 | 4.800 → 5.250 → **5.350** | #93 (4.800), #120/#96 (5.250), #97 (5.350) |
| `sandbox+redact` | 1.450 | **1.700** | #120, #96, #97 (ADR-103 errata) |
| `exec` | 3.400 | 3.400 | #41 sudah di `main` (ADR-106) |
| `exec/classify` | 1.600 | **1.800** | #112 |
| `loop` | 1.500 | **1.800** | #102, #103 (ADR-110) |
| `ctxdb` | 3.100 | 3.100 | ADR-087 |

`loop` 1.800 hanya ada di branch loop-d/loop-e; `main` masih 1.500. Naikkan hanya lewat PR yang
memang memuat kodenya, dan pastikan `TestBudgetTableSum`/`loc` hijau.

### Target performa yang BELUM tercapai (jujur)

Sumber: `docs/F1-REPORT.md` §"Kinerja", artefak `bench/codetools/results/f1-perf2-4ade771.json`
(host 12 thread, load 6–13, 20 run/kueri). Angka adalah Σp50/Σp95 relatif `rg` 15.1.0.

| Metrik | Target | Terukur | Status |
|---|---|---|---|
| grep literal C1 | ≤ 1,2× | 1,79 / 1,94 (rentang antar putaran ≈ 1,75–1,95) | **✗** |
| grep regex C1 | ≤ 1,5× | 2,62 / 2,75 (laporan owner mencatat 1,85–2,10 pada putaran lain; keduanya di atas target) | **✗** |
| grep literal C2/C3 | ≤ 1,2× | 2,21 / 1,55 | **✗** |
| glob C1 | ≤ 1,2× | 1,07 / 1,08 | ✓ |
| CT-10 `search_files` | ≥ 10× vs hermes | ×9,9 | **✗** |
| CT-10 `patch` p95 | ≤ 6 ms di mesin referensi Q14 | **belum diukur di Q14**; lokal p95 median 5,72 ms, maks 8,8 ms | **belum** |

Penyebab terdokumentasi: `regexp` Go ≈ 35 MB/s per core untuk pola tanpa literal; literal
butuh 6 syscall/file vs ≈ 4 di `rg`. CT-11 (token grep) juga belum memenuhi target 0,6× dan
menunggu keputusan owner (`TestGrepOutputTokens` sengaja gagal). Jangan menutup ini dengan
menurunkan target tanpa ADR.

### ADR yang mengikat pekerjaan lanjutan

- ADR-094..106 sudah di `main` (CI F1 #0, schema, redact, sandbox, atribusi, netx, budget,
  provider, codetools, review artifact, exec, classify).
- **ADR-107 (config), ADR-108 (assemble), ADR-109 (tools)** ada di #82/#75 dan **belum di
  `main`**. ADR-107 di lineage lamanya (`f1-loop-final`) berarti hal lain (loop) — jangan
  tertukar.
- **ADR-110 (loop)** ada di #102/#103, belum di `main`.
- ADR-103 memuat errata benchmark (S16-8) dan Keputusan CT-11; ADR-077/082 mengatur target
  CT-10 (bandingkan dengan durabilitas setara, bukan hermes tanpa fsync).

## 5. Kredensial

Tidak ada kredensial di repo: tidak ada `.env`, key, atau token yang di-commit
(`.gitignore` hanya menutup artefak temp/ledger lock). Yang harus dibuat ulang di mesin baru:

1. **Token GitHub** — `gh auth login` (device flow) atau fine-grained PAT dengan scope
   `contents: read/write`, `pull_requests: read/write`, `workflows: read/write`,
   `administration: read` (untuk membaca branch protection). Jangan simpan PAT di repo.
2. **Key LLM** untuk smoke (§8). Key F0 lama tidak bisa dipakai ulang; buat key baru di
   provider (contoh di `bench/h2h/cmd/h2h/main.go`: upstream dan model diberikan lewat flag).

Cara aman menyimpan di mesin baru:

```bash
install -d -m 700 ~/.config/damnbot
cat > ~/.config/damnbot/env <<'EOF'
export DAMNBOT_LLM_KEY='...'
export DAMNBOT_LLM_UPSTREAM='https://...'
export DAMNBOT_LLM_MODEL='...'
EOF
chmod 600 ~/.config/damnbot/env
```

Muat per-shell dengan `set -a; . ~/.config/damnbot/env; set +a`. Jangan menaruhnya di
`~/.bashrc` yang di-commit/dibagikan, jangan pernah menulis key ke file di dalam repo, dan
jangan menempelkannya ke isu/PR. Proxy rekam `bench/h2h` hanya membaca key dari `-key-file`
dan menyimpan header `Authorization` sebagai `REDACTED`; tetap gunakan file di luar repo
(mis. `~/.config/damnbot/llm_key`).

## 6. Cara melanjutkan di mesin baru

1. `docs/VPS.md` (bootstrap Debian + akses dari ponsel) ada di PR **#119**, belum di `main`.
   Sebelum #119 merge, ikuti langkah manual di bawah; setelah merge, pakai `docs/VPS.md` dan
   `scripts/vps-bootstrap.sh`.
2. Prasyarat: Go **1.26.9** (`go.mod`), `git`, `gh`, `make`, `golangci-lint` (dipin lewat
   `tools/go.mod`; `make lint` membangunnya), `ripgrep` untuk oracle benchmark (opsional),
   `tmux`.
3. Clone dan autentikasi:

```bash
git clone https://github.com/yuldan/damn-bot.git
cd damn-bot
gh auth login
git log --oneline -5 origin/main   # harus 93c8fc8 atau lebih baru
```

4. Build dan test:

```bash
make build        # cmd/damnbot
make test         # go test -race ./...
make vet-custom deadcode loc archtest
```

5. Jalankan pekerjaan panjang di `tmux` agar tahan putus koneksi:

```bash
tmux new -s damnbot
# di dalam: kerja seperti biasa; detach Ctrl-b d
```

6. Lanjut dari **§3** (urutan merge). Tidak perlu memulihkan `/tmp` atau worktree lama: cukup
   `git worktree add /tmp/<nama> <branch>` untuk branch yang dibutuhkan. Semua state
   (commit, PR, check, review) ada di GitHub.
7. Bila mulai dari VPS: `docs/VPS.md` (setelah #119) mencakup cloud-init, desktop ringan
   (Guacamole), dan akses dari ponsel; `scripts/vps-bootstrap.sh` + `deploy/cloud-init.yaml`
   ada di PR yang sama.

## 7. Pekerjaan yang menunggu review keamanan

Merge ditahan sampai verdict tertulis.

### Tools (#78, #79, #105, #80, #81, #82)

- Dokumen: **`docs/REVIEW-F1-SEC7.md`** (putaran 1, sudah di `main` lewat #113) — verdict:
  **1 BLOCKER** (TL7-4 `shell_allow` berubah menjadi mode shell tanpa approval), 5 MAJOR,
  3 MINOR; #79 dan #81 `changes required`, #78/#105/#80 `approve`, #82 `approve dengan errata`.
- Putaran 2: **PR #121** (`docs/REVIEW-F1-SEC7.md` diperbarui). Semua TL7-1..11 dinyatakan
  diperbaiki (probe + `-race`), **tetapi** ada tiga blocker baru di head stack:
  1. gate E-01: `internal/tools → internal/boundary` belum ada di matriks import
     (`TestImportGraph` gagal sejak #79);
  2. gate E-10: penempatan `nolint` di `golden_test.go` (`TestGlobalAllowlist` gagal sejak #80);
  3. penanda konflik tak terselesaikan di `docs/ENGINEERING.md:326-334` pada **setiap** head,
     termasuk #78.
- Status #78 saat snapshot: **8 check merah** (coverage, deadcode, lint, meta-test,
  test-timing, ubuntu/macOS/Windows test) — konsisten dengan tiga blocker di atas.
- **Jangan merge #78–#82 sebelum ketiganya diperbaiki dan #121 hijau.** Perbaiki di branch
  asal, lalu restack seluruh stack (#78 adalah akar).

### Loop (#118, #102, #103)

- Dokumen: **`docs/REVIEW-F1-SEC8.md`** (putaran 1 di `main` lewat #114; putaran 2–4 lewat
  #115). Putaran 4 verdict: `changes required` — LP8-16 BLOCKER (result call lain hilang),
  LP8-17/18/19 MAJOR, LP8-20 MINOR.
- Putaran 5: **PR #117** (`review-f1-sec8-round5`). Verdict: **approve untuk #118 (`8d8f8f8`),
  #102 (`2a4c7d3`), #103 (`1415068`)**; semua lima temuan terbukti diperbaiki dengan probe
  independen, `-race` penuh, dan arch gate. Satu MINOR non-blocking (LP8-21: agregasi
  `cancelled_confirmed` bergantung urutan).
- Urutan: merge **#117 dulu** sebagai catatan verdict, lalu stack loop (§3 langkah 6).
  Verdict berlaku untuk head di atas; head baru setelah perbaikan = perlu re-review.

### Di luar dua stack itu

#28 (netx DLP) menunggu re-check range-diff `d2ee9d5..17bbaad` + oracle; auditor sudah
approve deteksi NETX-26. #35 (REVIEW-F1-SEC3, provider/ledger) dan #68 (laporan review
codetools) hanya dokumen, tetapi tetap harus hijau sebelum merge.

## 8. Sisa pekerjaan F1 / fase berikut

### F1 yang belum ditutup

1. **Smoke LLM Damn-Bot** — belum pernah dijalankan karena belum ada kredensial. Bila
   dijalankan:
   - pakai proxy rekam `bench/h2h` dan cap **US$2,00** yang tersimpan di ledger
     `bench/h2h/results/f1-spend.json` (saat snapshot `spent_usd = 0`); cap tidak bisa dinaikkan
     dari CLI — kenaikan = ADR;
   - perintah acuan: `cd bench && go run ./h2h/cmd/h2h -key-file ~/.config/damnbot/llm_key
     -upstream <url> -model <model> -price-in <n> -price-cached <n> -price-out <n>
     -out h2h/results/<tanggal>`;
   - ledger dan direktori hasil di-commit; jangan commit key.
2. **Tools + loop** masuk `main` setelah gate §7.
3. **exec**: perbaiki `test-timing`/`loc-budget` di #45/#62/#108, restack #52, lalu merge
   #40–#109.
4. **classify**: #111 `pr-meta` merah (periksa judul Conventional Commits, CHANGELOG
   `Unreleased`, dan kebutuhan `docs/DECISIONS.md` karena `loc.budget` berubah).
5. **codetools**: ct11–ct15 merge; sisa temuan terbuka:
   - CT27-5 **Windows**: lock hard link belum memakai file ID (`GetFileInformationByHandle`);
   - CT-01/02/10/11 belum memenuhi target (§4), butuh keputusan owner: engine regex lain,
     revisi target lewat ADR, atau terima dengan ADR errata;
   - CT-11 token grep: opsi 0,74–0,78× hanya dengan kehilangan informasi; `TestGrepOutputTokens`
     masih gagal dan berstatus `pending:F1`.
6. **netx** #28 merge; **ctxdb** #106 merge; **VPS** #119 merge.
7. **Test yang masih `pending:F1`** dan gate fase: jalankan `make phase-acceptance` setelah
   semua stack masuk; `PHASE` tetap `F1` sampai smoke + acceptance hijau.

### Fase berikut (ROADMAP)

`docs/ROADMAP.md` → **F2 — Context DB, memori, modul lazy**: `ctxdb` lengkap (migrasi
0001–0007, FTS5, leases, crash recovery), `assemble` tier lengkap (katalog, retrieval memori,
skill), `compaction`, `memory` terstruktur + karantina, `skills` retrieval, `recall`,
fork/rewind, `modules` (registry lazy + host API) + `tool_search`/`tool_invoke`, `journal`
(action journal + undo), budget guard harian/bulanan/provider, estimator token gambar +
pemicu compaction, opsi enkripsi at-rest + keychain. Acceptance F2 ada di ROADMAP (P1 ≤ 5.000
tok `all-modules`, undo byte-identik ≥ 100 skenario, `recall` ≤ 50 ms p95 @ 1 jt event,
`Store.Append` ≤ 10 ms p99, EC-CTX/EC-STATE/EC-PROV-008/EC-TCALL-010 covered).

**Jangan mulai F2 sebelum F1 benar-benar ditutup**: stack F1 yang belum merge akan sangat mahal
di-restack setelah `main` bergerak.

---

### Ringkasan satu paragraf

F1 sudah memiliki fondasi di `main` (CI 24 gate, schema, boundary, redact, sandbox, netx,
budget, provider, codetools ct1–ct10, exec lapisan proses, classify awal). 41 PR terbuka
menunggu: exec, config/assemble, tools, loop, sisa classify, dan codetools ct11–ct15.
Dua stack ditahan review keamanan (tools dan loop). Empat PR konflik, 20 check merah, dan
target performa grep/patch belum tercapai. Tidak ada kredensial yang tersimpan; smoke LLM
belum dijalankan.
