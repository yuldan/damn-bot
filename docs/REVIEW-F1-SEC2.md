# REVIEW-F1-SEC2: Review keamanan F1 (codetools, prefilter redact, hard link S16-7)

Tanggal: 2026-10-19. Reviewer independen; bukan penulis PR yang direview.
- Kode yang direview tidak diubah.
- Eksperimen dijalankan di salinan `/tmp` yang dihapus setelah selesai.
- Tidak ada secret yang dicetak.
- Klaim keamanan diuji dengan mutasi di salinan: test harus gagal bila perbaikannya dilepas.

Severity mengikuti [`REVIEW-F1-SEC1.md`](REVIEW-F1-SEC1.md):
- **BLOCKER**: wajib diperbaiki sebelum merge.
- **MAJOR**: wajib diperbaiki sebelum acceptance F1.
- **MINOR**: errata.

| PR | Head | Base | Verdict | BLOCKER | MAJOR | MINOR |
|---|---|---|---|---:|---:|---:|
| #27 codetools engine | `34e35c9` | `f1-sandbox-b` | **changes required** | 2 | 6 | 4 |
| #29 prefilter redact | `87ecebf` → `77a9d07` (rebase; isi `internal/redact` identik) | `main` | **approve** | 0 | 0 | 1 |
| PR hard link S16-7 (f1-leaf.SecFixes) | — | — | *menunggu PR dibuka* | | | |

Catatan rebase: head #15 `48648fa` dan #16 `ecc7445` adalah rebase ke `main` dengan isi `internal/sandbox` identik dengan `f7499c4`/`c1095ed` (`git diff` kosong). Verdict putaran 2 di REVIEW-F1-SEC1 tetap berlaku, termasuk MAJOR S16-7 yang masih terbuka.

## PR #27 — `internal/codetools` + API sandbox baru (changes required)

Perintah yang dijalankan:
- `go test -race -count=1 ./internal/codetools/... ./internal/sandbox/...`: ok.
- `GOOS=windows|darwin go vet` + `go test -c`: ok.
- `FuzzEditApply` 30 s (176.858 eksekusi): 0 gagal.
- CT-20: 330 kasus txtar lulus.
- Benchmark `BenchmarkCodetools/*`: tidak konklusif karena load host 27 pada 12 core.
- Eksperimen reviewer:
  - kasus hard link: file besar, edit no-op, disk penuh (tmpfs 1 MiB lewat `unshare`);
  - FIFO;
  - `.gitignore` symlink dan `excludesFile` repo;
  - setgid;
  - edit lewat alias secara paralel;
  - race `OpenResolved` 3.000×;
  - race grep yang sudah di-approve.
