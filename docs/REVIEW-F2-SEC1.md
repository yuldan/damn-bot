# REVIEW-F2-SEC1 — Review keamanan journal & undo stack (#139, #141, #143)

**Status: lengkap — Putaran 1: 2 BLOCKER, 2 MAJOR, 10 MINOR. Putaran 2: JN2-1,2,3,4,5,7,8,9,10(komponen akhir),12,13,15 tertutup; JN2-6/JN2-11/JN2-14 tersisa. Putaran 3 (`#143` `9dd611f`): JN2-14 tertutup (74–81 ms ext4, 29–30 ms tmpfs), batch `UndoLast` diverifikasi; JN2-16 tinggal 2 komentar, JN2-21 residual. Verdict akhir: #139 `approve`, #141 `approve`, #143 `approve`.** Review independen atas stack action-journal + undo F2 (SPEC §18.1, P26, ADR-117). Reviewer tidak menulis kode yang direview; kode produk tidak diubah. Tanggal: 2026-10-21. Pemeriksaan runtime di Linux; Windows/macOS dibaca statis dan di-cross-compile.

Head yang direview (Putaran 1, stacked, belum di-merge): `#139` `3140bb9` (`f2-journal-1`), `#141` `923cff3` (`f2-journal-2`), `#143` `4d5628d` (`f2-journal-3`). Worktree terpisah `~/.cache/damnbot/wt-f2-review` dari `origin/main` `42ad2b1`. Probe ditulis sebagai `TestZZ*` sementara di worktree, dijalankan, lalu dihapus sebelum dokumen ini di-commit.

Batas ukuran per PR (E-84, baris Go non-test +/−): #139 = 462, #141 = 379, #143 = 2 — semuanya ≤ 600. LOC (E-19, hitung setara `scc --no-gen`): `internal/journal` 421/500, `internal/ctxdb/journalstore` 246/400.

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #139 | tipe + `journal.Store` 5 method, `journalstore` (`Record`/`Get`/`List`/`Blob`/`Finish`, retensi, open-turn) | **changes required** — JN2-2 (BLOCKER gate E-79), JN2-3 (MAJOR blob korup fail-open), JN2-7/8/9 |
| #141 | executor undo (`write`/`edit`/`delete`/`move`), fsync/rename, hard link, `Finish` | **changes required — 1 BLOCKER** (JN2-1 symlink dalam root), JN2-4 (MAJOR mode 0000→0644), JN2-5/6/10/11/12/13 |
| #143 | `TestUndoSuite` 200 skenario + refusals + multi-file | **changes required (warisan)** — isi test sendiri approve dan sehat, tetapi head memuat JN2-1/JN2-2 dari #139/#141 sehingga gate #143 merah sampai stack diperbaiki; tambah skenario regresi saat fix |

Ringkasan: **2 BLOCKER, 2 MAJOR, 10 MINOR.** BLOCKER keamanan JN2-1 (undo mengikuti symlink yang dipasang setelah aksi dan menghapus/menimpa file lain di dalam root, tetapi melaporkan `undone`), dan BLOCKER gate JN2-2 (`TestCodeownersComplete` gagal karena `internal/journal` tidak punya baris CODEOWNERS). Keduanya tidak boleh di-soften: stack belum layak merge/wire sebelum diperbaiki.

## Temuan

