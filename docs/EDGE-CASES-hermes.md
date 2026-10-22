# EDGE-CASES-hermes — katalog bug nyata yang sudah diperbaiki hermes

Tujuan: regression test Damn-Bot agar rewrite Go tidak mengulang bug lama hermes-agent (snapshot `868b9f244f`, 2026-10-17). Repo hermes dibaca saja.

## Ringkasan per area

| Area | Prefix | Jumlah |
|---|---|---|
| Provider/streaming | `EC-PROV` | 30 |
| Tool-call parsing | `EC-TCALL` | 12 |
| Context/compaction | `EC-CTX` | 22 |
| State/SQLite | `EC-STATE` | 16 |
| File I/O & encoding | `EC-FIO` | 19 |
| Terminal/proses | `EC-TERM` | 10 |
| Windows | `EC-WIN` | 17 |
| Platform chat | `EC-PLAT` | 24 |
| Config/startup | `EC-CFG` | 8 |
| Keamanan | `EC-SEC` | 8 |
| **Total** | | **166** |

## Metode

```bash
cd /home/jelangkung/Dokumen/hermes-agent
git log --oneline | wc -l                                   # 55582 commit
git log --format='%h|%ad|%s' --date=short -E --grep='^fix(\(|:)' > /tmp/hm_fix_all.txt   # 27988 commit fix
head -1500 /tmp/hm_fix_all.txt                              # sampel 1500 terbaru (2026-10-10..17)
# distribusi scope 1500 terbaru: gateway 362, desktop 180, cli 95, (tanpa scope) 88, tui 74, plugin-catalog 45, onboarding 38, acp 32, …
sed -E 's/^[0-9a-f]+\|[^|]+\|fix\(([^)]*)\).*/\1/' /tmp/hm_fix_1500.txt | sort | uniq -c | sort -rn
```

Penambangan lanjutan untuk area yang masih tipis (Windows, File I/O, Platform chat) menyaring commit desktop/TUI/dashboard/catalog/onboarding/e2e/installer/test/release:

```bash
grep -iE 'telegram' /tmp/hm_fix_all.txt            # 419 commit
grep -iE 'windows|win32' /tmp/hm_fix_all.txt       # 482 commit
grep -iE 'encoding|utf-?8|utf-?16|crlf|\bbom\b|surrogate|unicode' /tmp/hm_fix_all.txt   # 221 commit
```

Sumber kedua: komentar kode yang memuat `#NNNNN` di file kritis (`agent/context_compressor.py`, `agent/conversation_compression.py`,
`agent/auxiliary_client.py`, `agent/agent_runtime_helpers.py`, `agent/credential_pool.py`, `hermes_state*.py`, `tools/budget_config.py`,
`tools/file_tools.py`, `tools/terminal_tool*.py`, `hermes_bootstrap.py`, `hermes_startup_watchdog.py`, `gateway/run.py`) — 869 baris referensi
(855 blok komentar unik) dipindai dengan regex `#\d{4,6}`, lalu dipilih yang pelajarannya umum.

Catatan seleksi:
- Sampel 1500 commit fix terbaru didominasi desktop/TUI/gateway-internal (spesifik Electron/arsitektur hermes) sehingga mayoritas entri diambil dari komentar kode berbasis issue, yang menjelaskan gejala+penyebab.
- Dibuang: bug murni Python (GIL, ContextVar, PEP 540), Electron/desktop, plugin-catalog, onboarding, Nous-portal-spesifik, kecuali pelajarannya umum.
- Kolom Sumber berisi `file:line` + `#issue` dari komentar, atau hash commit pendek.

