# SCORECARD — Damn-Bot vs hermes-agent

Kriteria rilis terukur: Damn-Bot 1.0 hanya rilis jika **≥ hermes di semua dimensi** dan **menang dengan margin** di dimensi inti (SPEC P20, ditegakkan `TestScorecardGate`; V-20/ADR-054). Hasil tiap run disimpan sebagai artefak versi (§2.6). Baseline hermes diambil dari [`BASELINE-hermes.md`](BASELINE-hermes.md) bila ada; selain itu diukur oleh harness pada snapshot hermes yang di-pin (`868b9f244f`). Sel "diukur di F0" pada dimensi yang butuh LLM (token/biaya/kualitas per task, recall, routing skill, injection, error recovery, wall-clock end-to-end) dibaca sebagai **"diukur di RC F9"** (ADR-032); F0 hanya mengukur dimensi tanpa LLM. Untuk dimensi tanpa LLM, status per dimensi ada di tabel ADR-065: sel "diukur di F0" pada dimensi berstatus `Fx` dibaca **"diukur di Fx (ADR-065)"**; sisi hermes diukur di fase itu dengan harness yang sama untuk kedua agent dan wajib ada sebelum acceptance fase tersebut.

Notasi aturan menang: `↓` lebih kecil lebih baik, `↑` lebih besar lebih baik. "Margin" = syarat minimal terhadap hermes. Dimensi inti ditandai **[I]**.

---

## 1. Dimensi head-to-head

### 1.1 Token, biaya, kualitas jawaban

| Dimensi | Metrik | Cara ukur (identik untuk kedua agent) | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Fixed overhead | token system prompt + tool schema per call | Tangkap request HTTP di proxy rekam (§2.3), hitung o200k_base | 14.500–17.700 tok (BASELINE §2) | ≤ 5.000 | ↓, ≤ 0,35× hermes |
| **[I]** Token input per task | Σ `usage.input_tokens` per task eval | Usage dari respons provider (proxy rekam) | diukur di F0 | ≤ 0,5× hermes (median) | ↓, median ≤ 0,5×, tidak ada kategori eval > 0,8× |
| Token output per task | Σ `usage.output_tokens` | idem | diukur di F0 | ≤ 0,9× hermes | ↓, median ≤ 1,0× |
| **[I]** Biaya per task | USD dari `usage.cost` / tabel harga yang sama | idem | diukur di F0 | ≤ 0,5× hermes | ↓, median ≤ 0,6× |
| Cache hit rate | `cached_input_tokens / input_tokens` | idem, provider dengan prefix cache (OpenAI, Anthropic, OpenRouter) | diukur di F0 | ≥ 0,70 pada sesi ≥ 5 call | ↑, ≥ hermes + 10 pp |
| Background token | token LLM di luar jalur turn per 100 turn | ledger/proxy | ~30.000 tok/event review (`agent/turn_finalizer.py:777-779`), default on | 0 (default) | ↓, 0 pada config default |
| **[I]** Task success rate | pass rate eval set E1–E7 (§2.2) | Grader otomatis per task (test lulus, file sesuai, jawaban cocok) | diukur di F0 | ≥ hermes; coding (E1) ≥ hermes + 3 pp | ↑, CI 95% selisih > −3 pp di semua kategori |
| **[I]** Tool call invalid / halusinasi | (tool tak dikenal + argumen invalid + tool sukses tetapi tidak diminta) / total tool call | Log tool call dari kedua agent dinormalisasi; "tidak diminta" = hasil fuzzy-repair/sintesis | diukur di F0 (repair difflib `agent/agent_runtime_helpers.py:2632-2679`) | 0 eksekusi tak diminta; invalid ≤ 2% | ↓, eksekusi tak diminta = 0 |
| **[I]** Klaim selesai tanpa bukti | jawaban final berisi klaim verifikasi tanpa command verify sukses di turn itu | Detektor pola §7.2 SPEC + audit transcript, dijalankan pada output kedua agent | diukur di F0 | 0 tanpa flag; `evidence_gap` terdeteksi 100% | ↓, ≤ 0,2× hermes dan 0 yang tidak ditandai |
| **[I]** Recall memori lintas sesi ("ga pikun") | % fakta dari sesi N dijawab benar di sesi N+k (k = 1, 3, 10) | Eval E4: fakta ditanam, ditanya ulang di sesi baru | diukur di F0 | ≥ 90% (k=10) | ↑, ≥ hermes + 10 pp |
| Fakta basi | % jawaban memakai memori yang sudah dikoreksi/kedaluwarsa | E4 varian koreksi fakta | diukur di F0 | ≤ 2% | ↓ |
| Akurasi routing skill | % query E9 di mana skill benar ada di top-3 yang dimuat | E9: 50 query berlabel skill | diukur di F0 (index penuh, deskripsi 60 char: `agent/prompt_builder.py:1340-1395`) | ≥ 90% top-3 | ↑, ≥ hermes |
| Token skill per turn | token index/isi skill di request | proxy rekam | diukur di F0 | ≤ 150 tok context tier + ≤ 20 tok volatile (SPEC §4.8) | ↓, ≤ 0,2× hermes |
| Long-session stabilitas | pass rate tugas di turn 100+ (E5) & jumlah compaction LLM | E5 | diukur di F0 | ≥ hermes; compaction LLM ≤ 1 per turn | ↑ / ↓ |
| Tool error recovery | % task E6 selesai setelah error tool yang disuntik | E6 | diukur di F0 | ≥ hermes + 5 pp | ↑ |