`confirmed` = dibuktikan dengan probe di worktree; `static` = dibuktikan dari kode/dokumen.

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| JN2-1 | #141 | **BLOCKER** | confirmed | Undo memakai path yang sudah di-*resolve* symlink. Bila path aksi diganti symlink ke file lain **di dalam root** yang isinya sama dengan after-image, konflik tidak terdeteksi: undo create **menghapus target symlink** dan undo write **menimpa target**, sambil melaporkan `undone`. | `undo.go:189-218` (`current` → `resolved`), `:99-139`, `:234-283`, `:285-290`; `sandbox.go:165-197`; probe `TestZZProbeSymlinkInsideRootCreate` → `status=undone detail="removed the file created by the action"`, `q still exists: false`; `TestZZProbeSymlinkInsideRootWrite` → `status=undone`, `q content="old"` |
| JN2-2 | #139+ | **BLOCKER (gate)** | confirmed | `go test ./internal/archtest/ -count=1` gagal `TestCodeownersComplete`: direktori baru `internal/journal` tidak punya baris di `.github/CODEOWNERS`. `origin/main` lulus. PR hanya menjalankan subset `-run TestImportGraph\|TestFileLength\|TestGlobalAllowlist`, sehingga gate E-79 lolos dari PR. | `repometa_test.go:152`; `.github/CODEOWNERS` hanya punya `/internal/ctxdb/` (`:19`) dan `/internal/sandbox/` (`:27`), tanpa `/internal/journal/`; head exit 1, `origin/main` exit 0 |
| JN2-3 | #139 | **MAJOR** | confirmed | Blob before-image **korup** tidak diverifikasi hash-nya. `journalstore.Blob` hanya meneruskan `ctxdb.ReadBlob` yang membaca file tanpa menghitung ulang sha256; undo menulis isi korup ke file dan mencatat `undone`. | `journalstore.go:229-235`; `ctxdb/blob.go:75-96` (tanpa verifikasi); probe `TestZZProbeCorruptBeforeBlob`: blob diganti `"EVIL"` → `status=undone detail="restored the before image" content="EVIL"` |
| JN2-4 | #141 | **MAJOR** | confirmed | Mode `0000` pada before-image diubah menjadi `0644` (`if mode == 0 { mode = 0o644 }`). File yang sengaja tidak dapat dibaca siapa pun menjadi world-readable setelah undo — pelebaran izin senyap. | `undo.go:235-237`; probe `TestZZProbeModeZero`: `mode=-rw-r--r--` (want `----------`) |
| JN2-5 | #141 | MINOR | confirmed | Setuid/setgid tidak kembali: `f.Chmod(mode)` dilakukan **sebelum** `writeAll`, dan kernel Linux membersihkan `S_ISUID`/`S_ISGID` saat write oleh user non-root. Sticky bertahan. | `undo.go:262` (Chmod) vs `:267` (writeAll); probe `TestZZProbeSetuid`: `mode=trwxr-xr-x want=ugtrwxr-xr-x` |
| JN2-6 | #141 | MINOR | static | Temp+rename membuat inode baru milik proses undo; tidak ada `fchown` atau salin xattr/ACL/SELinux/capabilities. Owner dan metadata keamanan berubah senyap, berbeda dari `edit_file` codetools yang mempertahankan owner+xattr. | `undo.go:258-281`; bandingkan `docs/CODE-TOOLS.md:308-314` |
| JN2-7 | #139 | MINOR | confirmed | `blob_sha` pendek/rusak dari inverse JSON menyebabkan **panic** (`slice bounds out of range [:2]`) di `ctxdb.blobName`, bukan `ErrBlob`. Ini melanggar fail-closed dan E-23 (tidak ada panic di library) pada jalur baru. | `ctxdb/blob.go:23`; `journalstore.go:229`; probe `TestZZProbeShortBlobSha`: `PANIC=runtime error: slice bounds out of range [:2] with length 1` |
| JN2-8 | #139 | MINOR | confirmed | Bila aksi **terbaru** sendiri lebih besar dari `max_bytes`, `cutoff` mengembalikan `now`, sehingga aksi itu langsung `expired` dan tidak pernah bisa di-undo — termasuk aksi yang baru saja terjadi. | `journalstore.go:265-276`; probe `TestZZProbeNewestActionOverByteCap`: `bytes_held=6 expired=true` dengan `MaxBytes: 1` |
| JN2-9 | #139 | MINOR | static | `bytes_held` hanya men-*dedup* sha di dalam satu aksi; `cutoff` menjumlahkan kolom per baris, jadi blob yang dipakai bersama K aksi dihitung K kali. Kuota 2 GiB menjadi lebih ketat dari disk nyata (arah fail-closed, tetapi tidak terdokumentasi). | `journalstore.go:97-116` vs `:265-276` |
| JN2-10 | #141 | MINOR | static | `os.Remove`/`os.Rename` memakai path tekstual hasil `Check`; leluhur yang ditukar symlink di jendela itu dapat mengarahkan hapus/rename ke luar root. TRUST-MODEL §3 mengakui kelas ini untuk codetools, tetapi untuk undo dampaknya **hapus file luar root**, bukan hanya memindahkan byte. Tidak ada primitif unlink/rename berbasis dir-fd. | `undo.go:278`, `:285-290`, `:292-303`; `sandbox.go:165-197`; `docs/TRUST-MODEL.md:49` |
| JN2-11 | #141 | MINOR | static | `current` memakai `io.ReadAll` dan `Blob` membaca seluruh file; undo file N byte membutuhkan memori ≥ 2N (hingga ~4N dengan growth). Aksi 2 GiB (masih mungkin sebelum expiry) butuh ~4 GiB RAM; file lebih besar = OOM. | `undo.go:210`; `ctxdb/blob.go:79`; `journalstore.go:230` |
| JN2-12 | #141 | MINOR | confirmed | Undo create pada path yang kini **hard link** menghapus link tanpa `--force`, padahal ADR-117 menyatakan hard link ditolak kecuali `--force`. Dampak terbatas: hanya nama link yang hilang; data di link lain utuh. | `undo.go:118-126` (cabang `Before == nil` langsung `remove`) vs `:245-249`; probe `TestZZProbeHardlinkDeleteNoForce`: `status=undone detail="removed the file created by the action"`, `p_exists=false` |
| JN2-13 | #141 | MINOR | static | `undoMove` hanya me-rename inode dan tidak pernah menerapkan mode before; cabang hard link di `write` juga melewati `Chmod` sepenuhnya. Perubahan mode pada sumber/tujuan tidak dipulihkan. | `undo.go:141-186`, `:245-259` |
| JN2-14 | #143 | MINOR | static | Metrik SPEC §18.1 "undo 50 file ≤ 200 ms" tidak diukur/di-assert di mana pun; `TestUndoSuiteMultiFile` hanya 3 file. `List` melakukan N+1 `Get` dan tiap `Get` memindai `cutoff` atas seluruh `actions`, sehingga `undo N` = O(N·A). | `internal/ctxdb/journalstore/undo_suite_test.go:497-529`; `journalstore.go:194-227`, `:168-192` |

