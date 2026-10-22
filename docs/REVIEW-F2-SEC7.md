# REVIEW-F2-SEC7 — Review keamanan kripto/backup (ADR-124)

Review independen untuk rangkaian PR ADR-124 (enkripsi at-rest opsional dan arsip backup terenkripsi GAP-SEC-01). Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. PR berikutnya (2a backup key store/kontainer, 2b CLI, 2c restore) akan ditambahkan sebagai round baru di dokumen ini.

## Round 1 — PR #185 (key store) — 2026-10-21

**Verdict: REQUEST CHANGES — 2 HIGH, 4 MEDIUM, 4 MINOR, 2 nit.**

Head yang direview: #185 `f2-crypto` `15151b2` (2 commit: `0eab80c` ADR-124, `15151b2` `internal/secret`; basis `main` `85f3554`). Metode: worktree terpisah, Linux amd64, `GOMAXPROCS=2 GOFLAGS=-p=1 nice -n 19`. Probe `TestZZ*` di `internal/secret` dijalankan lalu dihapus.

Gate:
- `go test -race -count=1 ./internal/secret/... ./internal/archtest/...` → **ok**.
- `GOOS=windows|darwin|freebsd go vet` dan `go test -c ./internal/secret` → **ok**.
- `go mod tidy -diff` → bersih; `go mod verify` → `all modules verified`.
- `make loc` → `secret 430 / 720`.

### Cakupan PR 1 dibandingkan yang diminta

ADR-124 butir 12 memecah pekerjaan: **PR 1 = key store at-rest + keychain/file**, sedangkan backup key store, X25519, kontainer `DMBK1`, AEAD/HKDF, dan CLI ada di 2a/2b/2c. #185 hanya berisi `internal/secret` (`secret.go`, `atrest.go`, `fileutil.go`), yaitu KEK at-rest di keychain atau file. **Belum ada** X25519, KEK→DEK wrap, XChaCha20-Poly1305, HKDF, nonce, AAD, atau format wrap; `golang.org/x/crypto` juga belum ada di `go.mod`. Poin-poin tersebut akan direview di round 2a. Konstanta `backupFile`/`BackupAccountPrefix` sudah ada tetapi belum dipakai.

### Temuan

`confirmed` berarti dibuktikan dengan probe; `static` berarti dibuktikan dari kode.

| ID | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|
| SEC7-1 | **HIGH** | confirmed | **Fallback sumber diam-diam setelah provisioning.** `EnsureAtRest` (`internal/secret/atrest.go:74-116`) tidak menerima sumber atau id yang tercatat. Bila `at-rest.key` tidak ada dan keychain *sementara* tidak tersedia (D-Bus/Secret Service mati, collection terkunci, sesi SSH tanpa bus), `keyringAvailable` (`secret.go:125-131`) menganggap error apa pun selain `ErrNotFound` sebagai "tidak tersedia". Akibatnya **KEK file baru dibuat** dengan id berbeda dan tanpa error. Ini langsung melanggar ADR-124 butir 1 ("keychain yang sementara tidak tersedia **tidak** boleh memicu fallback ke kunci file baru — fail-closed `key_unavailable`"). Setelah KEK file baru ada, run berikutnya juga akan lebih dulu memilih file tersebut (lihat SEC7-2), sehingga DB atau DEK yang dibungkus dengan KEK keychain tidak lagi dapat dibuka atau data baru terbungkus kunci lain. | Probe `TestZZSilentFallback`: provisioning `source=keychain id=6ef2…`; keychain mati → `EnsureAtRest` mengembalikan `source=file id=d503…`, `err=nil`, `same key=false`. `AtRest(keychain, id)` dengan benar mengembalikan `ErrKeyUnavailable`, tetapi API yang membuat kunci tidak mengenal provisioning sebelumnya. |
| SEC7-2 | **HIGH** | confirmed | **File yang ditanam mengalahkan kunci keychain.** `EnsureAtRest` selalu mengutamakan `<config_dir>/at-rest.key` bila file itu ada. Pada instalasi yang KEK-nya di keychain, siapa pun yang dapat menulis satu file 0600 di `<config_dir>` (proses user lain, atau agent/shell yang tidak dideny untuk direktori itu; deny bawaan hanya `<config_dir>/secrets.enc`) membuat `EnsureAtRest` **memakai kunci pilihan penyerang**. Data baru kemudian dienkripsi dengan kunci yang diketahui penyerang, sehingga kerahasiaan at-rest hilang. | Probe `TestZZPlantedFilePreferred`: provisioning keychain, lalu menanam `at-rest.key` (0600) berisi kunci penyerang → `source=file attackerKey=true err=nil`. |
| SEC7-3 | MEDIUM | confirmed | **Cek permission rawan TOCTOU dan mengikuti symlink.** `readSmall` (`secret.go:192-218`) memanggil `root.Lstat(name)` → `checkPermissions` → `root.Open(name)` lewat path. Tidak ada `fstat` pada handle, tidak ada `O_NOFOLLOW`, dan tidak ada `os.SameFile` antara Lstat dan handle. `os.Root.Open` mengikuti symlink selama targetnya masih di dalam root. File 0644 atau symlink yang ditukar di antara keduanya tetap diterima. | Probe `TestZZReadRace` (swap `at-rest.key` ↔ file 0644 / symlink ke file 0644 di direktori yang sama, 2 s): kunci dari file **0644 diterima 2.300×**, dan lewat **symlink diterima 4.825×**. Perbaikan: `OpenFile(name, O_RDONLY|O_NOFOLLOW)` → `f.Stat()` → cek tipe, mode, dan owner pada handle (Unix: `Stat_t.Uid == os.Getuid()`). |
| SEC7-4 | MEDIUM | confirmed | **Permission dan owner direktori kunci tidak dicek**, padahal ADR-124 butir 1 menyatakan "Direktori kunci 0700". `writeAtomic`/`createExclusive` memanggil `os.MkdirAll(dir, 0o700)`, yang tidak mengubah direktori yang sudah ada. Pembacaan tidak pernah memeriksa mode atau owner direktori. Direktori 0777 memungkinkan pengguna lain mengganti atau menanam `at-rest.key` (SEC7-2/3). Kepemilikan file juga tidak dicek. | Probe `TestZZDirPerms`: config dir `0777` → `AtRest` ok. Direktori `0755` yang sudah ada → `EnsureAtRest` ok, mode tetap `0755`. Perbaikan: `fstat` direktori (handle `os.Root`/`Open(".")`) dengan `mode&0o077 == 0` dan `uid` = pengguna, lalu tolak dengan `ErrPermissions` (tanpa chmod diam-diam, sesuai ADR). |
| SEC7-5 | MEDIUM | static | **Rotasi tidak crash-safe.** `RotateAtRest` (`atrest.go:120-147`) **menimpa** KEK lama, baik di keychain (`Set` pada account yang sama) maupun di file (`writeAtomic` → rename atas `at-rest.key`), *sebelum* pemanggil membungkus ulang DEK. Crash, error, atau kegagalan rewrap di antara keduanya membuat DEK yang terbungkus KEK lama tidak bisa dibuka, sehingga seluruh DB terenkripsi hilang. Selain itu, API tidak menyediakan cara memuat KEK lama setelah rotasi. | Perbaikan: simpan KEK baru dengan id/versi baru (account `at-rest:<profile>:<id>` atau file `at-rest.<id>.key`) dan biarkan KEK lama tetap ada sampai rewrap DEK dikomit. Hapus KEK lama hanya di langkah eksplisit sesudah verifikasi. |
| SEC7-6 | MEDIUM | static | **Operasi keychain mengabaikan `ctx` dan bisa memblokir tanpa batas.** `go-keyring` tidak menerima context. Pada Linux, `Get` memanggil `svc.Unlock`, sehingga collection yang terkunci memicu prompt Secret Service yang menunggu interaksi; pada macOS dapat muncul dialog izin keychain. `ctx.Err()` hanya dicek sebelum panggilan. Pada headless/CI/gateway, `run` atau backup dapat hang. | Perbaikan: jalankan panggilan keyring di goroutine dengan batas waktu (pakai `ctx`), kembalikan `ErrKeyUnavailable` saat timeout, dan dokumentasikan bahwa hasilnya tidak boleh memicu fallback (SEC7-1). |
| SEC7-7 | MINOR | static (+ probe parsial) | **Linux headless: autolaunch D-Bus.** `go-keyring` → `dbus.SessionBus()` (godbus v5.2.2). Bila `DBUS_SESSION_BUS_ADDRESS` kosong dan `/run/user/<uid>/bus` tidak ada, godbus menjalankan `dbus-launch`, yang memunculkan `dbus-daemon` sesi baru yang tertinggal per proses, dan memanggil `os.Setenv("DBUS_SESSION_BUS_ADDRESS", …)` yang mengubah environment proses. Di host review ini (bus user systemd ada, tanpa Secret Service), `EnsureAtRest` tanpa env D-Bus jatuh ke **file** dalam 11 ms, dengan env diisi `unix:path=/run/user/1001/bus`. | Probe headless (env `DBUS_SESSION_BUS_ADDRESS`/`XDG_RUNTIME_DIR`/`DISPLAY` dihapus): `source=file`, tanpa hang, dan jumlah `dbus-daemon` tetap. Jalur autolaunch dianalisis statis (`conn.go:77-87`, `conn_other.go:18`). Perbaikan: deteksi tidak adanya bus atau Secret Service tanpa autolaunch (cek env + socket), atau putuskan keychain-vs-file sekali saat provisioning dan catat hasilnya (lihat SEC7-1). |
| SEC7-8 | MINOR | static | **Material kunci tidak dinolkan dan dapat tercetak.** `KEK.Key` adalah `[]byte` biasa tanpa `String()`/`GoString()`/`Format` yang meredaksi, sehingga `fmt.Printf("%+v", kek)` atau log struct mencetak byte kunci. Salinan base64 (string, immutable) dari file dan keychain tidak bisa dinolkan, dan buffer `readSmall`/JSON tetap di heap. Error yang ada **tidak** memuat material kunci (hanya id/mode), dan `go-keyring` macOS menulis secret lewat stdin `security -i` (bukan argv; `keyring_darwin.go:76-86`). | Perbaikan: tipe kunci dengan `String()`/`GoString()` = `"[redacted]"`, `clear()` buffer sementara (decode dan JSON) setelah dipakai, dan `Close/Zero` pada KEK untuk pemanggil. Penghapusan sepenuhnya dari memori Go memang tidak mungkin dijamin (dicatat sebagai residual). |
| SEC7-9 | MINOR | static | **Windows:** `checkPermissions` langsung `return nil`, termasuk cek symlink dan non-regular (Lstat di Windows tetap melaporkan reparse point/symlink). ACL juga tidak diperiksa. Perlindungan bergantung pada ACL default profil pengguna. | Perbaikan minimal: tetap tolak symlink dan non-regular di Windows, dan dokumentasikan ACL sebagai residual (atau periksa owner/DACL lewat `x/sys/windows`). |
| SEC7-10 | MINOR | static | **Pemilihan sumber pada first-use bergantung pada error apa pun.** `keyringAvailable` menganggap error selain `ErrNotFound` sebagai backend "tidak ada", termasuk error transien atau pengguna yang membatalkan prompt. Pada first-use ini menghasilkan kunci file tanpa pemberitahuan bahwa keychain sebenarnya ada. | Perbaikan: bedakan "backend tidak didukung/tidak ada" (`keyring.ErrUnsupportedPlatform`, tidak ada bus/Secret Service) dari error transien (error → `ErrKeyUnavailable`), dan laporkan sumber yang dipilih ke pengguna. |
| SEC7-11 | nit | static | `keyID = hex(SHA-256(key)[:16])` adalah sidik langsung dari material kunci. Untuk kunci acak 256-bit ini tidak dapat dieksploitasi, tetapi id muncul di DB, header arsip, dan output `create`. | Lebih baik `HMAC-SHA256(key, "damnbot/key-id/v1")[:16]` (domain-separated). Ini harus diputuskan sebelum id tersimpan di data (2a/3). |
| SEC7-12 | nit | static | LOC/ADR: `loc.budget` `secret 720` dengan komentar "terukur 506 (at-rest) + 190 (backup) = 696", padahal kode backup belum ada di PR ini (terukur **430**). ADR-124 butir 12 menyebut `golang.org/x/crypto v0.58.0` dipenuhi ADR ini, padahal `go.mod` belum memuatnya (benar, karena belum dipakai). | Sesuaikan komentar `loc.budget` (terukur 430 di PR 1) dan catat bahwa `x/crypto` masuk di PR 2a. |

### Diperiksa dan bersih

- **Dependency (E-48/ADR-033):** `github.com/zalando/go-keyring v0.2.7` (direct) sudah ada di `deps.allow` (runtime 23). Transitifnya `github.com/godbus/dbus/v5 v5.2.2` dan `github.com/danieljoos/wincred v1.2.3` bertanda `// indirect`. `golang.org/x/sys` naik 0.48 → 0.49 (kebutuhan go-keyring). `go.sum` hanya berubah untuk testify/objx (dependency test go-keyring). Semua modul ter-pin di `go.sum`, dan `go mod verify` ok. `internal/archtest/allowed.go`: `internal/secret` leaf (tanpa import internal). CODEOWNERS dan grafik ENGINEERING diperbarui.
- **LOC:** `secret 430 / 720`. Total core ENGINEERING 37.490 = 36.770 + 720 (konsisten dengan `loc.budget`; lihat SEC7-12 untuk komentar).
- **Pembuatan file:** `createExclusive`/`writeAtomic` memakai `O_EXCL` 0600 + `fsync(file)` + rename/link + `fsync(dir)`. Race first-use antar proses ditangani dengan link (`TestFirstUseRace`). Temp dihapus pada setiap error. Ukuran baca dibatasi 8 KiB.
- **Validasi kunci:** panjang 32 B, `keyID` dicocokkan dengan id tercatat dan isi file, `version == 1`. `AtRest(src, id)` ketat: tidak ada fallback, keychain mati → `ErrKeyUnavailable`, id salah → `ErrKeyMissing`, sumber tak dikenal → `ErrSource`.
- **Keyring macOS/Windows:** secret macOS lewat stdin `security -i` (tidak ada di argv). Windows memakai Credential Manager (`wincred`). Keduanya dapat di-cross-compile.

### Rekomendasi inti (SEC7-1/2)

`EnsureAtRest` hanya boleh dipakai sebagai **provisioning eksplisit** yang menghasilkan `(source, id)` untuk disimpan pemanggil (marker DB/`storage.key`). Semua pembukaan berikutnya wajib memakai `AtRest(source, id)`. Bila marker sudah ada, `EnsureAtRest` harus menolak (atau memanggil `AtRest`). Bila file *dan* item keychain sama-sama ada, atau sumber tercatat tidak cocok, hasilnya error (`key_source_conflict`), bukan pilihan diam-diam. Tambahkan test: keychain-provisioned + keychain mati → `ErrKeyUnavailable` (bukan kunci file baru); keychain-provisioned + file ditanam → ditolak.

**Verdict #185: REQUEST CHANGES** (SEC7-1, SEC7-2 HIGH; SEC7-3…6 MEDIUM).

---

## Round 2 — 2026-10-21 (#185 `b6bcbc6`, #189 `56a16f5`, #187 `942b0d8` pendahuluan)

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #185 `f2-crypto` | `b6bcbc6` | **APPROVE, tetapi hanya di-merge bersama #189** (jangan sendirian; lihat bawah) |
| #189 `f2-crypto-keys-b` | `56a16f5` (basis #185) | **REQUEST CHANGES**: SEC7-13 (MEDIUM), SEC7-16 (LOC) |
| #187 `f2-crypto-backup-a` | `942b0d8` (basis `15151b2`, kode round 1) | **REQUEST CHANGES / belum dapat direview final**: belum di-rebase ke #189 (konflik), dan memakai lagi pemilihan sumber gaya round 1 |