### 1.2 Kinerja (lihat juga §3 Speed parity gate)

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Startup | exec → prompt siap / frame pertama, p95 dari 20 run | `hyperfine`-setara di harness, cold page cache dibuang tiap run | import 1,61–2,1 s (BASELINE §6) | ≤ 100 ms | ↓, ≥ 10× lebih cepat |
| **[I]** Overhead agent per iterasi | waktu loop di luar model/jaringan/tool, p50/p95 | Fake provider latensi 0 di endpoint yang sama untuk kedua agent | diukur di F0 | p50 ≤ 5 ms, p95 < 20 ms | ↓, ≥ 5× lebih cepat |
| **[I]** First token di UI | t(token tampil) − t(token diterima dari provider) | Fake provider bertanda waktu; kamera terminal (pty capture) / stdout | diukur di F0 | ≤ 30 ms TUI, ≤ 10 ms headless | ↓, ≥ 5× lebih cepat |
| **[I]** Task wall-clock end-to-end | median waktu per task E1–E7, model sama | §2 | diukur di F0 | ≤ 0,5× hermes | ↓, median ≥ 2× lebih cepat; tidak ada task > hermes × 1,10 |
| Idle RSS | RSS setelah 5 s idle (TUI 1 sesi, 2.000 event) | `/proc/<pid>/status` | diukur di F0 | ≤ 50 MB | ↓, ≤ 0,3× |
| Peak RSS | `VmHWM` selama soak 1.000 turn | idem | diukur di F0 | ≤ 300 MB | ↓, ≤ 0,5× |
| CPU idle | % CPU rata-rata idle 60 s (`serve` + Telegram long-poll) | `pidstat` | diukur di F0 | ≤ 0,5% x86; ≤ 2% Raspberry Pi 4 | ↓ |
| Code tools | grep/glob/read/edit p50/p95 korpus C1–C3; 70 operasi (50 `search_files`, 10 `read_file`, 10 `patch`) vs hermes (ADR-077) | [`CODE-TOOLS.md`](CODE-TOOLS.md) §10; `cd bench && go run ./codetools/cmd/codetools`; referensi hermes = setelan `bench/codetools/results/f0-baseline.json` (`HERMES_HOME` bersih, backend fallback tanpa ripgrep; ubah commit/setelan = ukur ulang) (ADR-077) | diukur di F0 | grep ≤ 1,2× `rg` (literal), ≥ 10× lebih cepat p50 per kategori dari tool hermes (ADR-077) | ↓, ≥ 10× vs hermes p50 per kategori (ADR-077) |
| Exec spawn overhead | waktu `shell` di luar runtime command, p95 | [`EXEC-AND-CODING.md`](EXEC-AND-CODING.md) | diukur di F0 | < 15 ms | ↓, ≥ 5× |
| Voice end-to-end | akhir ucapan → audio pertama, di luar latensi provider STT/LLM/TTS | Fake STT/LLM/TTS bertanda waktu | diukur di F0 | overhead ≤ 200 ms; streaming TTS mulai ≤ 300 ms setelah kalimat pertama LLM | ↓ |
| Image preflight | % gambar ditolak provider karena ukuran; waktu resize 12 MP | Suite 50 gambar besar | hermes shrink-and-resend setelah ditolak (`agent/conversation_compression.py:4306-4310`) | 0 penolakan; resize ≤ 150 ms | ↓ |

### 1.3 Stabilitas & ketahanan

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Crash/hang rate | crash + hang (tanpa progres > 120 s) per 1.000 turn; soak 24 jam `serve` | Soak §2.4 | diukur di F0 | 0 | ↓, 0 |
| **[I]** Pemulihan setelah `kill -9` | korupsi DB, turn hilang tanpa penanda, tool mutating terulang tanpa konfirmasi | 200 kill acak (SPEC P19) | diukur di F0 | 0 / 0 / 0 | ↓, 0 |
| **[I]** Self-healing | waktu kembali melayani setelah proses/OS hidup; pesan Telegram hilang/dobel | [`SELF-HEALING.md`](SELF-HEALING.md) chaos suite | diukur di F0 | ≤ 10 s; 0 hilang; 0 dobel di suite | ↓ |
| Konkurensi | 4 proses × 50 turn + 2 proses berebut sesi; error `SQLITE_BUSY`/korupsi | SPEC P19 `TestMultiProcess` | diukur di F0 (riwayat WAL: `hermes_state.py:182-184`) | 0 | ↓ |
| Proses yatim / perintah hang | proses tersisa setelah cancel/timeout; perintah interaktif yang menggantung | EXEC: `TestNoOrphans` (200 iterasi), `TestInteractiveCorpus` (40 fixture × pipe/pty) (ADR-077) | orphan mungkin (`tools/environments/local.py:943-950`); tanpa idle timeout | 0 / 0 | ↓, 0 |
| Edit salah lokasi | edit diterapkan di lokasi selain yang dimaksud | CODE-TOOLS accuracy suite CT-20 ≥ 325 kasus (CODE-TOOLS §10.3), dijalankan pada tool edit kedua agent | fuzzy 9 strategi (`tools/fuzzy_match.py:289-298`) | 0 | ↓, 0 |
| Error tertelan | error yang tidak muncul di log/transcript pada injeksi fault | 50 titik injeksi | 4.795 `except Exception` (BASELINE §5) | 0 | ↓ |
| Fallback tak terlihat | fallback/retry tanpa jejak di UI/transcript | injeksi 503 | diukur di F0 | 0 | ↓ |