### JN2-1 (BLOCKER) — undo mengikuti symlink dalam root dan menghapus/menimpa file lain

`current(ctx, p)` memanggil `sandbox.Check`, yang sengaja me-*resolve* seluruh komponen symlink dan mengembalikan `resolved` (target). `undoPath` lalu memakai `resolved` untuk `os.Remove` (cabang create) atau `write` (cabang restore). Konflik hanya dibandingkan lewat **isi** target (`sha256`), bukan identitas path/inode. Bila penyerang/agen lain (shell `ln -s`) mengganti `p` dengan symlink ke `q` di dalam root yang isinya sama dengan after-image, maka:

- undo create (`before == nil`) menghapus `q` — file yang tidak pernah menjadi subjek aksi — dan `p` tetap ada sebagai symlink menggantung;
- undo write menulis before-image ke `q`, mengubah file lain, sementara `p` tetap symlink.

Keduanya mengembalikan `Status: undone` dan `Finish` mencatat `undone`; pengguna tidak diberi tahu bahwa file yang dipulihkan berbeda. Ini melanggar invarian inti `Undoer` ("current bytes must equal before (no-op) atau after (restore), selain itu conflict") karena "bytes" yang dibandingkan adalah bytes target, bukan bytes pada path aksi. Symlink yang menunjuk **keluar** root sudah benar ditolak (probe suite lulus); yang bocor adalah symlink di dalam root.

**Rekomendasi:** sebelum menyentuh path, gunakan `os.Lstat` pada path yang direkam dan tolak (`ErrConflict`/`ErrUnsafePath`) bila komponen terakhir adalah symlink atau tipenya bukan regular; idealnya operasikan dengan dir-fd + `openat2(RESOLVE_NO_SYMLINKS)`/`unlinkat(AT_SYMLINK_NOFOLLOW)` sehingga target symlink tidak pernah menjadi sasaran. Tambahkan regresi: create→symlink ke file berisi after-image, dan write→symlink ke file berisi after-image.

### JN2-2 (BLOCKER gate) — `internal/journal` tanpa baris CODEOWNERS

`TestCodeownersComplete` (E-79) mewajibkan setiap direktori `internal/*` punya baris ber-owner eksplisit. `.github/CODEOWNERS` sudah punya `/internal/ctxdb/` dan `/internal/sandbox/`, tetapi PR #139 menambah direktori top-level `internal/journal` tanpa baris. Perintah `go test ./internal/archtest/ -count=1` di head `4d5628d` gagal (`E-79 CODEOWNERS: direktori internal/journal tidak punya baris`), sedangkan perintah yang sama di `origin/main` lulus. Karena gate ini wajib di CI, stack tidak dapat merge apa adanya.

**Rekomendasi:** tambahkan `/internal/journal/ @yuldan` (owner yang sama dengan paket core lain) di `.github/CODEOWNERS`, dan jalankan `go test ./internal/archtest/ -count=1` (bukan subset) di setiap PR stack.

### JN2-3 (MAJOR) — blob before-image korup tidak fail-closed

`Store.Blob` meneruskan sha dari inverse JSON ke `ctxdb.ReadBlob`; `ReadBlob` hanya membaca `blobs/<sha[:2]>/<sha>` dan mengembalikan byte apa pun tanpa memverifikasi `sha256(b) == sha`. Probe menimpa file blob dengan `"EVIL"`: undo menulis `"EVIL"` ke target dan mencatat `undone`. Ini persis kasus "missing/corrupt blob must fail closed": blob hilang sudah benar (`ErrBlob`, `failed`), blob korup tidak.