## Provider/streaming

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-PROV-001 | Satu tool call ter-stream terpecah jadi beberapa call | Stream delta tool call LM Studio/Kimi/MiniMax | commit `3f3bdf3ee2` | ya — parser delta SSE umum | Rakit delta tool_call berindeks sama dari stream fragmen → tepat 1 call |
| EC-PROV-002 | Long-decode Gemini terputus kehilangan output | Stream Gemini drop di tengah decode panjang | commit `121aee41b6` | sebagian — jika adapter Gemini native | Simulasikan drop stream; pastikan resume dari checkpoint terakhir, bukan ulang dari nol |
| EC-PROV-003 | Call hilang saat ID item stream berotasi | Responses/Codex merotasi item ID | commit `5e24de476d` | ya — Responses API | Stream dengan item_id berubah tetap korelasi ke call_id yang sama |
| EC-PROV-004 | reasoning_content hilang saat replay tool call | Ollama Cloud menolak replay tanpa reasoning | commit `ce3f83088a` | ya | Replay riwayat assistant(tool_calls) mempertahankan field reasoning_content |
| EC-PROV-005 | Typed thinking block dibuang → 400 "thinking must be passed back" | DeepSeek V4 Pro mengembalikan blok thinking | `agent/agent_runtime_helpers.py:1528` #21944 | ya | Riwayat dengan blok thinking dikirim balik utuh pada turn berikut |
| EC-PROV-006 | Thinking berupa array JSON, bukan string | Backend non-strict (Mistral) via custom provider | `agent/agent_runtime_helpers.py:1532` #106006 | ya | Normalisasi reasoning list→string sebelum strip |
| EC-PROV-007 | Proxy kirim HTTP 200 dengan content kosong | Proxy OpenAI-compatible (one-api) menjawab kosong | `agent/context_compressor.py:3936` | ya | Respons 200 content kosong diperlakukan sebagai kegagalan, bukan hasil valid |
| EC-PROV-008 | finish_reason=length dianggap sukses | Ringkasan terpotong cap token output | `agent/context_compressor.py:3951` | ya | Output dengan finish_reason length ditandai partial dan tidak dipersist |
| EC-PROV-009 | Stream zombie tak pernah mati karena keepalive | Keepalive me-reset deadline no-progress | `agent/auxiliary_client.py:1368` | ya | Hanya payload substantif yang me-reset watchdog; keepalive tidak |
| EC-PROV-010 | Timeout bertumpuk 3×300 s → macet 20+ menit | Retry SDK + retry app + fallback | `agent/auxiliary_client.py:1189`, `:4749` #54465 | ya | Matikan retry SDK; satu pemilik budget retry; total waktu terbatas |
| EC-PROV-011 | temperature ditolak model reasoning, 400 berulang | Model tertentu menolak temperature | `agent/auxiliary_client.py:650` #51083 | ya | Cache penolakan per route; panggilan berikut tanpa temperature |
| EC-PROV-012 | reasoning_effort tidak dikenali endpoint | "Unrecognized request argument: reasoning_effort" | `agent/auxiliary_client.py:3423` #112781 | ya | Endpoint yang menolak parameter → retry tanpa parameter |
| EC-PROV-013 | Token EOS bocor ke jawaban final | Provider kompatibel mengirim token kontrol | `gateway/run.py:706` | ya | Strip token EOS dari final_response sebelum dikirim |
| EC-PROV-014 | 403 entitlement memicu loop refresh tanpa akhir | Token valid tetapi akun tak berhak | `agent/agent_runtime_helpers.py:909` #26847 | ya | 403 entitlement → berhenti, bukan refresh/retry |
| EC-PROV-015 | Indeks fallback tertinggal di ujung chain | Aktivasi fallback gagal | `agent/agent_runtime_helpers.py:1432` #20465 | ya | Fallback gagal tidak memblokir fallback berikutnya |
| EC-PROV-016 | Sesi tetap di fallback mahal selamanya | Primer pulih tetapi tak dicoba lagi | `agent/agent_runtime_helpers.py:1367` #114501 | ya | Setelah jendela kuota pulih, sesi kembali ke primer |
| EC-PROV-017 | Kredensial pool diikat ke host salah | Pool campuran satu provider beda endpoint (Azure) | `agent/credential_pool.py:642` #68237 | ya | Rotasi kredensial tidak memindahkan sesi ke host lain |
| EC-PROV-018 | Grant OAuth dicabut terus dirotasi tiap jam | Refresh gagal permanen dianggap exhausted | `agent/credential_pool.py:1210` #32849 | ya | invalid_grant → status DEAD, tidak masuk rotasi lagi |
| EC-PROV-019 | Refresh token terpakai di-replay | Singleton tertinggal satu rantai rotasi | `agent/credential_pool.py:1368` #106705 | ya | Refresh token lama tidak pernah dipakai ulang setelah rotasi |
| EC-PROV-020 | 400 "out of extra usage" ambigu membekukan kredensial | Anthropic: overage vs content filter | `agent/credential_pool.py:144` #82154 | ya | Billing ambigu → cooldown pendek saja |
| EC-PROV-021 | Kredensial env hilang menghapus entri pool di disk | Proses tanpa env var memuat pool | `agent/credential_pool.py:2972` #9331 | ya | Load pool non-destruktif terhadap entri env |
| EC-PROV-022 | Header Authorization hilang saat rebuild client | Key via key_cmd/Entra tidak di .api_key | `agent/auxiliary_client.py:4728` | ya | Rebuild client mempertahankan credential provider callback |
| EC-PROV-023 | Bare host lokal → 404 (Ollama/vLLM) | base_url tanpa /v1 | `agent/auxiliary_client.py:5078` #106010 | ya | Provider lokal alias menambahkan /v1 otomatis |
| EC-PROV-024 | Gambar dikirim ke model teks-only → 400 pasti | Fallback vision ke model tanpa vision | `agent/auxiliary_client.py:4312` #108349 | ya | Gate kapabilitas vision sebelum routing gambar |
| EC-PROV-025 | Fallback chain memilih model window lebih kecil | Kandidat fallback context lebih kecil dari permintaan | `agent/auxiliary_client.py:4340` #52392 | ya | Saring kandidat fallback berdasarkan context window |
| EC-PROV-026 | Region Bedrock aux berbeda dengan runtime utama | Aux client resolve region sendiri | `agent/auxiliary_client.py:4816` #53880 | sebagian | Aux call memakai region yang sama dengan runtime utama |
| EC-PROV-027 | Cache prompt 1% hit di Kimi/Qwen via OpenAI wire | Tanpa marker cache_control | `agent/agent_runtime_helpers.py:1812`, `:1886` | ya | Provider pendukung menerima marker cache_control pada wire OpenAI |
| EC-PROV-028 | client_kwargs dimutasi in-place, transport mati dipakai ulang | Mutasi dict konfigurasi bersama | `agent/agent_runtime_helpers.py:1990` | ya | Builder client tidak memutasi konfigurasi bersama |
| EC-PROV-029 | Pesan 429+auth diarahkan ke re-login | Envelope kuota berisi preamble auth | `gateway/run.py:618` #89401 | ya | Klasifikasi error: rate-limit menang atas auth |
| EC-PROV-030 | Connection reset dianggap endpoint down | Koneksi established putus mid-request | `gateway/run.py:404` #116323 | ya | Bedakan reset mid-request vs connect refused dalam pesan error |