- Mutasi:
  - `OpenResolved` dengan `Resolve=0`;
  - `recheckIdentity` dimatikan;
  - cabang `Denied` di walker dihapus;
  - jalur in-place dipaksakan (CT-25).

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| CT27-1 | **BLOCKER** | Cabang approved `OpenResolved` mengikuti symlink leluhur dan hanya mengecek deny lewat identitas. Ini bypass `deny_paths` dan regresi S16-2. Di luar root, `openBeneath(dir, base)` membuka `dir` dengan `unix.Open`, yang mengikuti symlink. Sesudahnya hanya `recheckIdentity` yang berjalan; `denyIDs` berisi inode direktori `~/.ssh`, bukan file di dalamnya. `recheckDeny` (path handle via `/proc/self/fd`) yang ada di `OpenApproved` tidak dipakai di sini. Komentar doc "setiap jalur menolak symlink di seluruh path" tidak benar untuk cabang ini. Walk grep yang di-approve memakai cabang ini: entri didaftar, lalu leluhurnya ditukar dengan symlink ke direktori deny, dan isi file deny masuk ke output model. Fallback Linux dan Windows menolak; macOS (`verifyInode` lewat leluhur yang sama) ikut terdampak [INFERENCE: statis]. | `internal/sandbox/resolved.go:56-65`, `:68-75`; `open_linux.go:32-37`; `internal/codetools/walk.go:262`; `grep.go:208-209`; bandingkan `open.go:63-81`.<br>(a) Deny `~/.ssh`, `/out/lnk -> ~/.ssh`: `OpenResolved("/out/lnk/id_rsa", approved)` mengembalikan isi kunci (openat2), sedangkan `OpenApproved` pada path yang sama → `ErrDeniedPath`.<br>(b) Grep `/out/proj` yang di-approve + flipper `proj/sub` ↔ symlink ke `~/.ssh`: iterasi 59 menghasilkan `<untrusted source="file:outside_workspace"> … proj/sub/id_rsa … 1:PRIVATEKEYMATERIAL`. | Di cabang approved, terapkan aturan tanpa-symlink yang sama dengan cabang inside: `openat2 RESOLVE_NO_SYMLINKS` dari `/`, atau walk `O_NOFOLLOW` di darwin/BSD. Alternatifnya, jalankan `recheckDeny` penuh. Tambahkan test race swap untuk kedua mode, plus test race walk approved di codetools. |
| CT27-2 | **BLOCKER** | File ber-hard-link > 64 MiB: edit rentang baris memotong file yang sedang di-stream, sehingga data hilang tetapi edit dilaporkan sukses. `commitStream` mengirim `nlink>1` ke `writeInPlace` (`O_TRUNC`), lalu `fill` membaca `SectionReader` dari inode yang sama, yang sudah terpotong. `readBack` meng-hash byte yang ditulis sehingga lulus. Kegagalan di tengah jalan (no-op, ENOSPC) juga meninggalkan file terpotong. Ini melanggar commit.go:38-39 dan aturan "tidak pernah parsial" CT-20/CT-25. | `internal/codetools/commit.go:55-58`, `:141-157`; `large_edit.go:46-50`.<br>File 65 MiB + hard link, edit `{1,1,"FIRST"}`: `err=nil`, ukuran 68.157.451 → 5 byte.<br>Edit no-op `{-1,-1,"last"}`: `invalid_args`, file → 0 byte.<br>Disk penuh (tmpfs 1 MiB): `disk_full`, file 364.544 byte campuran lama/baru. | File besar dengan `nlink>1`: tolak (`too_large`/`invalid_args`), atau stream ke temp dulu lalu salin ke target. Untuk semua tulis in-place: validasi penuh sebelum `O_TRUNC`, lalu `fallocate`. Tambahkan kasus hard link × besar/no-op/ENOSPC ke CT-20. |
| CT27-3 | MAJOR | Pemuatan ignore membaca file di luar root tanpa approval, dikendalikan oleh isi workspace. `readSmall` mengubah verdict `OutsideRoot` menjadi `OpenApproved` tanpa bertanya. Ada dua jalurnya: `.gitignore`/`.ignore` in-tree yang berupa symlink keluar root, dan `core.excludesFile`/`include.path` di `.git/config` repo. Perilaku ini melanggar ADR-079 butir 7, melampaui ADR-103 butir 4 (yang hanya untuk config global), dan berbeda dari git ≥ 2.32 yang menolak `.gitignore` symlink (git 2.53 `check-ignore`: ELOOP), sehingga juga menyimpang dari oracle CT-23. Isi file luar tidak sampai ke model secara langsung, tetapi menentukan file mana yang ditampilkan. | `internal/codetools/ignore.go:137-153`, `:178-206`, `:264-273`; `walk.go:280-284`. Repro: `root/.gitignore -> /out/private.txt` (`secretname.go`) → `secretname.go` hilang dari output grep. Hasil sama untuk `excludesFile` di `.git/config`. | File ignore in-tree hanya dibuka bila verdict-nya Inside dan komponen akhirnya bukan symlink; verdict OutsideRoot diperlakukan sebagai file tidak ada. Config di luar root hanya untuk path global tetap (XDG, `~/.gitconfig`, `include.path` miliknya). `excludesFile`/`include.path` level repo wajib Inside. |
| CT27-4 | MAJOR | Open yang blocking: FIFO membuat grep/glob/read_file/edit_file menggantung melewati deadline. Open memakai `O_RDONLY` tanpa `O_NONBLOCK` dan tipe file baru dicek sesudahnya. `.gitignore` FIFO tetap dimuat, karena `hasEntry` hanya mengecek nama. Goroutine yang terblok mengabaikan ctx, lalu `g.Wait()` menunggu selamanya: timeout tool rusak dan goroutine bocor. | `walk.go:280-284`, `:262`; `ignore.go:138`; `codetools.go:259`; `edittool.go:67-81`; `grep.go:272`. Repro: `mkfifo root/sub/.gitignore`, Grep dengan ctx 2 s masih terblok di 6 s. `mkfifo root/pipe`, Read(ctx 2 s) juga masih terblok di 6 s. | Buka dengan `O_NONBLOCK` lalu hapus flag-nya, atau cek tipe file lewat `fstatat` sebelum open. Cek `fstat` IsRegular segera setelah open. Lewati file ignore yang bukan file reguler. |
| CT27-5 | MAJOR | Lock edit dikunci pada path leksikal, padahal komentar menyebut "resolved path". Dua edit lewat alias symlink (atau varian huruf di FS case-insensitive) tidak saling serial, keduanya sukses, dan satu perubahan hilang. | `edittool.go:41-53`, `:186-187`, `:287-288`. Repro: `f.txt` + `link.txt -> f.txt` dengan tag sama, edit paralel. 6/300 iterasi keduanya ok dan file menjadi `"A\nb\n"`. | Kunci lock pada path ter-resolve atau (dev, ino). Pangkas map lock, yang sekarang tumbuh tanpa batas. |
| CT27-6 | MAJOR | API sandbox baru dan test crash CT-25 tidak teruji (vakum di bawah mutasi). Tidak ada test yang memanggil `OpenResolved`/`Denied`. Empat mutan lolos seluruh suite sandbox + codetools: (1) `Resolve=0` (tanpa guard symlink); (2) `recheckIdentity` dimatikan; (3) cabang `Denied` di walker dihapus, sehingga glob membocorkan nama file deny; (4) semua tulis dipaksa ke jalur in-place `O_TRUNC`, dan `TestAtomicWriteKill9` tetap lulus. | `internal/sandbox/resolved*.go` (tanpa `_test`); `walk.go:302`; `kill_test.go:42-60`. Race reviewer 3.000× menangkap mutan (1): leak 349. Tanpa mutasi: leak 0 di kedua mode. | Tambahkan: test race swap `OpenResolved` di kedua mode (asersi opened>0 && refused>0); test identitas hard link; test glob deny (tanpa nama, hanya `skipped: N denied`); titik kill CT-25 di dalam commit (misalnya setelah temp ada, lewat hook test). |
| CT27-7 | MAJOR | Test acceptance F1 belum ada, padahal body PR mengklaim "CT-21/22/… tests". Yang tidak ditemukan: `TestGrepParityRg` (CT-21), `TestGrepOutputTokens` (CT-11), `BenchmarkGrepLiteral/GrepRegex/Glob` / perbandingan `rg` (CT-01..03), dan `TestCodeToolsIDsComplete` (ROADMAP F1). Budget bench belum ditegakkan. | `grep -rn 'GrepParityRg\|GrepOutputTokens\|GrepLiteral' --include=*.go .` → kosong; `docs/ROADMAP.md` acceptance F1 | Koreksi body PR. Lacak sebagai pekerjaan acceptance F1, atau tambahkan di PR ini. Ukur ulang perf di runner yang idle. |
| CT27-8 | MAJOR | Tulis in-place untuk `nlink>1` (semua ukuran) merusak crash safety dan berjalan tanpa snapshot lebih dulu. Crash atau ENOSPC meninggalkan file parsial (direproduksi). Padahal §5.4 hanya mengizinkan in-place "setelah isi lama ada di `file_snapshots`", dan codetools tidak menyediakan hook pra-tulis (snapshot dicatat sesudah tool, SPEC §4.3 langkah 10). Hard link di dalam root yang menunjuk file di luar root juga ditulis ulang tanpa approval `outside_root`. | `commit.go:141-157`; SPEC §5.4/§4.3 | Tambahkan callback snapshot pra-tulis (atau kembalikan byte lama sebelum menulis) dan pre-alokasi. Putuskan bersama S16-7: hard link = approval. Catat sisanya sebagai residual di ADR-103. |
| CT27-9 | MINOR | Bit setuid/setgid/sticky hilang, tetapi `mode_preserved=true`. | `edittool.go:204`, `:314`; `large_edit.go:46`. Repro: `g+s` → hilang setelah edit. | Pertahankan bit khusus. Tambahkan kasus izin ke CT-20. |
| CT27-10 | MINOR | `CleanOrphanTemps` mencocokkan terlalu luas (setiap dot-file yang memuat `.damnbot-`) dan menghapus berdasarkan path (mengikuti leluhur yang tertukar). Belum ada pemanggil. | `commit.go:257-282` | Pola `^\..+\.damnbot-[a-z2-7]{12}$`; hapus lewat `unlinkat` pada fd direktori yang dibuka sandbox. |
| CT27-11 | MINOR | `rename`/`mkdir`/`remove` dilakukan berdasarkan path setelah `Check` (race residual; rename dalam satu direktori). | `commit.go:73`, `:100-101`, `:238`; `fsys_unix.go:37-42` | Gunakan `renameat`/`mkdirat` relatif ke fd direktori, atau dokumentasikan di ADR-103. |
| CT27-12 | MINOR | ADR-103 butir 2 dan body PR mengklaim fast path pencocokan deny yang tidak ada di diff. Di Windows, ACL eksplisit hilang setelah rename, tetapi `owner_preserved=true` [INFERENCE: statis]. | `sandbox.go:447-466`; `fsys_windows.go` (`copyOwner`) | Koreksi ADR-103. Di Windows, laporkan `owner_preserved=false` atau salin DACL. |

