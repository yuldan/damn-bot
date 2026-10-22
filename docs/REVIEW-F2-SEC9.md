# REVIEW-F2-SEC9 — Review keamanan probe engine SQLite + mode baca-saja (GAP-REL-04/05, ADR-132)

Review independen untuk #203 `rel04-sqlite-engine` `0c6cff3` (basis `main` `68dbec3`): `ctxdb.ProbeEngine`/`damnbot --version --verbose`, `Options.ReadOnly` (DSN `mode=ro` + `query_only`), dan pemetaan `ErrSchemaNewer` → exit 78 di `run`. Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

## Round 1 — 2026-10-21

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #203 engine probe + read-only | `0c6cff3` | **REQUEST CHANGES**: SEC9-1 (MEDIUM: open baca-saja membuat direktori dan gagal di direktori read-only), SEC9-5 (MINOR: kontrak exit 78 bertentangan dengan tabel error SPEC), SEC9-2/3/4 (MINOR/info) |

Gate di `0c6cff3`:
- `go test -race -tags goolm -count=1 -run 'ReadOnly|Engine|SQLite|Version|SchemaNewer|Exits78|Migrat' ./internal/ctxdb/ ./cmd/damnbot/` → **ok**.
- `go vet -tags goolm ./...` + `GOOS=windows|darwin|freebsd go vet ./internal/ctxdb ./cmd/damnbot` → bersih.
- `make deadcode` → 0 fungsi tak terjangkau.
- `make loc`: `ctxdb 3533 / 3700`. Jumlah `loc.budget` = Total core ENGINEERING (36.970) ✔.
- Binary: `damnbot --version --verbose` → `sqlite 3.53.4 fts5=true contentless_delete=true trigram=true json=true supported=true`, exit 0.

Probe `TestZZReadOnly` dan `TestZZDSN` (`internal/ctxdb`, DB nyata; snapshot isi + mtime semua file data dir sebelum open, selama open, dan setelah close) dijalankan lalu dihapus.

### Probe mode baca-saja

| Skenario | Hasil |
|---|---|
| DB skema lebih baru (`user_version = 9999`, writer sudah ditutup) | Open RO ✔, `SELECT` ✔; `Write` → `ErrReadOnly` ✔. Writer → `ErrSchemaNewer` ✔ |
| SQL tulis lewat read pool: `INSERT … RETURNING`, `PRAGMA user_version = 1` | `attempt to write a readonly database` ✔. Trigger tidak bisa berjalan karena setiap tulis ditolak di lapisan SQLite. |
| `PRAGMA journal_mode = DELETE` / `PRAGMA wal_checkpoint(TRUNCATE)` | ditolak (`disk I/O error` 3850 / 778) ✔ |
| **Hot WAL**: salinan `damnbot.db` + `-wal` berisi frame yang belum di-checkpoint, tanpa `-shm` | Open RO ✔, frame WAL terbaca. **File DB utama tidak berubah** (hash sama, tanpa checkpoint/recovery tulis) ✔. SQLite membuat `-shm` 32 KiB (wal-index di-rebuild). |
| DB bersih tanpa sidecar | SQLite membuat `-shm` (32 KiB) + `-wal` (0 B); isi `-shm` berubah setelah close. File DB utama tidak berubah ✔ (sesuai catatan konsekuensi ADR-132) |
| `VACUUM INTO '<tmp>/vac.db'` / `ATTACH '<tmp>/att.db'` lewat read pool | ditolak/gagal, **tetapi file tujuan tetap dibuat** → SEC9-3 |
| Data dir **belum ada** | Open RO gagal (`unable to open database file`) ✔, tetapi **data dir + `blobs/`, `blobs/tmp`, `blobs/.trash`, `run/`, `run/maint.holders/`, `backups/` dibuat** → SEC9-1 |
| Data dir **read-only** (dir 0500, file 0400; hot WAL) | **Open RO gagal**: `mkdirat blobs: permission denied` → SEC9-1 |
| Data dir dengan `#` atau `?` di nama (`<base>/a#b`) | Writer membuat file DB **di `<base>/a`** (di luar data dir) lalu gagal. Open RO `a#b` **berhasil membaca `<base>/a`**, padahal `mode=ro`/`query_only` terbuang bersama fragment → SEC9-4 |

### Probe engine (REL-04)

`ProbeEngine` membuka `:memory:` (tidak menyentuh data dir ✔), membaca `sqlite_version()`, lalu menjalankan probe DDL FTS5, `contentless_delete=1`, `tokenize='trigram'`, dan `json_valid('{}')`. `Supported()` = versi ≥ `MinSQLiteVersion` 3.51.3 (perbandingan semver numerik, bukan string ✔) + keempat probe. Engine yang ter-link: 3.53.4.