Gate:
- `go test -race -count=1 ./internal/secret/... ./internal/archtest/...` → ok pada `b6bcbc6` dan `56a16f5`. Suite memakai `goleak.VerifyTestMain`.
- `GOOS=windows|darwin|freebsd go vet` + `go test -c ./internal/secret` → ok untuk keduanya.
- `go mod tidy -diff` → bersih.
- `make loc`: `secret 522 / 720` (#185), `613 / 800` (#189).
- #187: `go test -race ./internal/backup ./internal/secret` → ok, dan `go mod verify` → ok (`golang.org/x/crypto v0.58.0` ada di `deps.allow`, ter-pin di `go.sum`).

Probe `TestZZR2*` di `internal/secret` (fake keyring dan keyring asli host) dijalankan lalu dihapus.

### Verdict per temuan round 1

| ID | #185 `b6bcbc6` | #189 `56a16f5` | Bukti |
|---|---|---|---|
| SEC7-1 fallback diam-diam | **FIXED** | FIXED | Marker `at-rest.meta` (`v1 <source> <id>`, dibuat `createExclusive`). `EnsureAtRest` (`atrest.go:82`) memuat marker lalu memanggil `atRest(src, id)` yang **hanya** membaca sumber tercatat. Provisioning hanya dilakukan bila marker tidak ada. Probe: keychain mati setelah provisioning → `ErrKeyUnavailable`. |
| SEC7-2 file yang ditanam | **FIXED (vektor yang dilaporkan)** | FIXED | File kunci kini bernama `at-rest.<id>.key` dan hanya dibaca bila marker menunjuknya. Probe: menanam file kunci saja → id tetap milik keychain. Residual: marker + kunci yang ditanam bersamaan → SEC7-14. |
| SEC7-3 TOCTOU/symlink | **FIXED** | FIXED | `openNoFollow` (Unix: `Lstat` + `OpenFile(O_RDONLY|O_NOFOLLOW)` → ELOOP), lalu `readSmall` memanggil `f.Stat()` dan `checkFile` **pada handle** (regular, mode `&0o077 == 0`, `uid == geteuid`). Probe race (swap ke file 0644 atau symlink): 26.925 / 17.757 penolakan dalam 2 s. Konstruksinya berbasis handle, sehingga inode 0644 atau symlink tidak dapat lolos cek. |
| SEC7-4 direktori 0700/owner | **FIXED** | FIXED | `openRoot` → `root.Open(".")` → `Stat` pada handle → `checkDir` (direktori, mode `0o077 == 0`, owner = euid). Probe: dir 0755 → `ErrPermissions`; kunci 0644 → `ErrPermissions`. |
| SEC7-5 rotasi | n/a (API rotasi dihapus) | **FIXED** | `StageAtRest` menyimpan KEK baru dengan id baru (account `at-rest[:profile]:<id>` / file `at-rest.<id>.key`), dan KEK lama tetap ada. `CommitAtRest` hanya mengganti marker (setelah memastikan KEK baru dapat dimuat). `RetireAtRest` menolak selama marker masih menunjuk kunci lama. Catatan protokol: SEC7-15. |
| SEC7-6 ctx/timeout keyring | **TERBUKA** | **FIXED** | `keyringCall` (`secret.go:151`): `context.WithTimeout(ctx, 10 s)` + goroutine; timeout → `ErrKeyUnavailable`. Goroutine yang hang tertinggal (by design, tidak bisa dibatalkan), dan kunci yang sempat ditulis setelah timeout menjadi item yatim. |
| SEC7-7 autolaunch D-Bus | **TERBUKA** | **FIXED** | `backendPresent` (`backend_linux.go`): hanya bus yang sudah ada (`DBUS_SESSION_BUS_ADDRESS`, `$XDG_RUNTIME_DIR/bus`, `/run/user/<uid>/bus`). Nit: nilai env `autolaunch:` tetap dianggap "ada", padahal godbus akan melakukan autolaunch untuk nilai itu. |
| SEC7-8 redaksi/zero | **FIXED** | FIXED | `KEK.String/GoString/Format` → `[redacted]` untuk semua verb; `Zero()` memanggil `clear`; buffer marker di-`clear`. String base64 dari keychain tidak dapat dinolkan (residual Go). |
| SEC7-9 Windows | **PARSIAL** | PARSIAL | Symlink dan non-regular kini ditolak (`platform_windows.go`), tetapi `Lstat` lalu `root.Open` (tanpa no-follow) masih rawan TOCTOU, dan ACL tidak diperiksa (residual terdokumentasi). |
| SEC7-10 error transien saat first-use | **FIXED** | FIXED, tetapi lihat SEC7-13 | `keyringProbe`: `ErrNotFound` → keychain, `ErrUnsupportedPlatform` → file, error lain → `ErrKeyUnavailable` (fail-closed). |
| SEC7-11 key id | **FIXED** | FIXED | `keyID = HMAC-SHA256(key, "damnbot/key-id/v1")[:16]`. `validID` = 16 B hex, sehingga nama file/account tidak dapat memuat path traversal. |
| SEC7-12 LOC/ADR | **FIXED** | lihat SEC7-16 | #185: `secret 720`, komentar "terukur 430" lalu 522. Total core 37.490 = jumlah `loc.budget`. |

**Apakah #185 tanpa #189 masih tidak aman?** Untuk kerahasiaan dan integritas kunci: **tidak**. SEC7-1/2/3/4/8/10/11 tertutup, dan API rotasi yang destruktif (SEC7-5) dihapus. Yang tersisa adalah risiko **ketersediaan**: panggilan keyring tanpa batas waktu dapat hang selamanya pada prompt unlock Secret Service atau dialog macOS (SEC7-6), dan Linux headless tanpa bus dapat menjalankan `dbus-launch` serta mengubah env proses (SEC7-7). Belum ada pemanggil di `main`, sehingga dampaknya laten. Rekomendasi tetap **merge #185 dan #189 bersama** (atau squash).

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-13 | #189 (#185 sama) | **MEDIUM** | confirmed (host asli) | **Provisioning gagal total di host Linux yang punya session bus tetapi tidak punya Secret Service**, yaitu kasus umum server/WSL/container dengan bus user systemd, atau sesi tmux/ssh dengan `DBUS_SESSION_BUS_ADDRESS` basi. `backendPresent()` bernilai true, lalu `Get` gagal dengan `org.freedesktop.secrets was not provided by any .service files` atau `dial unix …: no such file or directory`. `keyringProbe` memetakannya ke `ErrKeyUnavailable`, sehingga `EnsureAtRest` **tidak** jatuh ke file. ADR-124 butir 1 menjanjikan "keychain bila ada, selain itu file 0600". Arahnya gagal-tertutup (tidak ada kebocoran), tetapi enkripsi at-rest dan kunci backup tidak dapat diaktifkan di host-host tersebut. | Probe pada host review (`EnsureAtRest` dengan keyring asli): (1) env bus basi → `key store unavailable: … dial unix /tmp/dbus-…: connect: no such file`; (2) bus systemd tanpa Secret Service → `… org.freedesktop.secrets was not provided by any .service files`. Perbaikan: **pada provisioning saja**, perlakukan `ServiceUnknown`/`NameHasNoOwner` dan socket bus yang tidak ada sebagai "backend tidak tersedia" → file (dan laporkan sumber yang dipilih). Setelah marker ada, error yang sama tetap `ErrKeyUnavailable`. Tambahkan test dengan error `ServiceUnknown` palsu. |
| SEC7-14 | #185/#189 | MINOR | confirmed | **Marker dan kunci di direktori yang sama.** Penulis `<config_dir>` (pengguna yang sama, atau tool agent bila config dir berada di dalam root sandbox, mis. cwd = `$HOME`; deny bawaan hanya `<config_dir>/secrets.enc`) dapat menanam `at-rest.meta` + `at-rest.<id>.key` miliknya, dan `EnsureAtRest` akan memakai **kunci penyerang**. Untuk DB yang sudah ada, unwrap DEK dengan KEK yang salah gagal (AEAD), sehingga dampaknya sebatas DB baru atau provisioning yang belum terjadi. | Probe `TestZZR2Planted`: marker + kunci ditanam → `attacker=true err=nil`. Perbaikan: tambahkan `<config_dir>/at-rest.*` dan `<config_dir>/backup.keys` ke `deny_paths` bawaan, dan ikat `(source, id)` di sisi DB (`storage.key` / `module_state`) sehingga open selalu memanggil `AtRest(src, id)` dari DB, bukan dari marker. |
| SEC7-15 | #189 | MINOR | static | **Protokol rotasi bergantung pada urutan pemanggil.** Bila pemanggil menulis DEK yang dibungkus ulang (`storage.key`) **sebelum** `CommitAtRest` lalu crash, marker masih menunjuk KEK lama sementara DEK sudah terbungkus KEK baru. KEK baru masih tersimpan, tetapi tidak ada API untuk menemukan kunci yang di-stage. | Simpan id KEK pembungkus di header `storage.key` dan buka dengan `AtRest(src, idDariStorageKey)`, atau tulis kedua bungkus (lama dan baru) sampai commit. Spesifikasikan urutannya di PR 3 beserta test crash. |
| SEC7-16 | #189 | MINOR | static | **LOC tanpa ADR.** `loc.budget` `secret 720 → 800`, tetapi ENGINEERING §2.4 masih `secret 720` dan **Total core 37.490**, padahal jumlah `loc.budget` = **37.570**. ADR-124 butir LOC tidak diperbarui. `TestBudgetTableSum` tetap lulus karena hanya memeriksa batas. | Perbarui ADR-124 (atau amandemen), baris ENGINEERING `secret 800`, dan kedua angka Total core menjadi 37.570. |
| SEC7-17 | #189 | nit | static | `keyringCall` meninggalkan goroutine pada timeout (tidak dapat dibatalkan). `backendPresent` menerima `DBUS_SESSION_BUS_ADDRESS=autolaunch:`. | Dokumentasikan goroutine yang tertinggal (dibatasi satu per panggilan). Perlakukan `autolaunch:` seperti env kosong. |

### #187 (`942b0d8`) — review pendahuluan

#187 masih berbasis `15151b2` (key store round 1) dan **berkonflik** dengan #189 (`git merge-tree`). Fungsi yang dipakainya, `keyringAvailable`/`readKeyFileNamed`, sudah tidak ada di #189. Verdict final menunggu rebase. Catatan atas kode saat ini:

| ID | Severity | Temuan |
|---|---|---|
| SEC7-18 | MEDIUM | `Store.Backup` (`internal/secret/backup.go`) memilih backend lewat `keyringAvailable` gaya round 1 (error apa pun → file), sehingga mengulang pola SEC7-1/10 untuk kunci backup. Selain itu, `backup.keys` (berisi backend, `current`, dan pada backend file juga **private key**) yang ditanam penulis `<config_dir>` akan **langsung dipakai sebagai recipient arsip baru**. Arsip, termasuk yang dibuat dengan `--include-secrets`, lalu terenkripsi ke kunci penyerang. Setelah rebase, terapkan mekanisme yang sama dengan at-rest (probe/backend #189, fail-closed) ditambah deny bawaan (SEC7-14). |
| SEC7-19 | MINOR | HKDF (`header.go` `derive`) memakai `IKM = shared` saja (salt nil, info label). Praktik HPKE/age juga mengikat `ephemeral_pub ‖ recipient_pub` ke dalam KDF. Tamper pada `ephemeral` tetap tertangkap (string ephemeral ada di AAD header dan DEK), tetapi pengikatan di KDF lebih kuat dan murah. |
| SEC7-20 | MINOR | Tidak ada autentikasi pengirim. Siapa pun yang memegang **public key** recipient dapat membuat arsip valid (ECIES), termasuk `config.toml`/DB berbahaya. Public key tidak dipublikasikan (hanya id di header), tetapi restore (2c) harus memperlakukan isi arsip sebagai input tak tepercaya (validasi manifest, konfirmasi sebelum mengganti config dan secrets). Ini sebaiknya dicatat di ADR-124. |
| SEC7-21 | MINOR | `BackupKey` (memuat `Private []byte`) tidak punya redaksi `String`/`Format` dan `Zero`; DEK dan shared secret juga tidak dinolkan. |
| — | info | Reader/parse header belum ada di PR ini (test memakai parser khusus test), sehingga ketahanan parse (batas 4 KiB, ukuran base64, `chunk == 65536`, verifikasi tag atas byte mentah) direview di 2b/2c. Yang sudah benar: nonce 24 B acak dari `crypto/rand` per header dan per wrap; DEK acak 32 B per arsip; prefix nonce chunk 16 B acak; AAD header = `magic‖ver‖len‖raw`; AAD DEK = `magic‖ver‖recipient‖ephemeral‖wrap_nonce` (field berpanjang tetap); titik X25519 low-order ditolak (`curve25519.X25519` mengembalikan error); `x/crypto v0.58.0` ada di `deps.allow`. |

**Ringkasan:** #185 APPROVE bila di-merge bersama #189. #189 REQUEST CHANGES (SEC7-13 MEDIUM, SEC7-16 LOC; SEC7-14/15 MINOR). #187 REQUEST CHANGES / perlu rebase ke #189 (SEC7-18 MEDIUM, SEC7-19..21 MINOR).

---

## Round 3 — 2026-10-21 (seluruh stack kripto/backup)

Stack linear (basis `main` `85f3554`): #185 `b6bcbc6` → #189 `3a55e68` → #187 `b4800f5` → #191 `15690ed` → #192 `36d6704` → #194 `d25b75c`. Catatan proses: PR #187 memakai base `f2-crypto`, padahal branch-nya memuat commit #189, sehingga diff PR #187 ikut menampilkan #189. Base-nya sebaiknya `f2-crypto-keys-b`.

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185 key store | `b6bcbc6` | **APPROVE** (tidak berubah; merge bersama #189) |
| #189 rotasi/timeout/D-Bus | `3a55e68` | **APPROVE**: SEC7-13/14/15/16/17 FIXED, SEC7-9 diperbaiki (residual ACL) |
| #187 kunci backup + header | `b4800f5` | **APPROVE**: SEC7-18/19/21 FIXED, SEC7-20 terdokumentasi |
| #191 writer/reader DMBK1 | `15690ed` | **APPROVE** |
| #192 manifest/create/verify | `36d6704` | **APPROVE dengan MINOR** (SEC7-27, SEC7-29 snapshot, SEC7-30 ADR) |
| #194 CLI + restore bertahap | `d25b75c` | **REQUEST CHANGES**: SEC7-22 (HIGH), SEC7-23 (HIGH), SEC7-24 (MEDIUM), SEC7-25/26/28/29/30 (MINOR) |

Gate di `d25b75c`:
- `go test -race -tags goolm -count=1 ./internal/secret/... ./internal/backup/... ./cmd/damnbot/... ./internal/config/... ./internal/archtest/...` → **ok**.
- `./internal/ctxdb -run 'Scenario|Restore|Snapshot|Maint'` → ok (138 s).
- Test `internal/secret` dan `internal/backup` lulus di setiap commit antara.
- `GOOS=windows|darwin|freebsd go vet` + build `cmd/damnbot` → ok.
- `go mod tidy -diff` bersih; `go mod verify` ok.
- `make loc`: `secret 838 / 850`, `backup 1315 / 1400`, `ctxdb 3533 / 3600`.

Probe `TestZZ*` dan fuzz `FuzzZZ*` di `internal/backup`, plus probe host asli di `internal/secret`, dijalankan lalu dihapus.

### Lapisan key store (#189, #187)

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-13 | **FIXED** | `backendAbsent` (`internal/secret/backend_linux.go`) hanya dipakai pada **provisioning**. `ServiceUnknown`/`NameHasNoOwner`/`FileNotFound`/`Disconnected` atau `*net.OpError` dianggap backend absen → file. Probe host asli: env bus basi → `source:file`; bus systemd tanpa Secret Service → `source:file`. Setelah marker ada, sumber tercatat tetap ketat (`ErrKeyUnavailable`). `godbus/dbus/v5` menjadi dependency langsung (`deps.allow` + ADR-124 butir 12: dipakai untuk mengenali `ServiceUnknown`). |
| SEC7-14 | **FIXED (residual terdokumentasi)** | `defaultDenyPaths` + `<config_dir>/at-rest.*`, `<config_dir>/backup.keys`. Pengikatan `(source, id)` di DB dijadwalkan untuk PR 3 (ADR-124 butir 11). |
| SEC7-15 | **FIXED (spesifikasi)** | ADR-124 butir 11: `storage.key` mencatat id KEK pembungkus dan open memakai `AtRest(src, idDariStorageKey)`. Test crash wajib ada di PR 3. |
| SEC7-16 | **FIXED** | Lihat tabel LOC di bawah; Total core = jumlah `loc.budget` di setiap commit. |
| SEC7-17 | **FIXED** | `DBUS_SESSION_BUS_ADDRESS=autolaunch:` dianggap tidak ada. Goroutine yang tertinggal saat timeout didokumentasikan (`keyringCall`). |
| SEC7-9 | **PARSIAL (residual ACL)** | Windows: open lalu `f.Stat()` + `Lstat` pasca-open dengan `os.SameFile` → reparse point yang diganti terdeteksi. ACL tetap residual terdokumentasi. |
| SEC7-18 | **FIXED** | `Store.Backup` memakai `keyringProbe` yang sama (fail-closed, absen hanya saat provisioning). `backup.keys` termasuk deny bawaan. Nit: pemenang race first-use benar, tetapi item keychain milik proses yang kalah tertinggal sebagai item yatim. |
| SEC7-19 | **FIXED** | `derive(shared, info, bind)` dengan `label = info ‖ 0 ‖ ephemeral_pub ‖ recipient_pub` untuk subkunci header dan wrap. |
| SEC7-20 | **TERDOKUMENTASI** | ADR-124 butir 6: isi arsip diperlakukan sebagai input tak tepercaya (tanpa autentikasi pengirim). Implementasinya dinilai di SEC7-23. |
| SEC7-21 | **FIXED** | `BackupKey.String/GoString/Format` → `[redacted]`; `Zero()`. CLI memanggil `defer key.Zero()`. Pembacaan header men-`clear` shared secret. |

### Kontainer DMBK1 (#187/#191)

- **Parse header** (`read.go`): magic 6 B + versi 1 B (= 1) + panjang u16 dengan `1..4096`, lalu JSON + tag 16 B. Field divalidasi tepat (`v=1`, `wrap="x25519"`, `alg="xchacha20poly1305"`, `chunk=65536`). Base64 diperiksa ukurannya (ephemeral 32, nonce 24, prefix 16, wrapped DEK 48). **Tag diverifikasi** atas `magic‖ver‖len‖raw` sebelum `recipient` dipercaya, lalu DEK di-unwrap dengan AAD `magic‖ver‖recipient‖ephemeral‖wrap_nonce`. Titik X25519 low-order ditolak (`curve25519.X25519` mengembalikan error). `ReadMeta` sengaja tidak terautentikasi (untuk `ls`).
- **Chunk:** nonce = prefix acak 16 B ‖ counter u64 BE, dan DEK acak per arsip, sehingga nonce unik. AAD = `sha256(header) ‖ counter ‖ len ‖ final`. Panjang dibatasi `≤ 65536`; chunk non-final kosong ditolak; byte setelah chunk final ditolak. Akibatnya **truncation** (tidak ada chunk final → `ErrTruncated`), **reorder/duplikat** (counter ada di nonce dan AAD), dan **pemalsuan flag final** (ada di AAD) semuanya tertolak. Test: `TestTamperEveryByte`, `TestTruncated`, `TestTrailingRejected`, `TestEmptyFinalChunk`. Probe: flip byte di chunk terakhir, potong 30 B, atau tambah 1 B → masing-masing `chunk tag`, `ErrTruncated`, dan `trailing payload`.
- **Fuzz** (dua harness, ±640 rb exec, 2 worker): `ReadMeta` + `NewReader` + baca penuh + `Verify` via file, serta varian in-memory (`readAndCheckManifest` + `hashN`). **Tidak ada panic atau crash.** Throughput sempat 0/s pada beberapa interval, tetapi replay korpus "interesting" selesai dalam 0,1 s, sehingga jeda itu bukan hang pada input.
- **Manifest** (`manifest.go`): `v=1`, tidak kosong, path relatif yang sudah `Clean` tanpa `.`/`..`/backslash, terurut dan unik, sha 32 B hex, `size ≥ 0`, `mode ≤ 0o7777`. SHA manifest ada di header terautentikasi, dan ukuran manifest dibatasi 16 MiB. Arsip tidak dikompresi dan setiap byte entri berasal dari stream terautentikasi, sehingga total ekstraksi ≤ ukuran arsip (tidak ada bom ukuran).

### Restore dan CLI (#192/#194)

Probe (data dir sumber dan tujuan nyata, kunci backup dari `testKey`):

| Skenario | Hasil |
|---|---|
| Round-trip dengan **satu blob** | restore `err=nil`, tetapi blob terpasang di **`<data>/e9/<sha>`**, bukan `<data>/blobs/e9/<sha>` → SEC7-22 |
| Arsip rakitan (recipient benar): `blobs/purge.log`, `blobs/aa/<64×a>` dengan isi lain | restore `err=nil`, **`<data>/purge.log` = `EVIL`**, dan file `aa/aaaa…` tertulis di root data dir → SEC7-23 |
| Kunci salah | `ErrAuth` (header tag) sebelum staging; data hidup tidak berubah ✔ |
| Flip byte chunk terakhir / truncate / byte trailing | error bertipe; **DB hidup tidak berubah** ✔, tetapi **direktori `run/restore-*` berisi `damnbot.db` hasil dekripsi tertinggal** setiap kali gagal → SEC7-24 |
| `--dry-run` | data hidup tidak berubah; staging (plaintext) ditulis lalu dihapus → SEC7-25 |
| `--include-secrets` | tanpa flag: `[config.toml damnbot.db]`; dengan flag: `+ secrets.enc`; arsip 0600 ✔ |
| Restore ke config dir yang sudah punya `config.toml` | `config.toml` tetap `KEEP` (tidak ditimpa) ✔; `secrets.enc` dipasang karena belum ada ✔ |
| CLI `damnbot sessions restore <file>` | `usage …` exit 2; yang berfungsi justru `sessions backup restore` → SEC7-26 |

Yang sudah benar:
- Restore dua fase: seluruh entri di-hash per entri dan diperiksa sampai EOF trailing **sebelum** `ctxdb.Restore`.
- `ctxdb.Restore` mengambil barrier pemeliharaan (writer lain di-drain atau gagal `ErrDBBusy` tanpa mengganti DB; lease dibatalkan; skenario S016/S017 lulus), lalu memasang blob **sebelum** DB, dan DB lewat `damnbot.db.restore` + `fsync` + rename atomik.
- Permission: staging `MkdirTemp` + `chmod 0700`; file `O_EXCL 0600`; arsip `.partial` `O_EXCL 0600` → rename; direktori `backups` 0700 ditegakkan (`ensureDir` menolak mode yang lebih lebar).
- `config.toml` tidak pernah ditimpa, dan `secrets.enc` hanya bila `include_secrets` dan belum ada.

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-22 | #194 (`ctxdb.installBlobs`) | **HIGH** | confirmed | **Blob hasil restore terpasang di lokasi yang salah.** `installBlobs` berjalan di `<stage>/blobs` dan memakai `rel` relatif terhadap direktori itu (`"e9/<sha>"`), lalu `s.root.Rename(tmp, rel)` dengan root = data dir. Hasilnya `<data>/e9/<sha>`, bukan `<data>/blobs/e9/<sha>`. Semua blob arsip **hilang secara fungsional**: langkah `restore_missing` menandainya hilang, dan file yatim tertinggal di root data dir. `TestRestoreRoundTrip` tidak memuat blob, sehingga bug ini tidak terdeteksi. | Pasang ke `path.Join("blobs", rel)` dan tambahkan round-trip dengan blob (cek `PutBlob`/`ReadBlob` setelah restore). |
| SEC7-23 | #194 (`stagePath`/`installBlobs`) | **HIGH** | confirmed | **Path dan isi blob dari arsip tidak divalidasi.** `stagePath` menerima `blobs/<apa pun>` (asalkan `validPath`), dan `installBlobs` tidak memeriksa bentuk `blobs/<2 hex>/<64 hex>` maupun `sha256(isi) == nama`. Ditambah SEC7-22, arsip rakitan (siapa pun yang memegang public key recipient dapat membuatnya; ADR-124 butir 6 menetapkan isi arsip sebagai input tak tepercaya) **menulis file sembarang di root data dir**, misalnya menimpa `purge.log` (ledger purge, TRUST-MODEL §3), `run/*`, atau state exec, dan dapat memasang blob dengan isi yang tidak cocok dengan nama content-address-nya. | Whitelist ketat di `stagePath`: `^blobs/[0-9a-f]{2}/[0-9a-f]{64}$` dengan prefix sama dengan 2 hex pertama sha. Di `installBlobs`/`extract`, verifikasi `sha256(isi) == nama` (selain `entry.SHA256`). Tolak entri lain dengan `ErrCorrupt`, dan tambahkan test arsip rakitan (path di luar `blobs/`, nama ≠ isi). |
| SEC7-24 | #194 (`stageArchive`) | **MEDIUM** | confirmed | **Restore yang gagal meninggalkan staging plaintext.** `stageArchive` memakai named return `stage`, tetapi jalur error `return "", err` mengosongkan `stage` sebelum defer `os.RemoveAll(stage)` berjalan, sehingga yang dihapus adalah `""`. Setiap restore yang gagal (tamper, truncate, trailing, kunci benar tetapi entri rusak) meninggalkan `<data>/run/restore-*` berisi DB atau config hasil dekripsi. Ini melanggar "tidak ada file temp plaintext" dan "staging dihapus" (ADR-124 butir 6). | Simpan path staging di variabel lokal untuk cleanup. Hapus juga `run/restore-*` yatim saat start atau saat restore berikutnya. Tambahkan test: restore gagal → `run/` kosong. |
| SEC7-25 | #194 | MINOR | confirmed | `--dry-run` mendekripsi dan **menulis seluruh payload** ke staging di data dir sebelum menghapusnya. ADR menyatakan fase verifikasi tidak menyentuh data dir, dan dry-run seharusnya tidak menulis apa pun. | Jalankan dry-run seperti `Verify` (hash in-memory per entri, tanpa staging). |
| SEC7-26 | #194 | MINOR | confirmed | Bentuk CLI tidak sesuai ADR-124 butir 7 (`sessions restore <file> [--dry-run]`): `damnbot sessions restore X` → usage exit 2, sedangkan yang berfungsi `sessions backup restore X`. | Samakan dengan ADR (atau amandemen ADR + SPEC §10). |
| SEC7-27 | #192/#194 | MINOR | static | `sessions backup ls` mencetak `RecipientID` (serta error) dari header yang **tidak terautentikasi** tanpa sanitasi, sehingga arsip yang ditanam di `backups/` dapat menyuntikkan kontrol terminal. `ReadMeta` tidak memvalidasi format recipient (16 B hex). | Validasi `recipient` sebagai 32 hex di `decodeHeader`, dan sanitasi output `ls` (`cleanLine`). |
| SEC7-28 | #194 (`ctxdb.installDB`, pra-ada) | MINOR | static | `installDB` menghapus `-wal`/`-shm` **sebelum** rename DB baru. Crash di antara keduanya meninggalkan DB lama tanpa frame WAL yang belum di-checkpoint (normalnya drain barrier menutup handle yang melakukan TRUNCATE checkpoint, tetapi proses yang mati mendadak tidak). Hasilnya tidak tercampur, tetapi DB lama kehilangan commit terakhir. | `PRAGMA wal_checkpoint(TRUNCATE)` di bawah barrier sebelum swap, atau pindahkan WAL lama ke samping lalu rename. Tambahkan test crash-point. |
| SEC7-29 | #192/#194 | MINOR | static | (a) `installIfAbsent` memakai `Stat` → rename, sehingga `config.toml` yang dibuat bersamaan dapat tertimpa; `config.toml.restore` sisa crash memblokir restore berikutnya (`O_EXCL`). (b) Snapshot `VACUUM INTO` (`backups/.snapshot-*`, plaintext) tertinggal bila crash; direktorinya 0700. | (a) Publikasikan dengan `link` + `O_EXCL` seperti `secret.createExclusive`, dan bersihkan `.restore` sisa. (b) Bersihkan `.snapshot-*` yatim saat `create`. |
| SEC7-30 | #192/#194 | MINOR | static | **Dukungan ADR untuk budget.** Total core ENGINEERING = jumlah `loc.budget` di setiap commit (37.490 / 37.570 / 38.520 / 38.520 / 38.770 / **39.120**) ✔. Namun ADR-124 butir 12 masih menyebut `secret 720`, `backup ≤ 900`, `ctxdb 3.500 → 3.800/3.900`, Total 38.740, dan amandemen PR 2a/2c menyebut `secret 850 / backup 1150 / Total 38.770`. Kenaikan di #194 (`backup 1150 → 1400`, `ctxdb 3500 → 3600`, Total 39.120) belum ada amandemennya, dan komentar `loc.budget` untuk `ctxdb` masih "3.500". Budget `secret` yang berlaku adalah **850** (bukan 800) sejak #187. | Satu amandemen ADR-124 dengan angka akhir (`secret 850`, `backup 1400`, `ctxdb 3600`, Total 39.120), dan perbarui komentar `loc.budget`. |

**Ringkasan:** #185/#189/#187/#191 **APPROVE**; #192 **APPROVE** dengan MINOR; #194 **REQUEST CHANGES** (SEC7-22/23 HIGH, SEC7-24 MEDIUM). Setelah #194 diperbaiki, jalankan ulang probe arsip rakitan, round-trip blob, dan restore gagal (`run/` kosong).

## Round 4 — 2026-10-21 (stack di-restack + PR 3 at-rest)

Stack linear (basis `main` `0128e6e`): #185 `e18a3d1` → #189 `5f50671` → #187 `4a15631` (base sekarang `f2-crypto-keys-b` ✔) → #191 `cf416fd` → #192 `a20d0ad` → #194 `d7aa8a8` → #197 `f2e6332` (CLI, baru) → #198 `14a0e24` (PR 3: codec + `storage.key` + migrasi 0009 + events/blob terenkripsi).

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185 key store | `e18a3d1` | **APPROVE** (range-diff: identik dengan `b6bcbc6`, hanya rebase) |
| #189 rotasi/timeout/D-Bus | `5f50671` | **APPROVE** (identik dengan `3a55e68`; CI `macos-latest` merah karena `internal/exec` `TestInteractiveCorpus/man/pipe`, flake timing yang tidak terkait) |
| #187 kunci backup + header | `4a15631` | **APPROVE** (identik dengan `b4800f5`) |
| #191 writer/reader DMBK1 | `cf416fd` | **APPROVE** (identik dengan `15690ed`) |
| #192 manifest/create/verify | `a20d0ad` | **REQUEST CHANGES**: SEC7-31 (MEDIUM, `cmd/damnbot` tidak build), SEC7-32 (MINOR) |
| #194 restore bertahap | `d7aa8a8` | **REQUEST CHANGES**: SEC7-24 belum diperbaiki (MEDIUM), SEC7-31 (MEDIUM, CI `vet-cross` FAILURE), SEC7-33 (MINOR) |
| #197 CLI | `f2e6332` | **APPROVE dengan nit** (SEC7-26/27 diperbaiki; nit di SEC7-31/SEC7-27) |
| #198 at-rest (PR 3) | `14a0e24` | **REQUEST CHANGES**: SEC7-34 (HIGH), SEC7-35/36/37 (MEDIUM), SEC7-38/39/40 (MINOR) |

Range-diff `85f3554..d25b75c` vs `0128e6e..14a0e24`: empat commit pertama identik secara kode. `36d6704 → a20d0ad` berubah 36 baris (validasi recipient 32 hex di `decodeHeader`, refactor `removePartial`, test baru). `d25b75c` dipecah menjadi `d7aa8a8` (pengerasan restore) + `f2e6332` (CLI); `14a0e24` baru.

Gate di `14a0e24`:
- `go test -race -tags goolm -count=1 ./internal/secret/... ./internal/backup/... ./cmd/damnbot/... ./internal/config/... ./internal/archtest/...` → **ok**.
- `./internal/ctxdb -race -run 'AtRest|Scenario|Restore|Snapshot|Maint|Migrat'` → ok (190 s).
- `BenchmarkAppend` 1,50 ms/op p99 4,19 ms; `BenchmarkAppendEncrypted` 1,46 ms/op p99 5,18 ms (200 iterasi; target p99 ≤ 10 ms ✔).
- `GOOS=windows|darwin` build `cmd/damnbot` → ok; `go mod tidy -diff` bersih.
- `make loc` di `14a0e24`: `secret 1055 / 1100`, `ctxdb 3695 / 3700`, `backup 1367 / 1400`. Di `a20d0ad`, `d7aa8a8`, dan `f2e6332` juga lulus.
- **`go vet`/build `./cmd/damnbot` gagal di `a20d0ad` dan `d7aa8a8`** (`undefined: runSessions`) → SEC7-31.

Probe `TestZZ*` di `internal/backup` dan `internal/ctxdb` (termasuk DB pra-0009 yang dibuat oleh `f2e6332`) dijalankan lalu dihapus.

### Status temuan Round 3

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-22 | **FIXED** | Round-trip satu blob → `blobs/e9/<sha>` ✔. `installBlobs` memasang ke `blobs/<rel>` lewat tmp `blobs/.install-<sha>` (`O_EXCL`). |
| SEC7-23 | **FIXED** | `stagePath` mewajibkan `validBlobRel` (`blobs/<2 hex>/<64 hex>`, prefix cocok), dan `installBlobs` memeriksa `sha256(isi) == nama`. Probe: `blobs/purge.log` → `corrupt archive: blob path`, `purge.log` tetap `ORIGINAL`; prefix salah → ditolak; nama 64×`a` dengan isi lain → `restore blob … hash`; blob valid terpasang benar. |
| SEC7-24 | **BELUM DIPERBAIKI** | `stageArchive` masih memakai named return `stage` + `return "", err` + defer `os.RemoveAll(stage)`. Probe: tamper chunk terakhir, truncate, dan byte trailing **masing-masing meninggalkan `run/restore-*/{damnbot.db,config/config.toml,blobs/…}`** (plaintext). DB hidup tetap tidak berubah ✔. |
| SEC7-25 | **FIXED** | Dry-run memakai `verifyPayload` in-memory; probe tidak menunjukkan staging baru (`dry-run` dan `dry-tamper`). |
| SEC7-26 | **FIXED** (#197) | `damnbot sessions restore <file> [--dry-run]` menjadi bentuk kanonik. |
| SEC7-27 | **FIXED dengan nit** | `decodeHeader` memvalidasi recipient 32 hex, dan baris sukses `ls` memakai `safe()`. Nit: cabang error `writef(stdout, "%s\terror: %v\n", path, rerr)` mencetak `path` (nama file di `backups/`) tanpa sanitasi. Test baru tidak efektif (SEC7-32). |
| SEC7-28 | **FIXED dengan nit** | `installDB` menjalankan `checkpointOld` (`PRAGMA wal_checkpoint(TRUNCATE)`) sebelum menghapus WAL/SHM. Nit: komentar menyebut DB lama yang korup tetap dilanjutkan, padahal kode mengembalikan error (abort). Perilaku fail-closed dapat diterima, jadi samakan komentarnya. |
| SEC7-29 | **FIXED (a), (b) tetap** | `installIfAbsent` menghapus tmp sisa, lalu `O_EXCL` + `os.Link` (EEXIST → dilewati). Probe: `config.toml` = `KEEP` dan `config.toml.restore` sisa dibersihkan ✔. Regresi kecil: SEC7-33. (b) `.snapshot-*` yatim belum dibersihkan (MINOR, tetap). |
| SEC7-30 | **FIXED** | Jumlah `loc.budget` = Total core ENGINEERING di setiap commit: 37.490 / 37.570 / 38.520 / 38.520 / 38.770 / 39.120 / 39.120 / **39.470**. Amandemen ADR-124 PR 2a–2e (`secret 850 / backup 1400 / ctxdb 3600`, 39.120) dan PR 3 (`secret 1100 / ctxdb 3700`, 39.470) cocok dengan `loc.budget`. |

### Review #198 (at-rest)

Yang sudah benar:
- **AEAD**: XChaCha20-Poly1305 dengan subkunci per scope `HKDF(DEK, "dmbot/at-rest/v1/"+scope)` dan nonce acak 24 B per `Seal` (bukan deterministik; untuk blob ini lebih aman daripada nonce HMAC di draf ADR). Envelope `ver‖nonce‖ct`.
- **`storage.key`**: JSON `{v, source, kek_id, nonce, wrapped_dek}`, dibungkus KEK dengan AAD `dmbot/storage-key/v1|source|id`. Load memakai `AtRest(f.Source, f.KEKID)` yang ketat (SEC7-15 ✔).
- **Aktivasi** (`checkAtRest`): ada marker tetapi codec nil → `ErrEncrypted`; id DEK beda → `ErrEncrypted`; ada codec di DB yang sudah berisi events → `ErrAtRestPlaintext`. Di `run`, DB yang ada tanpa `storage.key` → `OpenCodec(create=false)` gagal, sehingga fail-closed (pesannya bukan `at_rest_existing_plaintext`; kosmetik). KEK yang hilang atau keychain yang tidak tersedia → error bertipe; tidak ada fallback plaintext.
- **Hash chain** dihitung atas plaintext, dan blob refs dihitung dari payload plaintext (GC tidak rusak).
- **FTS**: kolom `content` baris terenkripsi berisi `""`, sehingga `events_fts` **tidak** mengindeks plaintext. Probe: isi FTS `["" "" ""]`, dan file DB tidak memuat kanari. Konsekuensinya, `Search` mengembalikan 0 hit pada DB terenkripsi; blind index dijadwalkan di PR 4 (sudah tercatat di amandemen).
- Migrasi 0009 bersifat additive (`ALTER TABLE … ADD COLUMN`), dan trigger append-only tetap ada. Namun lihat SEC7-34.

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-31 | #192/#194 (`cmd/damnbot/main.go`) | **MEDIUM** | confirmed | **Commit antara tidak dapat di-build.** `a20d0ad` dan `d7aa8a8` masing-masing menambahkan dispatch `sessions` → `runSessions`, padahal `runSessions` baru ada di `f2e6332` (#197). `go vet ./cmd/damnbot` → `undefined: runSessions` di kedua commit; CI #194 `vet-cross` FAILURE. Akibatnya, di head (`14a0e24`) **ada dua blok dispatch identik** (`main.go:24-29`); blok kedua mati. Merge #192 atau #194 secara terpisah akan merusak `main`. | Pindahkan satu-satunya hunk `main.go` ke `f2e6332` dan hapus duplikatnya. Pastikan setiap commit lulus `go vet ./...`. |
| SEC7-32 | #192 (`backup_test.go`) | MINOR | confirmed | `TestReadMetaRejectsBadRecipient` tidak bisa gagal: test hanya `Fatal` bila `decodeHeader` menerima **dan** `len(id) == 32`, sedangkan tidak ada input yang panjangnya 32 (`""`, `"zz"`, 33×`a`). Regresi validasi recipient tidak akan terdeteksi. | Wajibkan error untuk setiap input invalid (tambahkan 32 karakter non-hex, misalnya 32×`z`) dan sukses untuk 32 hex. |
| SEC7-33 | #194 (`installIfAbsent`) | MINOR | confirmed | Bila `<config_dir>` belum ada, restore gagal dengan `create …/config.toml.restore: no such file or directory` **setelah** DB dan blob sudah dipasang. Hasilnya, data hidup sudah terganti tetapi CLI melaporkan gagal (Round 3 masih `MkdirAll`). | `MkdirAll(configDir, 0700)` sebelum fase pasang, atau pasang config sebelum swap DB. |
| SEC7-34 | #198 (migrasi 0009) | **HIGH** | confirmed | **Migrasi 0009 merusak DB yang sudah ada.** `UPDATE memory SET content_len = length(content)` memicu trigger `memory_no_update` (`WHEN dmb_maint() IS NOT 'purge'`), sedangkan `applyMigration` berjalan dalam mode normal. Probe: DB buatan `f2e6332` dengan satu baris `memory`, lalu `Open` di `14a0e24` → `ctxdb: migration 0009_atrest: … memory is immutable; use memory_decisions`. **Setiap instalasi yang punya baris memory tidak bisa membuka DB lagi** setelah upgrade, termasuk pengguna dengan `storage.encrypt=false` (default). Test hanya memakai DB kosong. | Hapus `UPDATE` (pakai `DEFAULT` lalu hitung `content_len` saat baca/tulis, atau `coalesce(content_len, length(content))`), atau jalankan backfill di mode maint yang dikenali trigger. Tambahkan test migrasi dari DB pra-0009 yang berisi memory/summaries/carriers. |
| SEC7-35 | #198 (`atrest.go`, `codec.go`) | **MEDIUM** | confirmed | **AAD hanya scope (`events.content`, `events.payload`, `blob`) dan tidak mengikat identitas baris.** Ciphertext dapat dipindah antar-baris atau antar-sesi tanpa gagal tag. Probe: `content_enc` event sesi A disalin ke event sesi B (penyerang offline dengan akses tulis file DB; trigger di-drop) → `History(B)` mengembalikan `"ALPHA-SECRET-A"` tanpa error. Hash chain akan mendeteksinya, tetapi tidak diverifikasi saat baca. | AAD = `scope‖session_id‖seq` untuk events (keduanya diketahui sebelum `INSERT`); `scope‖sha` untuk blob; kunci stabil serupa untuk tabel lain. Tambahkan test swap → error. |
| SEC7-36 | #198 (`EventHash`) | **MEDIUM** | confirmed | **Kolom `hash` plaintext menjadi oracle isi.** `hash = sha256(prev‖sid‖seq‖kind‖content‖payload‖blob)`, dan semua input selain `content`/`payload` disimpan plaintext. Pemegang file DB dapat menguji tebakan secara offline. Probe: konten `"yes"` dikonfirmasi dari `hash` tanpa DEK. Konten berentropi rendah (jawaban approval, output tool yang umum, payload JSON standar) bocor. | Untuk DB terenkripsi, gunakan HMAC dengan subkunci DEK (`HKDF(DEK,"chain")`) untuk hash chain, atau hash atas envelope ciphertext. Catat pilihannya di ADR-124. |
| SEC7-37 | #198 | **MEDIUM** | confirmed (FTS) / static | **Cakupan parsial yang tidak dinyatakan ke pengguna, dan pembaca yang mengabaikan codec.** (a) Migrasi menambah `summaries.content_enc`, `memory.content_enc`, `reasoning_carriers.payload_enc`, dan `gateway_inbound.payload_enc`, tetapi tidak ada kode yang memakainya. Isi memory, ringkasan percakapan, carrier reasoning, dan antrean gateway tetap plaintext (termasuk `summaries_fts`/`memory_fts`) walau `storage.encrypt=true`. (b) `compactstore` membaca `e.content`/`e.payload` langsung (`compactstore.go:43-50,70`). Pada DB terenkripsi, kompaksi melihat konten kosong dan payload `{}`, sehingga **flag `tainted` dan `called` hilang** (taint tracking melemah). | (a) Enkripsi kolom-kolom tersebut sekarang, atau tolak `storage.encrypt=true` dan dokumentasikan secara eksplisit di ADR/SPEC sampai cakupannya lengkap. (b) Arahkan semua pembaca events lewat `openText`; untuk flag yang dibutuhkan SQL, simpan kolom metadata non-rahasia terpisah. Tambahkan archtest atau grep bahwa `e.content`/`e.payload` tidak dibaca di luar `read.go`. |
| SEC7-38 | #198 (`secret/codec.go`) | MINOR | static | **Provisioning `storage.key` tidak atomik terhadap race.** `OpenCodec` memakai pola `os.Stat` → provision, dan `writeStorageKey` memakai `os.Rename` yang **menimpa**. Dua proses pertama yang berjalan bersamaan dapat menghasilkan DEK yang berbeda; data yang sudah ditulis dengan DEK pertama tidak dapat dibuka (marker DB → `ErrEncrypted`). Tmp bernama tetap `storage.key.tmp` (`O_EXCL`) sehingga sisa crash memblokir provisioning selamanya, dan tidak ada fsync direktori. `readStorageKey` berbasis path (`Stat` lalu `ReadFile`, mengikuti symlink), berbeda dari key store. | Publikasikan dengan `link` + `O_EXCL` (seperti `createExclusive`), lalu baca ulang pemenangnya; tmp acak + bersihkan sisa; fsync direktori; baca via open + `fstat` (no-follow), sama seperti key store. |
| SEC7-39 | #198/#197 (backup) | MINOR | static | Backup DB terenkripsi berisi ciphertext, tetapi manifest **tidak memuat `storage.key`** (DEK terbungkus KEK, aman disertakan). Restore ke data dir baru menghasilkan DB yang tidak bisa dibuka meski KEK tersedia. | Sertakan `storage.key` di arsip (dan pasang bila absen), atau dokumentasikan langkah manual di SPEC §11.6 + uji round-trip DB terenkripsi. |
| SEC7-40 | #198 (`blob.go`) | MINOR | static | AAD blob = `"blob"`, dan `ReadBlob` tidak memverifikasi `sha256(plaintext) == nama`. File blob terenkripsi yang ditukar diterima sebagai isi blob lain. | Ikat `sha` ke AAD (lihat SEC7-35) atau verifikasi hash setelah dekripsi. |

Catatan non-blocking: perintah CLI lain yang membuka ctxdb tanpa codec gagal tertutup (`ErrEncrypted`) pada DB terenkripsi (perilaku aman; dokumentasikan). `damnbot undo`/`review` belum mendukung `storage.encrypt`.

**Ringkasan:** #185/#189/#187/#191 **APPROVE** (rebase saja); #192 **REQUEST CHANGES** (SEC7-31 build; SEC7-32 test); #194 **REQUEST CHANGES** (SEC7-24 masih terbuka, SEC7-31, SEC7-33); #197 **APPROVE dengan nit**; #198 **REQUEST CHANGES** (SEC7-34 HIGH: upgrade DB yang berisi memory gagal untuk semua pengguna; SEC7-35/36/37 MEDIUM). Setelah diperbaiki, jalankan ulang: probe restore gagal (`run/` kosong), `go vet ./cmd/damnbot` per commit, migrasi dari DB pra-0009 yang berisi data, probe swap ciphertext antar-sesi, dan tebakan hash.

## Round 5 — 2026-10-21 (bagian backup: #192/#194/#197)

Cakupan: #192 `3749067`, #194 `78cd07b`, #197 `ee9d5b7`. #185 `e18a3d1`, #189 `5f50671`, #187 `4a15631`, dan #191 `cf416fd` tidak berubah. #198 (at-rest) dan PR 4 **belum direview ulang** di round ini, sehingga SEC7-34..38/40 tetap berstatus seperti Round 4.

Catatan stack: parent `78cd07b` (#194) adalah `bf51e3d`, **bukan** head #192 `3749067`. Range-diff menunjukkan `bf51e3d` = `3749067` **tanpa** `TestReadMetaRejectsBadRecipientBytes` (−17 baris test) → SEC7-42.

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185 / #189 / #187 / #191 | tidak berubah | **APPROVE** (Round 4 tetap berlaku) |
| #192 manifest/create/verify | `3749067` | **APPROVE**: SEC7-31 (bagian #192) dan SEC7-32 FIXED; `storage.key` ikut diarsipkan dengan benar |
| #194 restore bertahap | `78cd07b` | **REQUEST CHANGES (kecil)**: SEC7-24/29b/33 FIXED, tetapi `make loc` **GAGAL** (SEC7-41) dan basis commit basi (SEC7-42) |
| #197 CLI | `ee9d5b7` | **REQUEST CHANGES**: SEC7-43 (HIGH, `damnbot sessions …` tidak terjangkau; `make deadcode` GAGAL) dan SEC7-41 |

Gate:
- `go vet -tags goolm ./...` + `GOOS=windows|darwin|freebsd go vet ./cmd/damnbot ./internal/backup` → **bersih** di `3749067`, `bf51e3d`, `78cd07b`, dan `ee9d5b7` (SEC7-31: setiap commit build/vet).
- `go test -race -tags goolm -count=1 ./internal/backup/ ./internal/secret/ ./cmd/damnbot/` → ok di keempat commit. `./internal/ctxdb -race -run 'Restore|Snapshot|Scenario'` → ok (139 s) di `ee9d5b7`.
- `make loc`: `3749067`/`bf51e3d` lulus (`backup 1122 / 1150`). **`78cd07b`/`ee9d5b7`: `LOC GAGAL backup 1404 > 1400`**. Jumlah `loc.budget` = Total core ENGINEERING (38.770 / 39.120) ✔.
- `make deadcode` di `ee9d5b7` → **GAGAL**: `runSessions`, `sessionsBackupCreate`, `sessionsBackupLS`, `sessionPaths`, `secretStore`, `writef`/`writeln` (`cmd/damnbot/sessions.go`), dan `backup.ReadMeta`.
- Status CI saat penulisan: semua job #192/#194/#197 masih pending.

Probe `TestZZR5` (`internal/backup`, data dir nyata, kunci `testKey`) dan binary `damnbot` dari `ee9d5b7` dijalankan lalu dihapus.

| Skenario | Hasil |
|---|---|
| Restore gagal: flip byte chunk terakhir / truncate 30 B / byte trailing | error bertipe; tree data dir hanya `damnbot.db`, **tidak ada `run/restore-*`** ✔ |
| `--dry-run` | `err=nil`, tidak ada staging ✔ |
| `config_dir` belum ada (2 tingkat) | restore `err=nil`, direktori dibuat (`MkdirAll 0700`) ✔ |
| `config_dir` berupa file | `backup: config dir: … not a directory` **sebelum** DB/`storage.key` dipasang; DB tidak berubah, `storage.key` tidak terpasang ✔ |
| `create` dengan `storage.key` (0600) di data dir | arsip **tidak** memuat byte `storage.key` dalam bentuk clear (kanari tidak ditemukan) ✔; entri manifest `0o600` (`appendOptional`); dibaca langsung dari data dir, tanpa salinan temp |
| Restore ke data dir baru | `storage.key` terpasang byte-identik, mode `0600` ✔; `damnbot.db` 0600 |
| Restore ke data dir dengan `storage.key` lain | `storage.key` tetap `OTHER` (install-if-absent, `O_EXCL` + `link`) ✔; DB tetap diganti → lihat SEC7-39 |
| `.snapshot-stale` di `backups/` sebelum `create` | dihapus ✔ |
| `ReadMeta` dengan recipient 32×`z` | `corrupt archive: recipient id` ✔ |
| `damnbot sessions restore /x.dmbk`, `damnbot sessions backup ls` (binary `ee9d5b7`) | **`Usage of damnbot: -version` exit 2** → SEC7-43 |

### Status temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-24 | **FIXED** (#194) | `stageArchive` memakai variabel lokal `dir` + flag `keep` untuk cleanup. `TestRestoreCorruptLeavesNoStaging` (tampered/truncated/trailing) dan probe: `run/` kosong. |
| SEC7-29b | **FIXED** (#194) | `snapshotFile` menghapus `.snapshot-*` yatim sebelum `CreateTemp`. |
| SEC7-31 | **FIXED** (#192/#194), tetapi **overcorrected** di #197 | Hunk `main.go` dihapus dari #192 dan #194 sehingga keduanya build/vet. Namun #197 tidak menambahkan dispatch ke `main.go` → SEC7-43. |
| SEC7-32 | **FIXED** (#192) | `TestReadMetaRejectsBadRecipientBytes` mengganti recipient di header asli dengan 32×`z` dan mewajibkan `ErrCorrupt` (efektif). Nit: `TestReadMetaRejectsBadRecipient` lama yang tidak efektif masih ada; hapus atau perbaiki. Test baru hilang di basis #194 (SEC7-42). |
| SEC7-33 | **FIXED** (#194) | `preflightConfig` (`MkdirAll 0700` + `IsDir`) berjalan sebelum `storage.key` dan swap DB. `TestRestorePreflightConfig` + probe. |
| SEC7-27 nit | **FIXED** (#197) | Cabang error `ls` memakai `safe(path)` dan `safe(rerr.Error())`. |
| SEC7-28 nit | **FIXED** (#194) | Komentar `checkpointOld` disamakan (gagal checkpoint → abort), dan WAL/SHM hanya dihapus bila checkpoint sukses. |
| SEC7-39 | **SEDANG DIKERJAKAN** | #192 mengarsipkan `storage.key` (0600 di payload terenkripsi; tidak pernah ada salinan plaintext di disk selain file aslinya). #194 memasangnya hanya bila absen. Kasus yang tersisa: (a) restore ke data dir yang sudah punya `storage.key` **lain** → DB terenkripsi hasil restore tidak cocok dengan `storage.key` yang tersisa (marker → `ErrEncrypted`; arsip tetap sumber pemulihan); (b) `storage.key` dipasang **sebelum** `ctxdb.Restore`, sehingga bila swap DB gagal (mis. `ErrDBBusy`), `storage.key` arsip tertinggal di data dir plaintext (fail-closed: `ErrAtRestPlaintext`/unwrap gagal, tetapi membingungkan). Round-trip restore ke data dir terenkripsi baru (KEK sama) belum diuji end-to-end. Akan dinilai bersama #198/PR 4. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-41 | #194/#197 | MINOR (gate) | confirmed | `make loc`: **`backup 1404 > 1400`** di `78cd07b` dan `ee9d5b7` (preflight + cleanup snapshot + `storage.key` menambah ±38 baris). | Pangkas ≥ 4 baris, atau naikkan budget beserta amandemen ADR-124 dan Total core ENGINEERING (jumlah `loc.budget` harus tetap sama dengan tabel). |
| SEC7-42 | #194 | MINOR (proses) | confirmed | #194 dibangun di atas `bf51e3d` (versi lama #192), bukan `3749067`. Diff PR #194 terhadap base `f2-crypto-backup-c` **menghapus `TestReadMetaRejectsBadRecipientBytes`**, sehingga merge berurutan akan membatalkan perbaikan SEC7-32. | Rebase #194 (dan #197) ke `3749067`. Pastikan `git range-diff` hanya menampilkan perubahan #194. |
| SEC7-43 | #197 | **HIGH** | confirmed | **CLI backup/restore tidak terjangkau.** Dispatch `sessions` → `runSessions` dihapus dari #192/#194 (SEC7-31), tetapi tidak dipindahkan ke #197: `cmd/damnbot/main.go` di `ee9d5b7` tidak memanggil `runSessions`. Binary: `damnbot sessions restore …` / `damnbot sessions backup ls` → `Usage of damnbot: -version`, exit 2. `make deadcode` GAGAL (8 simbol). Test `sessions_test` memanggil `runSessions` langsung, sehingga tidak mendeteksinya. Seluruh fitur GAP-SEC-01 sisi pengguna (create/ls/verify/restore) tidak tersedia. | Tambahkan **satu** blok dispatch `sessions` di `main.go` dalam commit #197. Tambahkan test lewat `run([]string{"sessions", …})` (jalur `main`), bukan `runSessions` langsung. |

**Ringkasan:** #192 **APPROVE**; #194 **REQUEST CHANGES** (SEC7-41 gate LOC, SEC7-42 rebase; perbaikan keamanannya SEC7-24/29b/33 sudah FIXED); #197 **REQUEST CHANGES** (SEC7-43 HIGH: dispatch hilang; SEC7-41). SEC7-39 masih dikerjakan bersama #198/PR 4. Setelah diperbaiki, jalankan ulang: `make loc` + `make deadcode` per commit, `damnbot sessions backup create/ls/verify` + `damnbot sessions restore` lewat binary, dan range-diff #194 terhadap `3749067`.

## Round 6 — 2026-10-21 (seluruh stack kripto: backup + at-rest PR 3/3b/4)

Stack linear (basis `main` `0128e6e`): #185 `e18a3d1` → #189 `5f50671` → #187 `4a15631` → #191 `cf416fd` → #192 `256b98a` → #194 `af6ddff` → #197 `1479142` → #198 `b66556d` → #199 `ca20e01` → #201 `e8d825a`. Parent setiap commit = head PR di bawahnya (SEC7-42 FIXED). Range-diff terhadap Round 5: #192 menghapus test recipient lama yang tidak efektif dan memakai `TestReadMetaRejectsBadRecipientBytes`; #194 hanya mengubah budget `backup 1400 → 1450` (Total 39.170); #197 menambah dispatch `sessions` + `TestSessionsDispatchThroughRun`.

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185 / #189 / #187 / #191 | tidak berubah | **APPROVE** |
| #192 manifest/create/verify | `256b98a` | **APPROVE** |
| #194 restore bertahap | `af6ddff` | **APPROVE dengan MINOR** (SEC7-52: kenaikan budget `backup 1450` tanpa amandemen ADR) |
| #197 CLI | `1479142` | **APPROVE** (SEC7-43 FIXED) |
| #198 at-rest core (PR 3) | `b66556d` | **APPROVE bersyarat**: SEC7-34/35/36/38/40 FIXED. Jangan dirilis sendiri: tanpa #201, `storage.encrypt=true` membiarkan memory/summaries plaintext (SEC7-37). Merge bersama #199/#201 setelah keduanya diperbaiki. MINOR: SEC7-49, SEC7-52. |
| #199 restore → dir terenkripsi (PR 3b) | `ca20e01` | **REQUEST CHANGES**: SEC7-44 (HIGH), SEC7-45 (HIGH), SEC7-50 (archtest gagal), `make loc` GAGAL |
| #201 memory/summaries + blind index (PR 4) | `e8d825a` | **REQUEST CHANGES**: SEC7-46/47/51 (MEDIUM), SEC7-48/49/53 (MINOR) |

Gate:
- `go vet -tags goolm ./...` + `GOOS=windows|darwin|freebsd go vet ./cmd/damnbot ./internal/backup ./internal/ctxdb/... ./internal/secret` → **bersih di setiap commit** (`256b98a` … `e8d825a`).
- `make deadcode` → 0 fungsi tak terjangkau di setiap commit.
- `make loc`: lulus di `256b98a` (backup 1122/1150), `af6ddff`/`1479142` (backup 1404/1450, ctxdb 3577/3600), `b66556d` (secret 1127/1150, ctxdb 3724/3750, backup 1405/1450), `e8d825a` (secret **1150/1150**, ctxdb 4123/4150, backup 1462/1500). **`ca20e01`: `LOC GAGAL ctxdb 4067 > 3750`, `backup 1462 > 1450`** (kenaikan baru ada di #201).
- Jumlah `loc.budget` = Total core ENGINEERING (tabel dan prosa) di setiap commit: 38.770 / 39.170 / 39.170 / 39.620 / 39.620 / 40.120 ✔. Dukungan ADR: lihat SEC7-52.
- `go test -race -tags goolm -count=1` di `e8d825a`: `secret`, `backup`, `cmd/damnbot`, `config`, `ctxdb/compactstore`, `ctxdb/memstore`, `ctxdb/skillstore` → ok; `./internal/ctxdb -run 'AtRest|Scenario|Restore|Snapshot|Maint|Migrat|FTS|Search'` → ok (217 s). **`internal/archtest` GAGAL**: `TestNolintDensity` (E-52) 49 nolint / 43.208 baris > 1 per 1.000; pertama kali gagal di `ca20e01` (48/43.032) → SEC7-50. Test `secret`/`backup`/`cmd` lulus di `b66556d` dan `ca20e01`.

Probe yang dijalankan lalu dihapus: `TestZZR6Conversion` + `TestZZR6b` (`internal/backup`), `TestZZR6`/`TestZZBlindFTS`/`TestZZMemFTS`/`TestZZMig6` (`internal/ctxdb`), `TestZZCompactEncrypted` (`compactstore`), DB pra-0009 yang dibuat oleh `1479142`, dan binary `damnbot` dari `e8d825a`. Semua memakai codec `secret` nyata.

### Status temuan SEC7-24..43

| ID | Verdict | Bukti (Round 6) |
|---|---|---|
| SEC7-24 | **FIXED** | Tamper/truncate/trailing → error bertipe; tree data dir hanya `damnbot.db`, tanpa `run/restore-*`. Restore terenkripsi yang gagal (probe A/B) juga tidak meninggalkan staging. |
| SEC7-25 | **FIXED** | Dry-run `err=nil`, tanpa staging. |
| SEC7-26/27 | **FIXED** | `sessions restore <file>`; `ls` disanitasi. |
| SEC7-28 | **FIXED** | Checkpoint lama → abort bila gagal; WAL/SHM hanya dihapus setelah sukses. |
| SEC7-29 | **FIXED** (a, b) | `config.toml.restore` sisa dibersihkan; `.snapshot-*` yatim dihapus saat `create` (probe). |
| SEC7-30 | digantikan | Lihat SEC7-52. |
| SEC7-31 | **FIXED** | Setiap commit build/vet (termasuk cross-OS). |
| SEC7-32 | **FIXED** | `ReadMeta` recipient 32×`z` → `corrupt archive: recipient id`; test lama yang tidak efektif dihapus. |
| SEC7-33 | **FIXED** | `config_dir` berupa file → error sebelum DB dipasang. |
| SEC7-34 | **FIXED** (#198) | 0009: `content_len INTEGER` nullable, tanpa `UPDATE`. Probe: DB `1479142` berisi memory + summary + event → `Open` di `e8d825a` sukses (`user_version` 11), History dan Search jalan. Residual: SEC7-49. |
| SEC7-35 | **FIXED** (#198) untuk events/summaries/memory | `sealRow` mengenkripsi `identity‖0x00‖plaintext` (identity events = `sid|seq`, summaries = `sid|from:to`, memory = `scope|enc_id`), dan `openRow` mewajibkan prefix itu. Probe: `content_enc` sesi A disalin ke B → `History(B)` = `row identity mismatch: encrypted_db`. Blob: scope `blob:<sha>` + verifikasi `sha256` setelah dekripsi. Jalur yang tidak memakai `OpenRow`: SEC7-46. |
| SEC7-36 | **FIXED** (#198) | Hash chain DB terenkripsi = HMAC-SHA256 dengan kunci `HKDF(DEK,"dmbot/at-rest/v1/event-hash")`. Probe: tebakan `"yes"` tidak lagi terkonfirmasi lewat `EventHash` publik. |
| SEC7-37 | **PARSIAL** | (a) Memory/skills (`memory`), summaries terenkripsi di runtime (#201). `reasoning_carriers`/`gateway_inbound` hanya disegel oleh konversi restore; writer runtime-nya tidak ada di stack ini → SEC7-51. (b) `compactstore` kini mendekripsi, tetapi salah (SEC7-46). Flag `tainted` (`summariesQ`) dan `called` (`stateQ`) masih dibaca dengan `json_extract` atas `events.payload` plaintext, padahal isinya placeholder `{}` pada DB terenkripsi, sehingga **flag tetap hilang**. |
| SEC7-38 | **FIXED** (#198) | `writeStorageKey`: tmp acak, `O_EXCL`, `link` (tidak menimpa), pihak yang kalah memuat kunci pemenang, fsync dir. `readStorageKeyFile`: `Lstat` (tolak symlink/non-regular) + open + `fstat` `SameFile`, batas 8 KiB. Probe: 8 goroutine `OpenCodec(create=true)` → **id DEK sama**, 0 error. |
| SEC7-39 | **PARSIAL** (#199) | Arsip dengan `storage.key` ke dir tanpa kunci → codec dari arsip (`OpenCodecFile`), `storage.key` dipasang bila absen. Dir dengan `storage.key` **berbeda** → `ErrKeySource` sebelum swap, DB tidak dipasang ✔ (probe C). KEK tidak ada di mesin tujuan → `key not found` sebelum swap ✔ (probe D). Namun arsip terenkripsi **yang berisi blob** gagal (SEC7-44), dan konversi plaintext → terenkripsi gagal (SEC7-45). |
| SEC7-40 | **FIXED** (#198) | `PutBlob`/`ReadBlob` memakai scope `blob:<sha>` + cek hash. Restore memakai scope lain → SEC7-44. |
| SEC7-41 | **FIXED** di #194/#197 | `backup 1404/1450` lulus. Dukungan ADR → SEC7-52. |
| SEC7-42 | **FIXED** | Stack linear; #194 berbasis `256b98a`. |
| SEC7-43 | **FIXED** (#197) | `main.go` memanggil `runSessions`; `TestSessionsDispatchThroughRun`. Binary `e8d825a`: `damnbot sessions backup ls --data-dir …` → exit 0; `make deadcode` 0. |

### Probe at-rest / restore (#198–#201)

| Skenario | Hasil |
|---|---|
| B-source: DB terenkripsi (codec nyata), 2 event + 1 blob | History/ReadBlob ✔; file DB tanpa kanari plaintext ✔; **Search "PLAINCANARY" → 0 hit** (SEC7-47) |
| Arsip dari DB terenkripsi | tidak ada kanari plaintext di arsip ✔ |
| **B**: restore arsip terenkripsi (dengan blob) ke dir baru, key store sama | **gagal**: `restore blob 95c1…: secret: invalid key file: decrypt` → SEC7-44 |
| **A**: restore arsip plaintext (2 event + 1 blob) ke dir baru dengan `storage.encrypt=true` | **gagal**: `seal events scan: sql: expected 7 destination arguments in Scan, not 3` → SEC7-45 |
| C: arsip terenkripsi → dir dengan `storage.key` lain | `ErrKeySource`, DB tidak dipasang ✔ |
| D: arsip terenkripsi → mesin lain (KEK tidak ada) | `open storage key: secret: key not found`, DB tidak dipasang ✔ |
| Swap `content_enc` antar-sesi | `row identity mismatch` ✔ |
| `compactstore.Load` di DB terenkripsi (event `hello`, payload `{"tainted":true}`) | **content = `"S|1\x00hello"`, payload = `"S|1\x00{\"tainted\":true}"`** → SEC7-46 |
| `Search` (events) di DB terenkripsi: `hello`, `world`, `hello world`, `deploy`, `HELLO`, … | **0 hit untuk semua query** (plaintext: 1 hit) → SEC7-47 |
| `memory_fts` (identity `user|abc`, isi `alpha beta gamma`) | `alpha` 0, `beta` 1, `gamma` 1 → SEC7-48 |
| DB pra-0009 dengan 1 baris memory | migrasi sukses; `content_len IS NULL` = 1 baris → SEC7-49 |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-44 | #199 (`ctxdb/restore.go installBlobs`) | **HIGH** | confirmed | **Backup DB terenkripsi yang berisi blob tidak dapat direstore.** `installBlobs` mendekripsi dengan scope `"blob"`, padahal `PutBlob` menyegel dengan `"blob:"+sha` (#198, SEC7-40). Setiap arsip terenkripsi yang memuat ≥ 1 blob gagal dengan `secret: invalid key file: decrypt` (pesannya juga menyesatkan, karena mengarah ke key file). Disaster recovery untuk pengguna `storage.encrypt` tidak berfungsi. `TestRestoreEncryptedBackupToFreshDir` tidak memuat blob. | Pakai `"blob:"+sha` (satu helper bersama untuk scope blob). Tambahkan blob ke test round-trip terenkripsi. Bungkus error AEAD sebagai `ErrCorrupt`/`ErrEncrypted`, bukan `ErrKeyFile`. |
| SEC7-45 | #199 (`ctxdb/encryptfile.go`) | **HIGH** | confirmed (a) / static (b–e) | **Konversi restore plaintext → terenkripsi rusak.** (a) `sealEvents` men-`Scan` 3 tujuan untuk 7 kolom → gagal untuk setiap arsip yang punya event (probe A). `sid`/`seq`/`kind`/`blob` tidak pernah terisi. (b) Format segel konversi **berbeda dari runtime**: events `Seal("events.content:"+id, plaintext)` vs runtime `sealRow("events.content", identity‖0x00‖plaintext)`; summaries/memory disegel tanpa identity, padahal runtime `OpenRow` mewajibkan `sid|from:to` / `scope|enc_id` (`enc_id` tidak diisi konversi). Setelah (a) diperbaiki pun, DB hasil konversi tidak dapat dibaca. (c) Konversi berjalan **sebelum migrasi**, sehingga arsip dari binary lama (tanpa kolom `*_enc`) gagal. (d) FTS dikosongkan (`delete-all`) tanpa membangun ulang blind index. (e) Trigger dibuat ulang dengan mengeksekusi teks `sql` dari `sqlite_master` **arsip** (input tak tepercaya, ADR-124 butir 6). Tidak ada test konversi dengan data. | Satu fungsi segel bersama runtime/konversi (`sealRow` + identity yang sama, `enc_id` diisi); migrasi DB staging ke `schemaMax` sebelum konversi; bangun ulang FTS lewat fungsi yang sama dengan trigger; buat ulang trigger dari migrasi tertanam, bukan dari arsip. Test: arsip plaintext berisi event/summary/memory/blob → restore `encrypt=true` → History/ReadBlob/Search/compactstore benar + kanari tidak ada di file. |
| SEC7-46 | #201 (`compactstore.events`) | **MEDIUM** | confirmed | `compactstore` membuka event dengan `tx.OpenText` (tanpa identity), padahal event disegel dengan `sealRow`. Input summarizer menjadi `"S|1\x00hello"`, dan payload menjadi `"S|1\x00{…}"` (JSON tidak valid). Identity dan byte NUL ikut terkirim ke provider, dan pengikatan baris (SEC7-35) tidak diverifikasi di jalur ini. | Pakai `tx.OpenRow("events.…", sid+"|"+seq, …)`. Tambahkan test compactstore pada DB terenkripsi yang membandingkan konten persis. |
| SEC7-47 | #201 (`read.go Search`) | **MEDIUM** | confirmed | **Blind index events tidak pernah cocok.** `Search` membangun ekspresi FTS5 ber-kutip (`"hello"`), lalu meneruskannya ke `dmb_fts_norm(?, 'events.content', '')`, yang meng-HMAC token **beserta kutip/operator**. Probe: 8 query → 0 hit pada DB terenkripsi (plaintext: 1 hit). Selain itu, sisi indeks memakai `ftsNorm` yang tidak memecah tanda baca, sehingga `"rollback."`/`"read.go"` ≠ token query. Memory search lewat `memstore.ftsQuery` (yang membuang tanda baca di query) hanya cocok untuk kata yang tidak menempel tanda baca. | HMAC per token **sebelum** menyusun ekspresi FTS5 (kutip/OR ditambahkan setelahnya); samakan tokenisasi indeks dengan query (pisah non-alnum seperti `unicode61`). Test Search/recall end-to-end pada DB terenkripsi. |
| SEC7-48 | #201 (0010 `memory_ai`) | MINOR | confirmed | Trigger memakai identity `scope|name`, sedangkan `memstore`/`skillstore` menyegel dengan `scope|enc_id`. Prefix tidak terbuang, kata pertama menempel ke `…\x00`, dan kata itu tidak dapat dicari (probe: `alpha` 0 hit). | Pakai `NEW.scope || '|' || NEW.enc_id`. |
| SEC7-49 | #198/#201 (`memstore` cap) | MINOR | confirmed | 0009 menambah `content_len` nullable tanpa backfill, sedangkan cap scope memakai `SUM(m.content_len)`. Baris memory lama (probe: 1 baris NULL) dihitung 0, sehingga cap memory terlampaui setelah upgrade. | `COALESCE(m.content_len, length(m.content))` (plaintext lama), atau backfill di mode maint. |
| SEC7-50 | #199/#201 | MINOR (gate) | confirmed | `TestNolintDensity` (E-52) gagal mulai `ca20e01` (48 nolint / 43.032 baris) dan di `e8d825a` (49/43.208); batas 1 per 1.000. Sumbernya `//nolint:gocyclo/gocognit/nestif/dupl/gosec` baru di `restore.go`/`encryptfile.go`/`memstore`. | Pecah fungsi alih-alih menambah nolint. |
| SEC7-51 | #198/#201 ↔ #190/#193/#200 | **MEDIUM** (coupling) | static | **Carrier tetap plaintext setelah kedua stack mendarat, dan restore terenkripsi merusak sesi yang punya carrier.** `carrierstore.Save` (#190) menulis `reasoning_carriers.payload` byte mentah (teks thinking) dan tidak mengenal `payload_enc`/codec, sehingga dengan `storage.encrypt=true` isi reasoning tersimpan plaintext. Ini bertentangan dengan komentar skema 0007 ("terenkripsi bila storage.encrypt") dan amandemen PR 4 ("carrier/gateway encryption"). Sebaliknya, konversi restore mengubah `payload=X''` + `payload_enc`, tetapi `carrierstore.Load` hanya membaca `payload`. Akibatnya carrier kosong → `projectCarriers` mengembalikan error "carrier invalid" → setiap request sesi itu gagal. `gateway_inbound` belum punya writer runtime, tetapi butuh hal yang sama. | **Coupling wajib:** PR yang di-merge **kedua** (carriers atau kripto) membawa integrasinya: `carrierstore.Save/Load` memakai `Tx.SealRow/OpenRow("reasoning_carriers.payload", identity = session|event_id|ord)` dengan placeholder `X''`, `payload_sha` dihitung atas plaintext, dan carrier serta gateway masuk test kanari lewat writer nyata. Tidak boleh ada rilis yang memuat kedua stack tanpa integrasi ini. Bila carriers mendarat lebih dulu, #201 menambah integrasinya; bila kripto lebih dulu, #190 di-rebase dengan codec. |
| SEC7-52 | #194/#198/#201 (budget) | MINOR | static | **Budget tidak didukung ADR di commit antara.** #194: `backup 1400 → 1450` (Total 39.170) tanpa amandemen; amandemen PR 2a–2e tetap menyebut 1.400/39.120. #198: `secret 1150`, `ctxdb 3750`, Total 39.620, sedangkan amandemen PR 3 menyebut 1.100/3.700/39.470. #201: amandemen "PR 4" (berbahasa Inggris, tidak konsisten dengan dokumen) menyebut `ctxdb 3750 → 4150`, `compactstore 250 → 300`, `backup 1450 → 1500`, Total 39.620 → **40.120**. Angka ini cocok dengan `loc.budget`, tetapi klaim "carrier/gateway encryption"-nya tidak terpenuhi (SEC7-51). Komentar `loc.budget` basi ("terukur 1315", "3.700 … terukur 3653"). `secret` di `e8d825a` = 1150/1150 (tanpa headroom). #199 gagal `make loc` karena kenaikannya baru ada di #201. | Satu amandemen per PR (bahasa Indonesia) dengan angka terukur dan budget yang dipakai PR itu. Pindahkan kenaikan `ctxdb`/`backup` ke #199. Perbarui komentar `loc.budget`. |
| SEC7-53 | #201 (`atrest_test.go`) | MINOR | static | `TestAtRestNoPlaintextCanary` menyegel kanari secara manual dengan `SealText` (bukan writer nyata `compactstore`/`memstore`/`skillstore`/`carrierstore`/gateway) dan memakai format tanpa identity yang tidak dipakai runtime. Test ini tidak dapat mendeteksi SEC7-46/51. | Kanari lewat writer nyata per kolom, lalu baca balik lewat reader nyata. |

**Ringkasan:** #185/#189/#187/#191/#192/#197 **APPROVE**; #194 **APPROVE dengan MINOR** (SEC7-52); #198 **APPROVE bersyarat** (perbaikan SEC7-34/35/36/38/40 terverifikasi; jangan dirilis tanpa #199/#201 yang sudah diperbaiki); #199 **REQUEST CHANGES** (SEC7-44/45 HIGH: restore DB terenkripsi dengan blob dan konversi plaintext → terenkripsi keduanya gagal; `make loc` + archtest gagal); #201 **REQUEST CHANGES** (SEC7-46 compactstore, SEC7-47 blind index tidak pernah cocok, SEC7-51 coupling carrier; MINOR SEC7-48/49/53). Setelah diperbaiki, jalankan ulang: probe A/B (konversi + blob terenkripsi), Search/recall pada DB terenkripsi, compactstore pada DB terenkripsi, dan kanari lewat writer nyata (termasuk carrier bila #190 sudah mendarat).

## Round 7 — 2026-10-21 (#199/#201 diperbarui)

Head: #199 `16410e7` (11 commit di atas `b66556d`: konversi lewat ladder migrasi biner + codec runtime, trigger dari skema baru, amandemen budget PR 3b), #201 `6714e0c` (blind FTS dibangun ulang saat konversi, identity trigger memory, test penulis nyata, quoted search). Head lain tidak berubah: #185 `e18a3d1`, #189 `5f50671`, #187 `4a15631`, #191 `cf416fd`, #192 `256b98a`, #194 `af6ddff`, #197 `1479142`, #198 `b66556d`.

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185/#189/#187/#191/#192/#197 | tidak berubah | **APPROVE** |
| #194 | `af6ddff` | **APPROVE dengan MINOR** (SEC7-52, tetap) |
| #198 at-rest core | `b66556d` | **APPROVE bersyarat**: syarat merge di bagian "Syarat merge vs carrier (SEC7-51)" |
| #199 restore → dir terenkripsi | `16410e7` | **REQUEST CHANGES (gate)**: SEC7-44/45 **FIXED** (probe), tetapi `internal/archtest` gagal (SEC7-50) dan Total core ENGINEERING ≠ jumlah `loc.budget` (SEC7-52) |
| #201 memory/summaries + blind index | `6714e0c` | **REQUEST CHANGES**: SEC7-46 **belum diperbaiki**, SEC7-54 baru (MEDIUM), archtest gagal (SEC7-50 + import graph), SEC7-52, SEC7-51 |

Gate:
- `go vet -tags goolm ./...` + `GOOS=windows|darwin|freebsd go vet ./internal/ctxdb/... ./internal/backup ./cmd/damnbot` → bersih di `16410e7` dan `6714e0c`.
- `make deadcode` → 0.
- `go test -race -tags goolm -count=1 ./internal/secret ./internal/backup ./cmd/damnbot ./internal/ctxdb/{compactstore,memstore,skillstore}` → ok; `./internal/ctxdb -race -run 'AtRest|Restore|Snapshot|Migrat|FTS|Search|Scenario'` → ok (237 s).
- **`internal/archtest` GAGAL di kedua head**: `TestNolintDensity` 45 nolint / 43.119 baris (`16410e7`) dan 46 / 43.347 (`6714e0c`) > 1 per 1.000. Di `6714e0c` juga **`TestImportGraph`**: edge ilegal `internal/ctxdb → internal/compaction`, `→ internal/ctxdb/compactstore`, `→ internal/ctxdb/memstore` (test=true, dari `atrest_writers_test.go` paket `ctxdb_test`).
- `make loc`: `16410e7` `ctxdb 4132/4400`, `backup 1462/1550`, `compactstore 248/250`, `secret 1150/1150`; `6714e0c` `ctxdb 4253/4400`, `compactstore 288/300`, `backup 1462/1550`, `secret 1150/1150`. **Jumlah `loc.budget` ≠ Total core ENGINEERING**: `16410e7` 40.370 vs **39.620**; `6714e0c` 40.420 vs **40.120** (tabel ENGINEERING masih `ctxdb 3.750/4.150`, `backup 1.450/1.500`).

Probe yang dijalankan lalu dihapus: `TestZZR7` (`internal/backup`: A plaintext → terenkripsi, B terenkripsi + blob → dir baru, C arsip DB skema lama dari binary `1479142` → terenkripsi), `TestZZR7Summary2`, `TestZZQuoted`, dan `TestZZMem7` (`internal/ctxdb`, codec `secret` nyata).

| Skenario | Hasil |
|---|---|
| **A** restore arsip plaintext (2 event + blob) dengan `encrypt=true` | `err=nil`; History = `["PLAINCANARY hello world", "PLAINCANARY tool output"]`; ReadBlob ✔; Search `hello`/`world`/`PLAINCANARY`/`tool output` → 1/1/2/1; tidak ada kanari plaintext di file mana pun; tanpa sisa `restore-*`/`.migrate-*` ✔ |
| **B** arsip terenkripsi + blob → dir baru | `err=nil`; History/ReadBlob/Search sama dengan sumber ✔ (Round 6: gagal decrypt blob) |
| **C** arsip DB skema lama (pra-0009, event + memory + summary) → terenkripsi | `err=nil` (ladder migrasi biner); Search `OLDCANARY` → 2; tanpa plaintext di disk ✔. History mengembalikan `summary_ref` dengan isi **`"{}"`** → SEC7-54 |
| `compactstore.Load` di A, B, dan sumber terenkripsi | event = **`"S|1\x00PLAINCANARY hello world"`**, `"S|2\x00…"` → SEC7-46 tetap |
| Summary disegel `SealRow("summaries.content", "S|1:5", …, "{}")` (jalur `compactstore.summary`) lalu `History` | plaintext: isi summary ✔; **terenkripsi: `"{}"`** → SEC7-54 |
| Search `"hello world"` (frasa ber-kutip) | plaintext 1 hit, **terenkripsi 0** (test #201 memakai codec palsu `FTS = ToUpper`, sehingga tidak menangkap ini) → MINOR (SEC7-47 residual) |
| `memory_fts` identity `user|abc` (`alpha beta gamma`) | `alpha`/`beta`/`gamma` → 1/1/1 ✔ (SEC7-48) |

### Status temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-44 | **FIXED** (#199) | Probe B. |
| SEC7-45 | **FIXED** (#199/#201) | Probe A dan C: staging dimigrasikan dengan ladder biner (`migrateStaged`), trigger diambil dari DB yang dibuat biner (`trustedTriggers`), sehingga tidak ada SQL dari arsip yang dieksekusi. Baris disegel dengan format runtime, FTS di-rebuild dengan token HMAC, `secure_delete` + `VACUUM`. |
| SEC7-46 | **BELUM DIPERBAIKI** | `compactstore.events` masih `tx.OpenText("events.content"/"events.payload", …)` (`compactstore.go:198/202`). Probe: konten yang menuju summarizer `"S|1\x00…"`, payload `"S|1\x00{…}"`. `TestAtRestCompactorEncrypted` memakai codec palsu dan hanya memeriksa summary serta tidak adanya plaintext, sehingga isi event tidak diperiksa. Compactor belum disambungkan di `cmd`, jadi saat ini laten. |
| SEC7-47 | **FIXED dengan residual** | Kata tunggal/multi-kata/huruf besar cocok (A/B). Residual MINOR: frasa ber-kutip 0 hit dengan codec nyata. |
| SEC7-48 | **FIXED** | Trigger `memory_ai` memakai `scope||'|'||enc_id`. |
| SEC7-49 | **TERBUKA** (MINOR) | Cap memory masih `SUM(m.content_len)` dan baris lama `NULL` (tidak diklaim). |
| SEC7-50 | **BELUM DIPERBAIKI** | archtest `TestNolintDensity` gagal di kedua head; ditambah `TestImportGraph` di #201. |
| SEC7-51 | **TERBUKA** (disengaja) | Lihat syarat merge di bawah. Amandemen PR 4 kini mengakui bahwa penulis runtime carrier/gateway belum ada ✔. |
| SEC7-52 | **PARSIAL** | Amandemen ADR-124 PR 3b (`ctxdb 3750 → 4400`, `backup 1450 → 1550`) dan PR 4 (`compactstore 250 → 300`, Total **40.420**) kini berbahasa Indonesia dan cocok dengan `loc.budget` di `6714e0c`. Namun tabel dan prosa ENGINEERING §2.4 tidak diperbarui (39.620 / 40.120, `ctxdb 3.750/4.150`, `backup 1.450/1.500`). PR 4 juga mengulang kenaikan `ctxdb 3750 → 4400` yang sudah ada di PR 3b. `secret` 1150/1150 tanpa headroom. |
| SEC7-53 | **FIXED dengan catatan** | `TestAtRestWritersSearchAndCanary` memakai penulis nyata (Append, memstore.Put, compactstore.Commit), tetapi dengan codec palsu (`FTS = ToUpper`, `Hash = "h:"+s`), sehingga frasa ber-kutip dan format HMAC nyata tidak teruji. Tambahkan varian dengan `secret.NewCodec`. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-54 | #201 (`read.go historySummaries`) | **MEDIUM** | confirmed | `History` membaca `sm.content` tanpa `content_enc`. Di DB terenkripsi, setiap `summary_ref` berisi placeholder **`"{}"`** (probe: plaintext → isi summary, terenkripsi → `"{}"`; probe C juga). Seluruh ringkasan compaction hilang dari History untuk pengguna `storage.encrypt`. `ctxStore.History` di `cmd` belum memakai `summary_ref`, jadi ini laten, tetapi `History` adalah API riwayat resmi (ADR-134 butir 5). | Ambil `content_enc` + `OpenRow("summaries.content", sid+"|"+from+":"+to, …)` di `historySummaries`. Test History + summary dengan codec nyata. |
| SEC7-50b | #201 | MINOR (gate) | confirmed | `TestImportGraph`: `atrest_writers_test.go` (paket `ctxdb_test`) mengimpor `compaction`, `ctxdb/compactstore`, `ctxdb/memstore`, yang tidak diizinkan matriks E-01 untuk `internal/ctxdb`. | Pindahkan test ke paket konsumen (`compactstore`/`memstore`) atau ke paket integrasi yang diizinkan. |

### Syarat merge vs carrier (SEC7-51)

Fakta: carrier (#190/#193/#200) akan mendarat di `main` lebih dulu. `carrierstore.Save` menulis `reasoning_carriers.payload` (byte mentah, termasuk teks thinking) tanpa codec, dan `carrierstore.Load` hanya membaca `payload`. Bila #198/#199/#201 mendarat tanpa integrasi:
1. DB dengan `storage.encrypt=true` menyimpan **carrier plaintext** di tabel append-only (tidak bisa dihapus belakangan) dan di setiap backup.
2. Restore yang mengonversi arsip plaintext (`sealCarriers`: `payload=X''` + `payload_enc`) menghasilkan carrier kosong saat dimuat, sehingga `projectCarriers` mengembalikan "carrier invalid" dan **setiap request di sesi itu gagal**.

**Syarat (wajib, salah satu):**
- **(a) Fail-closed:** selama `carrierstore` belum menyegel/membuka lewat codec, `storage.encrypt=true` harus ditolak saat open. Bentuknya: `secret.OpenCodec`/`run` gagal dengan error bertipe (mis. `ErrAtRestUnsupported: reasoning_carriers writer is not encrypted`), dan restore dengan `encrypt=true` ditolak sebelum swap. Dengan begitu tidak ada DB terenkripsi yang dapat menerima baris carrier plaintext. **Atau**
- **(b) Integrasi di jendela merge yang sama:** sebelum #198 di-merge ke `main` yang sudah memuat carrier, PR penyerta (rebase #201 atau PR baru yang di-merge sebelum rilis berikutnya dan sebelum `storage.encrypt` dapat diaktifkan) mengubah `carrierstore.Save/Load` ke `Tx.SealRow/OpenRow("reasoning_carriers.payload", identity, …)`, dengan placeholder `X''`, `payload_sha` atas plaintext, dan identity yang sama dengan `sealCarriers` konversi. Ditambah test kanari carrier lewat `carrierstore.Save` nyata + round-trip restore-konversi → `Load` → `projectCarriers`.

"Follow-up PR sebelum rilis" saja **tidak cukup** kecuali (a) berlaku di antaranya. Baris plaintext yang tertulis di antara merge tidak dapat dihapus karena tabelnya append-only.

**Ringkasan:** #199: SEC7-44/45 (HIGH) **FIXED** dan terverifikasi (konversi plaintext, arsip terenkripsi dengan blob, dan arsip skema lama semuanya terbaca tanpa plaintext di disk). Yang tersisa hanya gate: archtest nolint dan Total core ENGINEERING. #201 **REQUEST CHANGES**: SEC7-46 masih terbuka (diklaim fixed), SEC7-54 baru (History summary `"{}"`), archtest nolint + import graph, dan sinkronisasi ENGINEERING. #198/#199/#201 **belum boleh di-merge** ke `main` yang memuat carrier, kecuali syarat (a) atau (b) di atas dipenuhi.

## Round 8 — 2026-10-21 (#199/#201 diperbarui, #217 baru)

Head: #199 `5c50d6a` (di atas `16410e7`: nolint dihapus via fsync+rename di konversi, Total core diselaraskan), #201 `11e9230` (rebase `97a5279 … 084ddc5` + `d5fccbb` compactstore/History/search + test pindah ke `internal/integration` + `11e9230` test cap), **#217 `76790a7`** (`f2-crypto-carrier`, di atas #201: enkripsi `carrierstore`, opsi (b) SEC7-51). Head lain tidak berubah (#185 … #198 seperti Round 7).

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185/#189/#187/#191/#192/#197 | tidak berubah | **APPROVE** |
| #194 | `af6ddff` | **APPROVE dengan MINOR** (SEC7-52 historis) |
| #198 | `b66556d` | **APPROVE**, dengan syarat merge bersama #199/#201/#217 (lihat bagian syarat merge) |
| #199 | `5c50d6a` | **APPROVE** (SEC7-44/45/50 FIXED; gate hijau) |
| #201 | `11e9230` | **REQUEST CHANGES (kecil)**: SEC7-46/47/48/49/50/52/53/54 FIXED; **SEC7-37b masih terbuka** (MEDIUM: flag `tainted`/`called` hilang di DB terenkripsi) |
| #217 | `76790a7` | **APPROVE dengan MINOR** (SEC7-51 FIXED lewat opsi (b); SEC7-55 MINOR; koordinasi duplikasi `carrierstore` dengan #190) |

Gate:
- `go vet -tags goolm ./...` + `GOOS=windows|darwin|freebsd go vet ./internal/ctxdb/... ./internal/backup ./cmd/damnbot` → bersih di `5c50d6a`, `11e9230`, `76790a7`.
- `make deadcode` → 0. **`internal/archtest` → ok di ketiga head** (Round 7: gagal).
- `go test -race -tags goolm -count=1 ./internal/{secret,backup,integration} ./cmd/damnbot ./internal/ctxdb/{compactstore,memstore,skillstore,carrierstore}` → ok (paket yang ada di head masing-masing); `./internal/ctxdb -race -run 'AtRest|Restore|Snapshot|Migrat|FTS|Search|Scenario'` → ok (226 s).
- `make loc` + Total core ENGINEERING (baris infra `integration 200` dikecualikan dari Σ core oleh `infraDirs`): `5c50d6a` `ctxdb 4140/4400`, `backup 1462/1550`, `compactstore 248/250`, Σ **40.370** = ENGINEERING ✔; `11e9230` `ctxdb 4279/4400`, `compactstore 289/300`, Σ **40.420** ✔; `76790a7` `+ ctxdb/carrierstore 291/400`, Σ **40.820** ✔. `secret` 1150/1150 (tanpa headroom).
- **`internal/integration`** sah sebagai pengecualian archtest, bukan gate yang dilonggarkan: paket test-only (hanya `atrest_test.go`), terdaftar di `testOnlyPackages()` (kode produksi termasuk `cmd/damnbot` tidak boleh mengimpornya), punya baris matriks E-01 eksplisit (`ctxdb`, `memstore`, `compactstore`, `carrierstore`, `compaction`, `backup`, `secret`, `redact`, `loop/event`), `TESTONLY` + `infraDirs` (di luar Σ core), dan entri CODEOWNERS. Matriks `internal/ctxdb` sendiri tidak diubah, karena test `ctxdb_test` yang ilegal dipindahkan dan bukan diizinkan.

Probe (codec `secret` nyata; dijalankan lalu dihapus): `TestZZR8` (`internal/backup`: DB terenkripsi berisi event + CJK + summary lewat `SealRow` + carrier lewat `carrierstore.Save` nyata → backup → restore; dan arsip plaintext dengan carrier → restore `encrypt=true`), `TestZZR8Carrier` (`carrierstore`: compactstore, swap `payload_enc`, `payload_sha`), `TestZZR8Taint` (`compactstore.Commit` summary tainted).

| Skenario | Sumber terenkripsi | Restore arsip terenkripsi | Konversi plaintext → terenkripsi |
|---|---|---|---|
| History mentah | event utuh ✔ | ✔ | ✔ |
| `summary_ref` (History) | `{"goal":"SUMMARYCANARY"}` ✔ (Round 7: `"{}"`) | ✔ | ✔ |
| Search `hello` / `"hello world"` / `你好` / `世界` / `PLAINCANARY` / `SUMMARYCANARY` | 1/1/1/1/2/1 ✔ (frasa + CJK) | sama ✔ | sama ✔ |
| `carrierstore.Load` | payload carrier utuh ✔ | ✔ | ✔ (identity konversi = identity runtime) |
| Kanari (`PLAINCANARY`, `CARRIERCANARY`, `SIGCANARY`, `SUMMARYCANARY`) di file mana pun | tidak ada ✔ | tidak ada ✔ | tidak ada ✔ |

| Skenario lain | Hasil |
|---|---|
| `compactstore.Load` (DB terenkripsi): `hello` + payload `{"tainted":true}` | content `"hello"`, payload `{"tainted":true}` ✔ (Round 7: `"S|1\x00hello"`) |
| `payload_enc` carrier baris A disalin ke baris B | `Load` → `row identity mismatch: encrypted_db` ✔ |
| `compactstore.Commit` summary `Tainted:true` + Attempt summarized → `Load` | plaintext: `tainted=[true]`, `LastCall=summarized`; **terenkripsi: `tainted=[false]`, `LastCall=nil`** → SEC7-37b |
| Kolom `reasoning_carriers.payload_sha` | = `sha256(plaintext)` (plaintext di kolom) → SEC7-55 |

### Status temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-37b | **TERBUKA** (MEDIUM) | `summariesQ` masih `json_extract(e.payload, '$.tainted')` dan `stateQ` masih `json_extract(payload, '$.called')` atas `events.payload` plaintext, padahal di DB terenkripsi isinya placeholder `{}`. Probe: summary tainted dimuat sebagai **untainted** (tidak dirender di `<untrusted>`, ADR-070), dan `LastCall` hilang sehingga cooldown compaction tidak berlaku (summarizer dapat dipanggil ulang setiap turn). Perbaikan: dekripsi payload event `compaction`/`summary_ref` lewat `OpenRow` di Go (bukan `json_extract`), atau simpan flag non-rahasia (`tainted`, `called`) di kolom/tabel metadata plaintext. Tambahkan test real-codec. |
| SEC7-46 | **FIXED** | `compactstore.events` memakai `OpenRow(…, sid|seq)` untuk content dan payload (probe). |
| SEC7-47 | **FIXED** | Tokenizer bersama untuk indeks dan query: frasa ber-kutip dan CJK cocok dengan codec nyata. |
| SEC7-48 | **FIXED** | (Round 7). |
| SEC7-49 | **FIXED** | `COALESCE(m.content_len, length(m.content))`; test integrasi untuk baris lama `NULL`. |
| SEC7-50 | **FIXED** | Nolint di konversi diganti fsync + rename (`CreateTemp`/`Sync`/`Chmod`/rename); `TestNolintDensity` dan `TestImportGraph` lulus. |
| SEC7-51 | **FIXED (opsi b)** lewat #217 | `Save`: `SealRow("reasoning_carriers.payload", event_id|ord, payload, "")`. `Load`: `OpenRow` dengan identity yang sama. Konversi restore: `Seal(scope, "event_id|ord\x00"+payload)`, format yang identik dengan `sealRow`, dibuktikan oleh probe konversi → `Load`. Kanari lewat `Save` nyata tanpa plaintext di disk; swap terdeteksi. |
| SEC7-52 | **FIXED** | Σ `loc.budget` (core) = Total core ENGINEERING di ketiga head; amandemen ADR-124 PR 3b/PR 4/carrier mencatat 40.370 / 40.420 / 40.820. Nit: komentar `loc.budget` carrierstore "terukur 339", padahal `make loc` 291. |
| SEC7-53 | **FIXED** | Test integrasi memakai penulis nyata, dan probe ini mengonfirmasinya dengan codec nyata. |
| SEC7-54 | **FIXED** | `historySummaries` membaca `content_enc` + `OpenRow(…, sid|from:to)` (probe). |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|---|
| SEC7-55 | #217 | MINOR | confirmed | `reasoning_carriers.payload_sha` = `sha256(plaintext)` disimpan plaintext (dipakai `BySHA` untuk memetakan penolakan signature). **Tebakan praktis rendah**: payload carrier selalu memuat bidang opaque berentropi tinggi buatan provider (`signature`, `encrypted_content`, `thought_signature`, `data`), sehingga brute-force tidak layak. Hash ini tetap berfungsi sebagai oracle konfirmasi bagi pihak yang sudah memegang salinan carrier (mis. log provider/proxy). Ini juga tidak konsisten dengan SEC7-36, yang sudah memakai hash berkunci untuk hash chain. | Saat codec aktif, simpan `codec.Hash("carrier"‖payload)` (HMAC subkunci DEK) di `payload_sha` dan pakai fungsi yang sama di `BySHA`/`Refs`. Ref di `events.payload` sudah terenkripsi. Opsional sebelum rilis. |

### Syarat merge (pembaruan SEC7-51)

- #217 memenuhi opsi (b). Syaratnya kini: **#198, #199, #201, #217 di-merge dalam satu jendela merge**, dan `storage.encrypt` tidak boleh dapat diaktifkan di build mana pun yang memuat #198 tanpa #217.
- **`carrierstore` versi #217 harus menggantikan versi #190**, tidak boleh berdampingan. #217 saat ini menambahkan `internal/ctxdb/carrierstore` sendiri (budget 400, komentar "berbasis ADR-131"), sedangkan #190 juga menambahkannya (budget 300). Siapa pun yang di-merge kedua harus me-rebase ke satu paket, satu baris `loc.budget`, dan satu baris matriks E-01. Versi tanpa `SealRow`/`OpenRow` tidak boleh tersisa: `Save` yang menulis plaintext akan mengulang SEC7-51. Setelah rebase, jalankan ulang `TestZZR8` (kanari carrier + konversi → `Load`) dan matriks `projectCarriers` di #193/#200.
- `gateway_inbound`: konversi menyegel baris, tetapi belum ada penulis/pembaca runtime. Saat writer gateway mendarat, berlaku syarat yang sama dengan carrier.

**Ringkasan:** #199 **APPROVE**; #217 **APPROVE dengan MINOR** (SEC7-55); #201 **REQUEST CHANGES (kecil)**, karena satu temuan MEDIUM yang lama masih terbuka (SEC7-37b: taint summary dan cooldown compaction hilang di DB terenkripsi). Selain itu seluruh SEC7-44..54 tertutup dengan codec nyata, archtest hijau, dan Total core konsisten.

## Round 9 — 2026-10-21 (SEC7-37b, SEC7-55)

Head: #201 `bf1ae24` (`939fccc` dekripsi payload `summary_ref`/`compaction` di compactstore + `bf1ae24` budget `compactstore 350`), #217 `e9e7ab2` (`ccc91c2` = carrier yang sama dengan `76790a7` plus amandemen ADR yang disesuaikan, + `e9e7ab2` `payload_sha` berkunci). `git range-diff 11e9230..76790a7 bf1ae24..e9e7ab2`: `76790a7 → ccc91c2` hanya berbeda di teks amandemen PR 4 dan baris ENGINEERING/Total core (compactstore 350, Total 40.870). Kode carrier tidak berubah.

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #201 | `bf1ae24` | **APPROVE** (SEC7-37b FIXED; nit dokumen di bawah) |
| #217 | `e9e7ab2` | **APPROVE** (SEC7-55 FIXED; syarat merge Round 8 tetap) |

Gate di kedua head: `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet ./internal/ctxdb/... ./internal/backup` bersih; `make deadcode` 0; `go test -race -tags goolm -count=1 ./internal/archtest ./internal/integration ./internal/backup ./internal/ctxdb/{compactstore,memstore}` (+ `carrierstore` di `e9e7ab2`) → ok; `./internal/ctxdb -race -run 'AtRest|Restore|Migrat|FTS|Search'` → ok. `make loc`: `bf1ae24` `ctxdb 4279/4400`, `compactstore 330/350`; `e9e7ab2` `ctxdb 4282/4400`, `compactstore 330/350`, `carrierstore 297/400`. Total core ENGINEERING **40.470** / **40.870** = Σ `loc.budget` core (archtest `repometa` lulus). Kenaikan `compactstore 300 → 350` didukung angka terukur 330 (dekripsi payload per event `compaction`/`summary_ref`).

Probe (codec `secret` nyata; dijalankan lalu dihapus):

| Skenario | Plaintext | Terenkripsi |
|---|---|---|
| `compactstore.Commit` 2 summary (`Tainted` true lalu false, Attempt summarized `AtMS` 11 lalu 22) → `Load` | `tainted=[true false]`, `LastCall={summarized context_length 22}` | **`tainted=[true false]`, `LastCall={summarized context_length 22}`** ✔ (Round 8: `[false]`, `nil`) |
| `carrierstore.Save` → kolom `payload_sha` | = sha256(plaintext) | **≠ sha256(plaintext)** (HMAC `codec.Hash`) ✔ |
| `Refs()[0].SHA` = kolom; `Load().SHA` = kolom | ✔ | ✔ |
| `BySHA(Store.PayloadSHA(p))` / `BySHA(sha256 plaintext)` | `[1]` / `[1]` | `[1]` / **`[]`** ✔ (tidak ada oracle sha polos) |
| Arsip plaintext berisi carrier → restore `encrypt=true` | – | `Load` 1 carrier, payload utuh, `SHA` = hash berkunci, `BySHA(keyed)=[1]`, `BySHA(plain)=[]` ✔ (`sealCarriers` menulis ulang `payload_sha`) |

| ID | Verdict | Bukti |
|---|---|---|
| SEC7-37b | **FIXED** (#201) | `summariesQ` mengambil `payload`/`payload_enc` event `summary_ref`, lalu `OpenRow("events.payload", sid|seq)` → `refPayload.Tainted`. `lastCall` membaca event `compaction` (terbaru dulu) lewat `OpenRow` dan memakai yang `Called`. Probe di atas. |
| SEC7-55 | **FIXED** (#217) | `Store.PayloadSHA` = `codec.Hash(payload)` saat `Encrypted()`, selain itu sha256. Dipakai oleh `Save`, `Refs`, dan (via pemanggil) `BySHA`. Konversi restore menulis `payload_sha = codec.Hash(...)`. |

Catatan (tidak memblokir):
- **Nit dokumen #201**: di `bf1ae24`, amandemen ADR-124 PR 4 masih menulis "`ctxdb/compactstore` 250→300" sementara tabel ENGINEERING dan `loc.budget` sudah 350 (Total 40.470). Teks baru menjadi "250→350" di commit carrier (`ccc91c2`). Perbaiki di #201 agar commit itu konsisten sendiri. Komentar `loc.budget` carrierstore "terukur 291", padahal `make loc` 297.
- **Coupling dengan rebase carrier (#190/#193/#200)**: `BySHA` mencocokkan nilai yang diberikan pemanggil. Setelah rebase, `RejectCarriers` di `cmd/damnbot` wajib mengirim `Store.PayloadSHA(raw)` (atau SHA hasil `Load`), bukan sha256 yang dihitung `provider.captureCarriers`. Ref `carriers:[{ord,sha}]` di payload event assistant juga harus memakai `Store.Refs`, bukan sha provider. Kalau tidak, penolakan signature tidak pernah terpetakan di DB terenkripsi (regresi SEC8-2: carrier yang ditolak di-replay lagi). Uji ulang probe replay SEC8 Round 2 (400 `invalid_signature` → carrier tidak dikirim lagi) dengan `storage.encrypt=true` setelah rebase.
- `codec.Hash` memakai subkunci yang sama dengan hash chain event (tanpa label domain). Format input kedua domain berbeda (`prev|sid|seq|…` vs JSON carrier), jadi risiko tumbukan lintas domain dapat diabaikan. Opsional: awali input carrier dengan `"carrier\x00"`.

**Ringkasan:** #201 dan #217 **APPROVE**. Seluruh temuan SEC7 pada stack kripto tertutup dan terverifikasi dengan codec nyata. Syarat merge Round 8 tetap berlaku: #198/#199/#201/#217 dalam satu jendela, satu `carrierstore` (versi #217) yang menggantikan versi #190, dan uji replay penolakan signature dengan `storage.encrypt=true` setelah rebase carrier.

## Round 10 — 2026-10-21 (rebase ke `main` `e429331` yang sudah memuat carrier)

Stack di-rebase ke `main` `e429331` (#190/#193/#200 sudah di `main`). Head: #185 `a32a359` → #189 `828188b` (`11f248f` + commit CHANGELOG) → #187 `d39df0d` → #191 `fd44cd8` → #192 `a060f9e` → #194 `6bcb408` → #197 `323dd5a` → #198 `efb2f8d` → #199 `b720fc8` → #201 `11719d4` → #217 `1820dfb` (`75d2082`, `a602d78`, `4b3e1c1`, `1820dfb`).

**Verdict per PR:**

| PR | Head | Verdict |
|---|---|---|
| #185, #189, #187, #191, #192, #194, #197, #198, #199, #201 | lihat atas | **APPROVE** (rebase/budget saja; kode identik dengan head yang sudah disetujui) |
| #217 | `1820dfb` | **APPROVE**. SEC7-51 (syarat merge Round 8/9) **terpenuhi**: satu `carrierstore` (paket di `main`) dengan enkripsi, hash berkunci berdomain, dan jalur penolakan/ref lewat store. Replay SEC8-2 aman dengan `storage.encrypt=true`. |

### (a) Range-diff terhadap head yang disetujui

`git range-diff 0128e6e..e9e7ab2 e429331..1820dfb` + `git patch-id --stable` atas setiap commit (tanpa `docs/`, `CHANGELOG.md`, `loc.budget`, `.github/`): **18 dari 18 pasangan identik secara kode** dari `a8af6ca`/`e18a3d1` sampai `bf1ae24` (`e18a3d1→a32a359`, `5f50671→11f248f`, `4a15631→d39df0d`, `cf416fd→fd44cd8`, `256b98a→a060f9e`, `af6ddff→6bcb408`, `1479142→323dd5a`, `b66556d→efb2f8d`, `259fb65→8837c26`, `539d52b→d5594be`, `5c50d6a→b720fc8`, `97a5279→066da90`, `8892e72→2f06eba`, `d5fccbb→f3dd58e`, `11e9230→f1ad5c4`, `bf1ae24→51f66d9`, dan sisanya "="). Perbedaan "!" hanya di komentar `loc.budget`, CHANGELOG, dan teks ADR/ENGINEERING. Commit baru non-kode: `828188b` (CHANGELOG), `11719d4` (ADR compactstore 350), `4b3e1c1` (komentar carrierstore 297). Delta kode hanya ada di commit carrier: `ccc91c2→75d2082`, `e9e7ab2→a602d78`, dan `1820dfb` baru.

### (b) Satu `carrierstore`

`internal/ctxdb/carrierstore` adalah satu-satunya paket carrier (milik `main`, dari #190). `75d2082` memodifikasinya di tempat: `Save` → `tx.SealRow("reasoning_carriers.payload", "event_id|ord", payload, "")` + `payload_enc`; `Load` → `OpenRow` dengan identity yang sama (dan `slices.Clone` lama diganti salinan dari `OpenRow`). Satu baris `loc.budget` (`ctxdb/carrierstore 400`, terukur 298) dan satu entri matriks E-01. `Store.PayloadSHA` = `KeyedHash("carrier\x00" + payload)` saat terenkripsi, selain itu sha256. Konversi restore (`encryptfile.go`) memakai prefix yang sama: `codec.Hash("carrier\x00"+…)`.

### (c) Penolakan dan ref lewat store

`1820dfb`: `ctxCarrierStore.RejectCarriers` menghitung `store.PayloadSHA(cs[i].Raw)`, dan tidak lagi memakai sha256 dari provider/`cs[i].SHA`. Payload event assistant `carriers:[{ord,sha}]` diisi `turn.carrierRefs` → `deps.Carriers.CarrierRefs` → `Store.Refs` (`Ord: i`, sama dengan `SaveCarriers`). Fallback `plainCarrierRefs` hanya dipakai tanpa store.

### (d)(e) Probe end-to-end (`damnbot run` nyata + `providertest`; codec nyata via `[storage] encrypt = true`)

| Skenario | Plaintext | `storage.encrypt=true` |
|---|---|---|
| Replay SEC8-2: r1 → tool call + `SIG1`; r2 → 400 `invalid_signature`; retry; `SIG2`, `SIG3` | r2=`[SIG1]`, r3=`[]`, r4=`[SIG2]`, r5=`[SIG2 SIG3]` (SIG1 tidak di-replay) ✔ | **identik** ✔ (penolakan terpetakan lewat hash berkunci) |
| Byte `SIG*`/teks thinking di file data dir | ada (plaintext, sesuai desain) | **tidak ada** ✔ |
| Carrier bersih | tersimpan + di-replay ✔ | tersimpan terenkripsi + di-replay ✔ |
| Secret di `summary[].text` (SEC8-8) | di-drop, tidak di disk, tidak di-replay ✔ | sama ✔ |
| Carrier > 256 KiB (SEC8-5) | di-drop (`too_large`), tidak di disk, tidak di-replay ✔ | sama ✔ |

Gate di `1820dfb`: `go test -race -tags goolm -count=1 ./internal/{secret,backup,integration} ./internal/ctxdb/{carrierstore,compactstore,memstore} ./internal/loop/... ./internal/provider/... ./cmd/damnbot ./internal/archtest` → ok; `./internal/ctxdb -race` → ok (281 s); `go vet` + `GOOS=windows|darwin|freebsd` bersih; `make deadcode` 0; `go mod tidy -diff` bersih. `make loc`: `secret 1150/1150`, `backup 1462/1550`, `ctxdb 4289/4400`, `compactstore 330/350`, `carrierstore 298/400`, `loop 1920/1950`, `provider 2065/2100`; Total core **41.420** (archtest `repometa` = Σ `loc.budget`). Di setiap head antara (`a32a359` … `11719d4`): archtest ok, `make loc` lulus, `go vet ./...` bersih, dan Total core naik konsisten (38.340 → 41.320).

**Ringkasan:** seluruh stack kripto **APPROVE**. Rebase tidak mengubah kode yang sudah direview. #217 memenuhi syarat SEC7-51: satu `carrierstore` terenkripsi, hash berkunci `carrier\x00`, penolakan dan ref lewat `Store`, dan replay SEC8-2 tidak terulang dengan codec nyata. Caps dan redaksi carrier tetap berlaku di DB terenkripsi. Syarat merge yang tersisa: #198/#199/#201/#217 di-merge dalam satu jendela.

## Round 11 — 2026-10-22 (rebase ke `fa59d71`; aturan owner: e2e + kontrol negatif)

**Verdict:** stack **REQUEST CHANGES**. Rebase memperkenalkan tiga regresi: **SEC7-56 (HIGH)** exclusion #211 tidak bekerja di SQL saat enkripsi aktif; **SEC7-57 (HIGH, fungsional)** recall memori plaintext kembali ke AND (#228 terbatalkan); **SEC7-58 (MEDIUM)** open read-only + codec pada DB plaintext kosong → panic. Ditambah SEC7-59 (MEDIUM, aturan owner: e2e tanpa kontrol negatif), SEC7-60 (MEDIUM, CI: `loc-budget` #198 merah dan `archtest` #241 merah), dan SEC7-61 (MINOR).

| PR | Head | Head approve sebelumnya | Verdict |
|---|---|---|---|
| #185 | `9f9ef94` | `a32a359` | APPROVE (delta hanya dokumen/budget) |
| #189 | `230f5ba` | `828188b` | APPROVE (sama) |
| #187 | `bb47855` | `d39df0d` | APPROVE (sama) |
| #191 | `795fc87` | `fd44cd8` | APPROVE (sama) |
| #192 | `90290c8` | `a060f9e` | APPROVE (sama) |
| #194 | `c43dc95` | `6bcb408` | APPROVE (delta: `//nolint` test) |
| #197 | `ca3fcf7` | `323dd5a` | APPROVE (delta: konteks `main_test.go`) |
| #198 | `d8c7276` | `efb2f8d` | **REQUEST CHANGES**: SEC7-60 (`ctxdb` 4096 > 4000 di head ini, CI `loc-budget` gagal) |
| #199 | `937fe08` | `b720fc8` | **REQUEST CHANGES**: SEC7-56/57 (merge tangan `read.go`/`memstore`) |
| #201 | `7dec0ab` | `11719d4` | **REQUEST CHANGES**: SEC7-56 (`compactstore`), SEC7-57 (`ftsValue`), SEC7-58 |
| #217 | `5959ae2` | `1820dfb` | APPROVE kode (identik `=`); tertahan stack |
| #241 (e2e) | `2758130` | — | **REQUEST CHANGES**: SEC7-59 (kontrol negatif), SEC7-60 (`archtest` gagal) |

### 1. Range-diff (`e429331..1820dfb` vs `fa59d71..5959ae2`)

33 pasangan. `=`: `c6cf941`, `e5ae990`, `2d46dfd`, `63f14da`, `cdddeed`, `523df92`, `69efc15`, `eb7d8e0`, `85452c1`, `05b3ad1`, `5959ae2`. `4b3e1c1` (budget carrierstore) dihapus dan budget pindah ke baris lain. Commit baru: `58644b9` (test real-codec) dan `7dec0ab` (operator FTS). Delta kode non-trivial (selain konteks dokumen/`loc.budget`/`CODEOWNERS`):

- **`internal/ctxdb/read.go` (`searchFTS`)** menggabungkan `content_enc` (dekripsi) dengan `EXISTS (… x.kind = 'context_exclude' AND json_extract(x.payload, '$.turn_id') = e.turn_id)` milik #211, untuk event dan summary (`t.seq BETWEEN sm.from_seq AND sm.to_seq`) → lihat SEC7-56.
- **`internal/ctxdb/store.go`**: `Open` → `openReadOnly` (#203) **atau** migrate+attach+recovery, lalu **`checkAtRest` untuk kedua jalur**. `Options.Codec` ikut di kedua mode. `ftsValue` → `keyedFTSExpr` (`7dec0ab`) → lihat SEC7-57.
- **`internal/ctxdb/compactstore/compactstore.go`**: `summariesQ` = dekode `tainted_payload`/`tainted_hex` (OpenRow) **+** `NOT EXISTS … json_extract(x.payload, '$.turn_id')` milik #211; `notExcluded` untuk load event juga memakai `json_extract(x.payload, …)` → SEC7-56.
- **`internal/ctxdb/memstore/memstore.go`**: `MATCH ?` (#228) dikembalikan menjadi `MATCH dmb_fts_norm(?, 'memory.content', '')` → SEC7-57.
- **`internal/backup/restore*.go`**: hanya `//nolint:gosec` di test. **`cmd/damnbot/main_test.go`**: hanya konteks (`TestVersionVerbose`). **`cmd/damnbot/rel01_test.go`**: `startRunModel(…, nil)` (codec nil).

### 2. Exclusion + dekripsi

- **History:** benar. Eksklusi dihitung di Go atas event yang sudah didekripsi (`contextExclusions`/`excludeTurns`); `TestEncryptedHistoryExcludedTurns` lulus.
- **Search:** **rusak saat terenkripsi.** Payload event terenkripsi disimpan sebagai `payload='{}'` + `payload_enc`, sehingga `json_extract(x.payload,'$.turn_id')` = NULL dan `EXISTS` tidak pernah cocok. Probe `TestZZSec7R11b` (codec nyata, dihapus setelah dijalankan): plaintext → hit turn yang di-exclude `Excluded=true`; **terenkripsi → `Excluded=false`** (snippet "zebracanary excluded question" terdekripsi dan dikembalikan sebagai hit biasa).
- **compactstore:** penyebab yang sama (statis). `notExcluded` dan filter `summariesQ` memakai `json_extract(x.payload, …)`, sehingga dengan enkripsi turn yang di-exclude ikut dimuat ke input compaction dan summary yang mencakupnya tidak dibuang. Ini SEC11-1/SEC11-5 terbuka kembali, khusus mode terenkripsi.

### 3. Read-only + codec (#203)

| Probe (codec nyata) | Hasil |
|---|---|
| DB terenkripsi, read-only **tanpa codec** | `encrypted_db: storage.encrypt is false` ✔ (tidak fallback plaintext) |
| DB terenkripsi, read-only **DEK lain** | `encrypted_db: DEK id mismatch` ✔ |
| DB terenkripsi di **media RO** (fallback immutable), dengan codec | terbuka, History terdekripsi ✔; tanpa codec → `encrypted_db` ✔ |
| DB plaintext berisi event, read-only + codec | `at_rest_existing_plaintext` ✔ |
| **DB plaintext kosong (tanpa marker, tanpa event), read-only + codec** | **PANIC nil pointer**: `checkAtRest` mencoba `INSERT` marker lewat `s.w`, padahal `s.w` nil di mode read-only → SEC7-58 |

### 4. Test real-codec

`TestEncryptedHistoryExcludedTurns` dan `TestEncryptedReadOnlyOpen` memakai `secret.OpenCodec` (codec nyata) dan lulus. Keduanya tidak mencakup Search/compactstore (SEC7-56) maupun kontrol negatif read-only tanpa codec atau DEK salah (yang lulus di probe saya, tetapi tidak dikunci test).

### 5. E2E #241 `TestCryptoBackupRestore`

Binary nyata (harness #239: `go build ./cmd/damnbot`): `storage.encrypt=true` → `run` → sweep canary atas **seluruh** file data dir (termasuk `-wal`) → `sessions backup create` → sweep arsip → `sessions restore` ke dir baru → sweep → `run` kedua → History read-only terenkripsi memuat prompt pertama. Lulus lokal. **Kontrol negatif tidak ada.** Probe saya `TestZZCryptoNegativeControl` (dihapus setelah dijalankan): dengan `storage.encrypt=false`, sweep yang sama menemukan canary di `damnbot.db`, jadi sweep memang mampu mendeteksi plaintext. Kontrol ini harus ada di test (SEC7-59). Arsip `DMBK1` selalu terenkripsi, sehingga kontrol untuk arsip cukup lewat data dir.

### 6. Budget

`make loc` dan `go test ./internal/archtest` dijalankan di **setiap** head:

| Head | Total core (ENGINEERING) | `make loc` | archtest |
|---|---|---|---|
| #185 `9f9ef94` … #197 `ca3fcf7` | 38.750 … 40.530 | ok | ok |
| **#198 `d8c7276`** | 40.830 | **GAGAL: `ctxdb 4096 > 4000`** (CI `loc-budget`: fail) | ok |
| #199 `937fe08` (`ctxdb` 4700) / #201 `7dec0ab` / #217 `5959ae2` | 41.630 / 41.730 / 41.830 | ok | ok |
| **#241 `2758130`** | — | ok | **FAIL**: `TestGlobalAllowlist` (`internal/e2e/harness_test.go:binPath`), `TestImportGraph` (`internal/e2e` tidak ada di matriks), `TestCodeownersComplete` (`internal/e2e` tanpa CODEOWNERS) |

Klaim worker bahwa "loc + archtest ok di 12 head" tidak benar untuk #198 dan #241. Baris budget akhir (`ctxdb` 4.700, `compactstore` 360, `carrierstore` 400, `secret` 1.150, `backup` 1.550) konsisten dengan Total core per head. Kenaikan `ctxdb` +900 di atas `main` 3.800 tetap sebesar yang di-approve di Round 10 ditambah test real-codec.

Gate lain di `2758130`: `go test -race -tags goolm -count=1 ./internal/integration ./internal/ctxdb/compactstore ./internal/ctxdb/carrierstore ./internal/backup ./internal/secret` → ok; `go test ./internal/e2e -run TestCryptoBackupRestore` → ok; `GOOS=windows go vet` bersih.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| **SEC7-56** | **HIGH** | confirmed (Search, probe) + static (compactstore) | Dengan enkripsi aktif, payload event disimpan `'{}'` + `payload_enc`, sehingga **semua filter exclusion berbasis SQL** (`read.go` `searchFTS` untuk event dan summary, `compactstore` `notExcluded` dan `summariesQ`) yang memakai `json_extract(x.payload,'$.turn_id')` tidak pernah cocok. Turn yang gagal/beracun muncul lagi di recall (`Excluded=false`) dan masuk ke compaction/summary. #211/SEC11 tidak berlaku di mode terenkripsi. | Pakai kolom plaintext `x.turn_id = e.turn_id` (event `context_exclude` sudah membawa `TurnID`) di keempat lokasi, atau tabel/indeks exclusion plaintext. Tambahkan test real-codec untuk Search (`Excluded=true`) dan compactstore (summary yang mencakup turn ter-exclude dibuang), dengan kontrol plaintext. |
| **SEC7-57** | **HIGH (fungsional, mode default)** | confirmed (probe) | `memstore` kembali ke `MATCH dmb_fts_norm(?, …)`. Di mode **plaintext** (default), `ftsValue` = `ftsNorm` me-lowercase `"x" OR "y"` menjadi `"x" or "y"`, sehingga `or` dianggap term dan ekspresinya menjadi AND. Probe: "What is my locker number?" → **0 hit** plaintext (1 hit terenkripsi). Fix #228 (E4 29/30) terbatalkan. Selain itu, di mode terenkripsi `keyedFTSExpr` dipakai juga untuk nilai **index**, sehingga kata `OR`/`AND`/`NOT` huruf besar di konten tersimpan sebagai token plaintext. | Sisi query: plaintext → `ftsNorm` per token dengan operator dipertahankan (sama seperti `keyedFTSExpr` tanpa hash). Sisi index: selalu hash atau normalisasi semua token, tanpa pengecualian operator. Tambahkan test recall plaintext + terenkripsi untuk kueri pertanyaan. |
| **SEC7-58** | MEDIUM | confirmed (probe) | `checkAtRest` di jalur read-only dengan codec pada DB tanpa marker dan tanpa event menulis `INSERT … module_state` lewat `s.w` (nil) → panic. Contoh: `sessions ls`/`doctor` read-only dengan `storage.encrypt=true` pada data dir yang baru diinisialisasi. Mode read-only juga tidak boleh menulis. | Di read-only: jangan menulis marker. Perlakukan DB kosong tanpa marker sebagai terbaca (tidak ada isi), atau tolak dengan error bertipe. Tambahkan test. |
| SEC7-59 | MEDIUM (aturan owner) | open | `TestCryptoBackupRestore` tanpa kontrol negatif. | Subtest `storage.encrypt=false`: sweep yang sama **harus** menemukan canary di data dir (probe saya: `damnbot.db`). |
| SEC7-60 | MEDIUM (CI) | confirmed | #198 `d8c7276`: `ctxdb 4096 > 4000` (CI `loc-budget` merah). #241: `archtest` gagal (allowlist global, matriks import `internal/e2e`, `CODEOWNERS`). | Naikkan budget `ctxdb` di commit yang menambah kode (#198), bukan di #199. Untuk #241, ambil perubahan `archtest` dari #239 (allow/matrix/CODEOWNERS) bersama harness. |
| SEC7-61 | MINOR | open | `TestEncryptedReadOnlyOpen` tidak mengunci kontrol negatif (tanpa codec → `ErrEncrypted`, DEK salah → mismatch), padahal keduanya lulus di probe. `assertNoPlaintext` hanya memeriksa file DB utama. | Tambahkan kedua kasus. Sweep juga `-wal`/`-shm`/blob. |

**Ringkasan Round 11:** rebase mempertahankan dekripsi di History/Search/compactstore dan `checkAtRest` di jalur read-only #203. DB terenkripsi tidak pernah dibuka tanpa codec atau dengan DEK salah, termasuk lewat fallback immutable. Namun merge tangan kehilangan dua hal: exclusion #211 dalam SQL tidak berfungsi pada payload terenkripsi (SEC7-56, HIGH), dan recall memori plaintext kembali ke AND (SEC7-57, HIGH, mode default). Read-only + codec pada DB kosong juga panic (SEC7-58). E2E #241 berjalan di binary nyata dan menyapu data dir serta arsip, tetapi kontrol negatifnya belum ada (SEC7-59). CI `loc-budget` #198 dan `archtest` #241 merah (SEC7-60). `main` sudah di `70ff325`, jadi rebase berikutnya juga harus menutup temuan ini.

## Round 12 — 2026-10-22

**Verdict:** stack **REQUEST CHANGES (sempit)**. SEC7-56/58/59/60/61 FIXED. SEC7-57 FIXED untuk memori, tetapi perpindahan `skillstore` ke `MATCH ?` + `Tx.FTSQuery` membuka regresi baru **SEC7-62 (HIGH, mode plaintext default)**: injeksi operator FTS (syntax error pada `AND`/`OR`) dan pencarian skill CJK rusak. Head yang berubah: #198 `2347a65`, #199 `8469ceb`, #201 `730bdf9`, #217 `780b939`, #241 `d6d2a07`; head lain sama dengan Round 11 (APPROVE).

| PR | Head | Verdict |
|---|---|---|
| #185 / #189 / #187 / #191 / #192 / #194 / #197 | tidak berubah | APPROVE |
| #198 | `2347a65` | **APPROVE**: `ctxdb` 4200, terukur 4096 (`make loc` ok, archtest ok; spot-check saya) |
| #199 | `8469ceb` | APPROVE (delta budget/dokumen saja) |
| #201 | `730bdf9` | **REQUEST CHANGES**: SEC7-62 (`skillstore`) |
| #217 | `780b939` | APPROVE (rebase) |
| #241 | `d6d2a07` | APPROVE dengan MINOR (SEC7-63) |

`git range-diff fa59d71..2758130 fa59d71..d6d2a07`: perubahan ada di `2347a65`/`c52446c`/`8469ceb` (baris budget `ctxdb` 4000 → 4200 dipindah ke #198) dan `d6d2a07` (archtest/CODEOWNERS/kontrol e2e). Commit baru: **`730bdf9`** (turn_id exclusion, `FTSQuery`, `ErrReadOnly`, dan test real-codec).

Gate di `d6d2a07`: `go test -race -tags goolm -count=1 ./internal/integration ./internal/ctxdb ./internal/ctxdb/compactstore ./internal/ctxdb/memstore ./internal/ctxdb/skillstore ./internal/e2e ./cmd/damnbot ./internal/archtest` → ok; `make loc` `ctxdb 4677/4700`, `compactstore 335/360`, `carrierstore 298/400`; `make deadcode` 0. Spot-check #198 `2347a65`: `make loc` `ctxdb 4096/4200` ok, archtest ok.

### Verifikasi

- **SEC7-56 (fixed):** `searchFTS` (event dan summary), `compactstore.summariesQ`, dan `notExcluded` kini memakai kolom plaintext `x.turn_id = e.turn_id` / `t.turn_id`. Kedua penulis produksi menyetel kolom itu: loop (`appendAs(ctx, t.turnID, ContextExclude)`) dan crash recovery (`journal.go` `TurnID: turn`). `TestEncryptedSearchAndSummaryExclusions` + `TestPlaintextSearchAndSummaryExclusions` (codec nyata, plus kontrol plaintext) mengunci `Excluded=true` dan pembuangan summary ✔. Test lama yang menulis marker tanpa `TurnID` disesuaikan; penulis produksi tidak terpengaruh.
- **SEC7-57 (fixed untuk memori):** `memstore` → `MATCH ?` dengan `tx.FTSQuery(ftsQuery(text))`. Di plaintext ekspresinya tidak diubah (#228 utuh); di mode terenkripsi term di-hash dan `OR`/`AND`/`NOT` builder dipertahankan. Index terenkripsi tetap memakai `encFTS` (semua token di-hash); `TestEncryptedIndexHidesOperatorWords` ✔. Probe `TestZZMemOps` (plaintext dan terenkripsi; dua scope `user` dan `gateway:telegram:bob`):

| Kueri | plaintext | terenkripsi |
|---|---|---|
| "What is my locker number?" | `[user]` ✔ | `[user]` ✔ |
| `locker" OR "bob` / `NEAR(locker bob)` | `[user]`, tanpa error, tanpa scope lain ✔ | sama ✔ |
| `OR` / `AND NOT` / `bob*` / `"or" "near"` | `[]`, tanpa error ✔ | sama ✔ |

  Tidak ada injeksi operator di memori: `ftsQuery` hanya menyisakan huruf/angka dan mengutip setiap token. Kata user yang mengeja operator menjadi `"or"` (huruf kecil, dikutip), sehingga di-hash dan tidak menjadi operator.
- **SEC7-58 (fixed):** `checkAtRest` → `ErrReadOnly` bila `s.w == nil`; `TestReadOnlyCodecErrors` ✔.
- **SEC7-59 (fixed):** `TestCryptoPlaintextCanaryVisible` (binary nyata, `storage.encrypt=false`) memastikan canary ada di `damnbot.db` ✔. Catatannya ada di SEC7-63.
- **SEC7-60 (fixed):** lihat gate dan spot-check. #241 kini menambahkan `internal/e2e` ke matriks `allowed.go`, `globals.allow`, `CODEOWNERS`, dan baris `TESTONLY`.
- **SEC7-61 (fixed):** `TestReadOnlyCodecErrors` mencakup read-only tanpa codec dan DEK salah → `ErrEncrypted`.
- **Marker exclusion sebagai audit:** `excludeTurns` kini **mempertahankan** event `context_exclude` di `History`. Satu-satunya konsumen produksi, `ctxStore.History` (`cmd/damnbot/tools_wire.go`), hanya memetakan `user_message`, `assistant_message`, `tool_result`, dan `system_notice` ke pesan provider. `context_exclude` diabaikan, dan payload-nya hanya `{turn_id, reason}`. **Konten turn yang di-exclude tetap tidak sampai ke request provider** ✔. compactstore `eventsQ` hanya membaca kind yang terlihat model. Efek samping kecil: marker ikut menghabiskan satu slot `DefaultHistoryLimit`.

### SEC7-62: `skillstore` plaintext

`skillstore.ftsQuery` membuat term **tanpa kutip** dengan `*` di term terakhir. Sebelumnya kueri itu melewati `dmb_fts_norm(?2, …)` (lowercase + bigram CJK); kini plaintext dikirim **mentah** lewat `MATCH ?2`. Probe `TestZZSkillOps` (dihapus setelah dijalankan; pembanding `main` `fa59d71`):

| Kueri skill (plaintext, default) | `main` | `d6d2a07` |
|---|---|---|
| `deploy AND` | `[]` | **error `fts5: syntax error near "*"`** |
| `OR` | `[]` | **error `fts5: syntax error near "OR"`** |
| `deploy OR anything` | `[]` | `[deploy-prod]` (operator dari teks user) |
| `日本語`, `スキル` | `[日本語スキル]` | **`[]`** (bigram CJK hilang) |

Pesan user pertama menjadi kueri retrieval skill (tier konteks). Akibatnya, kata `OR`/`AND` di posisi tertentu menggagalkan pencarian skill, teks user menjadi operator FTS, dan skill bernama CJK tidak pernah ditemukan.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC7-56 | HIGH | **fixed** `730bdf9` | — | — |
| SEC7-57 | HIGH | **fixed (memori)** `730bdf9` | — | — |
| SEC7-58 | MEDIUM | **fixed** | — | — |
| SEC7-59 | MEDIUM | **fixed** | — | — |
| SEC7-60 | MEDIUM | **fixed** | — | — |
| SEC7-61 | MINOR | **fixed** | — | — |
| **SEC7-62** | **HIGH** (fungsional + injeksi operator, mode default) | confirmed (probe) | `skillstore.searchIDs` kini `MATCH ?2` dengan `tx.FTSQuery(query)`. Di plaintext, kueri tidak dinormalisasi lagi (tanpa `ftsNorm`, tanpa kutip): `AND`/`OR` user menghasilkan syntax error fts5 atau mengubah semantik, dan pencarian CJK rusak. | `skillstore.ftsQuery` meniru memstore: `ctxdb.FTSNorm` di Go, setiap token dikutip (`"tok"*` untuk prefiks), operator hanya dari builder. Tambahkan test plaintext + terenkripsi untuk `AND`/`OR`/CJK. |
| SEC7-63 | MINOR | open | Kontrol negatif e2e membaca `damnbot.db` langsung dengan canary berbeda, bukan memanggil **fungsi sweep yang sama** (`assertNoCanary`) yang diharapkan gagal. Kontrol membuktikan plaintext ada, tetapi tidak membuktikan helper sweep mampu mendeteksinya. | Ubah `scanCanaries` agar mengembalikan temuan; kontrol memanggil sweep yang sama atas data dir plaintext dan mengharapkan ≥ 1 temuan. |

**Ringkasan Round 12:** perbaikan Round 11 benar dan terverifikasi. Exclusion memakai `turn_id` di semua jalur SQL, dengan test real-codec dan kontrol plaintext. Recall memori #228 pulih di kedua mode tanpa injeksi operator. Read-only + codec tanpa writer → `ErrReadOnly`. Kontrol negatif e2e ada. Budget dan archtest hijau (#198 dan #241 di-spot-check). Marker audit tidak sampai ke provider. Sisa satu regresi: `skillstore` plaintext kehilangan normalisasi, sehingga terjadi injeksi operator/syntax error dan pencarian CJK rusak (SEC7-62, HIGH). Perbaikannya terlokalisasi di `skillstore.ftsQuery`. Setelah itu stack dapat di-approve, dengan SEC7-63 sebagai minor.

## Round 13 — 2026-10-22

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #201 | `8eb9583` | **APPROVE dengan MEDIUM terbuka (SEC7-64)**: SEC7-62 FIXED. Unigram CJK di index terenkripsi menambah kebocoran di luar pernyataan ADR-124 dan perlu amandemen ADR atau mitigasi |
| #217 | `b9449c1` | APPROVE (restack) |
| #241 | `cd8628e` | **APPROVE**: SEC7-63 FIXED |
| #261 (baru) | `6066258` | **REQUEST CHANGES**: **SEC7-65 (MEDIUM)** `ctxdb 5008 > 4700` (`make loc` GAGAL; Total core tidak berubah, jadi klaim "dalam budget" salah); **SEC7-66 (MEDIUM)** GAP-INF-02 tidak tersambung (`blob_fts` hanya diisi saat `ReindexFTS` dan tidak pernah di-query di produksi); SEC7-67 (info) |

### 1. SEC7-62/63 dan batas History

- **SEC7-62 (fixed):** `skillstore.ftsQuery` = `ctxdb.FTSNorm` lalu setiap term dikutip (`"…"`, kutip ganda di-escape, `"…"*` untuk term terakhir). `AND`/`OR`/`NOT`/`NEAR` menjadi literal, dan bigram CJK kembali seperti di `main`. `TestSearchOperatorsAndCJK` (plaintext) dan `TestEncryptedSkillSearchOperatorsAndCJK` (codec nyata) ✔. Di mode terenkripsi, `keyedFTSExpr` menghapus kutip, meng-hash term, dan mempertahankan `*` (prefiks atas HMAC 64-hex secara praktis berarti cocok tepat; tidak berbahaya).
- **SEC7-63 (fixed):** `TestCryptoPlaintextCanaryVisible` memanggil **sweep yang sama** (`assertCanary` → `scanTree`/`scanPath`, `present=true`) atas data dir plaintext ✔.
- **Batas History:** semantik "limit menghitung baris mentah, termasuk marker audit" didokumentasikan di `HistoryOptions.Limit`/`History` dan dikunci oleh `TestHistoryLimitCountsAuditMarker`. Marker tidak masuk ke request provider (Round 12), jadi dampaknya hanya satu slot jendela per marker. Pilihan ini dapat diterima.

### 2. Kebocoran unigram CJK (SEC7-64)

`encFTS` kini menambahkan HMAC **per karakter** untuk setiap token CJK, di samping token/bigram. Semua tabel FTS adalah `fts5(…, content='', contentless_delete=1)` dengan `detail` default (**`full`**), sehingga index menyimpan **posisi** token per baris. ADR-124 butir 9 menerima bahwa "token HMAC deterministik membocorkan kesamaan dan frekuensi token (**bukan isi**)". Penilaian:

| Bentuk index terenkripsi | Yang dapat direkonstruksi penyerang yang memegang file DB tanpa kunci |
|---|---|
| Token kata (Latin) + posisi | urutan simbol per baris = sandi substitusi tingkat kata; frekuensi kata (Zipf) + model bahasa memulihkan sebagian isi. Ini sudah melampaui frasa "bukan isi" di ADR-124, tetapi alfabetnya besar |
| Bigram CJK (sebelumnya) + posisi | sandi substitusi atas bigram yang tumpang-tindih; alfabet ~10⁵–10⁶ simbol, lebih sulit dipecahkan |
| **Unigram CJK (baru) + posisi** | **sandi substitusi monoalfabetik per karakter** dengan alfabet ~3.000 hanzi/kana yang distribusi frekuensinya terkenal. Dengan korpus percakapan yang wajar, analisis frekuensi + n-gram dapat memulihkan **isi** teks CJK. Ditambah bigram yang menautkan unigram bertetangga, pemulihannya makin mudah |

Kesimpulan: unigram CJK + `detail=full` mengubah kebocoran untuk pengguna CJK dari "kesamaan/frekuensi" menjadi **pemulihan isi yang praktis**. Ini melampaui model ancaman ADR-124 yang tertulis. Fitur ini opt-in, dan kebocoran posisi untuk token kata sudah ada sebelumnya, sehingga severity-nya MEDIUM, bukan HIGH. Pilihan perbaikan: (a) tabel FTS terenkripsi dengan `detail=none` atau `detail=column` (menghapus posisi; OR-of-terms + bm25 tetap bekerja dengan tf yang lebih kasar), **atau** (b) jangan meng-index unigram (kueri CJK satu karakter tidak didukung di mode terenkripsi), **dan** (c) amandemen ADR-124 yang menyatakan kebocoran posisi/urutan secara jujur. Minimal (c) sebelum merge.

### 3. #261 (`6066258`, di atas #217)

| Item | Penilaian |
|---|---|
| EC-STATE-001 | `QueryContext` dicoba ulang ≤ 3× hanya untuk `SQLITE_IOERR` (2/4 ms, menghormati ctx); jalur tulis tidak disentuh ✔ |
| EC-STATE-007 | `ClassifyIntegrity` → `ErrFTSCorrupt` untuk baris `fts5: corruption found` ✔ |
| EC-STATE-009 | `ReindexFTS` keyset-batch + `fts_meta` progres; `ensureFTS` hanya pada open tulis (dilewati bila `ReadOnly`) ✔ |
| **GAP-REL-05** | `ErrMigrationFailed` dibungkus di setiap error migrasi, dan `runHeadless` → **exit 78**. Ini cocok dengan SPEC §11.1/§22.1 GAP-REL-05 ("error migrasi apa pun → … exit 78") dan dengan putusan SEC9-6 (DB lebih baru → penulis exit 1; migrasi gagal → 78) ✔ |
| GAP-INF-01 | `recallExpr`: kueri CJK satu rune → `"x"*` ✔ |
| **GAP-INF-02** | `blob_fts` diisi oleh `reindexBlobs` (text/*, JSON, ≤ 16 MiB; `ReadBlob` didekripsi lalu `ftsPlain` → **`encFTS` saat terenkripsi**, tanpa token plaintext) ✔ untuk kerahasiaan. **Namun:** tidak ada trigger/penulis saat `Append`, sehingga blob baru tidak pernah ter-index sampai reindex berikutnya (yang hanya terjadi bila versi norm/layout berubah). Selain itu **tidak ada kode produksi yang men-query `blob_fts`**; hanya `TestFTSBlobIndex` (memanggil `ReindexFTS` secara eksplisit) dan benchmark. Fiturnya tidak tersambung → SEC7-66 |
| Redaksi | Index berasal dari byte blob yang **tersimpan**, jadi tidak ada jalur di luar redaksi yang sudah berlaku saat blob ditulis. Di plaintext, blob di disk juga plaintext, sehingga eksposurnya sama. Kebocoran posisi/unigram dari SEC7-64 juga berlaku untuk `blob_fts` terenkripsi |
| GAP-REL-03 | `TestChaos50Clients` (PR 2 s, nightly 30 m), target `chaos-nightly`, job nightly, meta-test `TestNightlyChaosWired` ✔ |

**Budget (SEC7-65):** `make loc` di `6066258`: **`LOC GAGAL ctxdb 5008 > 4700`**. Di #217 `b9449c1` angkanya `ctxdb 4682/4700`; #261 menambah ~326 baris `ctxdb` (total 376 non-test). `loc.budget` (`ctxdb 4700`) dan Total core (**41.830**) tidak diubah, sehingga klaim "Total core tetap karena masih dalam budget" **salah**. Laporan worker hanya mengutip baris `LOC ok TOTAL 37593 / 190000`. Perlu kenaikan `ctxdb` (≥ 5010) plus amandemen ADR dan Total core di ENGINEERING.

Gate di `6066258`: `go test -race -tags goolm -count=1 -run 'StateEdge|Migration|FTS|Chaos|Recall|Acceptance|Reindex|Blob|Encrypted|Skill' ./internal/ctxdb ./internal/integration` → ok; `internal/archtest` ok; `make loc` **GAGAL** (`ctxdb`).

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC7-62 | HIGH | **fixed** `8eb9583` | — | — |
| SEC7-63 | MINOR | **fixed** `cd8628e` | — | — |
| **SEC7-64** | MEDIUM | open | Unigram CJK + `detail=full` (posisi) di index FTS terenkripsi = sandi substitusi per karakter, sehingga isi CJK dapat dipulihkan dengan analisis frekuensi. Ini melampaui ADR-124 ("bukan isi"). Hal serupa, lebih lemah, berlaku untuk token kata. | `detail=none`/`column` untuk index terenkripsi, atau tanpa unigram. Minimal amandemen ADR-124 yang menyatakan kebocoran posisi/urutan dan risiko CJK, diputuskan owner. |
| **SEC7-65** | MEDIUM (CI) | confirmed | #261: `ctxdb 5008 > 4700`; Total core tidak dinaikkan. | Naikkan `ctxdb`, Total core, dan amandemen ADR (ADR-158..161). |
| **SEC7-66** | MEDIUM | confirmed (statis + test) | GAP-INF-02 tidak tersambung: `blob_fts` tidak di-update saat `Append` dan tidak dibaca oleh `Search`/recall. Ledger F2-ACCEPTANCE menandainya selesai. | Index blob saat append (trigger/penulis di jalur `PutBlob`+`Append`) dan sertakan `blob_fts` di `Search`/recall dengan filter exclusion/purge yang sama. Atau tandai item ini belum selesai di ledger. |
| SEC7-67 | info | — | `keyedFTSExpr` menambahkan `*` pada HMAC; prefiks atas hash 64-hex tidak bermakna tetapi tidak berbahaya. | Opsional: buang `*` di mode terenkripsi. |

**Ringkasan Round 13:** SEC7-62/63 tertutup, dan pilihan batas History (baris mentah, terdokumentasi, teruji) dapat diterima. #201/#217/#241 APPROVE, dengan catatan SEC7-64: unigram CJK di index terenkripsi berposisi memungkinkan pemulihan isi CJK, melebihi ADR-124, sehingga perlu keputusan owner (amandemen ADR atau `detail=none`/tanpa unigram). #261 benar secara semantik untuk EC-STATE-001/007/009, exit 78 sesuai SPEC, dan `blob_fts` terenkripsi memakai HMAC (bukan plaintext, tanpa bypass redaksi). Namun `ctxdb` melampaui budget (5008 > 4700, Total core tidak dinaikkan) dan `blob_fts` tidak pernah ditulis saat append maupun di-query (GAP-INF-02 belum tersambung) → REQUEST CHANGES.
