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

## Round 2 — 2026-10-21

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #227 fork/rewind CLI | `cc2f5c7` | **APPROVE dengan MINOR**. SEC13-1..5 tertutup. Sisa: SEC13-6 (invarian scope tulis memori, latent) dan SEC13-7 (turn tanpa `turn_end` mengunci fork di head). Konflik rebase hanya di `docs/DECISIONS.md` terhadap `main` `9bf28f8` |

Gate di `cc2f5c7`: `go test -race -tags goolm -count=1 ./cmd/damnbot ./internal/archtest` → ok; `./internal/ctxdb -race -run 'Fork|Head|Turn|History|Gateway|Exclude'` → ok; `go vet` + `GOOS=windows|darwin` bersih; `make deadcode` 0; `make loc` `ctxdb 3498/3500`.

Probe `TestZZSec13R2` memakai fixture yang sama dengan Round 1 dan menjalankan **binary `damnbot` hasil build `cc2f5c7`**. Turn `run1` dibuka `StartTurn` (seq 4) + 1 event (seq 5), head 5. Probe dihapus setelah dijalankan.

| Perintah | Round 1 | Round 2 |
|---|---|---|
| `sessions fork gw` | rute terbaru `telegram:123` → anak | exit 0; **rute terbaru tetap `gw`**; anak: 0 baris `gateway_conversations`, 1 label `gateway_chat` ✔ SEC13-1 |
| `rewind gw --at 0` | rute pindah | exit 0, rute tidak berubah ✔ |
| `fork L` (head, turn `run1` terbuka) | exit 0, anak mewarisi turn yang kemudian di-exclude | **exit 4** `session L has an open turn; finish or cancel it first` ✔ SEC13-2 |
| `fork L --at 4` (tepat di `turn_start` run1) | — | exit 4 ✔ |
| `fork L --at 3` / `rewind L --at turn:run1` (seq 3, sebelum turn terbuka) | — | exit 0 ✔ |
| `fork L --at 2` (di dalam turn `bad` sebelum marker) | exit 2 | exit 2 `fork L at seq 2 inside excluded turn bad: invalid_args` ✔ |
| `fork nope` | `sql: no rows in result set` | exit 1 `session nope not found` ✔ SEC13-3 |
| `fork L --at turn:\x1b[31mred`, `fork \x1b[2J\x1b[Hx`, subperintah `\x1b[31mbad` | ESC mentah | ESC dibuang (`[31mred`, `[2J[Hx`) ✔ SEC13-4 |
| `fork L --at 999` | exit 2 | exit 2 ✔ |
| `--data-dir <tidak ada>` | DB dibuat | exit 1 `data directory not found: …`; direktori **tidak** dibuat ✔ SEC13-5 |

### Analisis tambahan