### Exit `schema_newer`

`runHeadless` memetakan `errors.Is(err, ErrSchemaNewer)` → **exit 78**, setelah mencetak `run: … schema_newer` ke stderr (`TestRunSchemaNewerExits78`). Pemetaan ini cocok dengan SPEC §22 baris GAP-REL-05 ("DB lebih baru dari binary → proses tulis menolak start (exit 78)"), catatan resolusi REL-05 di §22, dan SELF-HEALING §1.2 (78 = `EX_CONFIG`, fatal tanpa restart). Namun ada dokumen yang bertentangan → SEC9-5.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC9-1 | **MEDIUM** | confirmed | **Open baca-saja memodifikasi disk dan gagal di media read-only.** `Open(ReadOnly)` tetap memanggil `newStore` → `openRoot`, yang menjalankan `os.MkdirAll(dir)` lalu `MkdirAll` untuk `blobs/tmp`, `blobs/.trash`, `run/maint.holders`, dan `backups`. (a) Data dir yang tidak ada **dibuat** (beserta layout) sebelum open DB gagal; `TestReadOnlyMissingDB` hanya memeriksa error. (b) Inspeksi DB di direktori read-only (mount RO, salinan forensik, dir milik user lain dengan izin baca) **gagal total** (`mkdirat blobs: permission denied`), padahal itu kasus utama REL-05. Ini bertentangan dengan ADR-132 butir 3 ("tidak membuat lock/holder"; `run/maint.holders/` tetap dibuat). | Jalur RO: `os.OpenRoot(dir)` tanpa `MkdirAll` (dir hilang → error), tanpa membuat subdirektori. Tambahkan test: dir hilang tetap tidak ada setelah open, dan dir 0500 + DB 0400 dapat dibaca. |
| SEC9-2 | info | confirmed | Pada DB WAL, SQLite membuat `-shm` (dan `-wal` 0 B) saat dibaca dengan `mode=ro` dan mengubah isi `-shm`. File DB utama tidak pernah berubah, termasuk pada hot WAL (tidak ada checkpoint/rollback; `journal_mode`/`wal_checkpoint` ditolak). Hal ini sudah dinyatakan di konsekuensi ADR-132. | Tidak perlu perubahan. Bila nanti dibutuhkan inspeksi tanpa jejak sama sekali (forensik), pertimbangkan salinan snapshot atau `immutable=1` khusus untuk file yang tidak sedang dipakai. |
| SEC9-3 | MINOR (defense-in-depth) | confirmed | `Store.Count`/`get` menerima SQL bebas. Pada read pool RO, `VACUUM INTO '<path>'` dan `ATTACH '<path>'` tetap **membuat file baru** di path sembarang, walaupun statement-nya gagal atau ditolak `query_only`. Saat ini semua SQL adalah konstanta kode, jadi tidak dapat dieksploitasi. Namun perintah inspeksi berikutnya (`sessions export`/`doctor`) tidak boleh meneruskan SQL atau nama file dari pengguna. | Dokumentasikan invarian "tanpa SQL pengguna" di ADR-132. Opsional: authorizer yang menolak `SQLITE_ATTACH`, atau `SQLITE_LIMIT_ATTACHED = 0` pada koneksi RO. |
| SEC9-4 | MINOR (pra-ada; memperlemah RO) | confirmed | DSN dibangun dengan konkatenasi string `"file:" + DBPath(dir) + "?…"` tanpa escape (`store.go:129` RO, `:226` writer). Path data dir yang mengandung `#` atau `?` di-parse SQLite sebagai fragment/query. Akibatnya: writer membuat DB di path terpotong (`<base>/a`, di luar data dir), dan open RO pada `a#b` membaca file lain itu **tanpa `mode=ro` dan `query_only`** (parameter ikut terbuang). Karena path berasal dari operator, dampaknya rendah, tetapi jaminan RO bergantung sepenuhnya pada parameter DSN ini. | Bangun URI dengan `url.URL{Scheme: "file", Path: …}` / `url.PathEscape` (escape `?`, `#`, `%`), atau tolak data dir yang mengandung karakter tersebut. Tambahkan test data dir `a#b`. |
| SEC9-5 | MINOR (spec) | static | **Kontrak exit 78 untuk `run` bertentangan dengan tabel error SPEC.** Mendukung: SPEC §22 GAP-REL-05 ("proses tulis menolak start (exit 78)"), resolusi REL-05 di §22, SELF-HEALING §1.2. Bertentangan: tabel error §4.3 baris `schema_newer` ("TUI/ACP/`run`/CLI masuk read-only + notice", **exit 1**), aturan prioritas exit CLI tepat di bawahnya ("error sebelum logika perintah … memakai exit tabel ini untuk perintah apa pun"), kontrak `run` 0/2 (§7.2), dan §11.11.7 (TUI/ACP/`run` lama → read-only + notice). ADR-132 butir 4 mengutip §11.11.7 untuk exit 78, padahal §11.11.7 tidak menyebut 78 untuk `run`. Arah perubahan (78 = fatal, jangan restart/retry) masuk akal untuk proses tulis, tetapi skrip yang mengikuti tabel §4.3 akan menerima kode yang tidak terdokumentasi. | Amandemen SPEC §4.3 (`schema_newer` → 78 untuk proses tulis; "read-only + notice" hanya untuk inspeksi), §7.2 (`run` 0/2/78), dan §11.11.7, atau ADR-132 menyatakan secara eksplisit baris mana yang digantikan. Perbaiki kutipan di komentar `run.go` dan ADR-132 butir 4. |

