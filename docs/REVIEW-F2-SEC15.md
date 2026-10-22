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

**Ringkasan:** #264 **REQUEST CHANGES**. Mekanismenya benar: hanya lewat mode purge, trigger append-only utuh, batch dengan jeda, dan ditolak saat purge aktif. `--yes` diwajibkan, dry-run tidak menghapus, dan default `prune` hanya menghapus aksi yang memang sudah tidak dapat di-undo. Namun maintenance tidak menghormati lease atau turn yang sedang berjalan (SEC15-1), dapat dijalankan model sendiri lewat tool `shell` sehingga `deny_paths` data dir terlewati (SEC15-2), dan e2e belum memiliki kontrol negatif (SEC15-3).
