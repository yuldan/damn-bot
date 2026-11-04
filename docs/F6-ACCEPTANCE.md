# F6-ACCEPTANCE — Ledger acceptance F6 (web, browser, media, voice, computer-use)

Status: **ledger hidup** untuk `PHASE=F6` pada `origin/main` `965fd150`. Semua baris berstatus awal **`BELUM`**. Satu baris hanya boleh berubah menjadi `LULUS` (atau `N/A` dengan alasan tertulis) pada PR yang benar-benar menambahkan tes bernama di kolom "Tes yang harus ada" dan tes itu lulus; tidak ada status yang diubah oleh dokumen saja.

Sumber normatif: ROADMAP F6 (docs/ROADMAP.md:183-193), 86 baris parity berfase F6 (semua `planned` pada `965fd150`; docs/FEATURE-PARITY.md), kontrak modul SPEC §9.4/§9.5 (SPEC.md:878-887), 9 baris GAP F6 (SPEC.md:2924,2982,2988-2989,2999-3000,3004-3005,3007; docs/ROADMAP.md:236), EC F6 (docs/ROADMAP.md:58; docs/EDGE-CASES-hermes.md:77), dan carryover F5 (docs/F5-ACCEPTANCE.md:72,94-95,101). Nama paket dan baris budget mengikuti usulan ADR-309 (docs/DECISIONS.md).

Aturan gate:

- **Penutupan (ADR-323).** Baris hanya `LULUS` bila PR yang sama menambah tes bernama, mengubah baris parity F6 `planned`→`LULUS`, dan mengubah baris ledger `BELUM`→`LULUS`. Baris yang tidak dapat dibuktikan ditulis `N/A — <alasan teknis> (carryover F7)`, bukan `BELUM`. Setelah `PHASE` > F6, `TestF6LedgerClosed` dan `TestParityPhaseColumn` menolak sisa `BELUM`/`planned`; selama `PHASE=F6` ledger hidup dan gate tidak aktif.
- `PHASE=F6 make phase-acceptance` pada fase ini mensertifikasi **F5** (ADR-080/ADR-176), bukan F6. Baris F6 di bawah menjadi gate acceptance saat `PHASE` dinaikkan ke `F7`; sampai saat itu setiap PR F6 wajib menjalankan tes baris yang disentuhnya. Gate ADR-323 menutup lubang ini: penaikan `PHASE` ke F7 gagal bila masih ada `BELUM`/`planned`.
- Tidak ada `t.Skip` runtime (REL-15). Backend eksternal diuji dengan server palsu (fake CDP/HTTP/STT/TTS) atau build tag eksplisit; binary nyata dibangun sekali oleh harness `internal/e2e` (internal/e2e/harness_test.go:27,47).
- Nama tes bersifat usulan target, bukan implementasi. Nama yang sudah diwajibkan SPEC dipertahankan apa adanya: `TestCDPAllowlist`, `TestWebCacheIsolation`, `TestBudgetGuardTools`, `BenchmarkImagePreflight`, `TestVoicePipelineLatency`, `TestToolsLockComplete`, `FuzzCDPMessage`, `FuzzDocRead`, `TestEdgeCases/<EC-ID>`.

## 1. Acceptance ROADMAP F6