**Rekomendasi:** verifikasi sha256 di `Store.Blob` (atau `ReadBlob`) dan kembalikan `journal.ErrBlob` bila tidak cocok, sebelum file disentuh. Tambah test korupsi (bukan hanya `os.Remove`) di `TestUndoSuiteRefusals`.

### JN2-4 (MAJOR) — mode `0000` menjadi `0644`

`write` menganggap `mode == 0` sebagai "tidak diketahui" dan memakai `0644`. Padahal `0000` adalah mode legal dan sengaja dipakai untuk file rahasia. Undo file seperti itu melebarkan izin menjadi world-readable. Pengirim `Record` juga bisa mengirim `Mode: 0` tanpa sengaja dan mendapat `0644` diam-diam.

**Rekomendasi:** simpan keberadaan mode secara eksplisit (mis. `Image.ModeValid bool` atau `Mode *fs.FileMode`) dan hanya pakai default bila mode tidak tersedia; tambah skenario `0000`/`0040` ke suite.

### JN2-5 (MINOR) — setuid/setgid hilang karena urutan chmod/write

`f.Chmod(mode)` dijalankan sebelum `writeAll`. Pada Linux, write oleh user non-root membersihkan `S_ISUID` dan `S_ISGID`; probe menunjukkan hasil `trwxr-xr-x` untuk before `04755` (sticky tetap). Klaim ADR-117 bahwa mode "termasuk setuid/setgid/sticky" dipulihkan tidak benar untuk setuid/setgid. Urutan yang benar: tulis → `Sync` → `Chmod` → `Sync`; atau `Fchmod` setelah write dan sebelum rename.

## Diperiksa dan bersih

- **Path di luar root / `deny_paths` / symlink keluar root** ditolak: `Check` memeriksa path leksikal dan hasil resolve; suite `outside-root-refused` dan `symlink-escape-refused` lulus; target di luar root tidak berubah.
- **Normalisasi path**: `filepath.Abs`+`Clean` menghapus `..`; path relatif dan NUL ditolak (`Record.Validate`, `sandbox.absPath`). `deny_paths` menang atas verdict lain.
- **Hard link saat menulis**: tanpa `--force` ditolak `ErrUnsafePath`; dengan `--force` ditulis in-place sehingga inode/link kedua ikut berubah dan mode tetap (`TestUndoHardlink` lulus).
- **Deteksi konflik**: isi berubah/parsial → `conflict`, file tidak disentuh; `--force` menimpa dan tercatat (`TestUndoConflictAndForce`, `partial-content-conflict`).
- **Blob before hilang** → `ErrBlob`/`failed`, file tidak berubah (`missing-before-blob` lulus). Inverse JSON rusak → `json.Unmarshal` error dari `Get` (fail-closed).
- **Crash sebelum side effect**: aksi tercatat tetapi file masih before → undo no-op `undone` (`crash-before-side-effect`).
- **Crash setelah file dipulihkan, sebelum `Finish`**: probe subprocess membunuh diri (`SIGKILL`) di dalam `Finish`; file sudah `old`, undo kedua menghasilkan `status=undone detail="already at the before image"` tanpa perubahan lain — idempoten dan aman untuk replay.
- **Undo kedua** pada state before adalah no-op dan menambah baris `action_undos` `undone` (suite mengecek hitungan 2).
- **Open-turn refusal**: state `running`, `tool_pending`, `tool_done`, `awaiting_confirmation`, `awaiting_approval` dianggap terbuka; `resumed`/`crashed` sudah ditutup `turn_end{crashed}` oleh recovery, jadi tidak salah dianggap terbuka (`TestOpenTurnRefuses`, `open-turn-refused`).
- **Append-only**: `actions`/`action_undos` menolak UPDATE/DELETE (`TestAppendOnly`).
- **Urutan tulis sebelum side effect**: `Record` mempublikasikan blob dan menyisipkan event `tool_dispatch` + baris `actions` dalam satu transaksi (`journalstore.go:119-141`); kontrak "side effect hanya setelah `Record` nil" didokumentasikan (saat ini belum ada wiring).
- **Retensi waktu**: `created_at < T_cut` → `expired` dan tercatat (`TestRetentionExpiry`, `expired`).
- **Byte-identik**: 200 skenario (5 operasi × 10 isi × 4 mode) lulus; isi mencakup empty, LF, CRLF, BOM UTF-8, biner/NUL, unicode, tanpa EOL, tab, JSON, 64 KiB; mode before diperiksa; undo kedua idempoten; baris `undone` bertambah. Multi-file `UndoLast(3)` berjalan newest-first.
- **Durabilitas**: temp `O_EXCL` 0600 di direktori tujuan → `Sync` → `rename`/`MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)` → `fsync` direktori (POSIX); tidak ada `O_TRUNC` sebelum pemeriksaan handle.
- **Batas arsitektur**: tidak ada `database/sql`/driver di `internal/journal`/`journalstore` (E-04); `journal.Store` tepat 5 method (E-09); `TestImportGraph`, `TestFileLength`, `TestGlobalAllowlist` lulus; tidak ada global mutable baru (hanya sentinel error); tidak ada import cycle; cross-compile `GOOS=windows` dan `darwin` untuk paket baru berhasil; `go vet` bersih; `go test -race` kedua paket hijau (29,8 dtk).
- **Ukuran**: E-84 dan LOC di atas; tidak ada file > 800 baris; fungsi baru ≤ 80 baris.

