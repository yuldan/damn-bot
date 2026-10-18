# REVIEW-F1-SEC1 — Review keamanan F1: redact, sandbox, netx DLP

Tanggal: 2026-10-19. Reviewer independen (bukan penulis PR). Review ini tidak mengubah kode yang direview. Semua eksperimen dijalankan di salinan `/tmp` yang dihapus setelah selesai, dan tidak ada secret yang dicetak.

| PR | Branch | Head yang direview | Base |
|---|---|---|---|
| #13 | `f1-redact-a` | `af95824` (awalnya `89aa7a5`; delta hanya `perf_test.go`) | `main` |
| #14 | `f1-redact-b` | `87c9d31` (awalnya `278d68e`; delta = rebase ke `af95824`) | `f1-redact-a` |
| #15 | `f1-sandbox-a` | `053536b` | `main` |
| #16 | `f1-sandbox-b` | `e8a40c7` | `f1-sandbox-a` |
| #17 | `f1-netx-3` | `54baeba` | `f1-redact-a` |

Rujukan normatif: SPEC §4.3 (langkah 5/7), §14.2, §14.3, §14.5, P17, P29, GAP-SEC-11, GAP-SEC-16; CODE-TOOLS §8; TRUST-MODEL §1–§3; ADR-071, ADR-079 butir 7–8, ADR-081, ADR-088, ADR-091 (NEW2-14), ADR-096, ADR-097, ADR-100.

Definisi severity:
- **BLOCKER**: wajib diperbaiki sebelum merge.
- **MAJOR**: wajib diperbaiki sebelum acceptance F1.
- **MINOR**: errata atau perbaikan lanjutan.

## Ringkasan verdict

| PR | Verdict | BLOCKER | MAJOR | MINOR |
|---|---|---:|---:|---:|
| #13 redact whole-blob | **approve** (MAJOR R-02 dilacak sampai acceptance F1) | 0 | 1 | 4 |
| #14 redact stream | **changes required** | 1 | 1 | 2 |
| #15 sandbox verdict + deny_paths | **changes required** | 1 | 2 | 4 |
| #16 sandbox open per OS | **approve**, merge setelah #15. MAJOR S16-1/S16-2 dilacak sampai acceptance F1; S16-2 sebaiknya dikerjakan bersama perbaikan S15-1. | 0 | 2 | 4 |
| #17 netx DLP + log metadata | **changes required** | 2 | 3 | 9 |

## Perintah yang dijalankan

- `go test -race -count=1 ./internal/redact/... ./internal/sandbox/... ./internal/netx/...` pada head masing-masing PR: ok.
- `GOOS=windows|darwin|freebsd go vet` dan `go test -c` untuk `internal/sandbox`: ok.
- `FuzzRedactStream` selama 45 s (3,85 juta eksekusi): tanpa temuan. Fuzz ini tidak mencapai bug S-01 karena hanya membandingkan input < 4 KiB.
- Benchmark:

  | Benchmark | Hasil |
  |---|---|
  | `BenchmarkRedact` (4 KiB) | 235–251 MB/s, 0 alloc |
  | `BenchmarkStreamRedact` | 218–223 MB/s, 0 alloc/Push |
  | `BenchmarkOpenFile` | 14,9–16,8 µs/op |
  | `BenchmarkEgressDLP` (2 KiB) | 14,2–16,7 µs/op |

- Pemeriksaan CI GitHub:
  - #13 `af95824`, #14 `87c9d31`, #15, dan #17: 24/24 sukses.
  - #16: run terakhir berisi 24 sukses dan 24 cancelled (tidak ada yang gagal).
  - Head lama #14 `278d68e`: job `coverage` gagal di `TestEdgeCases/EC-SEC-008` (run 39153148817). Lihat R-01.
- Korpus negatif redaksi: stdlib Go 1.26.9 (9.092 file, 91 MB) memberi 11 file hit, semuanya berbentuk kredensial (contoh `Authorization: Basic`, `http://user:pass@`, PEM test). Repo sendiri: tanpa false positive di luar teks spec/test.

## PR #13 — `internal/redact` whole-blob (approve)

Tidak ada bypass terhadap pola yang diwajibkan §14.3. `TestRedaction` lulus: korpus gitleaks 42 × 6 kanal dan sintetis 60 × 6, 0 bocor. Oracle NEW2-14 (tidak ada jendela 4 byte nilai secret di output): 0 hit. Korpus negatif (sha1, sha256, uuid, base64, base64url, lookalike): 0 false positive.

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| R-01 | MINOR (BLOCKER di head lama `89aa7a5`, tertutup di `af95824`) | `TestEdgeCases/EC-SEC-008` membandingkan waktu dinding per byte (16 KiB vs 256 KiB) di dalam test `t.Parallel`. Di `make coverage` test ini flaky: counter coverage bersama memicu kontensi. Kodenya sendiri linear: semua 14 unit + 8 unit adversarial datar di 0,4–18 ns/B dari 64 KiB sampai 16 MiB, jadi ini bukan DoS. Di `af95824` dipakai rasio berpasangan minimum dari 5 sampel; repro coverage lulus 5/5. | `internal/redact/perf_test.go:78-110`. Repro lama gagal 3/5: `go test -covermode=set -coverpkg=./internal/... -run 'TestEdgeCases\|TestStreamEdgeCases\|TestStreamRedactSplit\|TestRedaction' ./internal/redact/`. | Pindahkan asersi waktu ke `timing.Enforced` (job test-timing), atau pakai penghitung langkah deterministik. Catatan: commit `e1719cd` di branch #17 membawa versi lama test ini (lihat NETX-1). |
| R-02 | MAJOR | Pola `url_password` tidak menangkap bentuk URL yang umum: `redis://:Sup3rS3cretPw@cache:6379/0` (username kosong); `postgres://app:pa/ss+w0rd@db` (`/` di password); `postgres://app:p@ss@db` (sisa `ss` bocor); JSON-escaped `https:\/\/app:S3cretPw99@db`. Kode sesuai regex spec, jadi celahnya ada di spec. | `internal/redact/patterns.go:126-132`; tabel bypass sekali pakai dijalankan lewat `r.String` dan stream 1 byte. | Amandemen §14.3/ADR-096 (prinsip "paling konservatif"): user boleh kosong (`[^/\s:@]*`), dan password berjalan sampai `@` terakhir sebelum whitespace. Tambahkan bentuk `\/`. Tambahkan kasusnya ke `TestRedaction`. |
| R-03 | MINOR | Known secret berbentuk `user:pass` di `https://user:hunter22@h` hanya diredaksi bagian password-nya, sehingga `user` (≥ 4 char nilai secret) tetap tampil. Ini termasuk bocor menurut definisi NEW2-14. | `internal/redact/match.go` (cabang `m.start > i`, rescan hanya `data[:m.start]`) | Saat pola terstruktur cocok, rescan known-secret mencakup seluruh rentang kecocokan. |
| R-04 | MINOR | (a) `Authorization: Bearer "tok…"` dengan nilai bertanda kutip tidak diredaksi, padahal spec memakai `\S+`. (b) Known secret > 4095 B menaikkan `r.hold` untuk semua pola, sehingga hold Bearer/PEM melebihi cap 4 KiB §14.3. (c) Biaya known-secret sebanding dengan prefix bersama (147–6.136 ns/B untuk secret 257 B–16 KiB pada input adversarial). Tetap linear, tetapi tanpa batas panjang yang terdokumentasi. | `internal/redact/patterns.go`, `redact.go:64` | Izinkan kutip di nilai Bearer. Cap panjang known secret (atau hold per pola), lalu dokumentasikan di ADR-096. |
| R-05 | MINOR (informasi) | Pola `sk-` tanpa jangkar mengenai slug (5/5), dan `Authorization: token …` mengenai prosa. Bentuk ter-encode (`\u002d`, URL-encoded, base64, zero-width/fullwidth di dalam prefix, uppercase `GHP_`) memang di luar cakupan redaksi menurut ADR-096. Untuk egress, cakupannya ada di DLP netx (GAP-SEC-16). | — | Catat di ADR-096 sebagai sisa risiko yang diterima. |