Diperiksa tanpa temuan:
- `OpenResolved` di dalam root: `openat2 RESOLVE_NO_SYMLINKS` dari `/` dengan root kanonik setara dengan BENEATH. Race 3.000× tanpa mutasi: leak 0 di openat2 maupun fallback.
- Windows: `os.Root` + `finalPath == want`.
- Walker tidak mengikuti symlink dan melewati entri non-reguler. Footer grep/glob berbentuk `skipped: N denied`, tanpa nama.
- Pemuatan ignore tidak membaca di atas root sesi (`ignoreBase` berhenti di root). Config global tetap kalah oleh deny.
- Jalur tulis normal: temp `O_CREAT|O_EXCL` di direktori ter-resolve lewat sandbox → owner/xattr/chmod → fsync file → re-stat (`stale_read`) → rename → fsync direktori → read-back sha256. Disk penuh membiarkan file asli utuh, menghapus temp, dan mengembalikan `disk_full`.
- Windows: handle target ditutup sebelum `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`.
- Tulis lewat symlink di dalam root mengganti target dan mempertahankan link. Target di luar root → `outside_root` (approval).
- Multi-edit ≤ 64 MiB atomik: semua edit di-resolve terhadap isi asli sebelum ada tulis, dan error apa pun membiarkan file byte-identik.
- Encoding (BOM, UTF-16 dengan surrogate WTF-8, CRLF, byte invalid) dipertahankan. Render fail-closed bila hasilnya tidak representable.
- Guard sentinel ADR-071 berlaku di `old_string`/`new_string`/`new_text`/`content`, termasuk jalur file besar.
- CT-20: 330 kasus, asersi byte persis.
- Error open dipetakan lewat `errors.Is`.

