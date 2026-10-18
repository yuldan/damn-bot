# TRUST-MODEL — siapa dipercaya untuk apa

Dokumen ringkas untuk pelapor keamanan dan implementer. Sumber normatif tetap [`SPEC.md`](../SPEC.md): §14 (approval, sandbox, redaksi), §11.11 (model konkurensi & persistence), §22.1 baris GAP-SEC-28. Bila berbeda, SPEC berlaku.

## 1. Batas keamanan vs heuristik

| Jenis | Isi | Bug di sini |
|---|---|---|
| **Batas** (boundary) | Sandbox path SPEC §14.2; approval + preview SPEC §14.1 dan P26; gerbang DLP egress `internal/netx` (P29); akses gateway P25 (allowlist, owner-only untuk aksi berbahaya); guard sesi + fencing DB SPEC §11.10–§11.11; verifikasi tanda tangan update/indeks hub (ed25519) | vulnerability, in-scope |
| **Heuristik** (defense-in-depth) | Classifier shell, threat scan skill/memori, redaksi berbasis pola, taint `<untrusted>`, smart approval | dilaporkan sebagai kelemahan; bukan batas |

## 2. Aktor

| Aktor | Dipercaya untuk | Tidak dipercaya untuk | Penegakan |
|---|---|---|---|
| **User OS lokal** (akun yang menjalankan damnbot) | Semua: memegang file data, kunci, config | — | Di luar model: akses tulis ke `<data_dir>` setara akses penuh (dapat mengubah DB, trigger, binary) |
| **Owner** (identitas yang di-pair sebagai owner, P25) | Approval semua kelas, termasuk kelas berbahaya dan P26; menjawab approval lintas kanal; `edit` jarak jauh (selalu menjadi permintaan + preview baru) | Melewati P26 tanpa preview | SPEC §14.1, §11.11.10 |
| **User gateway** (identitas allowlisted, role `user`) | Mengobrol, tool non-berbahaya di sesi/memori miliknya, kuota P25 | Aksi berbahaya, approval kelas berbahaya, sesi/memori identitas lain | P25, isolasi scope memori |
| **Identitas tidak dikenal / `weak`** | Tidak ada (0 tool call, 0 LLM call) | Semua | P25, SEC-10 |
| **Kanal** (TUI, ACP client, web UI, platform chat, klien `mcp serve`) | Menyampaikan input dan keputusan dari identitas yang terautentikasi di kanal itu | Mengklaim identitas lain; menjawab approval di luar `channels[]` (kecuali owner) | Auth per kanal (§13), `request_id` + `preview_sha` (§11.11.10) |
| **Klien `mcp serve`** | Scope token: `read`, `send` (allowlist target + approval), `approve` (approve/deny saja) | `edit` argumen, sesi identitas lain | Token terikat profil + identitas (SEC-21), CHECK DB |
| **Server MCP** (tool pihak ketiga) | Schema + hasil tool sebagai **data** | Instruksi di deskripsi/hasil; efek tanpa approval (default `non_idempotent`, `untrusted`) | Pin tool (SEC-04), taint, approval §14.1 |
| **Skill** (bawaan, hub, buatan agent) | Instruksi kerja setelah lolos karantina/review | Melewati approval; skill hub tanpa tanda tangan | Karantina, threat scan, tanda tangan indeks (SEC-20) |
| **Model (output LLM)** | Usulan tool call dan teks | Apa pun tanpa validasi: argumen divalidasi schema (P3), aksi melewati approval/sandbox | P3–P5, §14 |
| **Konten eksternal** (web, file di luar workspace, hasil tool, email, pesan platform) | Data | Instruksi (`<untrusted>`, taint) | Taint → approval naik |
| **Proses damnbot lain** (TUI/ACP/`serve` milik user yang sama) | Menulis DB sesuai lease dan mode | Menulis sesi yang di-lease proses lain atau sedang dipurge | Fencing token + guard DB (§11.10–§11.11) |

## 3. Residu yang diakui (tidak dijamin)

| Residu | Dampak | Rujukan |
|---|---|---|
| Proses berhenti (SIGSTOP/debugger/hang) di dalam transaksi tulis | Penulisan semua proses tertahan; dilaporkan `db_write_stalled`, tidak dibunuh otomatis | SPEC §11.11.13 #37 |
| Time namespace berbeda pada kernel sama | Tidak didukung; liveness lease bisa salah, keselamatan tetap dijaga fencing | #40 |
| Id sesi di payload JSON bebas setelah purge | Purge menjamin rujukan terstruktur saja; payload bebas = anonimisasi best-effort | #48, I13 |
| Backup otomatis (`daily-*` 3, `premigrate-*` 2) dan arsip pengguna | Masih memuat data sesi yang di-purge sesudahnya sampai backup dirotasi/dihapus | SPEC §11.6 Backup/restore |
| Restore ke data dir lain (tanpa `purge.log`) | Purge yang terjadi setelah snapshot tidak dapat diterapkan ulang | SPEC §11.6 |
| `messages_send` tanpa `idempotency_key` di-retry klien | Dapat menghasilkan dua permintaan (masing-masing butuh approval) | #68 |
| macOS: turunan tool yang membuat session/process group baru (`setsid`) saat pemiliknya mati | Lolos dari SIGKILL pgid pengambil alih; Linux (`cgroup.kill`) dan Windows (Job Object) tidak terdampak | #74 |
| Pesan masuk antre (`gateway_inbound.payload`) | Isi tersimpan di DB sampai diproses (lalu dikosongkan); dicakup `storage.encrypt` | ADR-046 |
| Inbound dari platform yang tidak mengirim ulang event yang belum di-commit (push wajib-ack maupun tanpa ack) | Saat DB tidak bisa ditulis atau exit terencana, event masuk spool `run/inbound_spool/` (SPEC §11.11.7) dan tidak hilang. Platform ber-API riwayat memakai `gateway_cursors` untuk backfill sejak id terakhir yang ter-commit | ADR-052 AF-06 (`S084_SpoolDuringDrainBarrier`) |
| ≤ 1 LLM call atau ≤ 1 start tool basi per turn | Dari proses yang terhenti tepat setelah titik cek lease; hasil tidak ditulis | I4 |
