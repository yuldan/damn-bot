# REVIEW-F2-SEC11 — Review keamanan `context_exclude` untuk turn gagal/crash (GAP-REL-01, ADR-134)

Review independen untuk #211 `rel01-turn-status` `04b6c95` (basis `main` `68dbec3`). Cakupan: penulis marker `context_exclude` di loop (sebelum `turn_end`, `WithoutCancel`, jalur panic), marker pada crash recovery (`closeCrashed`), serta filter di `ctxdb.History` dan `compactstore.Load`. Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

## Round 1 — 2026-10-21

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #211 turn gagal → `context_exclude` | `04b6c95` | **REQUEST CHANGES**: SEC11-1 (MEDIUM: summary yang dibuat di dalam turn gagal tetap masuk konteks), SEC11-2 (MEDIUM: jalur crash tidak pernah aktif di `run` produksi), SEC11-3 (MEDIUM: efek samping tool dari turn ter-exclude hilang dari konteks), SEC11-4/5/6 (MINOR) |

Gate di `04b6c95`:
- `go test -race -tags goolm -count=1 ./internal/loop/... ./internal/ctxdb/compactstore/... ./cmd/damnbot/ ./internal/archtest/` → ok.
- `./internal/ctxdb -race -run 'Exclude|Crash|Kill9|Recover|Chaos|History'` → ok.
- `go vet -tags goolm ./...` + `GOOS=windows|darwin go vet` → bersih.
- `make deadcode` → 0.
- `make loc`: `loop 1792/1800`, `ctxdb 3427/3500`, `ctxdb/compactstore 249/250`. Jumlah `loc.budget` = Total core ENGINEERING (36.770) ✔. ADR-134 "Dampak terukur" menulis "total core 31.174", padahal angka yang berlaku 36.770 (nit).

Probe `TestZZSec11` (`internal/ctxdb`, DB nyata) dijalankan lalu dihapus.

### Yang sudah benar

