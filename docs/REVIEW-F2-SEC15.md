# REVIEW-F2-SEC15 — Review keamanan `checkpoints status|list|prune|clear|clear-legacy` (ADR-146)

Review independen singkat untuk #264 `f2-checkpoints` `5f56e3b` (satu commit di atas `f2-sessions-prune` `e3430ea`). Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Aturan owner berlaku: e2e + kontrol negatif. Tanggal: 2026-10-22.

## Round 1 — 2026-10-22

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #264 checkpoints CLI | `5f56e3b` | **REQUEST CHANGES**: SEC15-1 (MEDIUM: `clear`/`prune` berjalan saat sesi memegang lease dengan turn terbuka), SEC15-2 (MEDIUM: model dapat menjalankan `damnbot checkpoints clear --yes` lewat tool shell tanpa gate), SEC15-3 (MEDIUM, aturan owner: e2e tanpa kontrol negatif), SEC15-4 (MINOR) |

Gate di `5f56e3b`: `go test -race -tags goolm -count=1 -run Checkpoint ./cmd/damnbot ./internal/ctxdb ./internal/e2e` → ok; `internal/archtest` ok; `make loc` `ctxdb 4179/4250`; `GOOS=windows go vet` bersih. Belum ada check CI pada PR ini.

### Desain

`PruneCheckpoints`/`ClearCheckpoints` → `deleteCheckpoints`: batch ≤ 500 aksi per transaksi `modePurge` (`dmb_maint()='purge'`, yaitu pengecualian yang sudah ada di trigger append-only; **trigger tidak diubah**), jeda 110 ms antar batch, dan **ditolak bila purge sesi aktif** (`module_state` `purge` → `ErrPurgeInProgress`). Yang dihapus: `action_undos`, `blob_refs` milik aksi, dan `actions`. Event transkrip dan baris `blobs` dipertahankan; file blob direklamasi oleh `doctor gc` lewat refcount, sehingga blob yang juga dirujuk event tidak hilang. `status`/`list` memakai `openSessionsDBReadOnly`.

Probe `TestZZSec264` (`cmd/damnbot`, dihapus setelah dijalankan):

| Skenario | Hasil |
|---|---|
| Sesi `S` memegang **lease** (`AcquireLease`) + turn **terbuka** (`StartTurn`) di "proses writer"; `checkpoints clear --yes` dijalankan | **exit 0 `cleared 1 checkpoints`**, `actions` = 0 → SEC15-1 |
| `prune --dry-run` | stdout `would prune 0 …`; hash seluruh tree data dir sebelum = sesudah (tidak ada tulisan yang teramati) ✔ |
| Classifier (bash, `deny_paths` = data dir): `damnbot checkpoints clear --yes` | **mutating**, unsafe = **auto**, auto+allow = **auto** → SEC15-2 |
| sama: `damnbot sessions purge S --yes` | mutating, auto (masalah yang sama, pra-ada) |
| kontrol: `rm -rf <data dir>` | **denied_path** |

### Jawaban

- **Hanya lewat mode purge, trigger utuh:** ya. Penghapusan hanya lewat `s.tx(ctx, modePurge, …)`; tidak ada `DROP TRIGGER` maupun perubahan trigger.
- **Dapat dipicu model lewat tool?** **Ya** (SEC15-2). Tidak ada tool internal yang memanggil `PruneCheckpoints`/`ClearCheckpoints`, tetapi tool `shell` dapat menjalankan binary `damnbot`. Classifier tidak mengenali subperintah pemeliharaan Damn-Bot, sehingga `damnbot checkpoints clear --yes` = `mutating` dan auto di `--unsafe-auto-shell` atau dengan `shell_allow`. Ini **melewati `deny_paths`** yang melindungi data dir: DB diubah oleh proses `damnbot` itu sendiri, bukan lewat operand path. `--yes` tidak melindungi apa pun karena model ikut menulis flag itu. Akibatnya model dapat menghapus kemampuan `undo` atas perubahannya sendiri.
- **Saat sesi memegang lease atau turn sedang berjalan?** **Ya** (SEC15-1). Tidak ada pemeriksaan lease, holder hidup, atau `turn_journal` running. `clear` (dan `prune --older-than` kecil) menghapus aksi turn yang sedang berjalan, dan dapat berpacu dengan `damnbot undo`, yang mengambil lease sesi lalu membaca aksi dan menulis `action_undos` atas aksi yang baru saja dihapus.
- **Menghapus pre-image yang undo-nya masih diizinkan?** Dengan default **tidak**: cutoff = `journalstore.DefaultRetentionDays`, horizon yang sama dengan status `Expired` di `journal.Undo` (aksi kedaluwarsa memang ditolak undo). Namun `--older-than <d>` dengan `d` < retensi dan `clear` **sengaja** menghapus aksi yang masih dapat di-undo. Untuk pengguna yang mengonfirmasi `--yes` ini dapat diterima, asalkan bukan model (SEC15-2) dan bukan sesi yang sedang aktif (SEC15-1). Catatan: `T_cut` SPEC (§13, retensi 30 hari **atau** 2 GiB) bisa lebih baru daripada cutoff waktu, sehingga default CLI justru lebih konservatif.
- **`clear` mewajibkan `--yes`?** Ya: `clear`, `clear-legacy` (bila ada dir), dan `prune` non-dry-run menolak dengan exit 2 tanpa `--yes`. Hal ini dikunci `TestCheckpointsPruneClear` ✔.
- **Dry-run tidak pernah menulis?** Tidak ada penghapusan, dan tree tidak berubah di probe ✔. Namun `prune --dry-run` membuka DB lewat `openSessionsDB` (open **tulis**: migrasi, recovery, holder, `ensureFTS`), berbeda dengan `status`/`list` yang read-only. Pada DB yang butuh migrasi/recovery, dry-run dapat menulis (SEC15-4).
- **`clear-legacy`:** hanya `<data>/checkpoints` dan `<config>/checkpoints` yang berupa direktori; `os.RemoveAll` tidak mengikuti symlink (link yang dihapus, bukan targetnya) ✔.