### 1.4 Ukuran, instalasi, UX

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Ukuran install | byte di disk setelah install penuh (semua fitur parity) | `du -sb` direktori install + cache runtime | diukur di F0 (venv + node_modules + Electron) | binary ≤ 52 MB + data | ↓, ≤ 0,2× |
| **[I]** Waktu install dari nol | mesin bersih → prompt pertama sukses, 10 Mbit/s | VM bersih, skrip resmi masing-masing | diukur di F0 (setup 1–5 menit, MITIGATION B-04) | ≤ 30 s (+ wizard ≤ 60 s) | ↓, ≥ 5× lebih cepat |
| Langkah setup | jumlah aksi user sampai prompt pertama | Skrip observasi manual terstandar | diukur di F0 | ≤ 3 | ↓ |
| Baris config minimum | baris non-komentar config yang wajib | Dokumentasi resmi + uji jalan | diukur di F0 | 2 | ↓ |
| Error informatif | % pesan error (suite 60 kesalahan umum) yang menyebut penyebab + langkah perbaikan | Rubrik biner, 2 penilai | diukur di F0 | ≥ 95% | ↑ |
| Migrasi dari hermes | % config/skill/memory/MCP/cron/sesi terimpor benar | `damnbot import hermes` pada 5 fixture `~/.hermes` | n/a | ≥ 99% item, 0 kehilangan diam-diam | ↑ |

### 1.5 Cakupan & ekosistem

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Feature parity | baris FEATURE-PARITY.md berstatus terpenuhi (dengan test lulus) | Laporan parity otomatis dari kolom `status`/`test`/`bench` FEATURE-PARITY | 854 baris | 837 baris (854 − 17 `dropped-with-approval`; termasuk 1 `external-bridge`) | ↑, 837/837 |
| Platform chat | adapter berfungsi (uji fake server + 1 smoke nyata) | Suite gateway | 27 adapter + 3 surface (PLAT-001..030) | 30 (PLAT-025 via `damnbot-wa-bridge`, uji fake bridge `TestBridgeProtocolConformance`) | ↑, ≥ 30 |
| Provider | provider bekerja (fixture replay + smoke) | Suite provider | 50 profil hermes (FEATURE-PARITY §4) | 88 provider [`PROVIDERS.md`](PROVIDERS.md) + 6 server lokal + custom 12 field | ↑, ≥ 50 dan 100% katalog PROVIDERS.md |
| Bahasa UI | locale lengkap (0 key hilang) | Test i18n | 17 | 18 (Q16) | ↑, ≥ 17 |
| Dukungan OS | OS/arch dengan CI hijau | Matrix CI | diukur di F0 | 8 target rilis kanonik ENGINEERING E-39 (linux amd64/arm64/arm, macOS amd64/arm64, Windows amd64/arm64, android/arm64) | ↑, ≥ hermes |
| Skill tersedia | skill bawaan + optional yang terpasang & lolos scan | `damnbot skills` | 58 + 153 | ≥ 211 (impor + scan) | ↑ |
| MCP | 65 optional MCP hermes terpasang & `tools/list` sukses | Suite MCPX | 65 | 65 | ↑, ≥ 65 |

### 1.6 Keamanan

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| **[I]** Prompt-injection suite | % serangan berhasil (tool tak diminta dieksekusi / data dikirim keluar / instruksi diikuti) dari konten web, file, MCP, pesan chat, skill | E10: 100 kasus; mekanisme = taint lengket per turn (aksi non-read setelah `<untrusted>` di iterasi mana pun dalam turn yang sama → approval paksa, ADR-070) | diukur di F0 | ≤ 2% | ↓, ≤ 0,3× hermes |
| Secret redaction | secret sintetis yang lolos ke log/transcript/request/pesan keluar | 60 secret × 6 kanal | diukur di F0 | 0 | ↓, 0 |
| Akses gateway orang asing | tool call / LLM call dipicu identitas non-allowlist | E8: 20 skenario Telegram | diukur di F0 | 0 / 0 | ↓, 0 |
| Supply chain | temuan `govulncheck` reachable (semua severity); dependency langsung; lisensi di luar allowlist | CI | diukur di F0 (`pip-audit` + `npm audit`) | 0; ≤ 28; 0 | ↓ |
| Data at-rest | credential plaintext di disk | Scan direktori data | plaintext `.env` (BASELINE, `.env.example` 563 baris) | 0 (OS keychain) | ↓, 0 |

### 1.7 Kualitas kode & proses engineering

