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
| Smoke E3 | 10 task E3 dengan model murah lulus harness; artefak tersimpan | `TestF6E3Smoke10` + `TestF6E3Artifact` + `bench/evals/results/F6-e3.json` | LULUS |
| EC-PROV-024 | gate kapabilitas vision sebelum routing gambar; model teks-only tidak pernah menerima gambar | `TestEdgeCases/EC-PROV-024` | LULUS |
| Test tertunda ADR-083 | cap tool non-LLM ditegakkan sebelum dispatch | `TestBudgetGuardTools`, `TestF6ADR083ToolCap` | LULUS |
| Baseline hermes F6 | dimensi ADR-065/083 berstatus F6 diukur untuk kedua agent | `TestF6HermesBaseline`, `TestF6BaselineArtifactSchema` + `bench/speed/results/hermes-868b9f244f-F6.json` | LULUS |
| GAP F6 (9) | sembilan baris SPEC §22.1 lulus verifikasinya | tabel §8; anggota (`BELUM` di head ini): `GAP-SEC-05`, `GAP-COST-05`, `GAP-COST-11`, `GAP-COST-12`, `GAP-TOOL-01`, `GAP-TOOL-02`, `GAP-TOOL-06`, `GAP-TOOL-07`, `GAP-TOOL-09` | BELUM |
| Carryover F5→F6 | tiga item DITUNDA/N/A F5 ditutup | tabel §10; ketiga anggota `LULUS` (`F5-CODEINTEL-LANG`, `F5-BACKGROUND-WINDOWS`, `GAP-REL-14 residual`) | LULUS |