| Item ROADMAP F6 | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| MEDIA 29/31 | 29 baris MEDIA F6 `LULUS`; MEDIA-029 (Discord voice) dan MEDIA-031 (silk) `N/A` → F7 | tabel §6 (29 baris; semuanya `BELUM` di head ini) + `TestParityPhaseColumn`; MEDIA-029/031 `N/A — fase F7 di FEATURE-PARITY.md:862,864, bukan baris ledger F6 (carryover F7)` | BELUM |
| Ukuran gambar | 0 penolakan ukuran pada suite 50 gambar; resize 12 MP ≤ 150 ms | `BenchmarkImagePreflight`, `TestF6ImageSuite50`, `TestImagePreflightTargetNotLowered` | N/A — hardware: target 150 ms hanya sah di Q14; host ini 265–290 ms (carryover F7, tanpa klaim lulus) |
| Voice latency | overhead ujung-ucapan→audio-pertama ≤ 200 ms; TTS streaming mulai ≤ 300 ms setelah kalimat pertama LLM | `TestVoicePipelineLatency`, `TestF6VoiceLatencyBudget` (fake STT/LLM/TTS bertanda waktu) | LULUS |
| Smoke E3 | 10 task E3 dengan model murah lulus harness; artefak tersimpan | `TestF6E3Smoke10` + `bench/evals/results/F6-e3.json` | BELUM |
| EC-PROV-024 | gate kapabilitas vision sebelum routing gambar; model teks-only tidak pernah menerima gambar | `TestEdgeCases/EC-PROV-024` | LULUS |
| Test tertunda ADR-083 | cap tool non-LLM ditegakkan sebelum dispatch | `TestBudgetGuardTools` | BELUM |
| Baseline hermes F6 | dimensi ADR-065/083 berstatus F6 diukur untuk kedua agent | `TestF6HermesBaseline`, `TestF6BaselineArtifactSchema` + `bench/speed/results/hermes-868b9f244f-F6.json` | LULUS |
| GAP F6 (9) | sembilan baris SPEC §22.1 lulus verifikasinya | tabel §8; anggota (`BELUM` di head ini): `GAP-SEC-05`, `GAP-COST-05`, `GAP-COST-11`, `GAP-COST-12`, `GAP-TOOL-01`, `GAP-TOOL-02`, `GAP-TOOL-06`, `GAP-TOOL-07`, `GAP-TOOL-09` | BELUM |
| Carryover F5→F6 | tiga item DITUNDA/N/A F5 ditutup | tabel §10; anggota (`BELUM` di head ini): `F5-CODEINTEL-LANG`, `F5-BACKGROUND-WINDOWS`, `GAP-REL-14 residual` | BELUM |

Catatan **Baseline hermes F6**: `LULUS` di sini berarti **artefak terukur lengkap** (dua dimensi ADR-065/083 × dua agen, `TestF6HermesBaseline` + `TestF6BaselineArtifactSchema` hijau), bukan klaim target waktu tercapai. Artefak `bench/speed/results/hermes-868b9f244f-F6.json` diukur di workstation, bukan Q14. Verdict `image_preflight` = `fail`: Damn-Bot 265,0 ms dan hermes 453,5 ms terhadap target 150 ms yang hanya sah di Q14; target itu tetap `N/A — hardware` (ADR-323, §12) dan pengukuran Q14 tetap carryover F7. Verdict `voice_end_to_end` = `pass`: Damn-Bot 0,08 ms overhead / 0,05 ms TTS start, hermes 1,60 ms / 1,60 ms.

## 2. Ledger parity F6 — CLI (2)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| CLI-006 | `browser` (close-profile) | profil Chromium yang mengunci direktori ditutup; profil lain tidak disentuh; tanpa browser → error jelas, bukan panic | `TestF6CLI006BrowserCloseProfile` (unit) + `TestF6CLI006BrowserCloseProfileE2E` | LULUS | F6-04j |
| CLI-054 | `computer-use` (install, status, doctor, permissions, screen) | lima subperintah berjalan; tanpa cua-driver → `module_unavailable`; `permissions`/`screen` read-only | `TestF6CLI054ComputerUse` | BELUM | F6-06c |

## 3. Ledger parity F6 — SLASH (5)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| SLASH-063 | `/voice` on/off/tts/status | toggle tersimpan di config; status jujur; tanpa executable audio → `module_unavailable` | `TestF6SLASH063Voice` | BELUM | F6-05h |
| SLASH-064 | `/wake` on/off/status | listener hanya aktif opt-in; engine eksternal hilang → pesan instalasi | `TestF6SLASH064Wake` | BELUM | F6-05g |
| SLASH-084 | `/browser` connect/disconnect/status/use | koneksi CDP nyata/fake; `real_profile` butuh approval per sesi; disconnect menutup sesi | `TestF6SLASH084Browser` (TUI + gateway) | LULUS | F6-04h, F6-04j |
| SLASH-098 | `/paste` | gambar clipboard dibaca lewat executable eksternal; tidak ada gambar → pesan; isi tidak bocor ke log | `TestF6SLASH098Paste` | BELUM | F6-03a |
| SLASH-099 | `/image <path>` | gambar lokal dilampirkan dan melewati preflight; path di luar workspace mengikuti gate path | `TestF6SLASH099Image` | BELUM | F6-03a |