## Tool-call parsing

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-TCALL-001 | tool_calls: [] → 400 "empty array" | Assistant dengan array tool_calls kosong (DeepSeek, Kimi) | `agent/agent_runtime_helpers.py:2898` #58755 | ya | Hapus key tool_calls kosong/non-list sebelum kirim |
| EC-TCALL-002 | Duplicate tool_call_id → 400 | ID hasil tool tercatat dua kali | `agent/agent_runtime_helpers.py:3070` #58327 | ya | Dedupe tool_call_id pada payload |
| EC-TCALL-003 | Hasil tool yatim / call tanpa hasil | Interupsi atau kompresi memindahkan hasil | `agent/agent_runtime_helpers.py:3001`, `:585` | ya | Call tanpa hasil diberi stub; hasil tanpa call dibuang |
| EC-TCALL-004 | Variasi ID Responses (call_id/id/item) memicu orphan | call_id vs fc_ id | `agent/agent_runtime_helpers.py:280`, `:3086` | ya | Semua alias ID satu call dicocokkan sebagai satu grup |
| EC-TCALL-005 | Tool call tak tertutup di teks | Parsing tool call XML-in-text | `agent/agent_runtime_helpers.py:69` #101899 #102303 | sebagian — hanya bila parse text tool calls | Blok tool call tanpa penutup dibuang tanpa memakan teks lain |
| EC-TCALL-006 | Nama tool berisi sisa XML/atribut | Model menulis `session_search" parameter=...` | `agent/agent_runtime_helpers.py:2647` | ya | Repair nama tool memotong di karakter XML/quote pertama lalu fuzzy match |
| EC-TCALL-007 | Teks ditambahkan setelah JSON hasil tool | Warning ditempel ke string JSON | `tools/file_tools.py:1131` #90322 | ya | Hasil tool tetap JSON valid (warning sebagai field) |
| EC-TCALL-008 | path kosong/null tidak memakai default | Argumen eksplisit null | `tools/file_tools.py:1373` #112424 | ya | Arg null/blank → default schema |
| EC-TCALL-009 | Final answer hanya fragmen (kata salah skrip) | Stop teks degeneratif setelah kerja tool | `agent/agent_runtime_helpers.py:3367` #103483 | ya | Deteksi final degeneratif dan lanjutkan/retry |
| EC-TCALL-010 | Schema tool deferred memicu INVALID_ARGUMENT | tool_search menunda MCP tools | `agent/agent_runtime_helpers.py:3143` | sebagian | Payload hanya memuat tool yang dideklarasikan |
| EC-TCALL-011 | Bridge Codex: role=tool bocor sebagai string | Konverter chat→Responses privat berbeda | `agent/auxiliary_client.py:1403` | ya | Satu konverter chat→Responses dipakai semua jalur |
| EC-TCALL-012 | Padding " " dari primer merusak fallback | DeepSeek mengisi content spasi; provider lain menolak | `agent/agent_runtime_helpers.py:3512` #45655 | ya | Normalisasi content kosong/spasi saat ganti provider |