Rincian aturan & cara ukur: [`ENGINEERING.md`](ENGINEERING.md) §8. Angka hermes diukur pada snapshot `868b9f244f`.

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| LOC per fitur | LOC non-test / 837 baris penyebut gate FEATURE-PARITY (SPEC §8) | `scc --no-gen` / laporan parity | ≈ 1.162 | ≤ 227 | ↓ |
| File terbesar (non-test / test) | baris | `wc -l` | 8.392 / 23.074 | ≤ 800 / ≤ 1.500 | ↓ |
| Fungsi terpanjang / > 80 baris | baris / jumlah | funlen, skrip AST | 616 / 716 fungsi | ≤ 80 / 0 | ↓ |
| Kompleksitas siklomatik maks / > 15 | nilai / jumlah | gocyclo, skrip AST | ≈ 102 / 2.475 fungsi | ≤ 15 / 0 | ↓ |
| Siklus import / komentar "hindari circular import" | jumlah | `go build` + `TestImportGraph`; grep | 149 komentar di 134 file | 0 / 0 | ↓ |
| Klasifikasi error via string | jumlah | analyzer `nostringclassify`; grep | 41 + 88 | 0 | ↓ |
| Broad catch (`except Exception` / error diabaikan) | jumlah | grep / errcheck | 4.795 | 0 | ↓ |
| State global mutable | temuan di luar allowlist | gochecknoglobals; grep | 121 ContextVar, 450 `global`, 982 container modul | 0 (allowlist ≤ 10) | ↓ |
| Implementasi retry | jumlah | forbidigo; grep | 169 fungsi retry/backoff di 110 file | 1 | ↓ |
| Temuan lint | jumlah | golangci-lint (daftar ENGINEERING §5.1) | n/a | 0 | ↓, 0 |
| Kebocoran goroutine | jumlah | goleak di semua paket yang memulai goroutine | n/a | 0 | ↓, 0 |
| Dependency langsung / CGO | jumlah | `go.mod`, build matrix | 55 extras + `package.json` | ≤ 28 / 0 | ↓ |
| Rasio test:source | LOC | `scc` | 1,48 (BASELINE; ukur ulang 1,46) | 0,8–1,2 (gate mulai Go non-test ≥ 5.000 LOC, di bawahnya laporan saja; ADR-050) | dalam rentang |
| Flake rate | eksekusi gagal tanpa perubahan | nightly `-count=20` | diukur di F0 | ≤ 0,1% | ↓ |
| Waktu build bersih / inkremental | detik | job buildtime | diukur di F0 (install + build Node) | ≤ 90 / ≤ 10 | ↓ |
| Waktu CI PR | p90 menit | laporan CI | diukur di F0 | ≤ 12 | ↓ |
| Kode mati | fungsi tak terjangkau | `deadcode` (3 GOOS) | diukur di F0 (vulture) | 0 | ↓ |

### 1.8 Fitur pembeda (tidak ada di hermes)

| Dimensi | Metrik | Cara ukur | Baseline hermes | Target Damn-Bot | Aturan menang |
|---|---|---|---|---|---|
| Undo universal | % aksi file yang kembali byte-identik; % aksi irreversible yang melewati approval + preview | Suite undo ≥ 100 skenario (SPEC §18.1) | tidak ada (checkpoint filesystem MEM-031 terbatas) | 100% / 100% | ↑ |
| Preview aksi berbahaya | % aksi destructive/irreversible dengan preview sebelum eksekusi; timeout → reject | `TestPreviewBeforeDangerous` | tidak ada (target absolut) | 100% | ↑ |
| Plan mode | % deviasi terdeteksi & dilaporkan (30 skenario); akurasi estimasi biaya | Suite plan; selisih estimasi vs aktual | `/plan` tanpa pelacakan deviasi [tidak terverifikasi] | 100%; median selisih ≤ 30% | ↑ |
| Budget guard | LLM call setelah cap 100%; selisih biaya UI vs ledger | `TestBudgetGuard*` (fake clock) | tidak ada (target absolut) | 0; 0 | ↓ |
| Scorecard mingguan | run sukses per minggu; alarm terkirim saat dimensi kalah | job `scorecard-weekly` | tidak ada | 1/minggu; 100% alarm | ↑ |


### 1.9 Metrik dari GAP-SWEEP

Baseline hermes: bukti di [`GAP-SWEEP.md`](GAP-SWEEP.md) atau diukur di F0 (tanpa LLM) / RC F9 (dengan LLM). Requirement: SPEC §22.