## PR #29 — prefilter byte-pair redact (approve)

Tidak ada regresi deteksi: tidak ada satu input pun yang diredaksi versi lama tetapi tidak diredaksi versi baru.

| Pemeriksaan | Hasil |
|---|---|
| Oracle diferensial (paket `redact` dari `main` disalin sebagai `redactold`) | Setiap input dibandingkan antara versi baru dan lama lewat `Redact`, `String`, dan stream chunk 1/3/64 B. |
| `TestDiffCorpus` | 138.907 cek PASS. Korpus: gitleaks TSV + 60 sintetis × 6 kanal, korpus negatif, 50 respons natural, known secret, EC-SEC-001/005/007, semua baris `url_password_forms`, PEM (dengan dan tanpa END), variasi huruf `Authorization`, telegram, unicode, token 5.000 B (melewati hold cap). Variasi secret: 1 byte (`Q`, `é`, `0xff`, `:`, `-`, `a`), 2 byte (`ab`, `aB`, `Au`, `au`, `//`, `@`), 7 byte, multibyte, 9.000 B, bertumpuk `x`/`xy`/`xyz`, posisi awal/akhir buffer, uppercase. |
| `TestDiffRandom` | 200.000 input dari fragmen pola × 7 set secret: PASS. |
| `FuzzDiff` (diferensial whole + String + stream) | 5 menit, 5,77 juta eksekusi: PASS. |
| `FuzzPairPrefilter` (milik PR) | 2 menit, 2,3 juta eksekusi: PASS. |
| Mutasi (semua tertangkap) | (1) Pair `upper(head[1])` untuk pola case-insensitive dihapus → `aUtHoRiZaTiOn: BeArEr …` bocor. (2) Aturan "semua byte berikutnya" untuk literal pembuka 1 byte dihapus → telegram bocor. (3) Pengecualian byte terakhir dihapus → stream chunk 1 B membocorkan kunci openai. |
| `go test -race ./internal/redact/... ./internal/netx/...` | ok |
| EC-SEC-008 dengan `-cover` | 3/3 lulus |
| `vet` + `test -c` Windows/darwin | ok |
| Kinerja | `BenchmarkRedact` (4 KiB) 18,2–19,7 → 5,8–10,5 µs (≈ 2–3×); `BenchmarkStreamRedact` ≈ 2–2,6×; `BenchmarkDLPScan` plain 8 KiB ≈ 1,0–1,4×. Semua tetap 0 alokasi. |