## PR #14 — `internal/redact` stream (changes required)

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| S-01 | **BLOCKER** | Panic nil pointer saat hold `url_password` mencapai cap 4 KiB. Pola `url_password` tidak punya set `drop`. Potongan paksa di run user/password (`matchRun` max 0 → `stCut`) membuat `stream.go:55` menyetel `s.drop` ke pola itu, lalu `Push`/`Flush` berikutnya mendereferensi `s.drop.drop` yang nil. Pemicunya output normal model: URL panjang, JSON minified, query string panjang. Hasilnya turn/proses crash; whole-blob tidak terdampak. | `internal/redact/stream.go:55`, `:74`; `patterns.go:126`. **Diverifikasi ulang di `87c9d31`:** input `"see http://localhost:3000?q=" + 5000×"A" + " tail"` di-push per 64 B → `PANIC: runtime error: invalid memory address or nil pointer dereference`. Juga panic dengan chunk 1 B, `{"u":"http://x:1","d":[`+`"1,"`×3000, dan `x https://`+5000×`a`. | Beri `url_password` set `drop` (set password; untuk potongan di user, set user), atau buat `dropTail` memperlakukan `drop == nil` sebagai "tidak membuang apa-apa" tanpa melepas byte yang belum diputuskan. Tambah kasus regresi. |
| S-02 | MAJOR | Test tidak mencapai S-01. `TestStreamHoldCap` hanya mencakup bearer/telegram/jwt/PEM. `FuzzRedactStream` hanya membandingkan stream vs whole-blob bila `len(in) < holdCap` dan hanya memeriksa known secret, bukan pola. | `internal/redact/stream_test.go:206` | Hasilkan satu baris hold-cap untuk setiap pola dengan ekor tak terbatas langsung dari `r.pats`. Tambahkan seed fuzz > 4 KiB, dan oracle "tidak panic + output ⊆ fail-closed". |
| S-03 | MINOR | `TestFirstTokenLatency/redact` hanya mengukur CPU `Push` pertama (p95 1,84 µs) dengan chunk 20–80 B dan korpus berkosakata sekitar 60 kata. Untuk delta seukuran satu token, delta pertama yang seluruhnya berupa prefix (`a`, `e`, `s`, …) ditahan sampai delta berikutnya, yaitu satu interval antar-token. Yang diwajibkan P17 adalah latensi tampil. | `internal/redact/stream_test.go:18` | Pakai chunk seukuran token, dan laporkan "ditahan sampai delta berikutnya" secara terpisah dari CPU. |
| S-04 | MINOR | Setelah S-01 diperbaiki, potongan paksa > 4 KiB membuat output stream berbeda dari whole-blob untuk `url_password`. Ini fail-closed dan dapat diterima, tetapi komentar doc yang mengklaim output identik belum menyebut pengecualian ini. | `internal/redact/stream.go` (doc) | Perbarui komentar doc. |

Diperiksa tanpa temuan:
- Hold di dalam prefix (`gh`, `https:`, `-----BEG`).
- Look-behind Telegram antar chunk.
- PEM > 4 KiB dibuang sampai END.
- PEM tanpa END dibuang sampai `Flush`.
- Byte yang ditahan tidak pernah dilepas sebelum diputuskan; stream yang dibatalkan tidak memancarkan hold.
- `TestStreamRedactSplit` (semua 2-split dan 50 acak 3-split) lulus.
- Stream 1 byte = whole-blob untuk seluruh tabel bypass (kecuali S-01).
- 0 alloc per Push.