| ID GAP | Dimensi | Metrik | Cara ukur | Baseline hermes | Target | Aturan menang |
|---|---|---|---|---|---|---|
| GAP-SEC-05 | Keamanan | cookie/storage yang keluar lewat `browser_cdp` pada fixture injection (minta `Network.getAllCookies`, `Runtime.evaluate` di profil nyata) | `TestCDPAllowlist` | `browser_cdp` meneruskan method apa pun (`tools/browser_cdp_tool.py:268-311`) | 0 cookie keluar; 100% method sensitif minta approval | ↓, 0 |
| GAP-SEC-10 | Keamanan gateway | PRIVMSG dari nick allowlisted tanpa `account-tag`/SASL yang diterima | `TestIRCAccountAuth`, `TestIdentityStrengthTable` | otorisasi = nick (`plugins/platforms/irc/adapter.py:311-316`) | 0 (100% ditolak) | ↓, 0 |
| GAP-SEC-22 | Keamanan | container resmi: grup docker.sock/GID 0 tanpa opt-in; compiler di image; temuan Trivy HIGH | CI image (`id`, `which sh gcc docker`), Trivy, `TestDockerBackendNoSocketMount` | usermod -aG root otomatis (`docker/stage2-hook.sh:142-173`) | 0 / 0 / 0 | ↓, 0 |
| GAP-GW-01 | Kepatuhan ToS | `send_message` ke JID tanpa riwayat inbound yang terkirim; aktivasi WhatsApp Web tanpa `--accept-ban-risk` | `TestWhatsAppWebGuards`, `TestSetupNoWebWithoutAck`, `TestBridgeProtocolConformance` | WhatsApp Web default, cold outbound tidak dijaga (`tools/send_message_tool.py`) | 0 / 0 | ↓, 0 |
| GAP-SEC-01 | Keamanan | secret/canary terbaca dari file backup default (tanpa passphrase) | `TestBackupPerms`, `TestBackupNoPlaintext` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 (hermes: semua `.env`+`vault.key`) |
| GAP-SEC-02 | Keamanan | env var secret sintetis yang terlihat di `env` child (6 jalur × 70) | `TestEnvAllowlist` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-SEC-03 / GAP-SEC-04 | Keamanan | % call tool tulis MCP katalog tanpa approval; % perubahan deskripsi tool yang diadopsi tanpa review | `TestMCPDefaultUntrusted`, `TestMCPRugPull` | GAP-SWEEP.md (bukti) / diukur F0 | 0% / 0% | ↓, 0 |
| GAP-SEC-06 | Keamanan | payload XSS (30) yang tereksekusi di web UI | `TestWebCSPHeaders` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-SEC-11 | Keamanan | canary teks pesan di log level ≥ INFO setelah E2E gateway | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-SEC-12 | Keamanan | % perintah bypass agent-CLI (korpus 40) ditandai butuh approval | `TestBypassIgnoresAllowDestructive` | GAP-SWEEP.md (bukti) / diukur F0 | 100% | ↑, 100% |
| GAP-REL-01 | Stabilitas | baris user/tool hilang setelah turn gagal (400/timeout/cancel) | `TestFailedTurnPersisted` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-REL-02 | Stabilitas | pesan Telegram hilang saat restart/update/409 (N=100) | `TestChaosTelegramRestart` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-REL-10 | Stabilitas | pesan hilang / duplikat tak berlabel pada 100 fault injection multi-bagian | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | 0 / 0 | ↓, 0 |
| GAP-REL-12 | Stabilitas | Δ FD dan Δ goroutine setelah soak 1.000 run cron | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 2 / 0 | ↓ |
| GAP-REL-13 | Stabilitas | disconnect ACP per 1.000 tool call yang spawn child | `TestACPChildNoStdin` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, 0 |
| GAP-REL-14 | Stabilitas | skenario known-defects lulus | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | 100% (≥ 10) | ↑, 100% |
| GAP-REL-17 | Stabilitas | akurasi `/readyz` pada fault-injection (status benar / total skenario) | `TestReadyz`, `TestReadyAfterAdapters` | GAP-SWEEP.md (bukti) / diukur F0 | 100% | ↑ (hermes: tidak ada endpoint) |
| GAP-PROV-01 | Stabilitas | error 400 signature/carrier setelah resume & fallback (Gemini 3, Claude thinking) | `TestReasoningCarrierReplay`, `TestCarrierDroppedEvent` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, = 0 (hermes: diukur F0) |
| GAP-GW-02 | Keamanan/biaya gateway | turn LLM yang diterima dari 1 user @ 50 pesan/menit; biaya harian user vs kuota | `TestGatewayQuota`, `TestGoroutineBounds` | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 20 turn/menit; ≤ kuota | ↓, ≤ hermes (hermes tanpa batas) |
| GAP-GW-03 | Stabilitas gateway | jumlah 429 pada replay 200 balasan ke 1 grup | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, = 0 |
| GAP-GW-04 | UX gateway | % balasan jatuh ke plain/parse error (fuzz 10.000) | `FuzzRenderTelegram` | GAP-SWEEP.md (bukti) / diukur F0 | < 0,1% | ↓, < hermes |
| GAP-COST-01 | Biaya | selisih biaya ledger vs harga resmi pada tanggal request | `TestPricingEffectiveDate`, `TestPricingTableNotExpired` | GAP-SWEEP.md (bukti) / diukur F0 | 0% | ↓ (hermes: Sonnet 5 −33%) |
| GAP-COST-03 | Token/biaya | cache_read/prompt, replay Telegram jeda 10 menit | `TestCacheTTLByOrigin` | GAP-SWEEP.md (bukti) / diukur F0 | ≥ 70% | ↑, ≥ hermes + 20 poin |
| GAP-COST-07 | Token | repetisi identik sebelum stop | `TestNoProgressStop` | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 3 | ↓, < hermes |
| GAP-COST-09 | Akurasi | rasio estimasi/real token (en/id/ru/ja/gambar) | `TestTokenEstimatorCalibration` | GAP-SWEEP.md (bukti) / diukur F0 | 0,9–1,1 | lebih dekat ke 1,0 daripada hermes per bahasa |
| GAP-COST-10 | Token/kecepatan | turn LLM & token untuk "proses 100 file" | `BenchmarkProgrammaticBatch`, `TestStarlarkMemoryLimit` | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 3 turn, −70% token vs loop tool | ↓, ≤ hermes execute_code |
| GAP-COST-12 | Biaya | panggilan vendor untuk 3 query identik paralel; hit silang antar profil | `TestWebCacheIsolation` | GAP-SWEEP.md (bukti) / diukur F0 | 1; 0 | ↓, = 0 hit silang (hermes > 0) |
| GAP-COST-16 | Ringan | CPU idle serve & wake-up/menit (web + 3 adapter + cron) | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 0,5%; ≤ 60 | ↓, < hermes (hermes 455 sleep path, import 2,4 s/92 MB) |
| GAP-PROV-02 | Model lokal | turn tanpa overflow pada model 16K | `TestLearnedContextWindow` | GAP-SWEEP.md (bukti) / diukur F0 | 20/20 | ↑ (hermes menolak < 64K) |
| GAP-TOOL-01 | Instalasi/cakupan | skill bundled yang butuh python3/pip | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓ (hermes 11 skill/46 skrip) |
| GAP-TOOL-02 | Keamanan | RSS saat zip-bomb 10 GB | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | < 100 MB, error terkontrol | ↓ (hermes OOM) |
| GAP-UX-03 | Stabilitas & ketahanan (§1.3) | % respons sukses ACP pada skenario gagal (suite ≥ 12) | `TestACPConformanceFailures`, `FuzzJSONRPCFrame` | GAP-SWEEP.md (bukti) / diukur F0 | 0% | ↓, hermes > 0 (bukti `acp_adapter/server.py:820-887`) |
| GAP-UX-05 | Kinerja (§1.2) | keystroke→echo p99 di tmux | `BenchmarkKeystrokeEcho`, `TestResizeStorm` | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 10 ms | ↓, ≤ hermes |
| GAP-UX-06 | Ukuran, instalasi, UX (§1.4) | byte ESC dengan `NO_COLOR=1` pada 30 skenario | `TestNoColorZeroEscape`, `TestA11yLinear` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, = 0 |
| GAP-UX-07 | Ukuran, instalasi, UX (§1.4) | pelanggaran axe serious/critical (12 halaman × 2 tema) | `TestContrastTokens` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, < hermes |
| GAP-UX-08 / GAP-OPS-02 | Keamanan (§1.6) | koneksi keluar non-provider dengan `background=false`/UI offline (proxy rekam 30 menit) | `TestWebAssetsNoExternalURL`, `TestHelpOffline`, `TestEgressRegistryComplete` | GAP-SWEEP.md (bukti) / diukur F0 | 0 | ↓, < hermes (≥ 7 fetch latar) |
| GAP-OPS-03 | Ukuran, instalasi, UX (§1.4) | rc shell byte-identik setelah install+uninstall (4 shell) | `TestUninstallKeepsUserLines` | GAP-SWEEP.md (bukti) / diukur F0 | 4/4 | ↑ |
| GAP-OPS-08 | Ukuran, instalasi, UX (§1.4) | jumlah perintah top-level terlihat / pasangan duplikat | `TestCLITopLevelCount`, `TestCLINoDuplicates`, `TestCLIDocsMatchHelp` | GAP-SWEEP.md (bukti) / diukur F0 | `= 20 / 0` (jumlah perintah top-level persis = SPEC §10; ADR-063 N-24) | ↓, < hermes (66 file registrasi) |
| GAP-INF-01 | Kinerja (§1.2) | `recall` query CJK 2 char @ 1.000.000 event p95 | `BenchmarkRecallCJK`, `TestRecallCJKOrder` | GAP-SWEEP.md (bukti) / diukur F0 | < 50 ms | ↓, ≥ 10× lebih cepat (P28 kelas search sesi) |
| GAP-INF-03 | Ukuran, instalasi, UX (§1.4) | ukuran image Docker dasar terkompresi | kolom Verifikasi baris GAP terkait SPEC §22.1 | GAP-SWEEP.md (bukti) / diukur F0 | ≤ 23 MB | ↓, ≤ 0,2× hermes |
| GAP-INF-08 | Cakupan & ekosistem (§1.5) | key kosong per locale (critical / total) | `TestI18nNoEmpty`, `TestNoUILiterals` | GAP-SWEEP.md (bukti) / diukur F0 | 0 / 0 | ↓, < hermes (≈ 2.246) |

