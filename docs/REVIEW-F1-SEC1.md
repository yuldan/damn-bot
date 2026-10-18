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