Catatan **Baseline hermes F6**: `LULUS` di sini berarti **artefak terukur lengkap** (dua dimensi ADR-065/083 × dua agen, `TestF6HermesBaseline` + `TestF6BaselineArtifactSchema` hijau), bukan klaim target waktu tercapai. Artefak `bench/speed/results/hermes-868b9f244f-F6.json` diukur di workstation, bukan Q14. Verdict `image_preflight` = `fail`: Damn-Bot 265,0 ms dan hermes 453,5 ms terhadap target 150 ms yang hanya sah di Q14; target itu tetap `N/A — hardware` (ADR-323, §12) dan pengukuran Q14 tetap carryover F7. Verdict `voice_end_to_end` = `pass`: Damn-Bot 0,08 ms overhead / 0,05 ms TTS start, hermes 1,60 ms / 1,60 ms.
| TOOL-083 | `xai_video_edit` | hanya backend xAI; approval + harga; hasil blob | `TestF6TOOL083XAIVideoEdit` | LULUS | F6-03d |
| TOOL-084 | `xai_video_extend` | perpanjang video xAI; approval + harga; batas durasi | `TestF6TOOL084XAIVideoExtend` | LULUS | F6-03d |
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
| TSET-009 | toolset `computer_use` | 1 tool, inventaris=false, approval default | `TestF6TSET009ComputerUse` | LULUS | F6-06b |
| TSET-019 | toolset `image_gen` | 1 tool, inventaris=false, backend tersedia | `TestF6TSET019ImageGen` | LULUS | F6-03c |
| TSET-026 | toolset `tts` | 1 tool, inventaris=true, provider default eksplisit | `TestF6TSET026TTS` | LULUS | F6-05c |
| TSET-027 | toolset `video_gen` | 3 tool, default off | `TestF6TSET027VideoGen` | LULUS | F6-03d |
| TSET-028 | toolset `vision` | 1 tool, inventaris=false | `TestF6TSET028Vision` | LULUS | F6-03b |
| TSET-029 | toolset `video` | 1 tool, default off | `TestF6TSET029Video` | LULUS | F6-03e |
| TSET-030 | toolset `web` | 2 tool, inventaris=true | `TestF6TSET030Web` | LULUS | F6-01d |
| TSET-034 | toolset komposit `search` | bundle menyertakan `web`; 0 tool tak dikenal/duplikat | `TestF6TSET034SearchBundle` | LULUS | F6-00a |
| MEM-037 | Image eviction/replay | retire batch pada 80% limit, stub `sha`, 1 cache break/batch; sesi 30 screenshot full-miss ≤ 2 | `TestF6MEM037ImageEviction` | LULUS | F6-03f |
| UI-008 | Clipboard image / paste collapse | paste gambar via executable eksternal; paste panjang dilipat; tanpa clipboard → pesan | `TestF6UI008ClipboardImage` | LULUS | F6-03a |
| PLUG-012 | Plugin browser backends (Browserbase, Browser Use, Firecrawl) | tiga backend cloud dapat dipilih; tanpa env autodetect; kredensial keychain | `TestF6PLUG012BrowserBackends` | LULUS | F6-04f |
| PLUG-013 | Plugin computer_use (cua) | backend cua terdeteksi; absen → fail-closed | `TestF6PLUG013CUA` | LULUS | F6-06a |
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
| MEDIA-004 | Wake engine openWakeWord | engine terpilih dari config; binary/model absen → pesan | `TestF6MEDIA004OpenWakeWord` | LULUS | F6-05g |
| MEDIA-005 | Wake engine sherpa-onnx KWS | engine terpilih; kosakata dari config; absen → pesan | `TestF6MEDIA005SherpaKWS` | LULUS | F6-05g |
| MEDIA-006 | Wake engine Porcupine | access key hanya dari secret; absen → pesan | `TestF6MEDIA006Porcupine`, `TestF6MEDIA006PorcupineUsesSecret` | LULUS | F6-05g |
| MEDIA-007 | TTS 11 provider | 11 provider terdaftar; default eksplisit; backend tidak resmi `tos_risk` opt-in | `TestF6MEDIA007TTSProviders` | LULUS | F6-05c |
| MEDIA-008 | TTS streaming | audio pertama ≤ 300 ms setelah kalimat pertama; potong per kalimat; batal menutup stream | `TestF6MEDIA008StreamingLatency` | LULUS | F6-05d |
| MEDIA-009 | TTS command provider | argv tanpa shell; timeout; output audio divalidasi MIME | `TestF6MEDIA009TTSCommandArgv` | LULUS | F6-05c |
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
| MEDIA-030 | Media cache & repair gateway | unduh/cache inbound dengan batas ukuran + MIME sniff; repair idempoten; dedupe blob; tidak mengekstrak dokumen otomatis | `TestF6MEDIA030MediaCacheRepair` | LULUS | F6-03h |

## 7. Ledger parity F6 — EXEC (8)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| EXEC-036 | Computer use | backend cua eksternal; setiap input approval (kecuali auto+allow_auto) | `TestF6EXEC036CUAApproval` | LULUS | F6-06a |
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
| GAP-COST-05 | sesi 30 screenshot: full cache miss ≤ 2; 0 penolakan batas gambar; retire batch 80%→50% + stub `sha` | `TestF6MEM037ImageEviction`, `TestF6ImageSuite50` | LULUS |
| GAP-COST-11 | panggilan video > cap ditolak sebelum dispatch; retry jaringan → 1 job vendor per kunci; `uncertain` tanpa retry; approval + preview harga | `TestBudgetGuardTools`, `TestF6GAPCOST11BudgetToolsPayload`, `TestF6COST11VideoIdempotency` | LULUS |
| GAP-COST-12 | 3 subagent query identik → 1 panggilan vendor; 2 profil query sama → 0 hit silang; TTL 20/60 menit; `cached=true`; kunci setelah gerbang SSRF | `TestWebCacheIsolation` | LULUS |
| GAP-TOOL-01 | korpus 40 dokumen terbaca; round-trip tulis→baca 20 dokumen; `no_text_layer`/`unsupported`/`too_large` typed | `TestF6DocCorpus`, `FuzzDocRead` | LULUS |
| GAP-TOOL-02 | zip-bomb 10 GB uncompressed → error terkontrol, RSS < 100 MB; batas 256 MiB/100:1/10.000 entri/kedalaman 256; PDF ≤ 5.000 objek/halaman | `TestF6DocLimits` | LULUS |
| GAP-TOOL-06 | `doctor tools fetch/verify/ls` dengan lockfile sha256; byte rusak ditolak; lisensi dikonfirmasi; model tidak diunduh | `TestToolsLockComplete`, `TestF6GAPTOOL06DoctorTools` | LULUS |
| GAP-TOOL-07 | host tanpa ffmpeg: 0 MIME salah; MP3 80 MB terchunk internal; M4A → `module_unavailable`; hint `doctor tools fetch ffmpeg` | `TestF6AudioMIME` | LULUS |
| GAP-TOOL-09 | matriks env acak → backend identik; 100% hasil memuat `backend`; fallback hanya dari config + label; backend tidak resmi `tos_risk` opt-in dan bukan default | `TestF6BackendSelection` | LULUS |