## Context/compaction

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-CTX-001 | Kompresi tidak efektif → kompaksi tiap turn selamanya | System prompt + schema tool ≥ threshold | `agent/context_compressor.py:2927` #14695 | ya | Anti-thrash: verdict efektivitas berdasarkan prompt tokens nyata |
| EC-CTX-002 | Anti-thrash permanen memblok kompaksi | Breaker trip tanpa jalur pulih | `agent/context_compressor.py:3084` #14694 | ya | Setelah jendela recovery, satu probe diizinkan |
| EC-CTX-003 | Ringkasan pertama gagal → marker fallback disisip tiap turn | Tidak ada cooldown summary | `agent/context_compressor.py:3055` #11529 | ya | Kegagalan summary memasang cooldown |
| EC-CTX-004 | Kompresi menghancurkan middle saat provider down | Network/empty-content saat summary | `agent/context_compressor.py:4237` #29559 | ya | Kegagalan terminal → abort, transcript tidak berubah |
| EC-CTX-005 | Overload terus-menerus → session wipe | Semua attempt overload abort | `agent/context_compressor.py:796` #123167 | ya | Setelah N overload, commit ringkasan deterministik |
| EC-CTX-006 | Skill dianggap masih termuat setelah dipangkas | Compaction meringkas hasil skill_view | `agent/context_compressor.py:849` #32106 | ya | Marker [SKILL_PRUNED] kanonik dipertahankan (`TestEdgeCases/EC-CTX-006`) |
| EC-CTX-007 | Direktif MEDIA ikut ke ringkasan → kirim lampiran palsu | Directive delivery di transcript | `agent/context_compressor.py:1159` #14665 | sebagian | Direktif delivery dibuang sebelum summarizer |
| EC-CTX-008 | Gambar base64 lama ikut tiap request → 413 | Gambar pertama/tool screenshot tidak pernah dievict | `agent/context_compressor.py:1605` #89938, `:4820` | ya | Hanya gambar terbaru dikirim penuh; lama diganti placeholder |
| EC-CTX-009 | Ringkasan menyuruh "do nothing" pada cron | Satu-satunya user turn ada di head | `agent/context_compressor.py:511` #100818 | ya | Tugas aktif di-restate setelah boundary ringkasan |
| EC-CTX-010 | Summary harus role=user untuk Anthropic | Head hanya system, summary pesan pertama | `agent/context_compressor.py:5428` #52160 #58753 | ya | Summary tanpa user turn sebelumnya → role user |
| EC-CTX-011 | Pemotongan memisahkan assistant(tool_calls) dan tool | Cut di tengah grup tool | `agent/context_compressor.py:5006` #22566 | ya | Batas kompresi selalu selaras grup tool |
| EC-CTX-012 | Turn agentik besar → middle kosong, sesi macet | Anchor assistant ke turn sebelumnya | `agent/context_compressor.py:5152` #131412 | ya | Cut di batas grup tool bila turn aktif melebihi ceiling |
| EC-CTX-013 | Model menyalin ringkasan sebagai input baru | Tanpa end marker | `agent/context_compressor.py:5649` #11475 #33256 | ya | Ringkasan punya penanda akhir eksplisit |
| EC-CTX-014 | Ringkasan sesi lain bocor setelah /new | _previous_summary tidak direset | `agent/context_compressor.py:2266` #38788, `:5210` #83248 | ya | Reset sesi membersihkan state ringkasan |
| EC-CTX-015 | Kompresi memperbesar transcript (379K→687K) | Summary + retained tail lebih besar | `agent/conversation_compression.py:3150` | ya | Commit ditolak bila hasil lebih besar dari input |
| EC-CTX-016 | Interupsi user membatalkan summary → marker statis | Pesan masuk saat kompresi | `agent/context_compressor.py:3896` #23975 | ya | Panggilan summary atomik terhadap interupsi biasa |
| EC-CTX-017 | Output reservation diabaikan → 400 sebelum kompaksi | Budget input = context - max_tokens | `agent/context_compressor.py:2857` #43547 | ya | Threshold memperhitungkan max_tokens output |
| EC-CTX-018 | Estimasi kasar saja → kompresi tak pernah jalan | Tanpa usage provider | `agent/context_compressor.py:2988` #2153 | ya | Fallback ke estimasi bila usage tidak tersedia |
| EC-CTX-019 | Hasil tool pending di-stub → tool dijalankan ulang | Pass prune menyentuh ronde tool belum dibaca | `agent/context_compressor.py:3298` #61932 | ya | Ronde tool pending tidak dipangkas |
| EC-CTX-020 | Index out of range saat end > len | Tail cut mengembalikan len+1 | `agent/context_compressor.py:4498` #75588 | ya | Bounds dijepit defensif |
| EC-CTX-021 | Pesan baru selama kompresi hilang | Append bersamaan dengan compaction | `hermes_state_messages.py:228` #75316 | ya | Watermark: baris setelah start dikloning ke transcript baru |
| EC-CTX-022 | Rollback kompresi menduplikasi transcript | Raise sebelum archive selesai | `agent/conversation_compression.py:3781` #99477 | ya | Kegagalan commit tidak menyisakan marker persisted |

## State/SQLite

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-STATE-001 | SQLITE_IOERR transien di pembaca read-only | Checkpoint WAL bersamaan | `hermes_state.py:182`, `:747` #100436 #100871 | ya (bila SQLite+WAL) | Retry terbatas untuk IOERR transien pada pembaca |
| EC-STATE-002 | TRUNCATE checkpoint tiap close balapan dengan writer | Banyak open/close pendek (cron) | `hermes_state.py:1558` #45383 | ya | Hanya pemilik jangka panjang yang checkpoint TRUNCATE |
| EC-STATE-003 | Lock contention saat init schema tanpa retry | Proses lain memegang write lock | `hermes_state.py:832` | ya | DDL init dengan busy_timeout/retry |
| EC-STATE-004 | EMFILE karena read pool per profil | Gateway multiplex banyak profil | `hermes_state_readpool.py:150` #98573 | ya | Batas global koneksi + fallback ke writer |
| EC-STATE-005 | Dua writer ke file yang sama dalam satu proses | Handle redundan | `hermes_state_registry.py:57` #102827, `hermes_state.py:454` #90837 | ya | Registry: satu writer per path per proses |
| EC-STATE-006 | Reopen menulis ke generasi file baru | File di-restore/mv out-of-band | `hermes_state.py:1272` | ya | Deteksi identitas file berubah, tolak tulis |
| EC-STATE-007 | Korupsi FTS dianggap korupsi seluruh DB | Error fts5 diklasifikasi disk | `hermes_state_errors.py:292` #97794 | ya | Klasifikasi error indeks FTS terpisah |
| EC-STATE-008 | "database disk image is malformed" masuk bucket disk | Urutan substring check | `hermes_state_errors.py:109` | ya | Cek malformed sebelum cek disk |
| EC-STATE-009 | Rebuild FTS penuh menjenuhkan I/O | state.db besar | `hermes_state_common.py:925` #68858 | ya | Rebuild FTS inkremental/terbatas |
| EC-STATE-010 | Query sesi membaca & sort seluruh tabel | Statistik planner usang | `hermes_state_common.py:163` #119403 | ya | Index hint/ANALYZE untuk query per sesi |
| EC-STATE-011 | Timestamp rusak memaku recency 5e+246 | Satu baris timestamp buruk | `hermes_state_common.py:262` #91536 | ya | Clamp/abaikan timestamp tidak valid |
| EC-STATE-012 | Flush cursor basi melewati pesan baru | Cursor turn sebelumnya tidak direset | `gateway/run.py:4159` #44327 | ya | Reset cursor flush tiap turn baru |
| EC-STATE-013 | Pesan user terduplikasi saat re-encode | Marker persisted hilang | `agent/agent_runtime_helpers.py:762` #125331 | ya | Persist idempotent per baris |
| EC-STATE-014 | Walk lineage kompresi terpotong di 100 | Lineage >100 generasi | `hermes_state_compression.py:80` #125041 | ya | Walk lineage tanpa batas rendah, dengan cycle guard |
| EC-STATE-015 | Fixture tes menulis ke DB produksi | Env tes dengan HERMES_HOME asli | `hermes_state.py:215` #82770 | ya | Guard: proses tes tidak bisa membuka store produksi |
| EC-STATE-016 | Flock diwarisi child fork → lock basi | Lock lintas proses | `hermes_state_common.py:1230` #100108 | ya | Deteksi holder mati vs hidup; fail closed bila tak pasti |