Catatan non-blocking:
- Store RO tidak memeriksa marker pemeliharaan dan tidak memegang `maint.lock` bersama. Restore/purge tidak menunggu pembaca inspeksi, dan pembaca dapat terus membaca inode lama setelah swap DB. [INFERENCE] Di Windows, handle terbuka dapat menghambat rename/hapus file DB oleh restore. Dokumentasikan atau cek marker saat open.
- Budget `ctxdb` naik 3.500 → 3.700 (+200), padahal terukur 3.533 (+33). Stack kripto (#194/#198/#201) juga menaikkan baris `ctxdb` dan Total core, sehingga PR yang di-merge belakangan harus menyesuaikan jumlah `loc.budget` = Total core.

**Ringkasan:** #203 **REQUEST CHANGES**: SEC9-1 (MEDIUM) harus diperbaiki agar inspeksi benar-benar baca-saja dan bekerja di direktori read-only, dan SEC9-5 (dokumen exit code) direkonsiliasi. Jaminan inti terpenuhi: tidak ada tulis ke DB, tidak ada migrasi, recovery, holder, atau checkpoint, `Write` → `ErrReadOnly`, writer → `ErrSchemaNewer`/exit 78, dan probe engine benar. SEC9-3/4 MINOR.

## Round 2 — 2026-10-21

#203 `69a88cd` (di atas `0c6cff3`): `openReadRoot` (`os.OpenRoot` tanpa `MkdirAll`), fallback `immutable=1` bila open `mode=ro` gagal dan tidak ada `-wal` non-kosong, `Count` pada store RO hanya menerima `SELECT`/`WITH` (denylist kata kunci), `dsnPath` meng-escape `%`/`#`/`?`, dan `schema_newer` → exit 1.

**Verdict:** #203 **APPROVE dengan MINOR** (SEC9-1/3/4/5 FIXED; SEC9-6/7/8 MINOR, boleh follow-up).

Gate di `69a88cd`: `go test -race -tags goolm -count=1 ./internal/ctxdb/` → ok (261 s); `-run 'ReadOnly|Engine|SQLite|Version|SchemaNewer|Exits|Migrat|DSN' ./internal/ctxdb ./cmd/damnbot` → ok; `internal/archtest` ok; `go vet` + `GOOS=windows|darwin go vet ./internal/ctxdb` bersih; `make loc` `ctxdb 3583/3700`; `make deadcode` 0.

Probe `TestZZSec9R2`, `TestZZImmutable`, dan `TestZZImmutable2` (`internal/ctxdb`, DB nyata; writer `Store` dan koneksi SQLite mentah sebagai "proses lain") dijalankan lalu dihapus.

| Skenario | Hasil |
|---|---|
| Data dir tidak ada | `open …: no such file or directory`; **dir tidak dibuat** ✔ |
| Data dir kosong | `unable to open database file`; tree tetap kosong ✔ |
| Dir 0500 + DB 0400, tanpa writer | terbaca (`count=1`); tree sebelum = sesudah (tanpa `-shm`/`-wal` baru) ✔ |
| Writer hidup, `-wal` non-kosong | jalur normal; pembaca melihat commit baru (3 → 4) ✔ |
| Writer hidup, `-wal` 0 B, `-shm` mode 000, dir 0500 | open `mode=ro` normal **berhasil** (fallback tidak dipakai) ✔ |
| Koneksi `immutable=1` (DSN yang sama dengan fallback) + writer hidup | setelah writer commit lalu checkpoint TRUNCATE, koneksi yang sama tetap `count=0` → snapshot basi, tanpa lock → SEC9-8 |
| `Count`: `SELECT 1; ATTACH '<tmp>/a.db' AS a` | ditolak, **file tidak dibuat** ✔ |
| `Count`: `AT/**/TACH …` | syntax error (komentar tidak bisa memecah kata kunci), file tidak dibuat ✔ |
| `Count`: `WITH … INSERT … RETURNING` | ditolak ✔ |
| `Count`: `WITH x AS (SELECT 1) SELECT count(*) FROM x` | diterima ✔ |
| `Count`: `/* c */ SELECT 1`, `-- c\nSELECT 1`, `VALUES (1)`, `… WHERE created_at > 0`, `… WHERE updated_at > 0`, `pragma_table_info('events')` | **ditolak** (false positive) → SEC9-7 |
| Data dir `a#b`, `c?d`, `e%23f` | writer membuat DB **di dalam** dir; open RO membaca DB yang sama (`session S` ada); tidak ada file di luar dir ✔ |
| `run` + DB skema lebih baru | exit **1** + `schema_newer` (`TestRunSchemaNewerExits…`) ✔ |

| ID | Verdict / Severity | Bukti / Temuan | Perbaikan |
|---|---|---|---|
| SEC9-1 | **FIXED** | Lihat tabel: tidak ada dir/layout yang dibuat, dan inspeksi di dir read-only berjalan. | – |
| SEC9-2 | info (tetap) | Pada dir yang bisa ditulis, SQLite tetap dapat membuat `-shm`; file DB utama tidak berubah. | – |
| SEC9-3 | **FIXED** | `ATTACH`/`VACUUM INTO`/DML/`PRAGMA` ditolak sebelum dieksekusi. Statement ganda tetap memuat kata kunci terlarang. Komentar tidak bisa memecah kata kunci SQLite. Tidak ada fungsi pembuat file di build (`load_extension`/`writefile` tidak tersedia). | – |
| SEC9-4 | **FIXED** | `dsnPath` meng-escape `%`, `#`, `?` untuk DSN writer dan RO. | – |
| SEC9-5 | **FIXED** | `run` → exit 1 sesuai tabel §4.3, dan ADR-132 butir 4 diamandemen. | – |
| SEC9-6 | MINOR | SPEC §22.1 baris GAP-REL-05 masih menulis "DB lebih baru dari binary → proses tulis menolak start (**exit 78**)", dan `MITIGATION-MATRIX` GAP-REL-05 menyebut "exit 78". Keduanya kini bertentangan dengan §4.3 dan ADR-132 butir 4 (exit 1). | Ubah kedua baris (exit 78 hanya untuk `serve`/gate start sesuai SELF-HEALING §1.2). |
| SEC9-7 | MINOR | Denylist berbasis substring di seluruh query menolak query baca yang sah: kolom `created_at`/`updated_at` (mengandung `CREATE`/`UPDATE`), `deleted…`, `replace(…)`, `pragma_table_info(…)`, `VALUES`, dan query yang diawali komentar. `doctor` akan memakai kolom-kolom ini. | Ganti dengan pemeriksaan statement: siapkan dengan `sqlite3_stmt_readonly` (modernc: `Stmt` readonly) + tolak bila tail tidak kosong (satu statement), atau authorizer yang menolak `SQLITE_ATTACH`/`SQLITE_PRAGMA` tulis. Minimal: cocokkan kata kunci sebagai token (`\bATTACH\b`), bukan substring. |
| SEC9-8 | MINOR | **Fallback `immutable=1` dipicu oleh error apa pun** pada open `mode=ro`, asalkan tidak ada `-wal` non-kosong. Syarat ini tidak membuktikan "media read-only tanpa writer": writer lain bisa hidup dengan `-wal` 0 B (setelah checkpoint TRUNCATE atau sebelum tulis pertama). Probe menunjukkan koneksi `immutable` tidak pernah melihat commit/checkpoint berikutnya. Koneksi itu juga tanpa lock, sehingga checkpoint writer yang menulis ulang halaman DB selama pembaca membaca dapat menghasilkan hasil sobek/`malformed`. Pool (≤ 4 koneksi) bertahan sepanjang umur store. Dalam probe, jalur normal tetap berhasil (fallback tidak terpicu), jadi ini bukan eksploit yang terkonfirmasi. | Batasi fallback ke error `SQLITE_CANTOPEN`/`SQLITE_READONLY` **dan** dir tidak bisa ditulis (`access(W_OK)` gagal) **dan** tidak ada holder hidup (`run/maint.holders` kosong / `maint.lock` tidak terkunci). Dokumentasikan semantik snapshot di ADR-132. |

**Ringkasan:** #203 **APPROVE dengan MINOR**. Jaminan baca-saja kini terpenuhi tanpa jejak layout. Escape DSN dan exit code benar, dan `Count` memblokir pembuatan file. Sisa: SEC9-6 (dua baris dokumen exit 78), SEC9-7 (false positive filter `Count`), dan SEC9-8 (syarat fallback `immutable` terlalu longgar).