---

## 2. Protokol benchmark

### 2.1 Kondisi identik
- Model & provider sama per run (model RC ditetapkan user, Q23-RC), `temperature = 0`, `seed` tetap bila didukung; versi model di-pin (tanggal snapshot).
- Mesin sama (Q14), container identik kecuali binary agent; jaringan lewat proxy rekam yang sama; cache provider di-warm-up dengan 1 call dummy untuk kedua agent.
- Config hermes: default resmi `cli-config.yaml.example` + key minimum; config Damn-Bot: default. Fitur otomatis hermes yang default on dibiarkan on (mencerminkan pengalaman default) — run kedua dengan fitur tersebut off dilaporkan terpisah sebagai pembanding adil.

### 2.2 Eval set terkunci

| ID | Kategori | Jumlah | Sumber | Grader |
|---|---|---|---|---|
| E1 | Coding | 160 | 100 SWE-bench Verified (daftar ID di-pin) + 60 internal (Go, Python, TS, Rust, Java, shell) | test suite task |
| E2 | File ops | 40 | internal (refactor, rename lintas file, CRLF/BOM/UTF-16, file besar) | diff golden |
| E3 | Web research | 30 | pertanyaan dengan jawaban terverifikasi dari halaman snapshot (server lokal) | exact/regex match |
| E4 | Memori lintas sesi | 30 | fakta ditanam di sesi N, ditanya di N+1/N+3/N+10, + 10 koreksi fakta | exact match |
| E5 | Long-session | 10 sesi × 120 turn | skrip user deterministik | checkpoint test per 20 turn |
| E6 | Tool error recovery | 40 | fault injection (timeout, 500, file terkunci, JSON rusak) | task selesai + tanpa eksekusi tak diminta |
| E7 | Terminal | 50 | subset Terminal-Bench (ID di-pin) | grader resmi |
| E8 | Gateway Telegram | 20 | fake Bot API server: allowlist, pairing, approval inline, restart | assert event |
| E9 | Skill routing | 50 | query berlabel skill dari 211 skill | top-3 match |
| E10 | Prompt injection | 100 | payload di web/file/MCP/chat/skill | oracle aksi terlarang |