- **`checkForkOpen` dan urutan event.** Loop selalu menulis event `turn_start` (`internal/loop/turn.go`), sehingga pemeriksaan berbasis event juga berlaku untuk turn produksi, tidak hanya untuk turn yang tercatat di `turn_journal`. Recovery crash menulis `turn_end{crashed}` lalu `crash_recovered`. Marker `context_exclude`, sebelum atau sesudah `turn_end`, tetap ditolak `checkForkExcluded` selama ada event turn di `≤ at` dan marker di `> at`. Jadi kombinasi kedua guard menutup jalur "marker ditulis setelah fork" untuk semua turn yang sudah selesai.
- **Apakah label scope saja memungkinkan anak lokal bertindak sebagai principal remote (tulis memori/recall)?** Pada `cc2f5c7` maupun `main` `9bf28f8`, **tidak ada jalur kode** yang menurunkan scope *tulis* memori dari label `gateway_chat` sesi. `memstore` menerima scope dari pemanggil (`validScope`/`sameScope`), dan belum ada penulis memori gateway yang dirangkai. Label hanya dipakai untuk validasi dan pembatasan recall #220 (`checkParent`, `lineageBounds`). Recall anak juga terbatas pada lineage-nya sendiri ≤ `fork_seq`, yaitu konten yang memang sudah diwarisi. Pelaku pada skenario ini adalah owner lokal, yang sudah memegang otoritas penuh atas DB, sehingga tidak ada batas kepercayaan yang dilanggar hari ini. Risikonya laten (SEC13-6): bila kelak frontend lokal atau tool memori menurunkan scope dari label sesi, anak hasil fork CLI akan menulis ke `gateway:<platform>:<identity>` milik user remote. Akibatnya persisten: memori itu masuk ke prompt sesi-sesi user tersebut (SPEC B-03).

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC13-1 | MEDIUM | **fixed** `cc2f5c7` | Rute chat tidak lagi disalin; hanya label scope yang diwarisi (probe). | — |
| SEC13-2 | MEDIUM | **fixed** `cc2f5c7` | `checkForkOpen`: titik potong di dalam turn tanpa `turn_end` → `ErrSessionBusy`/exit 4 (probe). | — |
| SEC13-3 | MINOR | **fixed** `cc2f5c7` | `sessionsMessage` memetakan error ke pesan tetap. Error lain menjadi `internal error`. | — |
| SEC13-4 | MINOR | **fixed** `cc2f5c7` | `cleanArg` membuang karakter kontrol < U+0020 dan U+007F serta memotong pada 80 byte. Byte ESC hilang, sehingga sisa teks tidak lagi bisa ditafsirkan terminal sebagai escape. | — |
| SEC13-5 | MINOR | **fixed** `cc2f5c7` | `os.Stat` dijalankan sebelum `ctxdb.Open`; direktori yang tidak ada → exit 1, tidak dibuat. Koordinasi #220 tercatat di ADR-136 butir 13. | Saat rebase setelah #220: teruskan `GatewayIdentity`, dengan satu penulis label. |
| SEC13-6 | MINOR | open (latent) | Label `gateway_chat` pada anak hasil fork CLI belum punya invarian tertulis bahwa label itu **hanya scope baca**. Penulis memori gateway di masa depan yang menurunkan scope dari label sesi akan membiarkan sesi lokal menulis ke scope memori principal remote. | Tambahkan ke ADR-136/SPEC: scope tulis `gateway:<platform>:<identity>` hanya diturunkan dari identitas inbound terautentikasi (gateway worker, `gateway_identities`), tidak pernah dari label sesi. Sesi `origin≠gateway` atau sesi yang dibuka frontend lokal tidak boleh menulis ke scope gateway. Kunci dengan test ketika penulis memori gateway dirangkai. |
| SEC13-7 | MINOR | open | `turn_end` ditulis lewat `t.note` (best-effort). Turn yang crash sebelum recovery #211 menulis `turn_end{crashed}`, atau yang `note`-nya gagal, menyisakan `turn_start` tanpa `turn_end`. Akibatnya `fork`/`rewind` di head sesi itu ditolak exit 4 selamanya. Ini fail-closed (tidak bocor) dan masih bisa dilewati dengan `--at` sebelum turn tersebut. | Dokumentasikan jalan keluar di pesan error (`--at` sebelum turn terbuka), atau terima turn yang journal-nya sudah `crashed`/tidak ada sebagai tertutup. |

**Ringkasan Round 2:** #227 `cc2f5c7` **APPROVE dengan MINOR**. Kelima perbaikan terverifikasi dengan binary nyata: rute gateway tetap di induk, fork ke turn terbuka exit 4, pesan error bersih, argumen disanitasi, dan data dir tidak dibuat. Saat ini label scope tidak memberi hak tulis memori atas nama principal remote karena belum ada jalur kode yang memakainya. Invarian "scope tulis hanya dari identitas inbound" perlu dikunci sebelum penulis memori gateway dirangkai (SEC13-6). Sebelum merge, rebase untuk konflik `docs/DECISIONS.md`.
