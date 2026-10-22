# REVIEW-F2-SEC13 — Review keamanan CLI `sessions fork`/`rewind` (ADR-136)

Review independen untuk #227 `f2-fork-cli` `55cf6a4` (basis `main` `b219387`): `damnbot sessions fork <id> [--at <seq|turn:ID>]`, `damnbot sessions rewind <id> --at …`, guard `context_exclude` di `ctxdb.Fork`, dan pewarisan identitas gateway (`gateway_conversations` + label `gateway_chat`). Reviewer tidak menulis kode yang direview, dan kode produk tidak diubah. Tanggal: 2026-10-21.

## Round 1 — 2026-10-21

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #227 fork/rewind CLI | `55cf6a4` | **REQUEST CHANGES**: SEC13-1 (MEDIUM: fork/rewind CLI memindahkan percakapan gateway yang sedang berjalan), SEC13-2 (MEDIUM: fork saat turn berjalan mewarisi turn yang kemudian di-exclude), SEC13-3/4/5 (MINOR) |

Gate di `55cf6a4`: `go test -race -tags goolm -count=1 ./cmd/damnbot ./internal/archtest` → ok; `./internal/ctxdb -race -run 'Fork|Head|Turn|History|Gateway|Exclude'` → ok; `go vet` + `GOOS=windows|darwin` bersih; `make deadcode` 0; `make loc` `ctxdb 3484/3500`.

Probe `TestZZSec13` (`internal/ctxdb`) menanam DB nyata lalu menjalankan **binary `damnbot` hasil build `55cf6a4`** (`exec`, `--data-dir`). Probe dihapus setelah dijalankan. Isi DB: sesi `gw` (origin `gateway`, `gateway_conversations(telegram, 123)` → `gw`, label `gateway_chat=telegram:123`), sesi lokal `L` (turn `ok`; turn `bad` = 2 event + marker `context_exclude` di seq 4; turn `run1` dibuka `StartTurn` dengan 1 event dan belum selesai, head 6).

| Perintah | Hasil |
|---|---|
| `sessions fork gw` | exit 0, anak dibuat. **Rute terbaru `telegram:123` sekarang = anak**; `origin` anak `gateway`; label `gateway_chat` disalin → SEC13-1 |
| `sessions rewind gw --at 1` | exit 0, anak rewind **juga menjadi rute terbaru** chat tersebut → SEC13-1 |
| `fork L --at 2` / `--at 3` (di dalam turn `bad`, sebelum marker) | **exit 2** `fork L at seq N inside excluded turn bad` ✔ |
| `fork L --at 4` / `--at turn:bad` (marker ikut diwarisi) | exit 0 ✔; anak tetap mengecualikan turn `bad` |
| `rewind L --at turn:bad` | seq 1 (sebelum turn) ✔ |
| `fork L` (head 6) **saat turn `run1` masih berjalan**, lalu induk menulis `context_exclude{run1}` | induk: turn hilang dari History ✔; **anak: "RUNNING turn poison" tetap ada di History** → SEC13-2 |
| `fork nope` | exit 1 ✔; stderr `head: ctxdb: not_found: sql: no rows in result set` → SEC13-3 |
| `fork L --at abc` / `-1` / `turn:missing` / `999` | exit 2 ✔ |
| `rewind L` / `rewind L --at 999` (≥ head) | exit 2 ✔ |
| `fork "L'; DROP TABLE events;--"` | exit 1 `not_found` (parameterized, tidak ada efek) ✔ |
| `fork L --at turn:\x1b[31mred` | stderr memuat **escape ANSI mentah** dari argumen → SEC13-4 |

### Yang sudah benar