## 4. Ledger parity F6 — TOOL (24)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-006 | `browser_back` | kembali satu riwayat; tanpa riwayat → error typed, bukan no-op | `TestF6TOOL006BrowserBack` | LULUS | F6-04c |
| TOOL-007 | `browser_click` | klik berdasarkan ref snapshot; ref basi/asing → error tanpa klik | `TestF6TOOL007BrowserClick` | LULUS | F6-04c |
| TOOL-008 | `browser_console` | hanya membaca log console/buffer terbatas; body lewat redaksi; tidak mengeksekusi halaman | `TestF6TOOL008BrowserConsole` | LULUS | F6-04c |
| TOOL-009 | `browser_get_images` | daftar gambar + ukuran; hasil `<untrusted>` | `TestF6TOOL009BrowserGetImages` | LULUS | F6-04c |
| TOOL-010 | `browser_navigate` | tolak loopback/privat/link-local/CGNAT/metadata dan blocklist; dial ke IP tervalidasi; hasil `<untrusted>` | `TestF6TOOL010BrowserNavigate` | LULUS | F6-04c, F6-01e |
| TOOL-011 | `browser_press` | tombol key didispatch ke halaman; key tak dikenal → `invalid_args` | `TestF6TOOL011BrowserPress` | LULUS | F6-04c |
| TOOL-012 | `browser_scroll` | scroll halaman/elemen dengan clamp batas; tanpa layout → error jelas | `TestF6TOOL012BrowserScroll` | LULUS | F6-04c |
| TOOL-013 | `browser_snapshot` | accessibility tree ringkas ≤ P5; ref stabil; field sensitif diredaksi | `TestF6TOOL013BrowserSnapshot` | LULUS | F6-04b |
| TOOL-014 | `browser_type` | teks masuk ke field; nilai tidak muncul di log/event; effect `non_idempotent` | `TestF6TOOL014BrowserType` | LULUS | F6-04c |
| TOOL-015 | `browser_vault` | autofill hanya dari vault; nilai secret tidak pernah dicetak; approval setiap call; profil nyata opt-in | `TestF6TOOL015BrowserVault` | LULUS | F6-04c |
| TOOL-016 | `browser_vision` | screenshot → vision model; preflight ukuran; hasil `<untrusted>` | `TestF6TOOL016BrowserVision` | LULUS | F6-04c, F6-03a |
| TOOL-017 | `browser_cdp` | allowlist method aman; method sensitif (`Network.*Cookies`, `Storage.*`, `Runtime.evaluate`, `Debugger.*`, `Fetch.*`, `Target.*`, `Browser.*`) approval per call tanpa `allow_session`; `file:` butuh approval | `TestCDPAllowlist`, `TestF6TOOL017BrowserCDP` | LULUS | F6-04d |
| TOOL-018 | `browser_dialog` | alert/confirm/prompt dijawab; teks dialog tidak dieksekusi; default dismiss | `TestF6TOOL018BrowserDialog` | LULUS | F6-04d |
| TOOL-019 | `browser_exec` | hanya aktif saat backend `browser-use`; menggantikan tool browser lain; backend absen → fail-closed | `TestF6TOOL019BrowserExec` | LULUS | F6-04i |
| TOOL-023 | `computer_use` | setiap aksi input butuh approval kecuali `approval.mode=auto` + `allow_auto`; screenshot diredaksi | `TestF6TOOL023ComputerUse` | BELUM | F6-06b |
| TOOL-054 | `image_generate` | backend eksplisit; preview harga + approval; hasil blob; field `backend`; cap tool berlaku | `TestF6TOOL054ImageGenerate` | LULUS | F6-03c, F6-03g |
| TOOL-080 | `text_to_speech` | 11 provider; MIME/ekstensi jujur; Opus/OGG diprioritaskan; provider command tanpa shell + timeout | `TestF6TOOL080TTS` | BELUM | F6-05c |
<<<<<<< HEAD
| TOOL-081 | `video_analyze` | toolset `video` default off; video multimodal; hasil `<untrusted>`; ukuran dibatasi | `TestF6TOOL081VideoAnalyze` | BELUM | F6-03e |
| TOOL-082 | `video_generate` | selalu approval + preview harga; cap dicek sebelum dispatch; kunci idempotensi; kegagalan tak pasti = `uncertain` tanpa retry | `TestF6TOOL082VideoGenerate` | LULUS | F6-03d, F6-03g |
| TOOL-083 | `xai_video_edit` | hanya backend xAI; approval + harga; hasil blob | `TestF6TOOL083XAIVideoEdit` | LULUS | F6-03d |
| TOOL-084 | `xai_video_extend` | perpanjang video xAI; approval + harga; batas durasi | `TestF6TOOL084XAIVideoExtend` | LULUS | F6-03d |
=======
| TOOL-081 | `video_analyze` | toolset `video` default off; video multimodal; hasil `<untrusted>`; ukuran dibatasi | `TestF6TOOL081VideoAnalyze` | LULUS | F6-03e |
| TOOL-082 | `video_generate` | selalu approval + preview harga; cap dicek sebelum dispatch; kunci idempotensi; kegagalan tak pasti = `uncertain` tanpa retry | `TestF6TOOL082VideoGenerate` | BELUM | F6-03d, F6-03g |
| TOOL-083 | `xai_video_edit` | hanya backend xAI; approval + harga; hasil blob | `TestF6TOOL083XAIVideoEdit` | BELUM | F6-03d |
| TOOL-084 | `xai_video_extend` | perpanjang video xAI; approval + harga; batas durasi | `TestF6TOOL084XAIVideoExtend` | BELUM | F6-03d |
>>>>>>> 1424d460 (feat(f6): add video_analyze media tool and video toolset)
| TOOL-085 | `vision_analyze` | gate `capabilities.vision`; preflight resize; usage `purpose=media`; model teks-only → penolakan sebelum panggilan | `TestF6TOOL085VisionAnalyze` | BELUM | F6-03b |
| TOOL-086 | `web_extract` | 11 backend; markdown; `backend` + `cached` di hasil; `<untrusted>`; blocklist/SSRF | `TestF6TOOL086WebExtract` | LULUS | F6-01d, F6-01e; registry `web_fetch` (SPEC §3.2.4), alias `web_extract` |
| TOOL-087 | `web_search` | ≤10 hasil; snippet ≤300 char; `backend` di hasil; `<untrusted>`; single-flight | `TestF6TOOL087WebSearch` | LULUS | F6-01b, F6-01c, F6-01e |
| TOOL-085 | `vision_analyze` | gate `capabilities.vision`; preflight resize; usage `purpose=media`; model teks-only → penolakan sebelum panggilan | `TestF6TOOL085VisionAnalyze` | LULUS | F6-03b |
| TOOL-086 | `web_extract` | 11 backend; markdown; `backend` + `cached` di hasil; `<untrusted>`; blocklist/SSRF | `TestF6TOOL086WebExtract` | BELUM | F6-01d, F6-01e |
| TOOL-087 | `web_search` | ≤10 hasil; snippet ≤300 char; `backend` di hasil; `<untrusted>`; single-flight | `TestF6TOOL087WebSearch` | BELUM | F6-01b, F6-01c, F6-01e |