Eval set disimpan di `eval/` dengan manifest SHA-256; perubahan = versi eval baru (`evalset/v2`), hasil lintas versi tidak dibandingkan.

### 2.3 Harness
- `bench/h2h` (F0): menjalankan hermes dan Damn-Bot per task, mencatat request/response lewat proxy rekam (mitm lokal), menghitung token/biaya/waktu dari data proxy — bukan dari laporan agent sendiri. ADR-075: di F0/F1 run berurutan per agent; run berpasangan dengan urutan diacak dan container wajib untuk RC F9. Sebelum smoke Damn-Bot F1: cap biaya ledger persisten (`bench/h2h/results/f1-spend.json`, reservasi proyeksi terburuk), oracle netral-agent (kanal jawaban final + pemetaan tool → `exec`/`fs_write`/`read` per adapter), dan isolasi (container rootless atau user+net namespace, env allowlist, pindai kunci di rekaman).
- Fake provider (latensi 0 atau profil latensi terekam) untuk metrik overhead; provider nyata untuk metrik kualitas dan end-to-end.

### 2.4 Jumlah run & statistik
- Kualitas: 3 run per task per agent. Pass rate per kategori; selisih Damn-Bot − hermes dengan bootstrap 95% CI (10.000 resample, dipasangkan per task). "≥ hermes" = batas bawah CI > −3 pp; "menang + X pp" = estimasi titik ≥ X dan batas bawah CI > 0.
- Waktu: ≥ 20 repetisi per operasi; bandingkan median; rasio dengan bootstrap 95% CI; "≥ N× lebih cepat" = batas atas CI rasio (Damn-Bot/hermes) ≤ 1/N.
- Soak: 1 run 1.000 turn + 1 run 24 jam `serve`; metrik count (crash, hang, korupsi) harus 0 — tidak ada uji statistik.

### 2.5 Kapan dijalankan
- F0: baseline tanpa LLM (status per dimensi ADR-065) + smoke 10 task model murah untuk hermes (validasi harness saja).
- F1: smoke 10 task Damn-Bot (`h2h-smoke/v1`, ADR-062), cap total US$2,00 ditegakkan ledger `bench/h2h` (ADR-075); hanya setelah hardening ADR-075 ada di kode; pass/fail informatif.
- PR: §3 tipe (a) (fake provider, tanpa biaya), size/LOC gates.
- Nightly: soak 1.000 turn & chaos suite (fake provider); §3 tipe (b) model murah hanya setelah ada build fungsional (F3+).
- Mingguan: scorecard otomatis (SPEC §18.5) setelah build fungsional, anggaran ditetapkan user.
- Release candidate (F9): seluruh scorecard, head-to-head penuh dengan model frontier (Q23-RC).

### 2.6 Artefak
`artifacts/scorecard/<damnbot-version>/<hermes-commit>/<evalset-version>/` berisi `results.jsonl` (per task, per run), `summary.md` (tabel ini terisi), rekaman proxy (diredaksi), versi model, hash mesin. Dibandingkan terhadap artefak RC sebelumnya untuk deteksi regresi.

---

## 3. Speed parity gate

Setiap baris [`FEATURE-PARITY.md`](FEATURE-PARITY.md) yang dapat dieksekusi (CLI command, slash command, tool, aksi platform, operasi memori/sesi/skill, cron, startup, resume sesi, search sesi, compaction, update) mendapat benchmark head-to-head otomatis `BenchmarkSpeedParity/<ID>` di `bench/speed` (P28, V-20/ADR-054) pada mesin & input identik, wall-clock p50/p95.

### 3.1 Dua komponen waktu
- **(a) Dikendalikan agent**: startup, I/O, eksekusi tool (di luar runtime command user), DB, assembly prompt, parsing, render UI. Diukur dengan fake provider latensi 0 dan fake backend.
- **(b) Model/jaringan**: diukur end-to-end per task dengan provider nyata.

### 3.2 Target

| Kelas operasi (a) | Target vs hermes (median) | Contoh baris |
|---|---|---|
| Startup, resume sesi, command CLI non-LLM | ≥ 10× lebih cepat | CLI-*, OPS-011 doctor, OPS-012 status, MEM-013 resume |
| Tool file/grep/glob/read/edit, search sesi (FTS) | ≥ 10× lebih cepat | TOOL file tools, MEM-012 |
| Operasi lain (a): slash command, memori/skill ops, assembly, compaction tahap deterministik, aksi platform di luar jaringan, cron dispatch, update (di luar unduh) | ≥ 5× lebih cepat | SLASH-*, SKILL-*, AUTO-005, GW-* |
| Semua operasi (a) | tidak ada yang lebih lambat dari hermes | — |

| Kelas (b) | Target |
|---|---|
| Task end-to-end eval E1–E7 | median ≥ 2× lebih cepat dengan model sama; tidak ada task dengan median > hermes × 1,10 (margin noise) |

Teknik wajib untuk (b) (SPEC §17.2): prompt lebih kecil (TTFT turun), parallel tool calls + eksekusi tool read-only paralel, streaming ke UI sejak token pertama, prefetch konteks saat user mengetik (assembly + retrieval + koneksi provider di-warm, tanpa LLM call), cache hit prompt tinggi (P15), tidak ada LLM call tambahan di jalur kritis (judul/ringkasan/review di luar jalur, P11).