## PR #15 — `internal/sandbox` verdict + `deny_paths` (changes required)

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| S15-1 | **BLOCKER** | `deny_paths` bisa dilewati lewat alias symlink. Pola dikompilasi dari teks (Home tidak di-resolve) dan dicocokkan dengan path tertulis dan path ter-resolve target. Bila direktori yang ditolak adalah symlink (`~/.ssh -> ~/dotfiles/ssh`, pola stow/chezmoi), atau Home mengandung symlink (`/home -> /var/home`), path fisiknya tidak cocok keduanya. Jika target berada di dalam root (user bekerja di `~` atau `~/dotfiles`), verdict-nya `Inside` dan kunci dibaca tanpa approval. Jika di luar root, verdict-nya `outside_root`, yang bisa di-approve. Ini melanggar "deny_paths tidak bisa di-approve" (SPEC §4.3 langkah 5, CODE-TOOLS §8, ADR-097 butir 1) dan termasuk vulnerability (b) menurut TRUST-MODEL. | `internal/sandbox/sandbox.go:163-171` (cek `denied(abs)` lalu `denied(resolved)`); `:271-291` (`compileDeny`: `filepath.Join(home, …)` tanpa `EvalSymlinks`). Test sekali pakai `TestRvDenyAliases`: `home/.ssh -> home/dotfiles/ssh`, root=`home/dotfiles` → `Check(dotfiles/ssh/id_rsa)=inside`, `OpenFile` membaca kunci. Home bersymlink → `outside_root`, dan `OpenApproved` sukses. | Di `New`: resolve Home dan prefix bebas-glob terpanjang dari setiap pola absolut lewat `EvalSymlinks`, lalu tambahkan bentuk ter-resolve sebagai pola tambahan. Di PR #16: cek ulang deny terhadap path handle yang dibuka (S16-2). Tambahkan test untuk direktori deny bersymlink dan Home bersymlink. |
| S15-2 | MAJOR | Junction Windows tidak diikuti. Sejak Go 1.23 (`winsymlink`), junction dilaporkan sebagai `ModeIrregular` dan `EvalSymlinks` tidak mengikutinya. `resolve`/`danglingTarget` hanya mengikuti `ModeSymlink`, sehingga `root\j -> %USERPROFILE%\.ssh` menghasilkan `Inside`, bukan `denied_path`, dan junction ke luar root juga `Inside`, bukan `outside_root`. Open-nya lalu gagal `outside_workspace` (fail-closed), tetapi verdict-nya salah dan approval tidak pernah ditawarkan. ADR-097 mengklaim junction ditangani; tidak ada test junction. | `internal/sandbox/sandbox.go:203-256`; `GOROOT/src/os/types_windows.go:150-200`; `GOROOT/src/path/filepath/symlink.go:85-90`. [INFERENCE: butuh runner Windows] | Perlakukan reparse point `ModeIrregular` sebagai link (`os.Readlink` memberi target junction), atau resolve lewat handle. Tambahkan test Windows yang membuat junction (`mklink /J`), tanpa skip. |
| S15-3 | MAJOR | Pencocokan deny bersifat case-sensitive di Linux, padahal Linux juga me-mount FS case-insensitive (WSL `/mnt/c` drvfs, vfat/exFAT, ext4/f2fs casefold, SMB). Di proyek WSL `/mnt/c`, `root/.ENV` membuka `.env` dengan verdict `Inside`. ADR-097 butir 2 menolak pencocokan case-sensitive di macOS dengan alasan yang sama. | `internal/sandbox/sandbox.go:107`. Di ext4: `Check(root/.ENV)=inside`. Efek di mount case-insensitive = [INFERENCE] (tidak bisa di-mount tanpa root). | Cocokkan deny case-insensitive di semua OS. Ini fail-closed; false positive hanya untuk file berbeda seperti `.ENV`. |
| S15-4 | MINOR | macOS: Home/pola non-ASCII NFC vs NFD tidak cocok (APFS mengabaikan normalisasi), dan `strings.ToLower` tidak sama dengan case folding APFS. | `internal/sandbox/sandbox.go:330-367` [INFERENCE: statis] | Normalisasi NFC di darwin, atau gunakan cek inode pasca-open (S16-2). |
| S15-5 | MINOR | Hardlink ke file deny tidak tertangkap (deny berbasis path): `root/hl` (hardlink `~/.ssh/id_rsa`) → `inside`, dan isinya terbaca. Tool kode tidak bisa membuat hardlink, tetapi tarball atau shell yang di-approve bisa. Belum tercatat di TRUST-MODEL §3. | `internal/sandbox/sandbox.go:163-178`; `TestRvProcAndMisc` | Catat sebagai sisa risiko, atau tolak open dengan `st_nlink > 1` untuk (dev, ino) direktori deny literal yang dikumpulkan di `New`. |
| S15-6 | MINOR | Default deny tidak mencakup `/proc/*/task/*/mem` (hasilnya `outside_root`, bisa di-approve, padahal `/proc/*/mem` ditolak) dan `.env.local`/`.env.production`. Ini celah daftar default spec, bukan kode. | `SPEC.md` §12 `deny_paths`; `TestRvProcAndMisc` | Lewat ADR: tambahkan `/proc/*/task/*/mem` dan `**/.env.*` (kecuali `.env.example`). |
| S15-7 | MINOR | Path relatif di-resolve terhadap cwd proses, bukan root sesi (CODE-TOOLS §9). Fail-closed, tetapi file yang dimaksud salah di mode serve/gateway. | `internal/sandbox/sandbox.go:182-194`; `sandbox_test.go:165-174` | Wajibkan path absolut, atau resolve terhadap `Roots[0]`. |