## 5. Ledger parity F6 — TSET, MEM, UI, PLUG (18)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| TSET-003 | toolset `browser-cdp` | 2 tool, deferred, `available` saat inventaris=false | `TestF6TSET003BrowserCDP` | LULUS | F6-04d |
| TSET-004 | toolset `browser-use` | 1 tool; menggantikan tool browser lain; inventaris=false | `TestF6TSET004BrowserUse` | LULUS | F6-04i |
| TSET-005 | toolset `browser` | 11 tool komposisi, deferred, tanpa duplikat | `TestF6TSET005Browser` | LULUS | F6-04c |
| TSET-009 | toolset `computer_use` | 1 tool, inventaris=false, approval default | `TestF6TSET009ComputerUse` | BELUM | F6-06b |
| TSET-019 | toolset `image_gen` | 1 tool, inventaris=false, backend tersedia | `TestF6TSET019ImageGen` | LULUS | F6-03c |
| TSET-026 | toolset `tts` | 1 tool, inventaris=true, provider default eksplisit | `TestF6TSET026TTS` | BELUM | F6-05c |
| TSET-027 | toolset `video_gen` | 3 tool, default off | `TestF6TSET027VideoGen` | LULUS | F6-03d |
| TSET-028 | toolset `vision` | 1 tool, inventaris=false | `TestF6TSET028Vision` | LULUS | F6-03b |
| TSET-029 | toolset `video` | 1 tool, default off | `TestF6TSET029Video` | LULUS | F6-03e |
| TSET-030 | toolset `web` | 2 tool, inventaris=true | `TestF6TSET030Web` | LULUS | F6-01d |
| TSET-034 | toolset komposit `search` | bundle menyertakan `web`; 0 tool tak dikenal/duplikat | `TestF6TSET034SearchBundle` | LULUS | F6-00a |
| MEM-037 | Image eviction/replay | retire batch pada 80% limit, stub `sha`, 1 cache break/batch; sesi 30 screenshot full-miss ≤ 2 | `TestF6MEM037ImageEviction` | BELUM | F6-03f |
| UI-008 | Clipboard image / paste collapse | paste gambar via executable eksternal; paste panjang dilipat; tanpa clipboard → pesan | `TestF6UI008ClipboardPaste` | BELUM | F6-03a |
| PLUG-012 | Plugin browser backends (Browserbase, Browser Use, Firecrawl) | tiga backend cloud dapat dipilih; tanpa env autodetect; kredensial keychain | `TestF6PLUG012BrowserBackends` | LULUS | F6-04f |
| PLUG-013 | Plugin computer_use (cua) | backend cua terdeteksi; absen → fail-closed | `TestF6PLUG013ComputerUseCua` | BELUM | F6-06a |
| PLUG-020 | Plugin image_gen (8 backend) | matriks 8 backend lengkap, masing-masing satu fixture | `TestF6PLUG020ImageGenBackends` | LULUS | F6-03c |
| PLUG-027 | Plugin video_gen (4 backend) | matriks 4 backend lengkap + xAI edit/extend | `TestF6PLUG027VideoGenBackends` | LULUS | F6-03d |
| PLUG-028 | Plugin web (11 backend) | matriks 11 backend; backend eksplisit; `tos_risk` untuk tidak resmi | `TestF6PLUG028WebBackends`, `TestF6PLUG028WebPlugin` | LULUS | F6-01a..01c |