## 9. EC, test tertunda, E3, dan baseline hermes

| Item | Kriteria | Tes / artefak | Status |
|---|---|---|---|
| EC-PROV-024 | gambar tidak pernah dikirim ke model teks-only; gate `capabilities.vision` terjadi sebelum routing; fallback vision eksplisit | `TestEdgeCases/EC-PROV-024` (katalog `pending:F6` → `covered`) | LULUS |
| TestBudgetGuardTools (ADR-083) | 0 panggilan vendor setelah cap 100%; peringatan 80%; `budget_tools` membawa angka/periode/perintah naik; fake clock/usage | `TestBudgetGuardTools`, `TestF6ADR083ToolCap` | LULUS |
| Smoke E3 | 10 task E3 self-contained deterministik, model murah, dijalankan lewat harness yang ada; artefak hasil disimpan | `TestF6E3Smoke10`, `TestF6E3Artifact`, `bench/evals/results/F6-e3.json` | LULUS |
| Baseline hermes F6 | `Voice end-to-end` dan `Image preflight` diukur dengan harness yang sama untuk hermes dan Damn-Bot | `TestF6HermesBaseline`, `TestF6BaselineArtifactSchema`, `bench/speed/results/hermes-868b9f244f-F6.json` | LULUS |

Catatan **Baseline hermes F6** (§9): baris ini `LULUS` untuk kelengkapan artefak (dua dimensi × dua agen, diukur harness yang sama), **bukan** untuk target waktu. Sesuai §1, verdict `image_preflight` = `fail` terhadap target 150 ms yang hanya sah di Q14 (`N/A — hardware`, ADR-323); verdict `voice_end_to_end` = `pass`. Tidak ada klaim bahwa target image tercapai di workstation ini.

## 10. Carryover F5→F6 (3)

| Item | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| F5-CODEINTEL-LANG (JS/TS/Python) | `repo_map`/`code_intel` bekerja untuk js/ts/py; bahasa lain tetap `ErrUnsupportedLanguage`; aset wasm dimuat wazero tanpa CGO | `TestF6CodeintelJSTS`, `TestF6CodeintelPython`; kontrol `TestF5CodeintelLanguageJS/TS/Python`, `TestF5CodeintelLanguageGrammarUnavailable`, `TestF6CodeintelLanguageControls` | LULUS |
| F5-BACKGROUND-WINDOWS | `shell background:true` + registry `process_*` tersedia di Windows; `Close` membunuh seluruh pohon via Job Object; tidak ada proses yatim | `TestF5BackgroundWindowsJobObject` (runner Windows), `TestShellBackgroundWindowsCapability` | LULUS — log job `test (windows-latest, -race, 1)` PR #710 memuat `--- PASS` bernama untuk kedua tes (plus `TestPathLock*` dan `TestIdentifyHandleFailureWindows` dari step bukti yang sama) |
| GAP-REL-14 residual | hasil gambar MCP menjadi `Attachments` blob, bukan teks; bila model teks-only → gate vision (EC-PROV-024) | `TestF6GAPREL14MCPAttachment`, `TestEdgeCases/EC-PROV-024` | LULUS |

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