## Risiko residual

- **TOCTOU leluhur untuk unlink/rename (JN2-10)** — sudah diakui TRUST-MODEL §3 untuk operasi berbasis path codetools, tetapi belum dicatat untuk undo; pada undo dampaknya dapat berupa penghapusan file di luar root. Perlu primitif sandbox atau catatan residu eksplisit di ADR-117/TRUST-MODEL.
- **Filesystem case-insensitive / Windows / macOS** — hanya statis dan cross-compile; `foldRoot`, junction, dan semantik `MoveFileEx` tidak diuji runtime di sini.
- **Bind mount** — path di dalam root yang merupakan bind mount dari luar tetap dianggap "inside" oleh desain sandbox; bukan batas baru dari PR ini.
- **Owner/xattr/SELinux/capabilities (JN2-6)** — tidak dipulihkan oleh temp+rename; pada workspace multi-user atau sistem ber-SELinux, undo dapat mengubah konteks keamanan file.
- **Lease/guard F belum ditegakkan executor** — SPEC §18.1 mewajibkan undo dicatat dengan lease pemanggil dan `lease_fenced`/`session_purging` dilaporkan beserta file yang sudah dipulihkan; `Undoer`/`Finish` tidak membawa token lease. Ini harus dipenuhi lapisan wiring/CLI (`TestUndoCLIShortLease` belum ada).
- **Integrasi `Record`** — `Record` menulis event `tool_dispatch` sendiri; dispatcher `internal/tools` juga menulis `tool_dispatch` (`cmd/damnbot/tools_wire.go:20-32`). Wiring harus memilih satu jalur agar event tidak dobel, dan SPEC §4.3 langkah 8 juga mewajibkan `turn_journal` `tool_pending` dalam transaksi yang sama — `Record` belum menulisnya.
- **Memori file besar (JN2-11)** dan **kuota bytes_held (JN2-9)** — belum ada batas ukuran per aksi/streaming; aksi > 2 GiB langsung expired (JN2-8), tetapi aksi mendekati 2 GiB tetap dapat menekan RAM saat undo.
- **Belum ada wiring produksi** — ADR-117/PR #139 menyatakan stack belum disambungkan ke tools/CLI. Semua temuan di atas belum reachable di produksi sampai wiring; severity dinilai untuk saat stack di-wire.

## Tidak dicakup

- Wiring `internal/tools` dispatch, `cmd/damnbot`, TUI/web/Telegram, `--dry-run`, dan undo atas undo (redo) end-to-end.
- Aksi non-file: shell `mutating`, checkpoint git, memory/skill/config, approval/P26.
- Runtime Windows/macOS, filesystem case-insensitive, bind mount, dan multi-user nyata.
- Fuzz, bench 50-file ≤ 200 ms, stress konkurensi undo vs turn aktif, interaksi purge/GC, dan `-race` seluruh repo (hanya dua paket yang direview).
- Perbaikan setelah head yang direview; verdict berlaku untuk `3140bb9`, `923cff3`, `4d5628d`.

## Errata dokumen

- Body PR #141 menyatakan "a missing after-image is treated as `failed`". Implementasi tidak membaca blob after sama sekali (hanya sha di JSON); probe `TestZZProbeAfterBlobMissing` menghapus blob after dan undo tetap `undone`. Ini **bukan** masalah keamanan (sha pembanding tetap tersimpan), tetapi klaim PR/ADR perlu dikoreksi agar sesuai perilaku dan alasan desain ADR-117 ("blob after hanya dipakai nilai sha-nya").
- Commit #143 berjudul "100-scenario" dan CHANGELOG/ADR menyebut 200; yang benar adalah 200 (5×10×4, terverifikasi `grep -c "=== RUN   TestUndoSuite/"` = 200). Tidak ada dampak.