## 6. Ledger parity F6 — MEDIA (29)

Baris MEDIA-029 (Discord voice) dan MEDIA-031 (silk) berada di F7 dan **tidak** dihitung; sisanya 29 baris ada di bawah (FEATURE-PARITY.md:830-860). Keputusan per ID, dapat diverifikasi di tabel parity: `MEDIA-029 — N/A — fase F7 di FEATURE-PARITY.md:862, tidak ada baris ledger F6 (carryover F7)`; `MEDIA-031 — N/A — fase F7 di FEATURE-PARITY.md:864, tidak ada baris ledger F6 (carryover F7)`. Keduanya bukan `LULUS` dan tidak menutup baris agregat "MEDIA 29/31".

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| MEDIA-001 | Voice mode CLI/TUI | push-to-talk → STT → agent → TTS; barge-in; tanpa device → fail-closed | `TestF6MEDIA001VoiceMode`, `TestF6MEDIA001BargeIn` | LULUS | F6-05e |
| MEDIA-002 | Voice live (GPT realtime) | `gpt_live` opt-in; default `chained`; gagal realtime kembali ke chained dengan label | `TestF6MEDIA002GPTLiveOptIn` | LULUS | F6-05e |
| MEDIA-003 | Wake word | listener hanya saat enabled; membuka sesi baru tanpa mengirim audio mentah ke model | `TestF6MEDIA003WakeOptIn` | LULUS | F6-05g |
| MEDIA-004 | Wake engine openWakeWord | engine terpilih dari config; binary/model absen → pesan | `TestF6MEDIA004WakeOpenWakeWord` | BELUM | F6-05g |
| MEDIA-005 | Wake engine sherpa-onnx KWS | engine terpilih; kosakata dari config; absen → pesan | `TestF6MEDIA005WakeSherpa` | BELUM | F6-05g |
| MEDIA-006 | Wake engine Porcupine | access key hanya dari secret; absen → pesan | `TestF6MEDIA006WakePorcupine` | BELUM | F6-05g |
| MEDIA-007 | TTS 11 provider | 11 provider terdaftar; default eksplisit; backend tidak resmi `tos_risk` opt-in | `TestF6MEDIA007TTSProviders` | BELUM | F6-05c |
| MEDIA-008 | TTS streaming | audio pertama ≤ 300 ms setelah kalimat pertama; potong per kalimat; batal menutup stream | `TestF6MEDIA008StreamingLatency` | LULUS | F6-05d |
| MEDIA-009 | TTS command provider | argv tanpa shell; timeout; output audio divalidasi MIME | `TestF6MEDIA009TTSCommand` | BELUM | F6-05c |
| MEDIA-010 | STT 8 provider | 8 provider terdaftar; transkripsi pesan suara; backend eksplisit | `TestF6MEDIA010STTProviders`, `TestF6MEDIA010STTEmptyAudio` | LULUS | F6-05a |
| MEDIA-011 | STT whisper.cpp + chunking | whisper.cpp via executable; audio 80 MB/60 menit terchunk tanpa ffmpeg; M4A tanpa ffmpeg → `module_unavailable` | `TestF6MEDIA011WhisperChunking` | LULUS | F6-05b |
| MEDIA-012 | Vision (`vision_analyze`) | gambar dianalisis model vision; gambar dari user dilampirkan bila `capabilities.vision`; else tool | `TestF6MEDIA012VisionAnalyze` | LULUS | F6-03b |
| MEDIA-013 | Image routing native | multi-gambar dihormati sampai limit route; 0 penolakan ukuran | `TestF6MEDIA013ImageRouting` | LULUS | F6-03a, F6-03b || MEDIA-014 | Video analyze | video dianalisis model multimodal; batas ukuran/durasi jelas | `TestF6MEDIA014VideoAnalyze` | BELUM | F6-03e |
| MEDIA-015 | Image generation | `image_generate` memakai registry 8 backend; hasil blob + path | `TestF6MEDIA015ImageGeneration` | LULUS | F6-03c |
| MEDIA-016 | Image backend FAL | fixture FAL; 1 attempt (tanpa retry SDK); harga dicatat | `TestF6MEDIA016ImageFAL` | LULUS | F6-03c |
| MEDIA-017 | Image backend OpenAI | fixture GPT Image; dimensi/format divalidasi | `TestF6MEDIA017ImageOpenAI` | LULUS | F6-03c |
| MEDIA-018 | Image backend OpenAI Codex OAuth | OAuth token dipakai tanpa mencetak; absen → fail-closed | `TestF6MEDIA018ImageCodex` | LULUS | F6-03c |
| MEDIA-019 | Image backend OpenRouter/Nous | chat-completions image; `backend` di hasil | `TestF6MEDIA019ImageOpenRouter` | LULUS | F6-03c |
| MEDIA-020 | Image backend xAI | `grok-imagine-image`; parameter valid | `TestF6MEDIA020ImageXAI` | LULUS | F6-03c |
| MEDIA-021 | Image backend DeepInfra | FLUX/Qwen-Image; ukuran hasil divalidasi | `TestF6MEDIA021ImageDeepInfra` | LULUS | F6-03c |
| MEDIA-022 | Image backend Krea | Krea 2; endpoint/path benar | `TestF6MEDIA022ImageKrea` | LULUS | F6-03c |
| MEDIA-023 | Image backend Meta AI | muse-image; key hanya dari secret | `TestF6MEDIA023ImageMeta` | LULUS | F6-03c |
| MEDIA-024 | Video generation | `video_generate` + registry 4 backend; hasil blob | `TestF6MEDIA024VideoGeneration` | LULUS | F6-03d |
| MEDIA-025 | Video backend FAL | Veo/Kling/Pixverse; harga + approval; 1 job per retry | `TestF6MEDIA025VideoFAL` | LULUS | F6-03d |
| MEDIA-026 | Video backend xAI | Grok Imagine video + edit/extend | `TestF6MEDIA026VideoXAI` | LULUS | F6-03d |
| MEDIA-027 | Video backend OpenRouter | `/api/v1/videos`; polling terbatas | `TestF6MEDIA027VideoOpenRouter` | LULUS | F6-03d |
| MEDIA-028 | Video backend DeepInfra | text/image-to-video; batas ukuran | `TestF6MEDIA028VideoDeepInfra` | LULUS | F6-03d |
| MEDIA-030 | Media cache & repair gateway | unduh/cache inbound dengan batas ukuran + MIME sniff; repair idempoten; dedupe blob; tidak mengekstrak dokumen otomatis | `TestF6MEDIA030MediaCache` | BELUM | F6-03h |