Diperiksa tanpa temuan:
- `../`, path absolut, `//`.
- Rantai symlink ke deny → `denied_path`; symlink dangling keluar → `outside_root`; loop → error.
- Varian `/proc/*/environ`, termasuk `task`, `thread-self`, `/proc/self/root/<home>/.ssh/id_rsa`, `/proc/self/fd/N`, `/dev/fd/N` → `denied_path`.
- Trailing slash/dot, dan direktori bernama `.env`.
- Direktori blob: baca `inside`, tulis `outside_root`.
- Windows (statis): trailing dot/space, nama 8.3, ADS `:`, CON/NUL, prefix `\\?\`.

## PR #16 — `internal/sandbox` open per OS (approve setelah #15)

Upaya escape dengan race symlink tidak berhasil: 3 goroutine × 3.000 iterasi × mode primer dan fallback × 3 run. Kasus yang dicoba: dir ↔ symlink ke `~/.ssh`, file ↔ symlink keluar untuk `O_WRONLY|O_APPEND`, dan absent ↔ symlink dangling keluar untuk `O_CREATE`. Hasilnya LEAK=0, tulis keluar=0, buat keluar=0.

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| S16-1 | MAJOR | Windows: setiap junction di dalam root gagal `outside_workspace`. `os.Root` menolak target reparse absolut, dan target junction selalu absolut. Akibatnya layout pnpm `node_modules` dan dev-drive tidak bisa dibaca. Klaim ADR-097 "symlink dan junction" tidak diuji. | `internal/sandbox/open_windows.go:20-47`; `GOROOT/src/os/root_windows.go:39-78`; `root_openat.go:455-470`. [INFERENCE: statis] | Perbaiki bersama S15-2 (resolve junction di `Check`, sehingga path yang dibuka bebas reparse point). Tambahkan test junction Windows. |
| S16-2 | MAJOR | `OpenFile`/`OpenApproved` tidak mencocokkan ulang deny terhadap handle yang dibuka, sehingga alias S15-1 dan alias Windows (`\\.\C:\`, `\\?\Volume{GUID}`, `\\?\GLOBALROOT`, `\\localhost\C$`, `subst`) lolos sebagai `outside_root` dan bisa di-approve. Kode sudah memiliki path handle (`finalPath`, `/proc/self/fd`), tetapi tidak dicek terhadap deny. | `internal/sandbox/open.go:56-70`; `open_windows.go:36-38`; `normalize.go:386-405` | Setelah setiap open: ambil path handle (`GetFinalPathNameByHandle` / `readlink /proc/self/fd/N`, juga di jalur openat2), jalankan `denied()`, lalu tutup dengan `ErrDeniedPath` bila cocok. |
| S16-3 | MINOR | Fallback Linux < 5.6/seccomp dan macOS membuka direktori antara dengan `O_RDONLY\|O_DIRECTORY`, yang butuh izin baca, sedangkan openat2 hanya butuh izin search. Akibatnya ada false negative untuk direktori `0111`. | `internal/sandbox/walk_unix.go:228`; `TestRvExecOnlyDir`: primer ok, fallback `permission denied` | `O_PATH` (Linux) / `O_SEARCH` (darwin) untuk direktori antara. |
| S16-4 | MINOR | Fd root dibuka ulang lewat path (mengikuti symlink) pada setiap call, sehingga rename komponen root antara `Check` dan open bisa membelokkan open. Ini butuh akses tulis ke induk root. | `internal/sandbox/open_linux.go:115`; `walk_unix.go:222` | Buka `O_PATH`/`os.Root` sekali di `New` dan simpan. |
| S16-5 | MINOR | `OpenApproved` tidak terikat pada path ter-resolve yang di-approve user. Link yang ditukar setelah approval membuka file lain yang tidak di-deny. | `internal/sandbox/open.go:56-70` | Terima path ter-resolve yang di-approve sebagai parameter; bila berbeda → `ErrOutsideWorkspace`. |
| S16-6 | MINOR | CT-26 `TestSymlinkEscapeRace` bisa lulus tanpa race: test tidak mengasersi `opened>0 && refused>0` dan mengabaikan error flipper. (Log saat ini menunjukkan race memang terjadi: primer 604/347, fallback 152/703.) | `internal/sandbox/open_test.go:133-151`, `:179-187` | Asersi kedua hitungan > 0 per mode, dan gagalkan test bila flipper keluar sebelum `Kill`. |

Diperiksa tanpa temuan:
- openat2 `RESOLVE_BENEATH|NO_SYMLINKS|NO_MAGICLINKS`: EAGAIN/EINTR di-retry ≤ 16 kali lalu `ErrOutsideWorkspace`; EXDEV/ELOOP → `ErrOutsideWorkspace`; ENOSYS/EPERM → fallback.
- Fallback fail-closed: walk menolak symlink, dan `verifyProcFd` wajib cocok persis (gagal bila `/proc` tidak ada).
- macOS `verifyInode` (dev+ino).
- `O_CREAT` pada symlink dangling/tertukar ditolak, dan tidak ada yang dibuat di luar root.
- Tidak ada pembuatan direktori induk.
- Test bernama yang ada: `TestSymlinkEscapeRace` (CT-26), `TestCodeToolsSymlinkOutsideRoot`, `TestDenyPaths`.

## PR #17 — `internal/netx` DLP egress + log metadata (changes required)

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| NETX-1 | **BLOCKER** | PR tidak bisa di-merge dan membawa salinan basi PR #13. Branch ini berisi commit redact sendiri, `e1719cd`, yang merupakan versi lebih lama dari head #13 (perf test 3 sampel; head #13 kini `af95824` dengan rasio berpasangan). Akibatnya merge-base = `1a644e2`, dan GitHub menampilkan 18 file termasuk seluruh paket redact. PR ini juga konflik dengan base (`CHANGELOG.md`, `perf_test.go`) dan dengan `main` (`docs/F1-REPORT.md`). | `gh pr view 17`: CONFLICTING/DIRTY. `git merge-tree origin/f1-redact-a 54baeba` → konflik. `git diff 89aa7a5 e1719cd` → hanya `perf_test.go`. | Rebase `54baeba` ke `f1-redact-a` terkini dan buang `e1719cd`. Setelah #13 merge, rebase ke `main`. Diff PR harus hanya berisi netx + docs + CHANGELOG. |
| NETX-2 | **BLOCKER** | URL-encoding hanya dicakup dalam satu bentuk kanonik. `secretForms` hanya menambahkan `url.QueryEscape`/`url.PathEscape` (hex huruf besar, byte unreserved tetap mentah), dan `check` memindai `req.URL.String()` yang mempertahankan `RawQuery`/`RawPath` apa adanya. Bentuk RFC 3986 lain lolos, padahal server men-decode-nya kembali menjadi secret: setiap byte di-percent-encode (query dan path), `%xx` huruf kecil, double encoding, dan body form. GAP-SEC-16 dan ADR-100 butir 8 menyebut "url-encoded" sebagai encoding yang dicakup, dan instruksi seperti ini adalah yang paling mungkin diberikan prompt injection. | `internal/netx/dlp.go:49-50`, `:86`. `TestProbeBypass` sekali pakai: query/path/form pct-setiap-byte, double-encoding, dan pct huruf kecil semuanya `blocked=false`. Kontrol (raw, vault JSON, userinfo) `blocked=true`. | Percent-decode path, kunci/nilai query, dan body `application/x-www-form-urlencoded` berulang sampai stabil (dibatasi; hex case-insensitive), lalu pindai bentuk decode dan mentah. Tambahkan 5 kasus itu ke `TestEgressDLP`. |
| NETX-3 | MAJOR | Budget overhead GAP-SEC-16 (≤ 50 µs per request 8 KB, Aho-Corasick) tidak terukur dan tidak terpenuhi. Benchmark PR memakai body 2 KiB. Matcher berbasis byte pertama + perbandingan literal, sekitar 11 bentuk per secret, sehingga biayanya linear terhadap jumlah secret × ukuran body. Hasil ukur 8 KB: 10 secret 65–68 µs; hexdump 83–87 µs; 50 secret 88 µs (hex 243 µs); 200 secret 173–198 µs; secret dengan prefix bersama di hex 290 µs, 200 secret hex 4,97 ms. Klaim "≈15 µs" di ADR-100 butir 8 dan F1-REPORT tidak menguji requirement ini. | `internal/netx/dlp_test.go:151-160`; `internal/redact/redact.go:53-80`; benchmark sekali pakai `BenchmarkProbe8K*` | Benchmark 8 KB dengan ≥ 50 secret di bawah nama spec `BenchmarkDLPScan`, masukkan ke bench-budget. Gunakan automaton multi-pola dan berhenti pada hit pertama, tanpa Redact penuh + salinan. Koreksi klaim di ADR-100/F1-REPORT/PR. |
| NETX-4 | MAJOR | Secret di host URL yang diblok ikut tercetak di error dan log DEBUG. Eksfiltrasi lewat subdomain (`http://<secret>.evil.example/`) diblok dengan benar, tetapi `Do` membangun error dari `req.URL.Host` dan `logRequest` mencatat host tanpa redaksi. Ini melanggar P29/§14.3. | `internal/netx/client.go:124-125`, `:170`; `dlp_test.go:60`. `TestProbeHostLeak`: `blocked=true errLeak=true logLeak=true` | Redaksi host (`dlp.r.String`) di error dan log, atau hilangkan host saat diblok. Tambahkan kasus host ke `TestEgressDLP`/`TestLogMetadataOnly`. |
| NETX-5 | MAJOR | Kasus URL-encoding di `TestEgressDLP`/`TestSecretForms` kosong. `vaultSecret` hanya berisi karakter unreserved, sehingga `QueryEscape` = nilai mentah dan test tetap lulus walau bentuk URL dihapus. Inilah sebabnya NETX-2 tidak terdeteksi. Selain itu, setiap encoding hanya diuji di satu lokasi, bukan lokasi × encoding seperti GAP-SEC-16, dan tidak ada subtest `t.Run`. | `internal/netx/dlp_test.go:18`, `:48`, `:100` | Pakai secret dengan byte reserved (`a/b+c=d&e<f>"`). Ubah menjadi tabel lokasi {query, URL path, body, header} × encoding dengan `t.Run("web_fetch/<loc>/<enc>")`, termasuk percent-encoding non-kanonik. |
| NETX-6 | MINOR | `req.Host` dan `req.Trailer` dikirim ke wire tetapi tidak dipindai. | `internal/netx/dlp.go:84-116` | Pindai keduanya, atau tolak request Tool yang menyetelnya. |
| NETX-7 | MINOR | Byte yang dipindai (`GetBody()`) bisa berbeda dari yang dikirim (`req.Body`). `io.ReadAll` tanpa batas memakai memori sekitar 3× ukuran body. | `internal/netx/dlp.go:99-115` | Baca sekali dengan cap (fail-closed), pindai, lalu setel `Body`/`GetBody` dari byte yang sudah dipindai. |
| NETX-8 | MINOR | Transformasi lain tidak terdeteksi: keluaran `json.Marshal` untuk secret yang berisi `& < > " \`, escape `\uXXXX`, base64 MIME yang dibungkus per 76 kolom, hex campuran case atau berpemisah titik dua, body gzip, base32, uppercase, entitas HTML. Base64 untuk secret 7 byte terlewat di 1 dari 3 alignment (`minEncoded=8`). | `internal/netx/dlp.go:28`, `:34-58` | Minimal tambahkan bentuk JSON-escaped dan hex case-insensitive, serta strip CR/LF sebelum pencocokan base64. Sisanya catat sebagai sisa risiko di ADR-100/MITIGATION. |
| NETX-9 | MINOR | Nilai vault pendek (`admin`) memblok URL biasa (`/administration`), sehingga tool mati tanpa pemberitahuan yang jelas. | `internal/netx/dlp.go:45` | Dokumentasikan, atau terapkan panjang minimum/batas kata khusus di egress. |
| NETX-10 | MINOR | `Purpose(0) == Inference`: client yang lupa mengisi Purpose melewati DLP dan transport khusus alamat publik. | `internal/netx/client.go:20-29`, `:104-112` | Jadikan 0 tidak valid (request ditolak), atau jadikan `Tool` sebagai nilai nol. |
| NETX-11 | MINOR | Set secret dibekukan saat `New`, sehingga nilai vault/OAuth yang dimuat atau dirotasi kemudian (F2) tidak dipindai. | `internal/netx/client.go:63`, `:69` | `SetSecrets` atomik sebelum F2, atau dokumentasikan di ADR-100. |
| NETX-12 | MINOR | Tidak ada event `dlp_blocked` (P29/GAP-SEC-16), dan `ErrSecretEgress` bukan `ErrBlockedHost{reason}` yang bertipe. | `internal/netx/dlp.go:20` | Catat penundaan event ke F2 di ADR-100, atau tambahkan hook. |
| NETX-13 | MINOR | Nama benchmark di spec, `BenchmarkDLPScan`, tidak ada; PR memakai `BenchmarkEgressDLP`. | `internal/netx/dlp_test.go:153` | Gabungkan dengan perbaikan NETX-3. |
| NETX-14 | MINOR (di luar PR; `dial.go` sudah ada di main) | Klasifikasi SSRF menganggap alamat berikut publik: NAT64 `64:ff9b::/96` (mis. `64:ff9b::a9fe:a9fe` = metadata), IPv4-compatible `::7f00:1`, 6to4 `2002:7f00:1::`, `fec0::/10`, `0.0.0.0/8`, `198.18/15`, `240/4`, `192.0.0/24`. | `internal/netx/dial.go:48-51`; `TestProbeIPs` | Follow-up terpisah untuk pemilik netx: ekstrak IPv4 tertanam lalu periksa, dan tambahkan rentang special-purpose IANA. Menurut [INFERENCE], efek NAT64 → metadata bergantung pada resolver/gateway DNS64 di jaringan. |

Diperiksa tanpa temuan:
- Bentuk yang diblok sebelum koneksi dibuka (conns=0): raw, base64 std/url (padded/unpadded, semua alignment untuk ≥ 8 char), hex huruf kecil/besar, Query/PathEscape kanonik, userinfo, Basic auth, dan pola §14.3.
- Body Tool tanpa `GetBody` → `ErrBodyNotRewindable`, fail-closed.
- Semua header request Tool dipindai.
- Inference dan Background tidak dipindai, sesuai P29 (cakupannya egress dari argumen tool).
- Analyzer `nohttpclient` menjadikan netx satu-satunya pembuat client.
- Setiap retry dipindai ulang.
- Hop redirect tidak mengirim byte baru yang belum dipindai.
- Log DEBUG hanya berisi metadata (path diredaksi, tanpa query/header/body), dan INFO kosong.

## Daftar BLOCKER dan MAJOR

| ID | PR | Severity | Ringkas |
|---|---|---|---|
| S-01 | #14 | BLOCKER | Panic nil pointer di stream redaksi saat hold `url_password` mencapai cap 4 KiB (dipicu output URL panjang biasa) |
| S15-1 | #15 | BLOCKER | `deny_paths` bisa dilewati lewat direktori deny/Home bersymlink: kunci terbaca tanpa approval, atau bisa di-approve |
| NETX-1 | #17 | BLOCKER | PR konflik dan membawa salinan basi redact #13 (`e1719cd`) |
| NETX-2 | #17 | BLOCKER | DLP dilewati dengan percent-encoding non-kanonik (setiap byte, huruf kecil, double, body form) |
| R-02 | #13 | MAJOR | `url_password` tidak menangkap username kosong, `/` dan `@` di password, dan bentuk `\/` |
| S-02 | #14 | MAJOR | Test/fuzz tidak mencapai hold-cap per pola, maupun input > 4 KiB |
| S15-2 | #15 | MAJOR | Junction Windows tidak diikuti, sehingga verdict `Inside` salah |
| S15-3 | #15 | MAJOR | Deny case-sensitive di Linux pada FS case-insensitive (WSL/vfat/casefold) |
| S16-1 | #16 | MAJOR | Junction Windows di dalam root selalu gagal `outside_workspace` |
| S16-2 | #16 | MAJOR | Tidak ada cek deny ulang terhadap path handle yang dibuka (alias Windows, S15-1) |
| NETX-3 | #17 | MAJOR | Budget 8 KB ≤ 50 µs tidak diukur dan tidak terpenuhi (65–290 µs, 4,97 ms adversarial) |
| NETX-4 | #17 | MAJOR | Secret di host URL yang diblok tercetak di error dan log DEBUG |
| NETX-5 | #17 | MAJOR | Test URL-encoding kosong (secret tanpa byte reserved), lokasi × encoding tidak dicakup |

## Putaran 2 (re-review 2026-10-19)

Reviewer sama, tetap independen. Kode yang direview tidak diubah. Eksperimen dijalankan di salinan `/tmp` yang dihapus setelah selesai, dan tidak ada secret yang dicetak. Untuk perbaikan yang diklaim, test-nya dicek non-vakum dengan cara memutasi kode perbaikan di salinan: test harus gagal ketika perbaikannya dilepas.

| PR | Head | Base | Verdict putaran 2 | BLOCKER | MAJOR terbuka | MINOR terbuka/baru |
|---|---|---|---|---:|---:|---:|
| #13 | `be68a23` (sudah di-merge) | — | — (R-02 diverifikasi di `main`) | 0 | 0 | 2 (R-03, R-04) |
| #14 redact stream | `7321615` | `main` | **approve** | 0 | 0 | 3 |
| #15 sandbox verdict | `f7499c4` | `main` | **approve** | 0 | 0 | 4 |
| #16 sandbox open | `c1095ed` | `f1-sandbox-a` | **approve** (MAJOR S16-7 wajib diputuskan sebelum acceptance F1) | 0 | 1 | 4 |
| #17 netx DLP | `f40e83d` | `main` | **approve** (MAJOR NETX-15/16 wajib ditutup sebelum acceptance F1) | 0 | 2 | 9 |

Definisi severity sama dengan putaran 1. Karena itu MAJOR tidak menahan merge, tetapi wajib ditutup sebelum acceptance F1.

### Perintah yang dijalankan

- `go test -race -count=1 ./internal/{netx,redact,sandbox}/...` di setiap head: semuanya ok.
- `GOOS=windows|darwin|freebsd go vet` + `go test -c ./internal/sandbox`: ok.
- CI GitHub:
  - #15 `f7499c4`: run 39158150707, termasuk `test (windows-latest, -race)` dan `windows-11-arm` lulus. `TestJunctionVerdicts` dan `TestJunctionOpen` memakai `mklink /J` dengan `t.Fatalf`, jadi tidak pernah di-skip.
  - #17 `f40e83d`: 24 lulus, 24 cancelled (run yang tergantikan). Status `mergeable`, tetapi *behind* `main`.
- Benchmark:
  - `BenchmarkDLPScan -count=5`
  - `BenchmarkOpenFile`
  - stream redact dengan chunk 1/4/64 B
- Fuzz `FuzzRedactStream` sekitar 860 ribu eksekusi, ditambah seed > 4 KiB.
- Repro coverage EC-SEC-008: 5/5 untuk set terarah dan 3/3 untuk `./...` penuh. Juga lulus dengan `-race -cpu 1,4,12`.

### PR #17 — netx DLP @ `f40e83d`

| ID | Status | Bukti | Catatan |
|---|---|---|---|
| NETX-1 | closed | `git diff origin/main...f40e83d` hanya berisi netx/docs/CHANGELOG/Makefile; `git merge-tree origin/main` bersih | |
| NETX-2 | closed | `internal/netx/dlp.go:198-224` (lapisan decode), `:273-290` (`pctDecode`) | Diblok: percent setiap byte (hex huruf kecil, besar, dan campuran), double/triple, `%25` bersarang, form `+`, `QueryEscape`⁴, `json.Marshal` di dalam query. Celah sisa: NETX-17. |
| NETX-3 | partial → digantikan NETX-16 | `dlp_test.go:305-327` (`BenchmarkDLPScan`), Makefile `PERF_BENCH` | Benchmark 8 KB sudah ada dan masuk `make perf`, tetapi korpus "plain" tidak memuat `%` atau `+`. |
| NETX-4 | closed | `client.go:139-142` (error tanpa host), `:184` (`[blocked]`); `TestLogMetadataOnly` | Jalur refuse terpisah: NETX-21. |
| NETX-5 | closed | `dlp_test.go:24` (`reservedValue`); tabel lokasi × encoding dengan `t.Run` | |
| NETX-6 | closed | `dlp.go:166-174`; `TestDLPWire` host/trailer | |
| NETX-7 | partial (MINOR) | `dlp.go:175-189` | Body dibaca sekali dengan cap 8 MiB+1 dan dipasang ulang. Sisa: NETX-18. |
| NETX-8 | residual terdokumentasi | ADR-100 butir 8 | Bentuk JSON-marshal, hex case-insensitive, uppercase, dan strip CR/LF sudah ditambahkan. Escape `\/` dan pembungkus selain CR/LF **tidak** tercantum (NETX-19/20). |
| NETX-9 | residual terdokumentasi | ADR-100 butir 8 | |
| NETX-10 | closed | `client.go:20-31` (`iota+1`), `:125-126` (`ErrPurpose`) | Purpose 0/4/255 ditolak. |
| NETX-11 | closed | `client.go:87` (`atomic.Pointer`, `SetSecrets`) | Probe `-race` 4 penulis × 4 `Do`: selalu diblok. |
| NETX-12 | residual terdokumentasi | ADR-100 butir 8 (ditunda ke F2) | |
| NETX-13 | closed | `dlp_test.go:305` | |
| NETX-14 | closed | `dial.go:44-90`; `client_test.go:116-120` | Delapan rentang dari putaran 1 sudah non-publik. Sisa: NETX-22. |

**Penilaian kinerja (toleransi ×1,5).** Untuk input 8 KiB "plain":

| Ukuran | Hasil |
|---|---|
| Diklaim (CI EPYC, dari ADR) | 46–53 µs |
| Mesin reviewer, `-cpu=1`, load rendah | plain/50 34–46 µs; plain/200 42–62 µs |

SPEC §17 dan ADR-077 menetapkan ×1,5 hanya untuk waktu absolut di runner CI, sedangkan target persis diverifikasi di mesin referensi Q14 saat RC. Jadi melewati 50 µs **hanya** berkat ×1,5 pada input plain **dapat diterima** (paling berat MINOR).

Masalahnya ada di korpus. Benchmark tidak mewakili request nyata, dan request realistis melewati 75 µs (= 50 × 1,5). Ini MAJOR, dicatat sebagai NETX-16.

| Input | Waktu |
|---|---|
| plain + satu `+` | 81–82 µs |
| 50 secret pendek (< 8 byte) | 92–96 µs |
| decode/200 | 143–165 µs (batas adversarial terdokumentasi 150 µs) |

#### Temuan baru #17

| ID | Severity | Temuan | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| NETX-15 | MAJOR | Batas kerja `dlp_limit` memblok request yang sah. Budget dihitung `2·len + 2 KiB`, padahal setiap lapisan men-decode ulang seluruh buffer. Akibatnya satu token double-encoded (`%2520`, `redirect_uri`/link pelacak yang di-encode dua kali) di request ≥ ~1,5–2 KiB langsung kena `dlp_limit`, sehingga `web_fetch` gagal tanpa ada secret. `TestDLPBounds` malah mengunci perilaku ini. | `internal/netx/dlp.go:199-203`. Probe `check()` dengan 50 secret: query `next=`+`QueryEscape²(url)` + body prosa → pass di 512/1500 B, `dlp_limit` di ≥ 2048 B (sampai 64 KiB). `%252541` → `dlp_limit` mulai 1500 B. Body `"C++ … 50%25ab"` → `dlp_limit` di ≥ 2048 B. | `dlpMaxDepth` sudah membatasi kerja ke ≤ (1+3)·n. Hapus batas kerja tambahan atau jadikan ≥ 4n, atau decode hanya span di sekitar `%`/`+`. Tambahkan kasus negatif ini ke `TestDLPBounds`. |
| NETX-16 | MAJOR | Budget GAP-SEC-16 (≤ 50 µs per 8 KB) tidak terpenuhi untuk request realistis. Satu `%` atau `+` di mana pun membuat seluruh buffer melewati lapisan decode. Secret pendek dicek dengan `bytes.Contains` per secret per lapisan. | `dlp.go:215`, `:245`; angka di tabel kinerja di atas | Tambahkan kasus `plain+pct` ke `BenchmarkDLPScan` (gate `make perf`). Decode hanya span yang berubah, dan masukkan secret pendek ke indeks. |
| NETX-17 | MINOR | Kedalaman decode yang habis meloloskan request: pct-all lalu `%25` 3–4 kali **lolos** (request kecil), padahal batas lain fail-closed. | `dlp.go:215` (`depth == dlpMaxDepth` → `return nil`) | Bila lapisan ke-3 masih bisa di-decode, kembalikan `dlp_limit`. |
| NETX-18 | MINOR | `Body` nil/`NoBody` dengan `GetBody` diset: setelah 307, isi `GetBody` terkirim tanpa di-scan. | `dlp.go:175`. Probe: 2 hop, `secret_on_wire=true` | Di cabang tanpa body, set `req.GetBody = nil`. |
| NETX-19 | MINOR | `keepCRLF` bersifat global: satu secret multi-baris (PEM) mematikan deteksi base64 terbungkus untuk semua secret. Base64 yang dibungkus spasi, tab, U+2028, atau escape JSON literal `\r\n` lolos. | `dlp.go:66`, `:235` | Jalankan pass terpisah yang membuang whitespace/escape untuk bentuk base64/hex. Catat sisanya di ADR-100. |
| NETX-20 | MINOR | Escape JSON `\/` (`{"k":"a\/b…"}`, juga di dalam `QueryEscape`) lolos dan tidak tercantum sebagai residual. | Probe | Tambahkan ke daftar residual ADR-100, atau tambahkan lapisan unescape JSON. |
| NETX-21 | MINOR | Jalur refuse (Purpose tidak valid, background mati) masih mencetak host mentah. | `client.go:136` | Hilangkan host dari pesan, seperti perbaikan NETX-4. |
| NETX-22 | MINOR | Sisa celah SSRF yang masih dianggap publik: NAT64 local-use `64:ff9b:1::/48`, Teredo `2001::/32`, SIIT `::ffff:0:0:0/96`. Rentang dokumentasi/benchmark yang tidak bisa dirutekan juga dianggap publik. | `dial.go:44-90`; `TestProbeIPs` (46 alamat) | Ekstrak IPv4 tertanam untuk ketiga bentuk itu, lalu tambahkan rentang IANA yang tersisa. |
| NETX-23 | MINOR | Batas verifikasi absolut 256 KiB per request, dan setiap hit prefilter dibebani `len(form)` penuh. Contoh: 10 webhook Slack di vault + body 24 KB berisi 600 link → `dlp_limit` (false positive). | `dlp.go:261` | Hitung hanya byte yang benar-benar dibandingkan, atau skalakan batas dengan ukuran input. |
| NETX-24 | MINOR | Matcher berupa indeks hash prefix 8 byte + bitset, bukan Aho-Corasick seperti teks GAP-SEC-16. Secara teknis tidak bermasalah: biaya tidak bergantung pada jumlah secret (plain/1000 ≈ 37 µs). Namun penyimpangan dari teks spec belum dicatat. | `dlp.go:41-78`, `:244-270` | Tambahkan satu kalimat amandemen di ADR-100. |
| NETX-25 | MINOR | PR *behind* `main`. | API GitHub `mergeable_state=behind` | Rebase sebelum merge. |

### PR #14 — redact stream @ `7321615` (approve)

Diff #14 hanya mengubah `stream.go`, `stream_test.go`, `match.go` (hapus `nolint`), ADR-096 butir 9, dan CHANGELOG. Kode R-02 sudah ada di `main` lewat #13 (`be68a23`).

| ID | Status | Bukti | Catatan |
|---|---|---|---|
| R-02 | closed (di `main`) | `internal/redact/patterns.go:113-121`, `:140` (user boleh kosong, `opLastAt` di atas `nonWS`, jangkar `://` dan `:\/\/`); `match.go:95-111`; `TestRedaction/url_password_forms` (`redact_test.go:33-55`); ADR-096 butir 6 | Lima mutasi (`@` pertama, tanpa jangkar `:\/\/`, user min 1, password tanpa `/`, tanpa drop) semuanya membuat test gagal. Bentuk `p@ss@db`, `p@ss@@db`, `:\/\/u:p@ss\/x@h`, dan `p\u200b@ss@h` diredaksi utuh tanpa sisa. False positive yang terdokumentasi: `localhost:4873/@babel/core`, `:443/@types/node`, `?u=a@b.com`. Yang tetap tidak terdampak: `git@github.com:…`, `mailto:`, `x.com/@user`, `ssh://git@…`. |
| S-01 | closed | `patterns.go:114` (`drop: nonWS`); `stream.go` `process`/`dropTail` | Repro putaran 1 diulang dengan chunk 1/7/64/5000: hasilnya `see http://localhost:[REDACTED:url_password] tail` tanpa panic, maxHold 4096–4103. Mutasi "hapus drop" → `TestStreamUserinfoHoldCapRepro`, `TestStreamHoldCapPerPattern`, dan seed fuzz semuanya panic, jadi test-nya non-vakum. |
| S-02 | closed | `stream_test.go:326-398` (dibangkitkan dari `r.pats`); seed > 4 KiB di `:193-196` | Setiap pola × 4 bentuk ekor × chunk 1/3/64/4097: tanpa panic, tanpa bocor. Catatan MINOR R2-S-01: oracle fuzz "beda hanya lewat potongan bersentinel" vakum untuk mutan "`dropTail` tidak membuang apa pun". Mutan itu hanya tertangkap unit test. |
| S-03 | not closed (MINOR) | `stream_test.go:14-23`, `:225-236` | Chunk masih 20–80 B, tanpa ukuran "ditahan sampai delta berikutnya". |
| S-04 | closed | doc `Stream` | |
| R-01 / EC-SEC-008 | closed, tidak flaky | `perf_test.go:78-110` | |
| R-03 | not closed (MINOR) | `match.go:282-289` | Untuk known secret `admin:hunter22`, `admin` tetap tampil. |
| R-04 | not closed (MINOR) | `patterns.go:24`, `:106-109`; `redact.go:64` | Bearer ber-kutip tidak diredaksi; secret 10.000 B menaikkan hold di atas cap. |