## Putaran 2 — re-review perbaikan (2026-10-21)

Head baru (stacked): `#139` `c89e0dd`, `#141` `d1f27f2`, `#143` `3e7806a`. Metode: diff hanya pada hunk yang berubah vs head Putaran 1 (`3140bb9`/`923cff3`/`4d5628d`), probe `TestZZR2*` sementara di worktree terpisah (dihapus sebelum commit), lalu gate penuh. Kode produk tidak diubah.

### Verdict per PR (Putaran 2)

| PR | Head | Verdict |
|---|---|---|
| #139 | `c89e0dd` | **approve** — JN2-2/3/7/8/9/15 tertutup; tersisa JN2-16 (komentar identity), JN2-19/20 (catatan MINOR) |
| #141 | `d1f27f2` | **approve** — JN2-1/4/5/10/12/13 tertutup; JN2-6 (owner/xattr) dan JN2-11 (memori) tetap MINOR; JN2-16/17/18 baru MINOR |
| #143 | `3e7806a` | **changes required** — JN2-14 belum tertutup: `TestUndo50Files` gagal anggaran 200 ms di host ini (313,8 ms ext4; 215,8 ms tmpfs), sehingga `make test-timing`/`make check` merah |

### Status JN2-1..15

| ID | Status | Bukti (probe Putaran 2) |
|---|---|---|
| JN2-1 | **fixed** | `TestZZR2SymlinkCreate`/`TestZZR2SymlinkWrite`: `status=failed`, `err=journal: action conflicts with the current file: ... openat p: path escapes from parent`; target `q` tetap `"A"`/`"new"`; path tetap symlink. `os.Root` + `O_NOFOLLOW` pada path asli. |
| JN2-2 | **fixed** | `go test ./internal/archtest/ -count=1` → ok (exit 0); `/internal/journal/ @yuldan` ada; `origin/main` juga ok. |
| JN2-3 | **fixed** | `TestZZR2CorruptBlob`: blob diganti `"EVIL"` → `status=failed`, `ErrBlob: blob ... is corrupt`, isi file tetap `"new"`. |
| JN2-4 | **fixed** | `TestZZR2ModeZero` dengan `ModeSet=true`: mode akhir `----------` (0000). |
| JN2-5 | **fixed** | `TestZZR2Setuid`: mode akhir `ugtrwxr-xr-x` (setuid+setgid+sticky), chmod setelah write. |
| JN2-6 | **open (MINOR)** | Tidak ada `fchown`/salin xattr/ACL; temp+rename tetap mengganti owner dan metadata keamanan. ADR-117 hanya mengklaim mode. |
| JN2-7 | **fixed** | `TestZZR2ShortSha`: `js.Blob("x")` → `ErrBlob: invalid sha "x"`, tanpa panic; regex 64-hex. |
| JN2-8 | **fixed** | `TestZZR2NewestOverCap` (`MaxBytes: 1`): aksi terbaru `expired=false`; `cutoff` selalu mempertahankan aksi terbaru. |
| JN2-9 | **fixed** | `TestUndoSuiteUniqueBytes` lulus; `cutoff` men-*dedup* sha unik lintas aksi (bukan hanya dalam satu aksi). |
| JN2-10 | **fixed (komponen akhir)** | `remove`/`rename`/`chmod` kini dirfd-relative (`os.Root`) pada path asli; residual swap **direktori leluhur** tetap ada karena `parentRoot` membuka direktori lewat path setelah `Check` (kelas TRUST-MODEL §3). |
| JN2-11 | **partial (MINOR)** | Hash kini streaming (`io.CopyBuffer`) dan `Blob` dibatasi `maxBlobSize = 1 GiB` + re-hash, tetapi `Blob` tetap membaca seluruh before-image ke memori. Aksi > 1 GiB → `failed` (bukan crash). |
| JN2-12 | **fixed** | `TestZZR2HardlinkCreate`: tanpa `--force` → `conflict`/`ErrUnsafePath`, kedua link utuh; `--force` menghapus hanya path aksi. |
| JN2-13 | **fixed** | `undoMove` memanggil `chmod` setelah rename; hard-link write memakai `Chmod` setelah byte. Test `TestUndoMoveRestoresMode`. |
| JN2-14 | **open (MAJOR gate)** | `TestUndo50Files` (`DAMNBOT_TIMING=1`): **313,8 ms ext4**, **215,8 ms tmpfs** vs 200 ms; run berulang 306–354 ms. `make check` menyertakan `test-timing` → merah di host ini. Sensitif latensi fsync; Q14/NVMe perlu bukti terpisah. |
| JN2-15 | **fixed** | `TestZZR2NilRecord`: `js.Record(ctx, nil)` → error `journalstore: nil record: ...`, tanpa panic. |