## 13. Smoke E3 live (2026-11-04) — Amandemen (ADR-323)

**Amandemen (ADR-323, PR #696).** Baris "Smoke E3" di §1 dan §9 menjadi **`LULUS`** karena artefak run nyata, tes seleksi, dan tes skema artefak mendarat di PR yang sama. Kriteria baris tidak berubah: 10 task E3 deterministik, model murah, dijalankan lewat harness `bench/h2h`, lulus grader, artefak tersimpan.

| item | hasil |
| --- | --- |
| commit Damn-Bot | `8ca678df` (`origin/main` saat run, 2026-11-04; PR ini rebase di atas `f1b52bca`) |
| host | workstation, `nice -n 19`, `GOMAXPROCS=2`, `GOFLAGS=-p=1`, `ulimit -v` 4 GiB |
| task | 10/10 `ran`: E3-012, E3-016, E3-001, E3-026, E3-010, E3-020, E3-018, E3-009, E3-013, E3-017 |
| grader | 10/10 `pass` |
| harness error | 0 |
| agent | hermes `868b9f244f5aa3ad238e494048046fe4f29c28ba` (diverifikasi lewat blob SHA GitHub; checkout lokal tanpa `.git`); harness mencatat sisi Damn-Bot `N/A` |
| model | `claude-haiku-5-5`, harga `0,0286 / 0,00286 / 0,143` USD per Mtok (`https://teamorouter.cn/pricing`, dibaca 2026-11-04) |
| completion | 46 calls (2 aborted); 1.938.866 prompt / 5.267 completion / 0 cached token |
| biaya run | **US$0,05620475**; ledger F6 `0,07720813/2,00` (termasuk percobaan yang dibuang, lihat di bawah) |
| artefak | `bench/evals/results/F6-e3.json` (sha256 `3d0eeb7362176104dabd740ce4ce536bcf89cc0bc88d5d192de6420dd7956ee6`) |
| penjaga | `TestF6E3Smoke10` (seleksi deterministik) dan `TestF6E3Artifact` (menolak artefak kosong/placeholder; menolak `passed` tanpa exit/stop, calls, token, biaya, dan hash rekaman) |

Percobaan yang dibuang dicatat di `meta.attempts` artefak, bukan disembunyikan: kedua model gratis termurah (`deepseek-v4-flash-free`, `glm-5.3-flash-free`) dijawab upstream HTTP 402 "high demand" pada semua attempt (0 completion, 0 grader pass); `qwen3.7-flash` lulus 8/10 — E3-012 dan E3-018 menjawab angka sensus yang benar tetapi bertanda ribuan (`49,604` / `195,667`), yang ditolak regex grader; `claude-haiku-5-5` lulus 2/2 pada subset diagnostik itu lalu 10/10 pada run penerimaan. Rekaman mentah (`summary.json`, 10 `*.proxy.jsonl.gz`) diarsipkan di host run `/home/dan/.cache/damnbot/f6e3-raw-2026-11-04/` dan tidak di-commit; hash per task ada di artefak.

Perintah (kunci dibaca runtime dari `~/.config/damnbot/llm_key`, tidak pernah dicetak; `python3` menunjuk venv hermes):

```sh
cd bench && h2h -smoke f6e3 -hermes ~/.cache/damnbot/f2-inputs/hermes-src \
  -upstream https://api.teamorouter.com -model claude-haiku-5-5 \
  -price-in 0.0286 -price-cached 0.00286 -price-out 0.143 \
  -out h2h/results/2026-11-04-f6-e3-smoke -evals evals/v1 -timeout 300s -max-turns 20
```