## File I/O & encoding

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-FIO-001 | Overwrite file yang belum pernah dibaca/berubah | write_file tanpa read sebelumnya | `tools/file_tools.py:897` #65604 | ya | Tolak overwrite konten yang tidak pernah/berbeda dibaca |
| EC-FIO-002 | File kosong/gagal dibuat di sesi panjang | Env dibersihkan, cwd hilang | `tools/file_tools.py:343` #26211 | ya | Cwd sesi dipertahankan setelah env cleanup |
| EC-FIO-003 | search_files kosong di container | cwd host dipakai sebagai -w docker | `tools/file_tools.py:293` #50636 | ya | Validasi cwd terhadap backend sebelum eksekusi |
| EC-FIO-004 | Read file sangat besar membanjiri konteks | Tanpa cap byte+baris | `tools/file_tools.py:73` | ya | read_file punya cap ganda baris dan byte |
| EC-FIO-005 | Lone UTF-16 surrogate crash kirim pesan | Output model berisi surrogate | `gateway/run.py:695` | ya | Sanitasi surrogate sebelum encode/kirim |
| EC-FIO-006 | Cap per-tool melebihi window model | Registry cap tidak dijepit | `tools/budget_config.py:63` #23767 | ya | Cap hasil tool dijepit ke window model |
| EC-FIO-007 | JSON/config dengan BOM gagal di-parse | File disimpan Notepad (UTF-8 BOM) | commit `25ce047f98`, commit `5705b68f70` | ya | Pembaca config/JSON menerima BOM UTF-8 |
| EC-FIO-008 | File tulisan agent diberi BOM tak diinginkan | Penulis memakai utf-8-sig | commit `09a637c143` | ya | Penulisan file tidak pernah menambahkan BOM |
| EC-FIO-009 | Lone surrogate menyebabkan UnicodeEncodeError saat tulis JSON | Teks model berisi surrogate | commit `4157494d2f`, commit `0f76689076` | ya | Penulisan JSON/HTTP body meng-escape atau membuang surrogate |
| EC-FIO-010 | Range edit LSP meleset pada teks non-ASCII | Offset dihitung dalam code point, LSP minta UTF-16 | commit `d23d6e8218` | ya | Range LSP dihitung dalam unit UTF-16 (emoji/CJK) |
| EC-FIO-011 | Patch menduplikasi teks saat batas edit di tengah ekspansi unicode | Normalisasi unicode pada replace | commit `e90857ba07`, commit `13f25e5fd9` | ya | Patch pada teks dengan karakter ternormalisasi tidak menggandakan region |
| EC-FIO-012 | Output anak proses non-UTF-8 crash decode | Tool eksternal mengeluarkan byte invalid | commit `526a0015fb`, commit `b00b4bb7f2` | ya | Decode output proses dengan replacement, tidak panic |
| EC-FIO-013 | CRLF dan LF dihitung berbeda | File checkout Windows (autocrlf) | commit `791dc63c05` | ya | Metrik/diff menormalisasi akhir baris |
| EC-FIO-014 | Nama file non-UTF-8 hilang dari log/prune | Filesystem dengan nama byte arbitrer | commit `788976407a` | ya | Nama file invalid UTF-8 tetap dapat diproses dan dilaporkan |
| EC-FIO-015 | Pseudo-file kernel dibaca dengan encoding locale | /proc/* pada locale non-UTF-8 | commit `2be53ecd7b` | sebagian | Baca /proc dengan UTF-8 eksplisit |
| EC-FIO-016 | Pencarian CJK rusak di SSE | Body SSE di-decode dengan charset default | commit `28cdd72815` | ya | Body SSE selalu di-decode UTF-8 |
| EC-FIO-017 | Stream terkompresi brotli gagal di-decode | Accept-Encoding brotli + streaming | commit `9a8748796d` #59556 | ya | Klien streaming hanya mengiklankan encoding yang didukung |
| EC-FIO-018 | Separator U+2028/U+2029 memecah NDJSON | Unicode line separator di dalam string | commit `992b517fdd` | ya | Parser NDJSON hanya split pada \n |
| EC-FIO-019 | Nama zona waktu locale berisi surrogate | strftime pada locale Windows | commit `58694737e2` | sebagian | Format waktu tahan nama zona non-ASCII |

## Terminal/proses

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-TERM-001 | Buffer stdout membunuh proses background | Pipe tak dibaca | `tools/terminal_tool_background.py:36`, commit `5a1916cc5c` | ya | Proses background menulis ke file log, bukan pipe |
| EC-TERM-002 | Exit karena sinyal dilaporkan sebagai kode aneh | Popen -signum | `tools/terminal_tool_result.py:99` | ya | Kode negatif → pesan "terbunuh sinyal X" |
| EC-TERM-003 | Container diganti mid-command tanpa info | Sandbox dihapus out-of-band | `tools/terminal_tool_result.py:82` | ya | Hasil menyertakan peringatan sandbox diganti |
| EC-TERM-004 | Profil B memakai sandbox/cwd profil A | Task id sama lintas profil | `tools/terminal_tool.py:471` #123989 | ya | Kunci sandbox dikualifikasi per profil/sesi |
| EC-TERM-005 | Policy terminal ambient dipakai saat profil gagal resolve | Env TERMINAL_* sisa turn lain | `tools/terminal_tool.py:1097` #68559, `gateway/run.py:1826` | ya | Fail closed bila policy tidak bisa diresolve |
| EC-TERM-006 | Model mengulang perintah yang tak bisa dipindai | Guard menolak tanpa alasan | `tools/terminal_tool_guards.py:319` #113944 | ya | Penolakan menyebut alasan agar tidak loop |
| EC-TERM-007 | Probe proses kernel macet hampir 1 jam | psutil create_time di macOS | `tools/terminal_tool.py:1431` | sebagian | Semua probe proses dibatasi timeout |
| EC-TERM-008 | Thread worker macet menahan exit interpreter | Executor tanpa timeout | `gateway/run.py:5185` #53107 | ya | Shutdown punya backstop force-exit |
| EC-TERM-009 | EMFILE → gateway zombie | Kebocoran file descriptor | `gateway/run.py:3223` #37011 | ya | Uji kebocoran FD pada siklus turn berulang |
| EC-TERM-010 | Startup watchdog: disarm vs fire balapan | Transisi state tanpa lock | `hermes_startup_watchdog.py:88` #89750 | ya | Hanya satu transisi menang (armed→disarmed/firing) |

## Windows

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-WIN-001 | Stdio non-UTF-8 crash di Windows | Code page konsol | `hermes_bootstrap.py:304` #69413 | ya (Go juga perlu konsol UTF-8) | Output unicode ke konsol cp1252 tidak crash |
| EC-WIN-002 | Session resume rusak di Windows native | Marker PID basi | `gateway/run.py:4299` #33778 | ya | Resume sesi diuji di Windows |
| EC-WIN-003 | Gateway terlihat orphan (tanpa systemd) | Scheduled Task/VBS | `gateway/run.py:4713` | sebagian | Deteksi supervisi Windows |
| EC-WIN-004 | Launcher Windows lama menjalankan gateway kedua | Nama task tanpa suffix profil | `hermes_cli/gateway_windows_legacy.py:1` #116157 | sebagian | Install/uninstall membersihkan launcher lama |
| EC-WIN-005 | fcntl palsu terimpor di Windows | Modul pihak ketiga bernama fcntl | `hermes_state_lockguard.py:48` | sebagian | Gate lock berdasarkan OS, bukan ketersediaan modul |
| EC-WIN-006 | Output schtasks UTF-16/lokal salah decode → crash | Query Scheduled Task di Windows berlokal non-Inggris | commit `133004ac79`, commit `2da76e8672` | ya — service Windows | Decode output tool OS secara ketat, lalu fallback; tidak pernah panic |
| EC-WIN-007 | Volume docker dengan drive letter rusak | Path `C:\…` diterjemahkan ke mount docker | commit `0108ed7b86`, commit `e74f2b8621` | ya | Translator path mempertahankan drive letter pada volume |
| EC-WIN-008 | Peer named pipe salah diidentifikasi via PID | Control pipe Windows | commit `e665047411` | ya | Autentikasi peer pipe pakai token, bukan PID |
| EC-WIN-009 | Proses keluar dengan kode 259 dianggap masih hidup | STILL_ACTIVE (259) sebagai exit code nyata | commit `fc7d24650d` | ya | Exit code 259 dari proses yang sudah keluar diperlakukan selesai |
| EC-WIN-010 | Checkout gagal di path profil panjang | MAX_PATH 260 karakter | commit `3b63f7b0a4`, commit `855997b7d7` | ya | Operasi file di path >260 karakter berhasil (long-path prefix) |
| EC-WIN-011 | index.lock git yatim memblok update | git dibunuh di tengah operasi | commit `b773ba044e` | sebagian | Lock git basi dibersihkan hanya bila tidak ada git aktif di checkout |
| EC-WIN-012 | Path Git Bash (`/c/Users`) tidak dikenali API file | Shell Git Bash di Windows | commit `3251a180f0` | ya | Normalisasi path MSYS ↔ Windows di file tools |
| EC-WIN-013 | Perbandingan cwd gagal karena case/separator | Identitas path Windows case-insensitive | commit `8267406bfb` | ya | Kesetaraan path memakai normalisasi case + separator |
| EC-WIN-014 | Uninstall/rename gagal karena file terkunci | File plugin dimuat / dipegang antivirus | commit `61b7f957ba`, commit `2d66d6e2f8` | ya | Rename/delete retry singkat saat sharing violation |
| EC-WIN-015 | Deadlock pada pipe multiplexer bersama | Panggilan sinkron lewat satu named pipe | commit `773d63b358` | ya | Dua request bersamaan lewat pipe yang sama tidak deadlock |
| EC-WIN-016 | Pipe cp1252 merusak output anak proses | Encoding pipe default Windows | commit `b0cead1d48` | ya | Proses anak dipaksa UTF-8; parent decode UTF-8 eksplisit |
| EC-WIN-017 | Konsol PowerShell bukan UTF-8 | Skrip aktivasi tanpa set encoding | commit `612ce5b5a9` | ya | Launcher Windows set console code page UTF-8 |

## Platform chat

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-PLAT-001 | Connect Telegram lambat menahan semua platform | Cold start 180 s | `gateway/run.py:61` #85993 | ya | Connect satu platform tidak memblok platform lain |
| EC-PLAT-002 | Gateway crash pada TimedOut Telegram | Exception transien tak tertangani | `gateway/run.py:550` #31066 #31110 | ya | Error transien adapter tidak menjatuhkan gateway |
| EC-PLAT-003 | Topik Telegram ambigu pada target cron | chat_id positif + thread | `gateway/run.py:2180` #22773 #52060 | ya | Parsing target topik/thread dibedakan |
| EC-PLAT-004 | Balasan kosong tidak terkirim setelah kerja tool | api_calls>0 tanpa teks | `gateway/run.py:3090` #18765 | ya | Turn tanpa teks final mengirim notifikasi |
| EC-PLAT-005 | Pesan setelah /stop dibuang diam-diam | Race generasi | `gateway/run.py:3132` | ya | Pesan baru setelah stop selalu diproses |
| EC-PLAT-006 | Gambar turn lama tertempel ke balasan baru | Isolasi media antar turn | `gateway/run.py:1452` #34608 | ya | Media hanya dari turn saat ini |
| EC-PLAT-007 | Profil sibling memakai baris DM Telegram | Peer tuple identik antar bot | `hermes_state_gateway.py:521` #74285 | ya | Routing dikunci per profil/bot |
| EC-PLAT-008 | Pairing store bentrok antar proses | Edit bersamaan | commit `5fbc692a6b` | ya | Edit pairing diserialisasi lintas proses |
| EC-PLAT-009 | Penutup code fence ber-CRLF tidak dikenali (Telegram) | Pesan dengan akhir baris CRLF | commit `3480de0402` | ya | Formatter mengenali fence ``` diakhiri \r\n |
| EC-PLAT-010 | Triple backtick inline merusak formatting (Telegram) | Regex fence tidak di-anchor | commit `5f7b1e2d65`, commit `c82abc309a` | ya | Backtick inline tidak dianggap blok kode |
| EC-PLAT-011 | Batas 4096 dihitung dalam karakter, bukan UTF-16 (Telegram) | Emoji/CJK pada pesan panjang | commit `edd9fd66a4` | ya | Chunking Telegram menghitung panjang dalam unit UTF-16 |
| EC-PLAT-012 | Kartu approval melebihi 4096 setelah escape HTML (Telegram) | Escaping menambah panjang | commit `b502504b74` | ya | Ukur panjang setelah escaping sebelum kirim |
| EC-PLAT-013 | Balasan ter-split mengirim ulang bagian awal (Telegram) | Chunk ditolak di tengah, lalu retry | commit `595f3a289c` | ya | Retry melanjutkan dari chunk yang gagal; urutan per chat terjaga |
| EC-PLAT-014 | Edit di-flood mengulang dalam jendela penalti (Telegram) | 429 dengan retry_after | commit `75d92e3d7b`, commit `fb2f3e213a` | ya | Edit/send menunggu retry_after; pacing per chat |
| EC-PLAT-015 | Update satu chat diproses tidak berurutan (Telegram) | Dispatch konkuren | commit `a6d7e08fd1`, commit `5907983b96` | ya | Konkuren antar chat, berurutan dalam satu chat |
| EC-PLAT-016 | Polling macet tanpa pemulihan (Telegram) | Updater tidak quiesce setelah stall | commit `4a7ed0029d` #113618 | ya | Stall terkonfirmasi → adapter dibangun ulang |
| EC-PLAT-017 | Send Bot API menggantung tanpa batas (Telegram) | Tanpa deadline wall-clock | commit `04057250a3`, commit `85e673b4a5` | ya | Setiap send punya deadline; media deadline lebih panjang |
| EC-PLAT-018 | Reply anchor bukan message id menggagalkan kirim (Telegram) | reply_to berisi ID non-numerik | commit `29cda7a5ef` #139611 | ya | Anchor invalid → kirim tanpa reply |
| EC-PLAT-019 | Update ganda diproses setelah restart (Telegram) | Receipt update hilang saat rebuild | commit `a1838ea87a` #120257 | ya | Dedupe update_id persisten lintas restart |
| EC-PLAT-020 | Callback tombol tanpa cek otorisasi ulang (Telegram) | Callback model picker | commit `999b19b6ed` | ya | Callback query dicek allowlist sama seperti pesan |
| EC-PLAT-021 | Allowlist berbentuk string JSON salah di-split (Telegram) | Env `["1","2"]` di-split koma | commit `5cc9b31b7a`, commit `122ad719b5` | ya | Parser allowlist menerima JSON list dan CSV |
| EC-PLAT-022 | Bot membalas quote-reply bot lain tanpa mention (Telegram) | Grup multi-bot | commit `1a981d8e83` #106430 | ya | Pesan bot ke bot butuh @mention eksplisit |
| EC-PLAT-023 | Upload gambar besar gagal/terkompresi buruk (Telegram) | Batas ukuran foto | commit `fc73bb462e` | ya | Gambar besar dikompres ke JPEG sebelum upload |
| EC-PLAT-024 | Timeout baca body memutus kepemilikan sesi (WhatsApp) | Bridge HTTP lambat | commit `20bd00439c` | sebagian | Timeout body tidak melepas ownership bridge |

## Config/startup

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-CFG-001 | .env basi mengalahkan config.yaml | Guard "not in os.environ" | `gateway/run.py:2087` | ya | Precedence config terdokumentasi diuji |
| EC-CFG-002 | Edit config butuh restart gateway | Nilai di-cache saat start | `gateway/run.py:3249` #115635 | ya | Reload config live untuk setting yang didukung |
| EC-CFG-003 | Probe katalog HTTP sebelum turn pertama | Probe jaringan di jalur kritis | `gateway/run.py:960` #106064 #105986 | ya | Startup tidak menunggu probe jaringan |
| EC-CFG-004 | IPv6 hang memperlambat koneksi | Happy eyeballs tidak dipakai | `hermes_bootstrap.py:194` #114265 | ya | Koneksi dual-stack berlomba IPv4/IPv6 |
| EC-CFG-005 | Install campuran/basi memicu fallback salah | Modul lama tersisa | `agent/auxiliary_client.py:177` #64333 | sebagian | Deteksi versi biner/konfigurasi tidak cocok |
| EC-CFG-006 | Konstruktor kompresor memblok karena probe context | Probe sinkron di __init__ | `agent/context_compressor.py:2889` #32221 | ya | Konstruksi agent tanpa I/O jaringan |
| EC-CFG-007 | Override context_length per model diabaikan | custom_providers tidak dibaca | `agent/context_compressor.py:2823` #83324 | ya | Override context length per model dihormati |
| EC-CFG-008 | Pool kredensial tidak dimuat ulang saat switch provider | Mismatch pool.provider | `agent/agent_runtime_helpers.py:2473` #52727 | ya | Switch provider memuat pool provider baru |

## Keamanan

| ID | Gejala | Pemicu/kondisi | Sumber | Relevan untuk Damn-Bot? | Usulan regression test |
|---|---|---|---|---|---|
| EC-SEC-001 | Secret bocor ke chat lewat pesan error | Error provider berisi token | `gateway/run.py:581` #48456, `:571` #23810 | ya | Pesan error ke user selalu diredaksi |
| EC-SEC-002 | Kredensial gh CLI ambient dipakai diam-diam | Fallback aux tanpa konfigurasi eksplisit | `agent/auxiliary_client.py:2173` #114740 | ya | Kredensial ambient hanya dengan opt-in eksplisit |
| EC-SEC-003 | ~/.claude/.credentials.json dibaca tanpa izin | Auto-discover kredensial eksternal | `agent/credential_pool.py:2563` | ya | Kredensial eksternal hanya dibaca bila provider dikonfigurasi |
| EC-SEC-004 | Identitas OAuth dikirim ke gateway pihak ketiga | Host Anthropic-compatible lain | `agent/auxiliary_client.py:2919` #114967 | ya | Header OAuth hanya untuk host resmi persis |
| EC-SEC-005 | Nilai header kustom ter-log | Header entry provider | `agent/auxiliary_client.py:5144` #109595 | ya | Header sensitif tidak pernah ditulis ke log |
| EC-SEC-006 | Webhook pihak ketiga bisa eksekusi file/terminal | Payload tidak tepercaya | `toolsets.py:41` | ya | Toolset webhook terbatas web/vision/clarify |
| EC-SEC-007 | Dump request menulis "Bearer None"/key | Debug dump | `agent/agent_runtime_helpers.py:1566` #24293 | ya | Dump debug meredaksi Authorization |
| EC-SEC-008 | Regex token Telegram rentan ReDoS | Teks panjang berdigit dipindai redaktor | commit `70f79c8cde` | ya | Regex redaksi token di-anchor; uji input patologis selesai cepat |

## Batasan

- Tidak semua 27.988 commit fix dibaca; pemilihan berbasis kata kunci + komentar kode bernomor issue.
- Area Windows, File I/O, dan Platform chat sudah ditambang ulang dengan grep kata kunci. Platform non-Telegram (Discord, Slack, Matrix, dll.) masih tipis.
- Isi issue GitHub (`#NNNNN`) tidak dibuka; gejala diambil dari komentar kode/subjek commit.