### Aturan owner

E2E `TestCheckpointsCLI` (binary nyata, harness #239) menjalankan `status` → `list` → `prune --dry-run` → `prune --yes` → `status` (0) → `clear-legacy`. **Tidak ada kontrol negatif di level e2e**: penolakan tanpa `--yes` yang tidak menghapus apa pun, dan `prune` default yang tidak menyentuh aksi baru. Kedua kontrol itu hanya ada di unit (`TestCheckpointsPruneClear`). Tidak ada pula e2e untuk `clear --yes` → SEC15-3.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC15-1 | MEDIUM | confirmed (probe) | `prune`/`clear` tidak menghormati lease sesi maupun turn terbuka: aksi turn yang sedang berjalan ikut terhapus, dan dapat berpacu dengan `undo` yang memegang lease. | Di setiap batch (di dalam tx `modePurge`): lewati aksi milik sesi dengan lease aktif atau `turn_journal` running, **atau** tolak dengan `session_busy` (exit 4). Tambahkan test dengan lease + turn terbuka. |
| SEC15-2 | MEDIUM | confirmed (probe classifier) | Model dapat menjalankan `damnbot checkpoints clear --yes` (juga `sessions purge`) lewat tool `shell`: `mutating` → auto di unsafe atau dengan allow, sehingga `deny_paths` data dir terlewati karena mutasi terjadi di dalam proses `damnbot`. | Dua lapis: (a) CLI menolak subperintah pemeliharaan destruktif bila berjalan sebagai anak shell Damn-Bot (runner menyetel env marker/`DAMNBOT` untuk child; tolak dengan pesan "jalankan dari terminal"); (b) classifier menandai `damnbot <sessions purge|prune|restore, checkpoints prune|clear|clear-legacy, undo --force>` sebagai `Unknown`/`Destructive` (tidak pernah auto). Tambahkan test classifier plus e2e: perintah di dalam tool shell → ditolak, dengan kontrol dari terminal → berhasil. |
| SEC15-3 | MEDIUM (aturan owner) | open | E2E tanpa kontrol negatif dan tanpa `clear --yes`. | Di `TestCheckpointsCLI`: `prune`/`clear` tanpa `--yes` → exit 2 dan `status` tetap 1; `prune` default tidak menghapus aksi baru; `clear --yes` → 0. |
| SEC15-4 | MINOR | static | `prune --dry-run` membuka DB dalam mode tulis. | Gunakan `openSessionsDBReadOnly` untuk dry-run (dan untuk `CheckpointStats` sebelum konfirmasi). |

**Ringkasan:** #264 **REQUEST CHANGES**. Mekanisme utama benar: hanya lewat mode purge, trigger append-only utuh, batch dengan jeda, dan ditolak saat purge aktif. `--yes` diwajibkan, dry-run tidak menghapus, dan default `prune` hanya menghapus aksi yang memang sudah tidak dapat di-undo. Namun maintenance tidak menghormati lease atau turn yang sedang berjalan (SEC15-1), dapat dijalankan model sendiri lewat tool `shell` sehingga `deny_paths` data dir terlewati (SEC15-2), dan e2e belum memiliki kontrol negatif (SEC15-3).

## Round 2 — 2026-10-22 (recheck SEC15-2: #265 `9415d72`)

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #265 klasifikasi perawatan destruktif | `9415d72` | **REQUEST CHANGES**: SEC15-2 baru tertutup untuk ejaan yang dikenali. **SEC15-5 (MEDIUM, blocking)** `exec -a NAME` / `env -a NAME` / `env --argv0 NAME` membuat program sebenarnya (`damnbot`) tidak diklasifikasi → `mutating` → auto di `--unsafe-auto-shell` dan via `shell_allow`. SEC15-6/7/8 (MINOR) residual. |

Gate lokal: probe `TestZZSec265Probe` (`internal/exec/classify`, dihapus setelah dijalankan) di `9415d72`; `make loc` → `exec/classify 1894 / 1894`, `TOTAL 33772 / 190000` (ADR-167 benar, tetapi tepat di batas tanpa headroom).

### 1. Yang terverifikasi benar

Semua bentuk berikut menghasilkan `destructive` dan **tidak** `AutoApprove` di kelima mode (`TestDamnbotMaintenanceNeverAuto` juga menutup ini di level unit):

- `damnbot sessions purge s --yes`, `--yes=true`, `-y`, `damnbot --yes sessions purge s`, `damnbot -v sessions purge s --yes`;
- `damnbot checkpoints clear --yes`, `damnbot sessions backup restore f --yes`, `damnbot doctor gc`, `damnbot setup --uninstall --purge-data`, `damnbot update --rollback --data`;
- `damnbot --profile prod …` dan `--profile=prod …` (nilai flag dilewati dengan benar);
- `yes | damnbot sessions purge`, `damnbot sessions purge < file`, `FOO=1 damnbot …` (aturan verb tidak bergantung `--yes`);
- wrapper: `sudo`, `command`, `timeout`, `nohup`, `bash -c '…'` (nested), `env FOO=1 …`;
- cmd: `call damnbot …`, `cmd /c damnbot …`, `damnbot.exe …`; PowerShell: `& 'C:\tools\damnbot.exe' …`, `powershell -Command "damnbot …"`;
- path: `./damnbot`, `/usr/local/bin/damnbot`, nama huruf besar (Linux) — `cmdName`+`isDamnbotName` menormalkan base name/`.exe`/`.cmd`/`.bat`.

Bentuk dinamis menghasilkan **Unknown** (bukan Mutating), sesuai kriteria owner: `$(which damnbot) …`, `` `which damnbot` … ``, `D=damnbot; $D …`, `& $exe …` (Pwsh), `& (Get-Command damnbot) …`, `python3 -c '…'`. `exec -a x /usr/local/bin/damnbot …` menandai `outside_read` sehingga tidak auto. `--yes` di `shell_allow` pun tidak menolong karena `allowAuto` mensyaratkan `Class == Mutating`.

### 2. SEC15-5 (MEDIUM, blocking) — wrapper argv0 melewati klasifikasi

`skipOpts` (`internal/exec/classify/wrap.go:207-220`) melewati opsi apa pun, tetapi hanya menghabiskan **nilai** opsi yang terdaftar di `wrapperValued`. `exec:-a`, `env:-a`, dan `env:--argv0` tidak terdaftar, sehingga nama yang diberikan ke opsi itu dianggap sebagai **program** dan `damnbot` menjadi argumennya:

| Perintah | Class | Auto di unsafe + allow |
|---|---|---|
| `exec -a x damnbot sessions purge --yes` | **mutating** | **ya** |
| `exec -a x damnbot checkpoints clear --yes` | **mutating** | **ya** |
| `env -a x damnbot sessions purge s --yes` | **mutating** | **ya** |
| `env -a x damnbot checkpoints clear --yes` | **mutating** | **ya** |
| `env --argv0 x damnbot sessions purge s --yes` | **mutating** | **ya** |
| `env --argv0=x damnbot …` | destructive | tidak (bentuk `=` tertangani) |
| rantai `command` / `nohup` / `timeout 5` / `bash -c '…'` | **mutating** | **ya** |

Kedua wrapper benar-benar menyetel argv0 di host ini (`env -a zzz sh`, `env --argv0 zzz sh`, `exec -a zzz sh` → `$0=zzz`), dan CLI `damnbot` memakai `os.Args[1:]`, jadi argv0 tidak mengubah subcommand: purge/clear tetap berjalan. Ini membuka kembali persis jalur yang ditutup SEC15-2 (data hilang tanpa persetujuan di `--unsafe-auto-shell` / `shell_allow`), tanpa prasyarat apa pun (binary ada di PATH). Di tree #265 subcommand maintenance belum ada di CLI, jadi bypass ini menjadi live begitu CLI maintenance (#264/SPEC §10) mendarat; karena #264 dijadwalkan merge, gate ini harus benar sebelum itu.

**Perbaikan:** tambahkan `exec:-a`, `env:-a`, `env:--argv0` ke `wrapperValued`; bila `launcher` menangani `exec`, konsumsi `-a NAME` di sana juga; tambahkan keenam bentuk di atas ke `TestDamnbotMaintenanceNeverAuto`. Untuk pertahanan berlapis (karena alias/copy tidak dapat diklasifikasi; lihat SEC15-6), pertimbangkan penolakan di sisi CLI: perintah maintenance destruktif menolak berjalan bila penanda "dijalankan oleh shell agent" (env yang disuntikkan `EnvPolicy`, seperti SEC10-26) ada.

### 3. SEC15-6 (MINOR) — pengenalan berbasis nama tidak menangkap alias/copy/8.3

Probe: `db sessions purge s --yes`, `./x sessions purge s --yes`, `DAMNBO~1.EXE sessions purge --yes` (cmd), dan `damnbot. sessions purge --yes` (cmd) → **mutating**, auto di unsafe+allow. Membuat symlink/copy dari binary di luar root memicu `outside_read` (tidak auto), jadi jalur symlink butuh satu persetujuan; tetapi alias/fungsi pada **session shell persisten**, atau copy yang sudah berada di workspace/root, tidak terhalang. Nama pendek 8.3 dan trailing-dot adalah bentuk Windows yang belum diverifikasi di Linux. Pertimbangkan aturan berbasis **verb + `--yes`** (mis. positional words `sessions purge|prune|archive|restore`, `checkpoints clear|prune`, `doctor gc` + `--yes` → `Destructive`/`Unknown` tanpa melihat nama binary), atau penolakan di sisi CLI.

### 4. SEC15-7 (MINOR, laten) — flag global bernilai menggeser grup

`damnbotWords` hanya menghabiskan nilai `--profile`. Probe: `damnbot --config /x sessions purge s --yes`, `damnbot -p prod sessions purge s --yes`, `damnbot --data-dir /x sessions purge s --yes` → **mutating**, auto. Di tree #265 sendiri subcommand `sessions`/`checkpoints` belum ada di CLI (`main.go` hanya `run`/`--version`), sehingga semua bentuk maintenance keluar dengan usage; aturan ini adalah pertahanan untuk CLI saat #264/SPEC §10 mendarat. Pada CLI #264 (`main.go` melakukan dispatch bila `args[0]` adalah grup) bentuk flag global juga keluar dengan usage, tetapi begitu ada flag global bernilai, grup akan bergeser. Jika flag bernilai tak dikenal mendahului grup, naikkan `Unknown` alih-alih menggeser grup secara diam-diam.

### 5. SEC15-8 (MINOR, aturan owner) — e2e tanpa kontrol positif

`TestRunDeniesDamnbotMaintenance` memakai entry point `run()` yang sebenarnya (memenuhi syarat e2e) dan assertion-nya tidak vakum (hasil tool harus memuat "denied"). Namun tidak ada kontrol positif di harness yang sama (mis. `echo ok` di `--unsafe-auto-shell` benar-benar auto dan dieksekusi) yang membuktikan harness mencapai tool `shell`; dan bentuk SEC15-5 belum masuk e2e. Kontrol di level unit sudah baik (`TestDamnbotMaintenanceNegative`, `TestDamnbotMaintenanceNeverAuto`).

### Temuan Round 2

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC15-2 | MEDIUM | **sebagian fixed** | Ejaan yang dikenali sudah `Destructive` dan tidak pernah auto; namun lihat SEC15-5. | — |
| **SEC15-5** | **MEDIUM (blocking)** | confirmed (probe) | `exec -a NAME`, `env -a NAME`, `env --argv0 NAME` membuat `damnbot` tidak dikenali → `mutating` → auto di unsafe/shell_allow, membuka kembali SEC15-2. | Daftarkan opsi argv0 di `wrapperValued`; test keenam bentuk; pertimbangkan penolakan sisi CLI. |
| SEC15-6 | MINOR | confirmed (probe) | Alias/fungsi/copy/8.3/trailing-dot tidak dikenali → `mutating` + auto. | Aturan verb+`--yes`, atau penolakan sisi CLI untuk maintenance saat dijalankan shell agent. |
| SEC15-7 | MINOR | confirmed (probe, laten) | Flag global bernilai selain `--profile` menggeser grup → `mutating` + auto (belum reachable di CLI sekarang). | `Unknown` untuk flag bernilai tak dikenal; whitelist flag global. |
| SEC15-8 | MINOR (aturan owner) | open | E2E tanpa kontrol positif; bentuk SEC15-5 belum di-e2e. | Tambah `echo ok` → auto dan bentuk argv0 → denied. |

**Ringkasan Round 2:** #265 **REQUEST CHANGES**. Prinsipnya benar (Destructive tidak pernah auto), kontrol negatif unit ada, dan semua ejaan yang diuji penulis lulus. Tetapi `exec -a`/`env -a`/`env --argv0` membalik peran nama dan program sehingga perintah maintenance yang sama menjadi `mutating` dan **auto-approve** di mode unsafe — persis yang hendak ditutup SEC15-2. Perbaikan kecil (tiga entri `wrapperValued` + test) menutup jalur ini; alias/copy tetap residual yang paling baik ditutup di sisi CLI.

## Round 3 — 2026-10-22 — **PARTIAL** (recheck #264 `211f626`)

Reviewer dihentikan oleh owner saat round ini berjalan; isi di bawah adalah yang sudah terverifikasi. **Tidak ada bagian yang dijalankan setengah lalu disimpulkan.**

**Verdict:** | #264 | `211f626` | **APPROVE dengan MINOR** (partial: CI tidak tersedia; probe/edge belum lengkap) |

Gate lokal yang dijalankan: `go test -tags goolm -count=1 -run 'Checkpoint|Maintenance|Sessions' ./cmd/damnbot ./internal/ctxdb` → **ok**; `go test -run TestCheckpointsCLI -v ./internal/e2e` → **PASS**.

Terverifikasi:
- **SEC15-1 (busy/lease):** `deleteCheckpointBatch` memilih ≤500 kandidat lalu memanggil `checkpointBusy` **di dalam** transaksi `modePurge` sebelum `deleteActions`; query menolak bila sesi kandidat punya baris `turn_journal` **atau** `session_leases.expires_at > clk.BootMs()`. Timestamp lease memang boot-relative (`lease.go`), jadi perbandingannya konsisten. Karena cek dan hapus berada di satu transaksi tulis, tidak ada TOCTOU dengan `StartTurn`/`AcquireLease`. `ErrSessionBusy` → `checkpointsFail`/`sessionsErr` → **exit 4**, konsisten dengan `sessions purge` yang sudah ada.
- **Guard env maintenance:** `maintenanceBlocked` dipanggil sebelum membuka writer untuk `checkpoints prune` (non-dry-run), `clear`, `clear-legacy`, `sessions prune` (non-dry-run), dan `sessions archive`; dry-run tetap diizinkan. Test `TestMaintenanceGuardFromShellTool`/`TestSessionsGuardFromShellTool` memverifikasi perintah destruktif ditolak **dan** `status` tetap `actions: 2` (kontrol bahwa tidak ada mutasi).
- **SEC15-4 (dry-run read-only):** `checkpoints prune --dry-run` dan `sessions prune --dry-run` memakai `openSessionsDBReadOnly` (`Options.ReadOnly`); jalur hapus tetap writer.
- **SEC15-3 (kontrol negatif e2e):** `TestCheckpointsCLI` kini menjalankan `prune` tanpa `--yes` → exit 2 dan `status` tetap 1; default window `would prune 0`; `prune --yes` menghapus; `clear --yes` menghapus aksi kedua; `clear-legacy` "no legacy". Test unit busy memiliki kontrol positif (setelah lease dilepas, `ClearCheckpoints` = 1).

Temuan:
| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC15-9 | MINOR | open | Penolakan busy dan guard env hanya diuji di level unit; e2e biner menutup prune/clear tetapi tidak busy/guard. | Tambah e2e: tahan lease di parent → `checkpoints clear --yes` exit 4 dan `actions` tidak berubah; jalankan perintah dengan `DAMNBOT_EXEC_ID` diset → exit 2 dan state utuh. |
| SEC15-10 | MINOR | open (cross-stack) | `openSessionsStore` membuka DB **tanpa codec**, sehingga pada data dir terenkripsi (`storage.encrypt`) semua subcommand `checkpoints`/`sessions` gagal `ErrEncrypted`. Bukan lubang keamanan, tetapi integrasi dengan stack kripto harus diputuskan saat merge. | Teruskan codec (`runCodec`-style) ke `openSessionsStore`, atau dokumentasikan bahwa CLI maintenance hanya untuk data dir plaintext sampai F2 selesai. |
| — | INFO | — | Penolakan per batch bersifat seluruh-batch: satu sesi kandidat dengan lease hidup memblokir prune hingga sesi itu selesai. Fail-closed dan sesuai ADR-146 butir 8; catat sebagai konsekuensi. | — |

CI: `gh pr checks 264` → "no checks reported on the 'f2-checkpoints' branch"; `gh run list --commit 211f626` kosong. Karena itu klaim platform (Windows/macOS) belum terverifikasi.

## Round 4 — 2026-10-22 — **PARTIAL** (recheck #265 `90bd02b`, SEC15-5..8)

**Verdict (provisional):** | #265 | `90bd02b` | **APPROVE** — semua perbaikan SEC15-5..8 ada secara statis dan diuji penulis; **probe independen belum dijalankan ulang** karena owner menghentikan sesi (lihat catatan). |

Terverifikasi dari diff `9415d72..90bd02b`:
- **SEC15-5:** `wrapperValued` mendapat `env:-a`, `env:--argv0`, `exec:-a`, sehingga nama untuk opsi argv0 dikonsumsi dan kata program sebenarnya (`damnbot`) yang diklasifikasi. Test menambahkan `exec -a`, `env -a`, `env --argv0`, plus rantai `command`/`nohup`/`timeout`/`bash -c`; `TestDamnbotMaintenanceNeverAuto` mencakup semuanya di kelima mode.
- **SEC15-6:** `isDamnbotName` menormalkan trailing dot dan mengenali prefiks 8.3 `damnbo~`; `damnbotCommand` me-resolve argv0 path-like (`./db` → symlink ke binary) dan mengenali base hasil resolve. `TestDamnbotSymlinkAndCopies` menutup symlink dan mendokumentasikan copy bernama arbitrer sebagai keterbatasan statis. Residual (alias/fungsi di PATH, copy nama lain) tetap ada dan didokumentasikan.
- **SEC15-7:** `damnbotWords` menghabiskan nilai `--profile`, `--config`, `--data-dir`, `-p` (termasuk bentuk `=`); test menambahkan ketiga bentuk.
- **SEC15-8:** e2e `TestRunDamnbotMaintenanceGate` memakai `run()` sebenarnya dengan **kontrol positif** (`echo ok` benar-benar auto dan hasilnya "ok") dan bentuk argv0/purge yang harus "denied".
- Budget: `exec/classify 1908/1908` (ADR-167 diperbarui), masih tepat di batas.

**Belum dilakukan:** probe `TestZZSec265Probe` ulang pada `90bd02b` (semua bentuk bypass dari Round 2), dan `make loc` pada head ini. Keduanya harus dijalankan sebelum verdict final jika head ini masuk merge queue.

## Round 5 — 2026-10-22 — recheck #265 `b0b13241` (SEC15-5..8, 8.3, trailing dot)

Lingkup: `9415d72` → `90bd02b` (SEC15-5..8) → `b0b1324` "cover Windows 8.3 and trailing-dot damnbot names". `b0b1324` hanya menambah 4 baris test; penanganan 8.3/trailing dot ada di `90bd02b` (`isDamnbotName`). Round 4 hanya menilai `90bd02b` dari diff dan belum menjalankan probe — ronde ini menjalankan semuanya sendiri pada `b0b13241`.

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #265 klasifikasi perawatan destruktif | `b0b13241` | **REQUEST CHANGES (kecil)**: SEC15-5..8 + 8.3 + trailing dot **terverifikasi**; CI head hijau. Temuan baru **SEC15-11 (MEDIUM)** membuat gate masih bisa dilewati; sisa **SEC15-12 (MINOR, laten)** dan **SEC15-13 (MEDIUM, aturan owner — blocker merge)**. |

### CI pada head

Run `39758364783` (head `b0b13241`): **completed success — seluruh workflow hijau**. `test (ubuntu-latest|macos-latest|windows-latest, -race, 1)` → **success** (windows `make test RACE=-race` success); `changes`, `lint`, `deadcode`, `vet-cross`, `build-matrix`, `loc-budget`, `coverage`, `size`, `supply-chain`, `vuln`, `perf`, `bench-budget`, `bench-parity`, `phase-acceptance`, `fuzz (1..3)`, `test-timing` → success. Sebelumnya run `90bd02b`/`9415d72` **cancelled** (superseded), jadi Round 3/4 memang tanpa bukti CI.

### Verifikasi tiap klaim (independen, bukan dari diff)

| Item | Status | Bukti |
|---|---|---|
| SEC15-5 argv0 wrapper (`exec -a`, `env -a`, `env --argv0`) | **tertutup** | `wrap.go:12` menambah `env:-a`, `env:--argv0`, `exec:-a`. Probe: 6 bentuk → `destructive PPPPDD`; `TestDamnbotMaintenanceNeverAuto` lulus di kelima mode. Host benar-benar menyetel argv0: `env -a zzz sh -c 'echo $0'` → `zzz`, `env --argv0 zzz …` → `zzz`, `exec -a zzz …` → `zzz`. |
| SEC15-6 8.3 + trailing dot | **tertutup** | `isDamnbotName` (`damnbot.go:77-86`): trim trailing `.`, trim `.exe/.cmd/.bat`, akui prefiks `damnbo~`. Probe: `DAMNBO~1.EXE`, `damnbot.`, `damnbot.exe.`, `cmd /c DAMNBO~1.EXE`, `& 'C:\tools\DAMNBO~1.EXE'` → semua `destructive PPPPDDD`. |
| SEC15-6 alias/copy nama arbitrer | **residual tetap** | `./db` (symlink → binary) tertangkap (`TestDamnbotSymlinkAndCopies` lulus); copy bernama lain (`./x`) tidak punya identitas statis (ADR-167). |
| SEC15-7 flag global bernilai | **sebagian** | `--profile`, `--config`, `--data-dir`, `-p` (termasuk `=`) dikonsumsi; bentuk bernilai lain → **SEC15-12**. |
| SEC15-8 e2e kontrol positif | **tertutup** | `TestRunDamnbotMaintenanceGate` punya `positive-control` (`echo ok` harus benar-benar jalan) + 4 kasus `denied`; PASS lokal. Tetapi lihat **SEC15-13**. |

### Perintah tes yang dijalankan + hasil

- `nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1 go test -count=1 -v -run 'TestDamnbot' ./internal/exec/classify/` → **PASS** (4/4: Destructive, NeverAuto, Negative, SymlinkAndCopies).
- `… go test -tags goolm -count=1 -v -run TestRunDamnbotMaintenanceGate ./cmd/damnbot/` → **PASS** 5/5 subtest (positive-control 0.12s, purge, exec-argv0, env-argv0, env-argv0-long).
- `make loc` (GOMEMLIMIT=1500MiB, GOMAXPROCS=2, GOFLAGS=-p=1) → **tidak ada `GAGAL`**; `exec/classify 1908/1908`, `TOTAL 33786/190000` (tail lengkap di bawah).
- Probe `TestZZSec15R34`, `…R34b`, `…R34c`, `…R34d`, `…R34e` (ditulis lalu **dihapus**): tabel di bawah.

**Kontrol negatif** (harus tetap diizinkan / tidak salah label):

| Input | Kelas | Keputusan (ask,ask+allow,auto,auto+allow,headless,headless+allow,unsafe,unsafe+allow) |
|---|---|---|
| `damnbot sessions ls` | mutating | `QAQADAAA` |
| `damnbot checkpoints list` | mutating | `QAQADAAA` |
| `damnbot run --yes` | mutating | `QAQADAAA` |
| `damnbot --help` | mutating | `QAQADAAA` |
| `echo ok` | mutating | `QAQADAAA` (benar-benar jalan — dibuktikan e2e `positive-control`) |
| **positif** `damnbot sessions purge --yes` | **destructive** | `PPPPDDDD` |

### Probe SEC15-5..8

| Perintah | Kelas | Keputusan |
|---|---|---|
| `exec -a x damnbot sessions purge --yes` / `… checkpoints clear --yes` | destructive | `PPPPDDDD` |
| `env -a x damnbot sessions purge s --yes` / `env --argv0 x …` / `env --argv0=x …` | destructive | `PPPPDDDD` |
| `command exec -a x damnbot checkpoints clear --yes` | destructive | `PPPPDDDD` |
| `DAMNBO~1.EXE sessions purge s --yes` (Cmd), `& 'C:\tools\DAMNBO~1.EXE' …` (Pwsh) | destructive | `PPPPDDDD` |
| `damnbot. sessions purge s --yes` (Cmd), `damnbot.exe. …` (Pwsh), `cmd /c DAMNBO~1.EXE …` | destructive | `PPPPDDDD` |
| `damnbot --config /x sessions purge s --yes`, `-p prod …`, `--data-dir /x …` | destructive | `PPPPDDDD` |
| `sudo -u root damnbot sessions purge --yes`, `find . -exec damnbot sessions purge --yes ;` | destructive | `PPPPDDDD` |
| `yes \| damnbot sessions purge`, `… < /dev/null --yes`, `(…)`, `{ …; }`, `… && echo ok`, `… \| cat` | destructive | `PPPPDDDD` |
| `echo "sessions purge --yes" \| xargs damnbot`; `… \| xargs -I{} damnbot {} purge --yes` | **unknown** | `PPPPDDDD` (aman) |
| `damnbot $'sessions' purge --yes`, `damnbot ${G}sessions …`, `damnbot "$GROUP" …`, `G=sessions; damnbot $G …` | unknown | `PPPPDDDD` (aman) |

Kontrol negatif & positif semuanya benar ✔; tidak ada dari bentuk ini yang `Mutating`/auto.

### Temuan baru

**SEC15-11 (MEDIUM) — token grup boleh berupa glob; gate `Destructive` terlewat → `mutating` → auto.**

`damnbotWords` (`damnbot.go:41-58`) membandingkan token **mentah** dengan nama grup dan mengabaikan apakah token itu literal atau glob. Karena aturan hanya mengenali `sessions`/`checkpoints`/… persis, token yang di-*expand* shell menjadi salah satu nama itu tidak dikenali — dan perintah tetap `mutating` (bukan `unknown`), sehingga auto di `--unsafe-auto-shell` atau dengan `shell_allow` yang cocok.

| Perintah (Bash) | Kelas | Keputusan |
|---|---|---|
| `damnbot sess* purge --yes` | **mutating** | **`QAQADAAA`** |
| `damnbot [s]essions purge --yes` | **mutating** | `QAQADAAA` |
| `damnbot ses[s]ions purge --yes` | **mutating** | `QAQADAAA` |
| `damnbot ?essions purge --yes` | **mutating** | `QAQADAAA` |
| `damnbot sessio?s purge --yes` | **mutating** | `QAQADAAA` |
| `damnbot checkp* clear --yes` | **mutating** | `QAQADAAA` |
| `damnbot * purge --yes` | **mutating** | `QAQADAAA` |
| kontrol: `damnbot sessions purge --yes`, `"sessions"`, `se''ssions` | destructive | `PPPPDDDD` |
| kontrol aman: `damnbot {sessions,} purge --yes` | unknown | `PPPPDDDD` |

Bukti bahwa argv benar-benar menjadi perintah maintenance (shell mengekspansi glob):

```
$ mkdir work && cd work && touch sessions
$ bash -c 'printf "[%s]\n" damnbot sess* purge --yes'
[damnbot]
[sessions]
[purge]
[--yes]
$ bash -c 'printf "[%s]\n" damnbot [s]essions purge --yes'
[damnbot]
[sessions]
[purge]
[--yes]
```

Stub `damnbot` di `PATH` (echo argv) membuktikan program menerima argv identik dengan bentuk literal: `damnbot sess* purge --yes` → `argv: [sessions] [purge] [--yes]`. Tanpa file `sessions` di cwd, token tetap literal (`[sess*]` → CLI usage).

Prasyarat: shell POSIX (bash/zsh) **dan** satu file di cwd bernama `sessions` (dibuat model lewat `write_file`; auto di `auto-edit`). CLI #264 meng-dispatch pada `args[0] == "sessions"` (`cmd/damnbot/main.go`), jadi begitu #264 merge, `--unsafe-auto-shell`/`shell_allow` menjalankan purge/clear **tanpa approval** dan **melewati `deny_paths`** data dir (mutasi terjadi di dalam proses `damnbot`) — persis jalur yang ditutup SEC15-2. Ini juga tidak terlihat oleh aturan "argv0/command name" karena argv0 tetap `damnbot`.

**Perbaikan:** bila token grup bukan literal (mengandung `*?[` atau tanda kutip tak terurai) → naikkan `Unknown`; **atau** lebih kuat (selaras rekomendasi Round 2 untuk SEC15-6): key aturan pada **verb + `--yes`** — setiap kata posisional `purge|prune|clear|clear-legacy|archive|restore|gc` bersama `--yes` → `Destructive`, tanpa bergantung nama grup. Tambahkan `damnbot sess* purge --yes` (glob) ke `TestDamnbotMaintenanceNeverAuto` + kontrol negatif `damnbot sess* ls`.

**SEC15-12 (MINOR, laten).** `damnbotWords` hanya mengonsumsi nilai `--profile/--config/--data-dir/-p`; flag global bernilai lain menggeser grup: `damnbot --log-level error sessions purge --yes`, `damnbot --format json checkpoints clear --yes`, `damnbot --timeout 5 sessions purge --yes`, `damnbot -o json sessions purge --yes` → semua **mutating `QAQADAAA`** (seharusnya `Destructive`). Belum terjangkau karena CLI sekarang hanya meng-dispatch `args[0]` (`run`/`sessions`/`checkpoints`), sehingga `--flag` di posisi itu keluar usage. Menjadi live begitu ada flag global bernilai sebelum grup. **Perbaikan:** flag `-` bernilai tak dikenal di depan grup → `Unknown`; whitelist flag global.

**SEC15-13 (MEDIUM, aturan owner — blocker merge).** e2e `TestRunDamnbotMaintenanceGate` memanggil `run()` **in-process**, bukan biner asli sebagai anak proses; harness `internal/e2e` belum dipakai. #239 (`025ea0c`) sudah ada di `main` (`affe2d8`), jadi syarat owner "≥1 e2e lewat biner asli" berlaku. **Perbaikan:** satu skenario `internal/e2e` (biner asli) untuk `damnbot sessions purge --yes` / `checkpoints clear --yes` di `--unsafe-auto-shell` → `denied`, dengan kontrol positif, plus bentuk glob SEC15-11.

### Budget `loc.budget` / proyeksi merge

Head `b0b13241`: baris `exec/classify 1908` (ADR-167; Σ core +78 dari `1830`). Terukur `make loc`: **1908/1908**, tepat di batas. **Jika #238 (`exec/classify` → `1841`) merge lebih dulu**, rebase #265 menaikkan total menjadi **1841 + 78 = 1919** (delta kode #265 = 1908 − 1830 = 78); baris `loc.budget` kedua PR berkonflik dan Σ core harus dijumlahkan saat rebase. Perbaikan SEC15-11 sebaiknya tidak menambah LOC Go non-test; kalau perlu, amandemen ADR-167 lagi.

### Verifikasi lokal pada head `b0b13241`

- `go test -count=1 -v -run 'TestDamnbot' ./internal/exec/classify/` → `ok` (4/4 PASS).
- `go test -tags goolm -count=1 -v -run TestRunDamnbotMaintenanceGate ./cmd/damnbot/` → `ok` (5/5 PASS).
- `make loc` → tidak ada `GAGAL`. Tail lengkap:

```
SCC="go tool -modfile=tools/go.mod scc" scripts/loc-check.sh budget
LOC ok    testutil/timing        45 /     80
LOC ok    modules               881 /   1000
LOC ok    config                663 /   1200
LOC ok    exec/classify        1908 /   1908
LOC ok    compaction            788 /    900
LOC ok    sandbox+redact       1676 /   1700
LOC ok    codetools            5295 /   5350
LOC ok    netx                 1519 /   1800
LOC ok    provider             2065 /   2100
LOC ok    analyzers             105 /    400
LOC ok    budget                559 /    560
LOC ok    ctxdb/journalstore    379 /    400
LOC ok    tools                2168 /   2200
LOC ok    testutil               93 /    200
LOC ok    loop                 2037 /   2050
LOC ok    journal               557 /    560
LOC ok    ctxdb/carrierstore    275 /    300
LOC ok    ctxdb                3789 /   3800
LOC ok    ctxdb/compactstore    253 /    260
LOC ok    ctxdb/limitstore       46 /    100
LOC ok    boundary               75 /    150
LOC ok    ctxdb/ledgerstore     116 /    300
LOC ok    exec                 2676 /   3400
LOC ok    schema               1126 /   1200
LOC ok    archtest               69 /    300
LOC ok    ctxdb/memstore        494 /    600
LOC ok    assemble             1236 /   1300
LOC ok    memory+skills        1215 /   1230
LOC ok    ctxdb/skillstore      319 /    400
LOC ok    provider/registry      84 /    300
LOC ok    TOTAL 33786 / 190000
```

**Ringkasan Round 5:** #265 `b0b13241` menutup SEC15-5..8 **dan** bentuk 8.3/trailing dot dengan benar (probe independen + kontrol negatif; CI linux/macOS/windows hijau on head). Yang masih terbuka: **SEC15-11 (MEDIUM)** — token grup berupa glob (`damnbot sess* purge --yes`, `[s]essions`, …) tetap `mutating` sehingga auto di `--unsafe-auto-shell`/`shell_allow`; dengan satu file `sessions` di cwd argv yang diterima benar-benar `sessions purge --yes` (dibuktikan dengan stub + ekspansi bash), jadi begitu #264 merge gate itu bocor. Ditambah SEC15-12 (laten) dan SEC15-13 (e2e biner asli). Budget `1908/1908` penuh; bila #238 merge lebih dulu → **1919**.