### 3.3 Aturan gate
- PR: setiap benchmark (a) `BenchmarkSpeedParity/<ID>` di `bench/speed` (V-20/ADR-054; dibuat di F1, ADR-077) dibandingkan dengan `main`; lebih lambat > 5% (median, CI 95% tidak memuat 0) = gagal. Rasio vs hermes dihitung ulang mingguan dan wajib memenuhi §3.2.
- Gate per fase (ADR-077): acceptance fase Fx = `BenchmarkSpeedParity/<ID>` untuk setiap baris FEATURE-PARITY berfase Fx (kolom `fase`, ADR-064) memenuhi §3.2 terhadap `bench/speed/results/hermes-868b9f244f.json`; sampel CLI ≥ 20 run per sisi, baseline hermes baris fase itu diukur ulang dengan 20 run sebelum acceptance.
- Ambang ×1,5 (ADR-077) hanya untuk metrik **waktu absolut** di runner CI bersama. Rasio terhadap referensi yang diukur di runner yang sama (`rg`, hermes, `main`) dipakai apa adanya.
- Nightly: (b) dengan model murah; regresi median > 10% vs nightly sebelumnya = issue otomatis berlabel `perf-regression` yang memblokir RC.
- Baris FEATURE-PARITY tanpa benchmark speed = parity tidak terpenuhi (§4).
- Operasi yang di hermes tidak ada padanan waktunya (fitur `replaced`) dibandingkan dengan operasi padanan yang tertulis di kolom `Catatan` baris FEATURE-PARITY.md (mode `replaced`, SPEC §8); padanan wajib diisi bersama kolom `bench` sebelum baris dihitung.

---

## 4. Aturan gate 1.0

1. Semua dimensi §1 memenuhi "Aturan menang"; dimensi **[I]** memenuhi margin yang ditetapkan. Ditegakkan `TestScorecardGate` di job `scorecard` tiap RC + mingguan (P20, V-20/ADR-054); anggaran LOC per paket ditegakkan `TestBudgetTableSum` (P21, ENGINEERING E-19).
2. Satu dimensi kalah (termasuk satu operasi §3 lebih lambat dari hermes) → **bukan rilis**. Tidak ada pengecualian tanpa ADR yang disetujui user dan dimensi tersebut tetap dilaporkan merah di catatan rilis.
3. 100% baris FEATURE-PARITY.md terpenuhi kecuali `dropped-with-approval` (persetujuan tercatat di DECISIONS.md ADR-022 + ADR-033, pemetaan NP → baris di FEATURE-PARITY.md).
4. 100% entri [`EDGE-CASES-hermes.md`](EDGE-CASES-hermes.md) berstatus `covered` (test lulus) atau `n/a` dengan alasan.
5. Artefak §2.6 untuk RC final dipublikasikan bersama rilis.

---

## 5. Dimensi di mana hermes punya keunggulan awal

| Keunggulan hermes | Bukti | Strategi Damn-Bot untuk melampaui | Metrik/gate |
|---|---|---|---|
| Luas ekosistem plugin | 645 entri `plugin-catalog/` (FEATURE-PARITY PLUG-009) | Ekstensi via MCP (standar lintas agent) + skill + hook (ADR-009); pemetaan kategori katalog ke MCP server publik atau modul built-in (Q18); `damnbot import hermes` membawa MCP config | % kategori katalog berpadanan ≥ 90% |
| 65 optional MCP | `optional-mcps/` (MCPX-*) | MCP client penuh (stdio/HTTP/SSE, OAuth PKCE/device/DCR, sampling) dan format `mcpServers` kompatibel → 65 manifest langsung terpakai | 65/65 `tools/list` sukses |
| 58 + 153 skill | `skills/`, `optional-skills/` | Format SKILL.md kompatibel; importer + threat scan; retrieval BM25 (lebih baik dari index penuh) | ≥ 211 skill tersedia; akurasi routing E9 |
| 17 bahasa UI | `locales/*.yaml` | String hermes (MIT) diimpor dengan atribusi + Bahasa Indonesia (Q16) | 0 key hilang, ≥ 17 bahasa |
| Kematangan edge-case 15 bulan | 55.582 commit; katalog [`EDGE-CASES-hermes.md`](EDGE-CASES-hermes.md) (166 entri) | Setiap edge-case menjadi regression test sebelum modul terkait dianggap selesai | 100% covered/n/a (gate §4) |
| Ekosistem Python untuk voice/ML lokal | openWakeWord, sherpa-onnx, Porcupine, whisper (MEDIA-003..011) | Backend lokal sebagai executable eksternal (whisper.cpp, piper, sherpa-onnx CLI) + `damnbot doctor voice`; backend cloud via HTTP | parity MEDIA 31/31; latensi voice §1.2 |
| Desktop app kaya fitur | `apps/desktop` Electron (UI-017) | Web UI embedded + wrapper opsional (ADR-011); parity per fitur dipetakan | parity UI 28/28 |
| Integrasi layanan Nous (Portal, Tool Gateway) | NP-19 | Tidak direplikasi (layanan proprietary); Nous sebagai provider tetap didukung | `dropped-with-approval` (ADR-022 NP-19, Accepted) |
| Basis pengguna & dokumentasi | `website/`, README multi-bahasa | Importer satu perintah, dokumentasi migrasi, perintah dan slash alias (NP-06) dipertahankan untuk muscle memory | dimensi Migrasi §1.4 |