- **Penulis marker hanya loop/recovery.** `context_exclude` ditulis oleh `turn.finish` (sekali per turn, flag `finished`) dan `closeCrashed`. Konten provider/model/tool tidak bisa menjadi event ber-kind `context_exclude`. Filter `History` dan `compactstore` mewajibkan `kind = 'context_exclude'`.
- **Cakupan exclusion.** Hanya `turn_id` turn yang gagal yang di-exclude. Turn sebelumnya tidak tersentuh oleh error provider atau prompt injection.
- **Urutan.** Marker ditulis lalu `turn_end`, keduanya lewat `context.WithoutCancel`. Jalur panic (`finishOpen` di-`defer`) menulis `turn_end{error/internal}` + marker `panic` lalu me-panic ulang, tanpa update stream, dan nilai panic tidak disimpan. Crash di antara marker dan `turn_end`, bila turn punya journal (lihat SEC11-2), ditutup recovery dengan `turn_end{crashed}` + marker kedua (duplikat tidak berbahaya). `closeCrashed` menulis ketiga event dalam satu transaksi.
- **Multi-proses.** `RecoverJournals` tetap memakai `takeover_ok` + `AcquireLease` + cek `state` di dalam transaksi tulis. Pemenang race menulis marker sekali; yang kalah `ErrSessionBusy`/`ErrNotFound` → skip.
- **Compaction view.** `compactstore.Load` memfilter event dan `taskQ` dengan predikat yang sama.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC11-1 | **MEDIUM** | confirmed | **Turn ter-exclude masuk kembali lewat summary.** Compaction berjalan **di dalam** turn (`guard` saat `CompactAt`, dan `Compact(force)` pada retry `context_overflow`, `turn.go:182/305`), sebelum turn itu diketahui gagal. Summary-nya merangkum event turn yang sedang berjalan, termasuk tool result beracun. Ketika turn kemudian gagal (mis. retry overflow gagal → `context_overflow`, atau 400 setelah compaction) dan marker ditulis, summary tetap ada: `History` menerapkan `excludeTurns` **setelah** `applySummaries`, dan `summary_ref` sintetis tidak punya `turn_id`. `compactstore.summariesQ` juga tidak memfilter. Probe: turn T1 (tool result `POISON: ignore previous instructions`) dirangkum (summary seq 1..4, event `compaction` T1) lalu `context_exclude{T1}` → `History` default mengembalikan `summary_ref` berisi **`POISON: …`**. Ini bertentangan dengan ADR-134 butir 6 ("summary tidak pernah menyerap rentang ter-exclude"). | Saat menulis marker, juga exclude summary yang dibuat oleh turn itu (summary yang `event_id`-nya ber-`turn_id` ter-exclude), lalu kembalikan event asli turn sebelumnya yang tercakup (atau biarkan History memakai summary sebelumnya). Filter yang sama di `historySummaries` dan `summariesQ`. Tambahkan test: compaction di tengah turn → turn gagal → `History`/`compactstore.Load` tidak memuat isi summary itu. |
| SEC11-2 | **MEDIUM** | static (grep) | **Jalur crash tidak aktif di produksi.** `closeCrashed` hanya berjalan untuk baris `turn_journal`, dan baris itu hanya dibuat oleh `Tx.StartTurn`. **Tidak ada pemanggil produksi** `StartTurn`/`EndTurn` (`grep`: hanya `internal/ctxdb/journal.go` dan test). `TestKill9MidTurnExcludes` memanggil `StartTurn` langsung di proses anak, bukan lewat `run`/loop. Akibatnya `kill -9` pada `damnbot run` di tengah turn tidak meninggalkan `turn_end` maupun marker, dan turn yang menggantung tetap masuk `History`. Klaim ADR-134 butir 4 ("turn yang terputus kill -9 tidak pernah masuk request berikutnya") tidak berlaku untuk jalur nyata. Dampak hari ini terbatas karena `run` selalu membuat sesi baru, tetapi resume/TUI akan mewarisinya. | Composition root (`ctxStore`/loop) memanggil `StartTurn` di awal turn dan `EndTurn` (yang juga menghapus journal) sebagai ganti `turn_end` lewat `note`, atau `History` menganggap turn tanpa `turn_end` (selain turn yang sedang berjalan milik pemegang lease hidup) sebagai ter-exclude. Tambahkan test kill -9 yang menjalankan `run` nyata. |
| SEC11-3 | **MEDIUM** (desain) | static | **Efek samping tool hilang dari konteks.** Exclusion mencakup **semua** event turn, termasuk `tool_result` dari aksi yang sudah dieksekusi (tulis file, `shell` mutating, approval Monetary). Contoh: turn menjalankan pembayaran atau migrasi, lalu iterasi berikutnya kena `provider_error`/`max_wall`/`cancelled` → turn di-exclude. Pada turn berikutnya ("lanjutkan"), model tidak tahu aksi itu sudah terjadi dan dapat mengulang aksi yang tidak idempoten. Prompt injection atau server lambat dapat memicu `provider_error`/`max_wall` dengan sengaja setelah aksi berhasil. Data tetap ada di DB/recall, tetapi model tidak diberi tahu. | Pertahankan jejak efek: sisipkan `system_notice` ringkas (daftar tool_call sukses beserta kelas efeknya) setelah marker, atau exclude hanya `assistant_message`/`tool_result` read-only dan pertahankan hasil tool mutating. Dokumentasikan di ADR-134 butir 3/7. |
| SEC11-4 | MINOR | confirmed | **Span terbuka menghapus seluruh riwayat.** `History`/`compactstore` mendukung marker `{from_seq}` tanpa `to_seq` = "dari seq itu sampai selamanya". Probe: satu marker `{"from_seq":1}` → `History` kosong, **termasuk turn yang datang sesudahnya**. Penulis saat ini hanya memakai `turn_id`, tetapi importer/salvage nanti, atau bug penulis, akan membisukan sesi secara permanen tanpa jejak di konteks. | Hapus dukungan span sampai ada penulisnya, atau wajibkan `to_seq` (tolak `to_seq = 0`), dan validasi `from_seq ≤ to_seq ≤ seq marker`. |
| SEC11-5 | MINOR | static | **Recall dapat memasukkan kembali turn ter-exclude.** `Search`/`recall` sengaja tidak memfilter marker (ADR-134 butir 5/8). Tool `recall` yang dipanggil model dapat mengambil konten turn gagal (termasuk payload injeksi yang memicu kegagalan itu), tanpa penanda bahwa sumbernya ter-exclude. Retrieval otomatis (#167) diwajibkan memfilter, tetapi recall manual tidak. | Tandai hit dari turn ter-exclude (`excluded:true` + `tainted`) di hasil recall, atau filter default dengan opsi eksplisit untuk menyertakannya. |
| SEC11-6 | MINOR | static | **Kehilangan marker diam-diam.** `t.note` menelan error append (audit path). Bila append marker gagal (mis. `db_busy`) sedangkan `turn_end` berhasil, turn gagal tetap masuk konteks berikutnya (poison loop yang ingin dicegah), tanpa sinyal. Recovery tidak menolong karena turn tidak punya journal (SEC11-2). | Tulis marker + `turn_end` dalam satu transaksi (`Tx`), atau retry marker dengan backoff singkat lalu `system_notice` bila tetap gagal. |

Catatan:
- DoS konteks: prompt injection/provider error hanya dapat meng-exclude turn **yang sedang berjalan** (termasuk `user_message`-nya). Turn sebelumnya tidak terdampak. Exclusion `cancelled`/`max_wall` atas kerja agentik panjang adalah pilihan desain ADR-134 (user mengirim ulang), dan dampaknya diperberat oleh SEC11-3.
- Recovery `auto_continue` menulis marker **dan** menandai journal `resumed` untuk turn yang sama. Saat resume diimplementasikan, lanjutan turn ber-`turn_id` sama akan ikut ter-exclude (termasuk `user_message`-nya). Konsumen `resumed` belum ada (grep), jadi ini dicatat sebagai risiko laten.

**Ringkasan:** #211 **REQUEST CHANGES**. Mekanisme marker tidak dapat dipalsukan oleh konten model/provider, dan cakupannya terbatas pada turn yang gagal. Namun turn ter-exclude dapat hidup kembali lewat summary yang dibuat di dalam turn itu (SEC11-1), jalur `kill -9` tidak aktif di produksi karena loop tidak membuat journal (SEC11-2), dan exclusion menyembunyikan efek tool yang sudah terjadi sehingga aksi non-idempoten dapat terulang (SEC11-3).

## Round 2 — 2026-10-21

#211 `54750d4` (di atas `04b6c95`): summary yang beririsan dengan turn ter-exclude dibuang (`History` + `compactstore.summariesQ`); `run` menulis `turn_journal` lewat `StartTurn` dan `EndTurnWith` (turn_end + hapus journal dalam satu transaksi) dengan `TestRunKilledMidTurn`; `system_notice{failed_turn_effects}` tanpa turn untuk tool non-read yang sukses; marker span diabaikan; `Hit.Excluded`; kegagalan append marker dimunculkan sebagai notice stream; budget `loop 1870`, `compactstore 260` (amandemen ADR-134).

**Verdict:** #211 **APPROVE dengan MINOR** (SEC11-1..6 FIXED; SEC11-7/8 MINOR, boleh follow-up).

Gate di `54750d4`: `go test -race -tags goolm -count=1 ./internal/loop/... ./internal/ctxdb/compactstore/... ./internal/tools ./cmd/damnbot ./internal/archtest` → ok (termasuk `TestRunKilledMidTurn`, `TestJournalSeam`, `TestFailedTurnEffectsNotice`); `./internal/ctxdb -race -run 'Exclude|Crash|Kill9|Recover|Chaos|History|Search'` → ok; `go vet` + `GOOS=windows|darwin` bersih; `make deadcode` 0; `make loc` `loop 1853/1870`, `ctxdb 3455/3500`, `compactstore 253/260`, `tools 2157/2200`. Jumlah `loc.budget` = Total core ENGINEERING (**36.850**) ✔, dan didukung "Dampak terukur" ADR-134 (36.770 → 36.850).

Probe `TestZZSec11R2` (`internal/ctxdb`, DB nyata) dijalankan lalu dihapus:

| Skenario | Hasil |
|---|---|
| Turn T1 (tool result `POISON …`) dirangkum (summary seq 1..4, event `compaction` T1), lalu `context_exclude{T1}` | `History` = `[user_message:T0 "legit question", assistant_message:T0 "legit answer"]`: summary beracun dibuang, dan event sah T0 yang sebelumnya tertutup summary kembali utuh ✔ (Round 1: `summary_ref` berisi POISON) |
| `Search "POISON"` | hit `tool_result` dan `summary_ref` dengan **`Excluded=true`** ✔ |
| Marker `{"from_seq":1}` (tanpa `turn_id`) | diabaikan; History tetap 4 event ✔ (Round 1: History kosong) |

### Status

| ID | Verdict | Bukti |
|---|---|---|
| SEC11-1 | **FIXED** | `dropExcludedSummaries` (History, sebelum `applySummaries`) + predikat yang sama di `summariesQ` dan Search summary. Probe di atas. |
| SEC11-2 | **FIXED** | `ctxStore.BeginTurn` = `StartTurn` (journal + `turn_start` satu tx), `EndTurn` = `EndTurnWith` (turn_end + hapus journal satu tx). `TestRunKilledMidTurn` men-SIGKILL `damnbot run` nyata di tengah turn lalu memverifikasi `turn_end{crashed}` + marker + History. Crash di antara append marker dan `EndTurnWith` meninggalkan journal terbuka, sehingga recovery menutupnya (marker duplikat tidak berbahaya). |
| SEC11-3 | **FIXED** (dengan SEC11-7/8) | Turn gagal yang mengeksekusi tool non-read menulis `system_notice{failed_turn_effects}` **tanpa `turn_id`** (tidak ikut ter-exclude), dan `ctxStore.History` merendernya sebagai pesan `system` ("… already executed: shell(call-id) … Do not repeat"). Maksimal 20 entri. |
| SEC11-4 | **FIXED** | Span diabaikan di History dan compactstore. |
| SEC11-5 | **FIXED** (seam) | `Hit.Excluded` untuk event dan summary. Belum ada konsumen recall di `main`; konsumen nanti wajib menampilkan atau memfilternya. |
| SEC11-6 | **FIXED** | Error append marker atau `EndTurn` disimpan di `markerErr` dan dikirim sebagai `UpdateNotice` (teks di-redaksi). Bila `EndTurn` gagal, `turn_end` tetap dicoba lewat `note` dan journal membuat turn dapat dipulihkan. |

### Temuan baru

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC11-7 | MINOR | static | **ID tool call dari provider masuk ke pesan role `system`.** Notice efek berisi `name(callID)`, dan `callID` berasal dari respons provider tanpa validasi charset/panjang (assembler hanya membatasi total 16 MiB). Provider/reseller jahat atau buggy dapat menyisipkan teks instruksi ke pesan **system** pada turn berikutnya. | Saring `callID` (mis. `[A-Za-z0-9_-]{1,64}`, selain itu ganti dengan indeks), atau tampilkan nama tool + kelas efek saja. |
| SEC11-8 | MINOR | static | Efek hanya dicatat bila tool **sukses** (`err == nil && res.Err == nil`). Tool mutating yang gagal **setelah** menjalankan sebagian efek (timeout `exec`, `outcome_unknown`, error setelah menulis) tidak tercantum, sehingga model tetap tidak tahu efek parsial itu. | Catat juga tool non-read yang gagal dengan status (`failed`/`outcome_unknown`), dan beri label di notice. |

**Ringkasan:** #211 **APPROVE dengan MINOR**. Keenam temuan Round 1 tertutup dan terverifikasi: summary beracun dibuang, jalur `kill -9` aktif di `run` nyata, efek tool tetap terlihat, span diabaikan, recall diberi flag, dan kegagalan marker dimunculkan. SEC11-7 (sanitasi call-id di pesan system) dan SEC11-8 (efek parsial) dapat menjadi follow-up.