| ID | Severity | Temuan baru | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| R2-S-02 | MINOR | Ekor known secret bocor setelah potongan fail-closed di stream. Terjadi bila known secret berisi spasi dimulai di dalam kandidat `url_password` tepat sebelum hold mencapai 4 KiB. | Secret `"correct horse battery"`, input `"x http://h:"+N×'a'+secret+" end"` dengan N = 4085..4095, chunk 1. Hasil stream: `…[REDACTED:url_password] horse battery end`; whole-blob tidak bocor. Penyebab: `match.go:267` (`!m.cut` melewati `extend`) dan `dropTail` berhenti di whitespace. | Saat memotong, buang sampai akhir known secret parsial yang dimulai di dalam rentang potongan. Tambahkan kasusnya ke `TestStreamHoldCapPerPattern`. |
| R2-S-03 | MINOR (info) | Biaya stream sebanding dengan hold / ukuran chunk, karena hold dipindai ulang setiap `Push`: chunk 1 B = 1.605 ns/B, 4 B = 396, 64 B = 43–46. Dengan known secret 64 KiB di chunk 4 B: 6.053 ns/B. Tetap linear dan dibatasi hold. | Benchmark sekali pakai `rep_sep_4000` | Ditangani bersama R-04(b/c): batasi panjang known secret. |
| R2-S-04 | MINOR (dok) | `docs/F1-REPORT.md:122,134` (baris #14) belum mencantumkan test baru dan masih berstatus "menunggu review". | — | Perbarui saat merge. |

### PR #15 — sandbox verdict @ `f7499c4` (approve)

| ID | Status | Bukti | Catatan |
|---|---|---|---|
| S15-1 | closed | `internal/sandbox/sandbox.go:317-348` (bentuk kanonik prefix bebas-glob setiap pola absolut), `:377-389`; `Check` `:163-171`; `TestDenyAliases` | Live: `~/.ssh -> ~/dotfiles/ssh` dengan root `~/dotfiles`, Home asli maupun Home bersymlink → `ErrDeniedPath`. Mutasi "hapus canonicalPattern" → `TestDenyAliases` gagal (`sandbox_test.go:127`). Nit MINOR: komentar doc test menyebut OpenFile/OpenApproved padahal body test hanya memanggil `Check`, dan pola `<base>/absent/x` tidak diasersi. |
| S15-2 | closed | `sandbox.go:198-302` (reparse `ModeSymlink\|ModeIrregular` diikuti `os.Readlink`, ≤ 40 hop, target relatif di-join ke induk ter-resolve); `junction_windows_test.go:26` | CI Windows lulus (run 39158150707). Target `\\?\Volume{GUID}` → `outside_root` (fail-closed, ADR-097 butir 6). Residu MINOR: `firstLink` mulai dari komponen ke-2, sehingga junction tepat di bawah volume bergantung pada `EvalSymlinks` (belum diverifikasi di runner). |
| S15-3 | closed | `sandbox.go:396`, `:436-443` (fold di semua OS); `TestDenyPaths/case_folded*` | Mutasi "hapus ToLower" → test gagal di runner Linux. |
| S15-4 | not closed (MINOR) | Tidak ada normalisasi NFC/NFD. Fold memakai `strings.ToLower`: İ→i, tetapi ß, K (U+212A), dan ſ (U+017F) tidak ikut di-fold. | Hanya relevan di FS yang melakukan folding (macOS). |
| S15-5 | partial | lihat S16-7 | |
| S15-6 | not closed (MINOR, spec) | `SPEC.md` §12 `deny_paths` tidak berubah | |
| S15-7 | not closed (MINOR) | `sandbox.go:182-195`, `TestRelativePathUsesCwd` | |

### PR #16 — sandbox open @ `c1095ed` (approve; MAJOR S16-7)

| ID | Status | Bukti | Catatan |
|---|---|---|---|
| S16-1 | closed | `junction_windows_test.go:56` (`TestJunctionOpen`, kedua mode open); CI Windows race (job 117765680766) dan arm64 (117765836780) lulus | Path dari `Check` bebas reparse point, sehingga `os.Root` tidak lagi menolak junction di dalam root. |
| S16-2 | closed untuk alias path dan file deny literal | `open.go:46-47`, `:70`, `:80` (`recheckDeny` di kedua jalur open); `open_linux.go:54-70` (`/proc/self/fd`); `open_windows.go:36-47` (`GetFinalPathNameByHandle`); `sandbox.go:350-362` (`os.SameFile` vs `denyIDs`) | Mutasi "hapus cabang identitas" → `TestOpenRecheckDeny` dan `TestRecheckDenyClosedHandle` gagal. Open → recheck berbasis fd, tanpa jendela TOCTOU. Race 3.000 iterasi diulang: LEAK=0 (opened/refused 537/402 dan 171/697). |
| S16-3 | not closed (MINOR) | `walk_unix.go:222` | |
| S16-4 | not closed (MINOR, residual) | `open_linux.go:44`; `open_windows.go:20` | |
| S16-5 | not closed (MINOR) | `open.go:63` | Sebagian dimitigasi: target tertukar yang mengenai deny ditolak oleh `recheckDeny`. |
| S16-6 | not closed (MINOR) | `open_test.go:133-152`, `:180-188` | |

| ID | Severity | Temuan baru | Bukti / repro | Rekomendasi |
|---|---|---|---|---|
| S16-7 | MAJOR | `deny_paths` masih bisa dilewati lewat hard link ke file **di dalam direktori deny** atau ke target yang cocok glob (`**/.env`, `**/*.pem`). `denyIDs` hanya mencatat identitas pola yang sepenuhnya literal: direktori `~/.ssh` tercatat sebagai inode direktori, bukan inode file kunci, dan pola glob tidak dicatat sama sekali. Di Linux, `readlink(/proc/self/fd/N)` untuk hard link mengembalikan path di root, sehingga cek path juga tidak menangkapnya. ADR-097 butir 10 sudah mencantumkan "hard link ke file di dalam direktori deny" sebagai residual, dan eksploit ini butuh perintah yang sebelumnya di-approve (shell/tar), sehingga bukan BLOCKER. Namun klaim penutupan S15-5 hanya berlaku untuk file literal, dan `TestOpenRecheckDeny` vakum untuk kelas direktori/glob. | `internal/sandbox/sandbox.go:350-362`; `open.go:91-96`. Live di salinan `c1095ed`: `~/.ssh/id_rsa` + hard link `root/hl_key` → `Check=inside`, `OpenFile` membaca isi kunci. Sama untuk `root/.env`. Hard link ke `~/.netrc` (literal) ditolak. | Keputusan maintainer sebelum acceptance F1. Pilihan (a): tolak open file `st_nlink > 1` yang (dev, ino)-nya ada di bawah direktori deny literal, memakai himpunan yang dibangun di `New` lewat walk terbatas. Pilihan (b): tolak semua open `st_nlink > 1` untuk baca code tool di luar daftar izin. Pilihan (c): nyatakan eksplisit sebagai residual di TRUST-MODEL §3, dan perbaiki klaim S15-5 di ADR-097 + F1-REPORT. Tambahkan test direktori dan glob. |
| S16-8 | MINOR | Angka `BenchmarkOpenFile` di ADR-097 (≈ 9 µs, 34 alloc) sudah usang: kini 11,6 µs, 3,16 kB, 44 alloc/op. `recheckDeny` menambah sekitar 2,5–3 µs. | Benchmark | Perbarui ADR-097. |

Upaya bypass sandbox yang ditolak dengan benar (fail-closed atau `denied_path`):
- `/proc/self/root/…/.ssh/id_rsa`, `/proc/<pid>/cwd`, `/proc/self/fd/N`, `/dev/fd/N`.
- Target deny yang dibuat setelah `New`.
- Symlink ke materi deny yang dibuat setelah `New`.
- Glob `**/*.pem` lewat symlink di root.
- Bentuk Windows (statis): 8.3, ADS `file:stream`/`file::$DATA`, UNC `\\localhost\C$`, `\\?\`, `\\.\`, GLOBALROOT.
- Unicode Turkish İ/ı, ß, K (U+212A), ſ: tidak setara di FS case-sensitive, jadi tidak bocor. Relevansinya hanya di FS yang melakukan folding (S15-4).

Bind mount tidak bisa didemonstrasikan karena tidak ada `CAP_SYS_ADMIN`. Berdasarkan pembacaan statis, bind mount di atas root adalah residual (cek berbasis path + identitas).

### Daftar MAJOR terbuka setelah putaran 2 (wajib sebelum acceptance F1)

| ID | PR | Ringkas |
|---|---|---|
| NETX-15 | #17 | `dlp_limit` memblok request sah yang memuat satu token double-encoded di request ≥ ~2 KiB |
| NETX-16 | #17 | DLP 8 KB realistis (`+`/`%`, secret pendek) 81–96 µs, melewati 50 µs × 1,5. Benchmark tidak mewakili request nyata. |
| S16-7 | #16 | Hard link ke file di dalam direktori deny atau target glob lolos `recheckDeny`. Perlu diperbaiki, atau dinyatakan sebagai residual secara eksplisit. |

Semua BLOCKER putaran 1 (S-01, S15-1, NETX-1, NETX-2) sudah closed, dan tidak ada BLOCKER baru.