### Temuan baru (MINOR)

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| JN2-16 | #139+#141 | MINOR | static | Komentar menyatakan device/inode "dibandingkan sebelum mutasi", padahal ADR-117 (amandemen) menyatakan tidak ditegakkan dan kode tidak membandingkannya. Field `Device/Inode` menjadi data mati dan menyesatkan reviewer. | `undo.go:24,191`; `journal.go:50`; `journalstore.go:387` vs `docs/DECISIONS.md` amandemen ADR-117 |
| JN2-17 | #141 | MINOR | confirmed | Penolakan symlink mengembalikan `ErrConflict` tetapi baris `action_undos` dicatat `failed` (`res.Status` tetap default), bukan `conflict`. Operasional sulit membedakan konflik dari kegagalan I/O. | probe symlink: `status=failed`, `err` berisi `ErrConflict` |
| JN2-18 | #139+#141 | MINOR | static | `cutoff` memindai **semua** `actions` dan meng-`json.Unmarshal` inverse setiap `Get`; `UndoLast(n)` memanggil `List` (N+1 `Get`) lalu `Undo` per aksi (satu `Get` lagi) → O(n·A) decode. Ikut menyumbang JN2-14 pada riwayat aksi besar. | `journalstore.go:168-192`, `:323-357`, `:194-227` |
| JN2-19 | #139 | MINOR | static | `maxBlobSize = 1 GiB`: before-image > 1 GiB selalu `failed` walau masih dalam retensi. Codetools membatasi jauh di bawah ini, tetapi batas tidak tercantum di SPEC/ADR. | `journalstore.go:284-286` |
| JN2-20 | #141 | MINOR | static | Mode `0000` hanya benar bila pemanggil mengisi `Snapshot.ModeSet=true`; `Record` tidak memaksanya. Pemanggil yang hanya mengisi `Mode: 0` tetap mendapat `0644` (bug JN2-4 kembali). Kontrak ini perlu ditegakkan/didokumentasikan di sisi `Record`/codetools. | `journalstore.go:123`, `undo.go:270-274`; probe lulus hanya dengan `ModeSet=true` |

### Device/inode: diterima sebagai residual, dengan koreksi komentar

Caveat penulis benar dan **dapat diterima**: `codetools` menulis lewat temp+rename, sehingga inode after-image memang berbeda dari before-image pada setiap undo write yang sah; baris `actions` append-only juga tidak dapat menyimpan identitas after. Karena itu penegakan device/inode akan menolak undo yang benar. Pemeriksaan yang efektif dan sudah terbukti adalah `O_NOFOLLOW` pada path asli (menolak symlink yang dipasang setelah aksi), sha256 penuh before/after (konflik isi), dan verdict hard-link. Yang perlu diperbaiki hanyalah komentar yang mengklaim "dibandingkan" (JN2-16); field-nya boleh dihapus atau didokumentasikan sebagai jejak audit yang tidak ditegakkan.

### Verifikasi gate Putaran 2

- `go test ./internal/archtest/ -count=1` → ok (E-79/E-10/E-01/E-14).
- `go vet ./internal/journal/ ./internal/ctxdb/journalstore/ ./internal/archtest/` → bersih.
- Cross-build `GOOS=windows` dan `GOOS=darwin` untuk kedua paket → ok.
- `go test -race ./internal/journal/ ./internal/ctxdb/journalstore/ -count=1` → ok (1,06 s / 59,68 s).
- Coverage scoped: `internal/journal` **84,5 %** (kritis ≥ 80), `journalstore` **84,3 %** (≥ 70), exit 0.
- 200 skenario `TestUndoSuite` tetap ada dan lulus; `TestUndoSuiteRefusals` kini memuat corrupt-blob, hardlink-create, symlink, retensi, turn terbuka, dll.
- E-84 (Go non-test per PR): #139 **580**, #141 **483**, #143 **2** — ≤ 600. LOC: `journal` **505/520** (ADR-117 amendment + `loc.budget`), `journalstore` **341/400**.

### Residual dan batas Putaran 2