- Append-only: rewind = `Fork` di seq lebih awal; cabang lama utuh; tidak ada UPDATE/DELETE.
- Guard `checkForkExcluded`: memotong di tengah turn ber-marker (marker setelah titik potong) → `ErrInvalidSeq`/exit 2. `turn:ID` di-resolve ke batas turn (`fork` = event terakhir, termasuk marker; `rewind` = sebelum event pertama).
- Kode exit sesuai ADR-136 butir 7 (not_found 1, seq/flag 2, busy 4). Argumen diproses dengan query berparameter.
- **Recall dan cabang lama (bersama #220):** rewind = fork di `fork_seq` lebih awal. `lineageBounds` #220 membatasi leluhur ke `Seq ≤ ForkSeq` dan melewati link fork-0 (SEC12-8), sehingga konten cabang lama **setelah** titik rewind tidak ter-recall dari anak (terverifikasi di REVIEW-F2-SEC12 Round 4: `Fork(P,1)` hanya me-recall konten sebelum fork). Sisa SEC12-10 (leluhur di atas link fork-0) tetap berlaku.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC13-1 | **MEDIUM** | confirmed | **Fork/rewind dari CLI lokal memindahkan percakapan gateway yang sedang berjalan.** `inheritGateway` menyisipkan baris `gateway_conversations(platform, chat_id, thread_id, child, now)`. Resolusi chat → sesi memakai **baris terbaru** (`idx_gw_conv_latest`, SPEC "yang terbaru menang"), sehingga pesan berikutnya dari user gateway (mis. Telegram `123`) dikirim ke sesi anak hasil CLI. Probe: setelah `sessions fork gw`, rute terbaru = anak. Setelah `rewind gw --at 1`, rute pindah ke cabang yang di-rewind, sehingga riwayat yang dilihat agent untuk user remote berubah diam-diam tanpa sepengetahuan user tersebut. Anak juga membawa `origin=gateway` dan label principal, sehingga dari sisi scope recall/memori ia setara dengan sesi milik principal itu. SPEC menyediakan `/fork` sebagai **perintah chat** (dipicu oleh user gateway sendiri); CLI lokal tidak boleh memindahkan rute percakapan principal lain secara implisit. | Default CLI: **jangan** menyalin `gateway_conversations` (rute tetap di induk). Label identitas boleh diwarisi untuk scope recall dan validasi #220. Pemindahan rute hanya lewat `/fork` di chat, atau flag eksplisit (mis. `--move-chat`) dengan konfirmasi dan notice ke chat. Alternatif paling konservatif: tolak `sessions fork/rewind` untuk sesi `origin=gateway` dari CLI. Tambahkan test: rute terbaru tidak berubah setelah fork CLI. |
| SEC13-2 | **MEDIUM** | confirmed | **Fork saat turn induk masih berjalan mewarisi turn yang kemudian gagal tanpa marker.** `checkForkExcluded` hanya melihat marker yang **sudah** ada. Fork di head ketika turn `run1` masih terbuka (`turn_journal` running) berhasil. Ketika turn itu kemudian gagal dan induk menulis `context_exclude{run1}` (seq > `fork_seq`), anak tidak pernah melihat marker itu. History anak tetap memuat isi turn gagal/beracun (probe), sehingga konten yang menjadi alasan exclusion kembali dikirim ke model di cabang anak. | (a) Tolak fork/rewind yang titik potongnya berada di turn yang `turn_journal`-nya masih terbuka atau sesinya memegang lease turn aktif (`session_busy`/exit 4). **Dan/atau** (b) `History` anak menerapkan marker `context_exclude` milik leluhur untuk turn yang diwarisinya, termasuk marker yang ditulis leluhur **setelah** `fork_seq` (dengan mencocokkan `turn_id` event yang diwarisi). Opsi (b) juga menutup kasus crash recovery yang menulis marker belakangan. |
| SEC13-3 | MINOR | confirmed | Pesan error membocorkan detail internal: `sessions fork: head: ctxdb: not_found: sql: no rows in result set`. | Petakan error bertipe ke pesan stabil (`session <id> not found`) tanpa teks driver SQL. |
| SEC13-4 | MINOR | confirmed | Argumen (`turn:<id>`, id sesi) di-echo mentah ke stderr, termasuk escape ANSI/kontrol. Dampaknya kecil karena input berasal dari user lokal sendiri, tetapi id dapat berasal dari skrip atau copy-paste. | Sanitasi (`safe()` yang sudah dipakai `sessions backup ls`) sebelum mencetak id/turn. |
| SEC13-5 | MINOR | static | **Koordinasi dengan #220.** #220 menulis label `gateway_chat` di `Tx.CreateSession` (identitas diwarisi lewat `Session.GatewayIdentity`) dan memvalidasi identitas di `checkParent`. #227 menulis label sendiri di `inheritGateway` dan memanggil `CreateSession` tanpa `GatewayIdentity`. Bila #220 mendarat lebih dulu, fork sesi gateway lewat #227 akan ditolak `identity mismatch`. Bila keduanya digabung tanpa rebase hati-hati, label akan tertulis dua kali. Selain itu, `openSessionsDB` dengan `--data-dir` yang tidak ada akan **membuat** DB baru (efek samping untuk perintah yang hanya mencabangkan sesi). | Setelah rebase: `Fork` meneruskan `GatewayIdentity: src.identity`, dan **satu** penulis label (milik #220). Hapus penyisipan label di `inheritGateway`, dan `gateway_conversations` mengikuti keputusan SEC13-1. Buka DB dengan mode yang tidak membuat data dir baru (atau error bila tidak ada). |

**Ringkasan:** #227 **REQUEST CHANGES**. Mekanisme dasarnya benar: fork/rewind append-only, guard di tengah turn ber-marker, `turn:ID` di batas turn, kode exit, dan parameterisasi. Namun fork dari CLI lokal memindahkan rute percakapan gateway user lain ke cabang baru (SEC13-1), dan fork saat turn masih berjalan mewarisi turn yang kemudian di-exclude tanpa marker (SEC13-2). Dengan batas lineage #220, recall cabang lama setelah titik rewind sudah tertutup.