## 7. Ledger parity F6 — EXEC (8)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| EXEC-036 | Computer use | backend cua eksternal; setiap input approval (kecuali auto+allow_auto) | `TestF6EXEC036ComputerUse` | BELUM | F6-06a |
| EXEC-037 | Browser engine lokal | Chromium lokal terkelola; profil terisolasi default; startup/gagal jelas | `TestF6EXEC037BrowserLocalEngine` | LULUS | F6-04e |
| EXEC-038 | Browser engine Camofox | engine Camofox opt-in; endpoint/port divalidasi | `TestF6EXEC038Camofox` | LULUS | F6-04g |
| EXEC-039 | Browser engine Lightpanda | Lightpanda + fallback eksplisit, bukan diam | `TestF6EXEC039LightpandaFallback` | LULUS | F6-04g |
| EXEC-040 | Real profile / CDP connect | profil nyata hanya `real_profile=true` + approval per sesi; default profil terisolasi | `TestF6EXEC040RealProfileApproval` | LULUS | F6-04h |
| EXEC-041 | Browser extension control | default off; kontrol ekstensi tidak melebarkan akses profil nyata | `TestF6EXEC041BrowserExtension` | LULUS | F6-04h |
| EXEC-042 | Web backends 11 | matriks env acak → backend identik; tanpa autodetect; keyless vs keyed eksplisit | `TestF6EXEC042WebBackendsMatrix`, `TestF6EXEC042WebBackendMatrix` | LULUS | F6-01a..01c |
| EXEC-044 | Website blocklist | domain terblokir ditolak sebelum dial, termasuk redirect; daftar dari config | `TestF6EXEC044WebsiteBlocklist`, `TestF6EXEC044SiteBlocklist` | LULUS | F6-01e |