- Swap direktori leluhur antara `Check` dan `os.OpenRoot(dir)` masih mungkin (kelas TRUST-MODEL §3); komponen akhir sudah aman. Tidak ada probe balapan deterministik; dinyatakan residual, bukan temuan baru.
- Owner/xattr/ACL/SELinux/capabilities tidak dipulihkan (JN2-6); ADR-117 hanya menjanjikan byte + mode.
- JN2-14 sensitif filesystem: ext4 host ini 300–354 ms, tmpfs 215,8 ms; anggaran 200 ms belum dapat ditandatangani tanpa pengukuran Q14 atau optimasi (`Finish`/`Get` per aksi, cutoff O(A)).
- `maxBlobSize` 1 GiB (JN2-19) dan `ModeSet` opt-in (JN2-20) adalah batas kontrak yang perlu ditulis di ADR/codetools saat wiring.
- Windows/macOS: hanya cross-compile/statis; `oNoFollow=0` di Windows mengandalkan `os.Root` dan mutasi pada nama link (bukan target).
- Belum ada wiring produksi; seluruh temuan dinilai untuk saat stack disambungkan ke tools/CLI.


## Putaran 3 — verifikasi #143 setelah batch undo (`9dd611f`)

Head `#143` `9dd611f` (199 baris Go non-test; #139/#141 di bawahnya tidak berubah). Perubahan: `UndoLast` memakai satu `List` snapshot + satu `cutoff`, lalu satu `BatchFinisher.FinishBatch` (satu transaksi untuk semua `undo` event + `action_undos`); `statusFor` memetakan `ErrConflict` → `Conflict`; komentar device/inode diperbaiki sebagian; `maxBlobSize`/`ModeSet` didokumentasikan di amandemen ADR-117; `journal` budget 520→545 (terukur 539).

### Verdict

| PR | Head | Verdict |
|---|---|---|
| #143 | `9dd611f` | **approve** — JN2-14 tertutup; tidak ada regresi semantik undo; JN2-16 tinggal 2 komentar (MINOR) dan JN2-21 residual batch-crash |

### Bukti

- **50-file timing** (`DAMNBOT_TIMING=1`, `TestUndo50Files`): ext4 73,998 / 80,525 / 81,405 ms; tmpfs (/dev/shm) 29,145 / 29,413 / 30,108 ms — semuanya < 200 ms (sebelumnya 306–354 ms ext4).
- **Konflik di tengah batch** (`UndoLast(3)`, aksi tengah berubah): hasil `action=3 undone files=[f3]`, `action=2 conflict files=[]`, `action=1 undone files=[f1]`; `f1`/`f3` kembali ke before, `f2` tidak disentuh; `err=ErrConflict`; **3 baris `action_undos`** (undone/conflict/undone) ditulis dalam satu batch. Batch **tidak** me-rollback side effect file yang sudah terjadi (memang bukan bagian transaksi DB), tetapi melaporkan setiap file yang sudah dipulihkan lewat `Result.Files` dan melanjutkan aksi lain — sesuai perilaku `UndoLast` "every action is attempted".
- **Kegagalan transaksi batch**: wrapper `FinishBatch` yang gagal → kedua file sudah dipulihkan, `UndoLast` mengembalikan error batch + `Result.Files`, dan **0 baris `action_undos`** (tidak ada ledger parsial/silent success). Re-undo tetap idempoten.
- **Status per aksi**: symlink kini `status=conflict` (bukan `failed`) dan error `ErrConflict`; test `TestUndoSymlinkInsideRoot` diperbarui.
- **Tanpa regresi**: `go test ./internal/journal/ ./internal/ctxdb/journalstore/` (DAMNBOT_TIMING=1) hijau; 200 skenario suite + refusal tests lulus; `go test -race` kedua paket hijau (1,05 s / 54,85 s); `go test ./internal/archtest/ -count=1` hijau; `go vet` bersih; cross-build Windows/darwin hijau; coverage journal 84,9 % (≥80), journalstore 83,2 % (≥70); LOC journal 539/545, journalstore 379/400; E-84 #143 199 ≤ 600.

### Sisa MINOR

- **JN2-16 (sebagian)**: komentar di `undo.go` sudah benar, tetapi `journal.go:51` masih menulis device/inode "used to refuse an undo" dan `journalstore.go:431` "Undo compares it before mutating"; ADR-117 sudah benar (tidak ditegakkan). Perbaiki dua komentar itu atau hapus field.
- **JN2-21 (baru, residual)**: karena ledger ditulis sekali di akhir batch, crash `SIGKILL` setelah beberapa file dipulihkan tetapi sebelum `FinishBatch` meninggalkan **0 baris `undo`** untuk file yang sudah kembali (sebelumnya per-aksi sudah tercatat). Tidak ada korupsi data: re-run `undo N` idempoten dan mencatat semuanya; ini trade-off audit/throughput yang perlu dicatat di ADR/SPEC. Tidak menahan approve.