Argumen kebenaran: posisi hanya dilewati bila setiap pola yang dimulai dengan byte itu pasti gagal di cek byte kedua `matchAt` yang sudah ada. Lompatan itu tidak menyentuh memo skip. Pola dengan literal di tengah (telegram `:`, jangkar `://`/`:\/\/`) memakai literal pembuka 1 byte, sehingga semua pasangan diterima.

| ID | Severity | Temuan | Rekomendasi |
|---|---|---|---|
| PF-1 | MINOR | CHANGELOG menulis "≈3× pada prosa 8 KiB", padahal `BenchmarkRedact` memakai 4 KiB. Satu-satunya bench 8 KiB (`BenchmarkDLPScan`) hanya naik 1,0–1,4×. | Koreksi menjadi "≈2–3× pada prosa 4 KiB (`BenchmarkRedact`)". |

## PR hard link S16-7 (f1-leaf.SecFixes)

Belum ada PR terbuka per 2026-10-19. Bagian ini akan ditambahkan setelah PR tersedia. Perilaku yang diharapkan:
- `nlink>1` → approval wajib, dengan reason `hardlinked_file`.
- Amandemen ADR-097 dan TRUST-MODEL §3.
- Interaksi dengan CT27-2/CT27-8 (jalur tulis in-place hard link di codetools).

## Daftar BLOCKER/MAJOR terbuka

| ID | PR | Severity | Ringkas |
|---|---|---|---|
| CT27-1 | #27 | BLOCKER | `OpenResolved` approved mengikuti symlink leluhur, sehingga isi file deny bisa masuk ke output grep |
| CT27-2 | #27 | BLOCKER | Edit file > 64 MiB yang ber-hard-link memotong file (data hilang, dilaporkan sukses) |
| CT27-3 | #27 | MAJOR | Ignore/config workspace membaca file di luar root tanpa approval |
| CT27-4 | #27 | MAJOR | Open FIFO menggantung tool melewati deadline (goroutine bocor) |
| CT27-5 | #27 | MAJOR | Lock edit pada path leksikal, sehingga lost update lewat alias |
| CT27-6 | #27 | MAJOR | API sandbox baru dan CT-25 vakum di bawah mutasi |
| CT27-7 | #27 | MAJOR | Test acceptance CT-01..03/11/21 dan `TestCodeToolsIDsComplete` belum ada |
| CT27-8 | #27 | MAJOR | Tulis in-place hard link tanpa crash safety/snapshot pra-tulis |
| S16-7 | #16 | MAJOR | (dari REVIEW-F1-SEC1 putaran 2) hard link ke file di direktori deny/glob, menunggu PR SecFixes |