## 8. GAP F6 (9) — SPEC §22.1

| GAP | Kriteria verifikasi | Tes yang harus ada | Status |
|---|---|---|---|
| GAP-SEC-05 | fixture injection 20 varian → 0 cookie keluar; 100% method sensitif minta approval | `TestCDPAllowlist` | BELUM |
| GAP-COST-05 | sesi 30 screenshot: full cache miss ≤ 2; 0 penolakan batas gambar; retire batch 80%→50% + stub `sha` | `TestF6MEM037ImageEviction`, `TestF6ImageSuite50` | BELUM |
| GAP-COST-11 | panggilan video > cap ditolak sebelum dispatch; retry jaringan → 1 job vendor per kunci; `uncertain` tanpa retry; approval + preview harga | `TestBudgetGuardTools`, `TestF6COST11VideoIdempotency` | BELUM |
| GAP-COST-12 | 3 subagent query identik → 1 panggilan vendor; 2 profil query sama → 0 hit silang; TTL 20/60 menit; `cached=true`; kunci setelah gerbang SSRF | `TestWebCacheIsolation` | LULUS |
| GAP-TOOL-01 | korpus 40 dokumen terbaca; round-trip tulis→baca 20 dokumen; `no_text_layer`/`unsupported`/`too_large` typed | `TestF6DocCorpus`, `FuzzDocRead` | BELUM |
| GAP-TOOL-02 | zip-bomb 10 GB uncompressed → error terkontrol, RSS < 100 MB; batas 256 MiB/100:1/10.000 entri/kedalaman 256; PDF ≤ 5.000 objek/halaman | `TestF6DocLimits` | BELUM |
| GAP-TOOL-06 | `doctor tools fetch/verify/ls` dengan lockfile sha256; byte rusak ditolak; lisensi dikonfirmasi; model tidak diunduh | `TestToolsLockComplete`, `TestF6ToolsFetch` | BELUM |
| GAP-TOOL-07 | host tanpa ffmpeg: 0 MIME salah; MP3 80 MB terchunk internal; M4A → `module_unavailable`; hint `doctor tools fetch ffmpeg` | `TestF6AudioMIME` | BELUM |
| GAP-TOOL-09 | matriks env acak → backend identik; 100% hasil memuat `backend`; fallback hanya dari config + label; backend tidak resmi `tos_risk` opt-in dan bukan default | `TestF6BackendSelection` | LULUS |

## 9. EC, test tertunda, E3, dan baseline hermes

| Item | Kriteria | Tes / artefak | Status |
|---|---|---|---|
| EC-PROV-024 | gambar tidak pernah dikirim ke model teks-only; gate `capabilities.vision` terjadi sebelum routing; fallback vision eksplisit | `TestEdgeCases/EC-PROV-024` (katalog `pending:F6` → `covered`) | LULUS |
| TestBudgetGuardTools (ADR-083) | 0 panggilan vendor setelah cap 100%; peringatan 80%; `budget_tools` membawa angka/periode/perintah naik; fake clock/usage | `TestBudgetGuardTools` | BELUM |
| Smoke E3 | 10 task E3 self-contained deterministik, model murah, dijalankan lewat harness yang ada; artefak hasil disimpan | `TestF6E3Smoke10`, `bench/evals/results/F6-e3.json` | BELUM |
| Baseline hermes F6 | `Voice end-to-end` dan `Image preflight` diukur dengan harness yang sama untuk hermes dan Damn-Bot | `TestF6HermesBaseline`, `TestF6BaselineArtifactSchema`, `bench/speed/results/hermes-868b9f244f-F6.json` | LULUS |

Catatan **Baseline hermes F6** (§9): baris ini `LULUS` untuk kelengkapan artefak (dua dimensi × dua agen, diukur harness yang sama), **bukan** untuk target waktu. Sesuai §1, verdict `image_preflight` = `fail` terhadap target 150 ms yang hanya sah di Q14 (`N/A — hardware`, ADR-323); verdict `voice_end_to_end` = `pass`. Tidak ada klaim bahwa target image tercapai di workstation ini.

## 10. Carryover F5→F6 (3)

| Item | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| F5-CODEINTEL-LANG (JS/TS/Python) | `repo_map`/`code_intel` bekerja untuk js/ts/py; bahasa lain tetap `ErrUnsupportedLanguage`; aset wasm dimuat wazero tanpa CGO | `TestF6CodeintelJSTS`, `TestF6CodeintelPython` | BELUM |
| F5-BACKGROUND-WINDOWS | `shell background:true` + registry `process_*` tersedia di Windows; `Close` membunuh seluruh pohon via Job Object; tidak ada proses yatim | `TestF6JobObjectKillsTree` (runner Windows), `TestF6BackgroundSupported` | BELUM |
| GAP-REL-14 residual | hasil gambar MCP menjadi `Attachments` blob, bukan teks; bila model teks-only → gate vision (EC-PROV-024) | `TestF6MCPImageAttachment` | BELUM |

## 11. Perintah bukti

```sh
# Gate fase saat ini mensertifikasi F5 (ADR-176); F6 menjadi gate saat PHASE->F7.
PHASE=F6 make phase-acceptance

# Baris yang disentuh satu PR (contoh; ganti pola sesuai baris).
go test -race -run 'TestF6|TestCDPAllowlist|TestWebCacheIsolation|TestBudgetGuardTools|TestVoicePipelineLatency' ./...

# Gate penutupan ADR-323: hijau selama PHASE=F6; menolak sisa BELUM/planned saat PHASE>F6.
go test -count=1 -run 'TestF6LedgerClosed|TestParityPhaseColumn|TestF6LedgerGateRules' ./internal/archtest

# Budget/arsitektur tetap hijau setelah paket F6 bertambah.
make loc
make size-check

# Baseline F6 (dua agent, artefak yang sama) + skema artefak. `bench` adalah modul terpisah.
(cd bench && go test -count=1 -run 'TestF6HermesBaseline|TestF6BaselineArtifactSchema|TestF6BaselineVerdictRules' ./f6)
(cd bench && go test -count=1 -run 'TestNoLLMBaselineCoverage' ./speed)
```

Aturan perubahan ledger (ADR-323): ubah `BELUM` → `LULUS` hanya bersama tes bernama di PR yang sama **dan** baris parity F6 `planned`→`LULUS`; `N/A` wajib menyebut fase target dan alasan teknis di kolom catatan (`N/A — <alasan> (carryover F7)`), tidak boleh telanjang. Baris agregat hanya `LULUS` setelah semua anggota tertutup. Angka budget tidak diubah di sini; plafon dan baris `MODULE` F6 mengikuti ADR-309 pada PR fondasi.

## 12. N/A hardware — `BenchmarkImagePreflight` 12 MP (F6-03a)

`internal/media/preflight` sudah men-decode JPEG/PNG, mengabaikan orientasi EXIF (tidak ada rotasi/pertukaran dimensi), menurunkan sisi terpanjang/MP/byte ke preset (`Default`: 2048 px, 12 MP, 3,75 MB, JPEG q85), dan `TestF6ImageSuite50` membuktikan 50 gambar → 0 penolakan ukuran. Baris "Ukuran gambar" menjadi **`N/A — hardware`** (ADR-323): target waktu hanya sah diukur di Q14, sedangkan host ini mencatat 265–290 ms. Angka target dan asersi `TestImagePreflightTargetNotLowered` tidak diubah; tidak ada klaim `LULUS`, dan pengukuran Q14 tetap carryover F7.

`BenchmarkImagePreflight` (JPEG 4000×3000, host 2026-11-02) terukur **≈0,9 s/op** dengan 10 CPU dan **≈6,4 s/op** di bawah `GOMAXPROCS=2`/`nice -n 19`; komponen di bawah batas yang sama: decode ≈0,43–1,61 s, resize ≈0,26–0,57 s (fast path `*image.YCbCr` + box-average paralel), encode ≈0,13–0,18 s. Target SPEC §17 (≤150 ms) diukur di mesin referensi Q14; host ini jauh lebih lambat dan decode stdlib + encode sudah melewati 150 ms tanpa resize. **Open item**, bukan klaim lulus.

Usulan slice optimasi berikutnya: decode JPEG ber-skalasi DCT-domain 1/2–1/8 (hindari decode 12 MP penuh), resize paralel per baris dengan buffer terpisah, dan ukur ulang `BenchmarkImagePreflight` di Q14. Baris acceptance tetap `N/A — hardware` sampai pengukuran Q14 tersedia, dan tidak ada asersi tes yang diturunkan.
