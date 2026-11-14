# F9-ACCEPTANCE — Ledger acceptance F9 (hardening & rilis 1.0)

Status: **ledger hidup** untuk `PHASE=F9` pada `origin/main` `3352ac29`. Status per baris ada pada kolom `Status`; setiap baris agregat §1 baru `LULUS` setelah semua anggotanya tertutup, kecuali baris yang dire-fase owner lewat ADR. Per 2026-11-13, **GAP-SWEEP 118/118**, **EDGE-CASES 166/166**, dan **OS matrix 8/8** sudah `LULUS`; **soak/chaos** tetap `BELUM` karena prasyaratnya belum lulus (bukti dan penghalangnya dicatat pada amandemen ADR-347 di `docs/DECISIONS.md`); dan baris **artefak rilis & scorecard** (P20) **dire-fase ke F10** lewat ADR-356 dengan status `N/A — re-phased to F10 (ADR-356; P20 RC)` — **bukan** `LULUS` — dengan bukti terukur (43 dimensi terukur, 31 `not_measured`, 6 pelanggaran, P28 3/163, first token 6,38×) tercatat di ADR-356 dan `docs/SCORECARD-RC.md`. Gate parity `LULUS` (lihat §1). Kriteria dan angka tidak berubah dari fase sebelumnya kecuali baris yang eksplisit berstatus `LULUS`. Satu baris hanya boleh berubah menjadi `LULUS` pada PR yang benar-benar menambahkan tes bernama di kolom "Tes yang harus ada" (dan/atau artefak yang disebut) lalu tes/artefak itu lulus, atau menjadi `N/A` beralasan yang memenuhi §7. Baris agregat §1 hanya `LULUS` setelah semua anggotanya tertutup.

Sumber normatif: ROADMAP F9 (`docs/ROADMAP.md:216-226`, termasuk acceptance gate 1.0 dan tabel "Adopsi GAP-SWEEP"), 5 baris parity berfase F9 (`docs/FEATURE-PARITY.md:713,949-952`; UI-018 dan OPS-029..032), 8 baris GAP F9 (SPEC §22.1: `SPEC.md` — GAP-SEC-19, GAP-SEC-22, GAP-SEC-28, GAP-OPS-03, GAP-OPS-05, GAP-INF-03, GAP-INF-10, dan GAP-INF-02), katalog EC (tidak ada entri F9; `docs/ROADMAP.md:54-67`), carryover F8 (`docs/F8-ACCEPTANCE.md:240-245`), gate parity 837/837 (SPEC §8, V-28/ADR-054), dan ADR-347 (program F9, penanda rilis, agregat). Nama paket dan plafon modul mengikuti ADR-330; budget F9 = **0 LOC Go produk** (ROADMAP F9; perbaikan hardening diserap budget modul fasenya).

**GAP-INF-02 disertakan.** ADR-193 (keputusan owner 2026-10-21) menjadwalkan GAP-INF-02 ke F9, dan ROADMAP `:245` mencatatnya sebagai scope F9. Baris SPEC §22.1 (`SPEC.md:3035`) masih menulis `F2` sebagai peta historis; requirement dan verifikasinya tidak berubah, dan gate F9 118/118 tetap menghitung gap ini satu kali. Karena itu ledger F9 memuat **8** GAP, bukan 7.

Aturan gate:
- **Penutupan (ADR-347, melanjutkan ADR-330/ADR-326).** Baris hanya `LULUS` bila PR yang sama menambah tes bernama, mengubah baris parity F9 (`docs/FEATURE-PARITY.md`) menjadi `LULUS`, dan mengubah baris ledger `BELUM`→`LULUS`. Baris yang tidak dapat dibuktikan ditulis `N/A — <alasan teknis> (carryover F10)`, bukan `BELUM`.
- **Gate mesin.** F9 adalah fase terminal: tidak ada `PHASE` sesudahnya, sehingga `PHASE` tetap `F9`. `TestF9LedgerClosed` dan cabang F9 `TestParityPhaseColumn` **nonaktif selama `PHASE=F9`** dan hanya aktif bila penanda rilis `DAMNBOT_RELEASE=1` diset (ADR-347). Saat aktif, keduanya menolak `BELUM`, `N/A` telanjang (< 8 karakter alasan), dan baris parity F9 yang bukan `LULUS`/`N/A — …`; `TestF9LedgerGateRules` adalah kontrol negatifnya.
- **Agregat.** Enam baris §1 dihitung dari anggota, bukan dari narasi: parity 837/837 = 653 `LULUS` + 184 `N/A` re-phased (ADR-350) dengan 0 `planned` di F1–F9, GAP 118/118, EC 166/166, OS matrix 8 target, soak/chaos, dan artefak rilis/scorecard. Kerangka tes agregat yang belum lengkap (`TestF9GapSweep118`, `TestF9EdgeCases166`, `TestF9OSMatrix`, `TestF9SpeedParity`) memakai prasyarat yang sama: skip selama penanda rilis belum aktif, gagal bila penanda aktif dan prasyarat belum terpenuhi.
- **Tanpa `t.Skip` liar (REL-15).** Skip hanya di kerangka agregat di atas dengan alasan eksplisit dan gerbang `DAMNBOT_RELEASE=1`; tes baris data tetap wajib ada dan lulus.
- **Budget.** Tidak ada kenaikan `maxModuleLOC` 132.000, `maxCoreLOC`, atau plafon modul; F9 tidak menambah paket produk baru.

## 1. Acceptance ROADMAP F9 (6 agregat)

| Item ROADMAP F9 | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| Gate parity 837/837 | 653 baris `LULUS` + 184 baris `N/A — re-phased to F10 (ADR-350)` = 837 penyebut gate (854 baris − 17 `dropped-with-approval`), 0 `planned` di F1–F9; terverifikasi di main `4aad646f` dengan `TestParityPhaseColumn` + `TestF9ParityDeferred184` hijau dan `scripts/parity-audit.sh` (total=854, planned=0, LULUS=653, N/A=184, dropped=17; `Deferred184` n/a=184) | `TestParityPhaseColumn` (`internal/archtest/parity_phase_test.go:86`), `TestF9ParityDeferred184` (`internal/archtest/f9_parity_deferred_test.go`), `docs/FEATURE-PARITY.md` audit 837/837 | LULUS |
| GAP-SWEEP 118/118 | 118/118 requirement SPEC §22.1 terverifikasi dengan resolusi ADR-033/ADR-034; tabel SPEC §22.1 ≡ MITIGATION-MATRIX §10; `DAMNBOT_RELEASE=1 go test -count=1 -run '^TestF9GapSweep118$' ./internal/archtest` → PASS (118/118) di worktree `f9/gapsweep118`; 11 gap historis kini punya tes bernama yang lulus (SEC-02 `TestEnvAllowlist`, SEC-12 `TestBypassIgnoresAllowDestructive`, REL-06 `TestCancelConfirmed`/`TestCancelConfirmBeforeRedispatch`/`TestToolResultStatusClosed`, REL-15 `TestSkipRatchet`, COST-04 `TestPrefixStableAcrossFallback`, COST-07 `TestNoProgressStop`, COST-15 `TestEmptyResponseBudget`, TOOL-03 `TestWriteReadbackVerify`, UX-01 `TestACPClientFSUnsaved`/`TestACPStaleTagNoOverwrite`/`TestACPClientTerminalTimeout`, INF-05 `TestHookMatchesCI`, INF-07 `TestDependencyAgeGate`; ADR-355) | `TestF9GapSweep118` (`internal/archtest/gap_sweep_test.go`) | LULUS |
| EDGE-CASES 166/166 | 166/166 entri EDGE-CASES `covered` atau `n/a` beralasan; tidak ada `pending:F9`; terverifikasi di main `9ebe1084` dengan `DAMNBOT_RELEASE=1 go test -tags goolm -count=1 -run 'TestF9EdgeCases166\|TestEdgeCaseCatalogSync' ./internal/testutil/phase` → `ok` (166/166, 0 entri terbuka) | `TestF9EdgeCases166` (`internal/testutil/phase/f9_edgecases_test.go`), `TestEdgeCaseCatalogSync` (`internal/testutil/phase/acceptance_test.go:37`) | LULUS |
| OS matrix lengkap | 8 target rilis kanonik (E-39) build/test/runner sesuai CROSS-CUTTING §7; linux/arm dan android/arm64 menjalankan suite inti (ctxdb/exec/acp/headless) di QEMU, bukan hanya cross-compile | `TestF9OSMatrix` + `TestNightlyOSMatrixWired` (`internal/archtest/osmatrix_test.go`), `make build-matrix`, job `os-matrix-qemu` tier `full` (nightly via `ci-full`) dengan `scripts/os-matrix-qemu.sh`, job `test`/`sqlite-engine` tier `full` | LULUS — run [43403236417](https://github.com/yuldan/damn-bot/actions/runs/43403236417) head `7ec3a2c0`: 6/6 leg `test` (ubuntu-latest, ubuntu-24.04-arm, macos-latest, macos-15-intel, windows-latest, windows-11-arm), 2/2 leg `os-matrix-qemu` (linux/arm, android/arm64), `sqlite-engine` 8/8, `ci-required` hijau |
| Soak 24 jam + 1.000 turn + chaos penuh | soak 24 jam stabil (0 leak/0 crash), 1.000 turn, chaos penuh (kill -9, 50 klien, restart gateway) hijau; terverifikasi pada main `8fbafafa` lewat `DAMNBOT_SOAK_HOURS=24 DAMNBOT_SOAK_TURNS=1000` → PASS 86.432,52 s (1.000/1.000 turn di telegram/webhook/api_server, goroutine 23→21, FD 24→23, RSS 45,6→51,8 MB = +13,66% < 20%, 0 `crash_recovered`, `integrity_check=ok`) dan `DAMNBOT_CHAOS_KILLS=200` → PASS 123,70 s (8/8 skenario: Kill9 200 turn pulih rata-rata 105,4 ms/maks 546,4 ms, network cut, disk full, config/DB korup, reboot 229,3 ms, restart gateway, 50 klien 30 s); artefak `bench/f9/results/soak-acceptance-24h-1000t-8fbafafa-F9.json` + `bench/f9/results/chaos-200kills-8fbafafa-F9.json` | `TestF9Soak24h`, `TestF9ChaosFull`, `TestNightlyChaosWired` (`internal/archtest/repometa_test.go:307`) | LULUS |
| Artefak rilis & scorecard | `TestScorecardGate` semua dimensi ≥ hermes dan dimensi inti menang; speed parity (a)/(b) P28; baseline hermes dimensi ADR-065/083 diukur dengan harness sama; artefak scorecard RC dipublikasikan; invariant P1–P29 hijau; `TestChangelogPerTag` | `TestScorecardGate`, `TestF9SpeedParity` (`bench/speed/f9_test.go`), `BenchmarkSpeedParity/<ID>`, invariant P1–P29, artefak `bench/speed/results/` + `bench/h2h/results/` | N/A — re-phased to F10 (ADR-356; P20 RC) |

Catatan 2026-11-13 (scorecard RC, diperbarui ADR-356): `TestScorecardGate` (`bench/scorecard/scorecard_test.go`) dan artefak [`bench/scorecard/results/2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json) sudah ada; ringkasan di [`SCORECARD-RC.md`](SCORECARD-RC.md). Baris ini **tidak** diklaim `LULUS`: owner mere-fase P20 ke F10 (ADR-356) dengan status `N/A — re-phased to F10 (ADR-356; P20 RC)`. Angka apa adanya: gate mencatat **43 dimensi terukur dengan 6 pelanggaran absolut** (fungsi terpanjang 132 > 80, kompleksitas 40 > 15, retry 38 > 1, klasifikasi error string 1 > 0, flake 72% > 0,1%, CI p90 32,62 > 12 menit), **31 dimensi `not_measured`** (Q23-RC, soak penuh, VM instalasi, harness yang belum ada), dan speed parity P28 baru **3/163** ID (`bench/speed/results/damnbot-8fab87144e.json`; `TOOL-045` 5,238 ms vs hermes 3,091 ms). Angka dan alasan tidak diturunkan; tidak ada klaim lulus.

## 2. Ledger parity F9 — UI (1) dan OPS (4)

Kontrak yang sama untuk semua baris: artefak rilis dibangun dari resep tunggal (`make release`/CI), 0 jaringan tersembunyi, 0 nilai secret di artefak/log, dan setiap klaim instalasi dapat dijalankan pada mesin bersih.

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| UI-018 | Bootstrap installer (script) | `install.sh`/`install.ps1` ≤ 150 baris POSIX/PowerShell, unduh→verifikasi `SHA256SUMS` + `.minisig` (ed25519) sebelum ekstrak/eksekusi, tanpa pipe-to-shell, artefak ditandatangani, 0 Node/Electron/Rust | `TestF9UI018BootstrapInstaller`, `TestInstallerRejectsTamper` | LULUS | menggantikan Tauri (SPEC:844) |
| OPS-029 | Docker image & compose | image resmi 1 stage `FROM scratch`/distroless; compose opt-in; healthcheck `doctor health --ready` | `TestF9OPS029DockerCompose` | LULUS | GAP-SEC-22/GAP-INF-03 |
| OPS-030 | Nix flake | `flake.nix` membangun binary yang sama; dev shell tidak menambah dependency runtime | `TestF9OPS030NixFlake` | LULUS | — |
| OPS-031 | Termux/Android | build + unit di QEMU, smoke Termux arm64; 0 syscall Linux-desktop | `TestF9OPS031Termux` | LULUS | OS matrix |
| OPS-032 | Installer scripts | `install.sh`/`install.ps1` ≤ 150 baris POSIX/PowerShell; unduh→verifikasi→jalankan tanpa pipe-to-shell | `TestF9OPS032InstallerScripts`, `TestInstallerRejectsTamper` | LULUS | GAP-SEC-19/GAP-OPS-03 |

## 3. GAP F9 (8) — SPEC §22.1

| GAP | Kriteria verifikasi | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|
| GAP-SEC-19 | installer memverifikasi `SHA256SUMS` + `.minisig` (ed25519, kunci pinned) sebelum ekstrak/eksekusi; paket Homebrew/winget/deb/rpm; Authenticode Windows; 0 instruksi exclusion antivirus | `TestInstallerRejectsTamper`, `TestInstallerDocsNoAVExclusion` | LULUS | P1 |
| GAP-SEC-22 | image `ghcr.io/damnbot/damnbot` distroless static, `USER 65532:65532`, tanpa shell/compiler/docker-cli; `docker.sock` hanya opt-in `DAMNBOT_ALLOW_DOCKER_SOCKET=1` + WARN; backend docker tidak me-mount socket | `TestDockerImageNonRoot`, `TestDockerBackendNoSocketMount` | LULUS | P0 |
| GAP-SEC-28 | `SECURITY.md`: dukungan N/N−1, SLA ack 72 jam/triage 7 hari/fix critical 30 hari, GHSA private + email PGP, proses CVE; `docs/TRUST-MODEL.md` lengkap sesuai SPEC | `TestSecurityPolicySLA`, `TestTrustModelDoc` | LULUS | P1; `SECURITY.md:5`, `docs/TRUST-MODEL.md:5`, tes `internal/archtest/security_docs_test.go:33`, `:48` |
| GAP-OPS-03 | installer hanya menulis blok bermarker `# >>> damnbot >>>`…`# <<< damnbot <<<`; `install-manifest.json` mencatat path + sha256; `setup --uninstall [--dry-run] [--purge-data]` mengembalikan rc byte-identik | `TestInstallerRCMarkers`, `TestUninstallRestoresRC`, `TestInstallManifest` | LULUS | P1 |
| GAP-OPS-05 | satu format arsip portabel (sama dengan backup SEC-01) untuk config + skill + memori `approved`, tanpa secret; resolusi konflik `keep-local`/`take-remote`/`both`; impor melewati threat scan + karantina | `TestArchiveRoundTrip`, `TestArchiveNoSecret`, `TestRedaction` | LULUS | P2; payload DMBK1 via `sessions backup create --portable` + `import archive` (ADR-352); bukti `cmd/damnbot/portable_archive_test.go`, e2e `internal/e2e/archive_test.go` |
| GAP-INF-03 | image `FROM scratch`/distroless static; ukuran ≤ binary full + 2 MB (≤ 54 MB) dan layer terkompresi ≤ 23 MB; varian `-full` opsional; build multi-arch memakai binary rilis yang sama; healthcheck REL-17 | `TestDockerImageSize`, `TestDockerHealthcheck` | LULUS | P2 |
| GAP-INF-10 | referensi flag/config/env/entrypoint/unit/bind dibangkitkan dari kode; link checker offline; 0 flag docs hantu; sha sumber terjemahan + banner basi | `TestDocsLinkCheck`, `TestCLIDocsMatchHelp`, `TestConfigDocSync` | LULUS | P2; ADR-351. Bukti: `cmd/damnbot/docsgen_test.go:55` (ghost flag/perintah + `help` + drift CLI), `:82` (config dua arah), `:125` (link offline), `:145` (banner sha/STALE); artefak `docs/reference/{CLI,ENVIRONMENT,DEPLOYMENT}.md`, `docs/README.md`, `docs/id/README.md` |
| GAP-INF-02 | `fts_meta(name, norm_version, layout_version)`; re-index otomatis background (batch 5.000/transaksi, `partial` sampai selesai); `blob_fts` contentless untuk output tool ≤ 16 MiB; `recall{kind:"tool_result"}` tanpa `LIKE` | `TestFTSAutoUpgrade` (upgrade + kill -9 resume), `TestFTSReindexPartial`, `TestFTSMetaMigration`, `TestFTSBlobContentless`, `TestFTSBlobMaxBytes`, `TestRecallToolOutputUsesMatch`, `BenchmarkRecallToolOutput` + `TestRecallToolOutputP95` (p95 15,98 ms < 100 ms) | LULUS | P2; ADR-193/ROADMAP `:245`; migrasi 0018 `fts_meta.status/total`; EC-STATE-009 tetap sinkron; PR #924 + PR ini |

## 4. EC F9 (0)

Tidak ada EC F9: `docs/ROADMAP.md:54-67` memetakan `EC-PROV`, `EC-TCALL`, `EC-CTX`, `EC-STATE`, `EC-FIO`, `EC-TERM`, `EC-WIN`, `EC-PLAT`, `EC-CFG`, dan `EC-SEC` ke F1–F7; F9 hanya menutup sisa EC-WIN/edge-case yang gagal karena bug (ADR-080), bukan menambah entri baru. Tabel ini sengaja kosong dan tidak boleh diisi dengan status F8. `TestEdgeCaseCatalogSync` tetap berlaku: tidak ada `pending:F9` baru tanpa ADR.

## 5. Carryover F8 → F9 (2, N/A)

| Item | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| Smoke nyata nightly | adapter tanpa akun uji nyata tetap `N/A` selama `nightly.yml` tidak memuat `secrets.`/`vars.`/`DAMNBOT_SMOKE`; fake server tetap wajib | job `nightly` + `TestF7NightlySmokeMatrix` | N/A — no test account for any adapter (nightly smoke) (carryover F10) |
| Q14 image preflight | `BenchmarkImagePreflight` 12 MP ≤ 150 ms hanya sah diukur di Q14; target dan `TestImagePreflightTargetNotLowered` tidak berubah | pengukuran Q14 + artefak `bench/speed/results/` + `TestF7ImagePreflightQ14Artifact` | N/A — hardware: target hanya sah di Q14 (carryover F10) |

## 6. Perintah bukti

```sh
# Acceptance fase yang ditinggalkan (PHASE tetap F9; ADR-176, ADR-347).
make phase-acceptance

# Katalog EC dibaca dari paket pemilik tes; F9 tidak menambah entri.
DAMNBOT_PHASE_ACCEPTANCE=1 go test -tags goolm -count=1 ./internal/testutil/phase

# Gate ledger F9 aktif hanya dengan penanda rilis.
DAMNBOT_RELEASE=1 go test -count=1 -run 'TestF9LedgerClosed|TestParityPhaseColumn|TestF9LedgerGateRules' ./internal/archtest

# Kerangka agregat (skip selama DAMNBOT_RELEASE belum diset).
go test -count=1 -run 'TestF9GapSweep118|TestF9OSMatrix|TestNightlyOSMatrixWired' ./internal/archtest
cd bench && go test -count=1 ./speed

# OS matrix QEMU: hanya CI tier full/nightly (butuh docker + binfmt qemu-user-static).
# Job `os-matrix-qemu` menjalankan scripts/os-matrix-qemu.sh build <target> lalu run <target>.
# build <linux/arm|android/arm64>|run <linux/arm|android/arm64>

# Gerbang arsitektur/ukuran yang tetap berlaku.
make lint
make loc
make deadcode
make size-check
```

## 7. Definisi `N/A` (ADR-347, meneruskan ADR-330 butir 4)

`N/A` bukan `LULUS` dan bukan jalan pintas. Bentuk yang diterima gate: `N/A — <alasan teknis ≥ 8 karakter> (carryover F10)`; alasan wajib menyebut prasyarat yang hilang dan fase target. Pemakaian yang **sah**:

- adapter tanpa akun uji nyata → `N/A — no test account (nightly smoke) (carryover F10)` pada baris *smoke akun nyata* saja; baris fake server tetap wajib `LULUS`.
- pengukuran hardware Q14 → `N/A — hardware: target hanya sah di Q14 (carryover F10)`.
- artefak eksternal yang menunggu keputusan owner saat RC (mis. model/anggaran head-to-head Q23-RC) → hanya bila ADR mencatat penundaan dan baris agregat §1 tetap `BELUM`.
- baris parity F1–F5 yang dire-fase owner → `N/A — re-phased to F10 (ADR-350)`; bentuk ini memindahkan penagihan baris ke F10, bukan menutupnya, dan hanya sah bila daftar 184 ID dipin `TestF9ParityDeferred184`.
- baris agregat §1 yang dire-fase owner secara eksplisit → `N/A — re-phased to F10 (ADR-356; P20 RC)` untuk P20; sah hanya bila ADR mencatat angka apa adanya (43 terukur/31 `not_measured`/6 pelanggaran, P28 3/163), komitmen penuh F10 (74 dimensi, 6 pelanggaran, 160 ID P28, soak/VM/Q23-RC), dan tidak ada target atau ambang yang diturunkan. Ini pemindahan penagihan, bukan `LULUS`.

Pemakaian yang **ditolak**: `N/A` telanjang, `N/A` tanpa alasan (gate menolak < 8 karakter), `N/A` untuk menyembunyikan tes yang gagal, `N/A` pada baris parity yang tesnya bisa dibuat, dan `N/A` pada baris agregat yang anggotanya belum tertutup — kecuali re-fase owner di atas yang beralasan ADR.

## 8. Angka

- Baris data ledger: **15** = 5 parity (UI 1, OPS 4) + 8 GAP + 0 EC + 2 carryover F8.
- Baris agregat §1: **6**. Total baris ledger: **21**. Satu baris agregat (P20) berstatus `N/A — re-phased to F10 (ADR-356; P20 RC)`; sisanya mengikuti status anggotanya.
- P20 (artefak `bench/scorecard/results/2026-11-13-8fab87144e.json`, basis `8fab87144e`): **43 dimensi terukur / 31 `not_measured` / 6 pelanggaran** — fungsi terpanjang 132 > 80 (6 fungsi >80), kompleksitas maks 40 > 15 (23 fungsi >15), retry 38 > 1, klasifikasi error string 1 > 0 (`cron_store.go:88`), flake 18/25 nightly = 72% > 0,1%, CI p90 32,62 menit > 12 (median 25,35). P28 speed parity **3/163** (`TOOL-045` 5.238 ms vs hermes 3.091 ms = regresi; `TOOL-046` 0,509 ms, `TOOL-047` 10,519 ms). First token **6,178 ms vs 39,407 ms = 6,38×** (lulus) dan **16 dimensi** tabel pembanding hermes lulus seluruhnya; sisa 31 dimensi `not_measured` beralasan. Angka dan komitmen F10 ada di ADR-356 dan `docs/SCORECARD-RC.md`; tidak ada target yang diturunkan.
- Parity global: **837/837** penyebut gate (854 baris − 17 `dropped-with-approval`) = **653 `LULUS` + 184 `N/A` re-phased (ADR-350)**, 0 `planned` di F1–F9 setelah PR backfill F3/F4–F5 dan installer F9 bergabung; agregat parity `LULUS` di main `4aad646f` (`TestParityPhaseColumn` + `TestF9ParityDeferred184` hijau; audit `scripts/parity-audit.sh`: total=854, planned=0, LULUS=653, N/A=184, dropped=17).
- GAP F9 resmi: **7** (SEC-19/22/28, OPS-03/05, INF-03/10) + GAP-INF-02 (ADR-193) = **8**; GAP-SWEEP total **118/118**.
- EDGE-CASES: **166/166**; EC F9: **0**.
- OS matrix: **8** target E-39 = linux/{amd64,arm64,arm}, darwin/{amd64,arm64}, windows/{amd64,arm64}, android/arm64 (6 berrunner + 2 QEMU).
- Budget F9: **0 LOC Go produk**; `maxModuleLOC` tetap **132.000** dan `maxCoreLOC` tetap sesuai ADR-344.
- Ledger F8 ditutup pada `9c14575e` (167 baris data: 165 `LULUS` + 2 `N/A`; 7 agregat `LULUS`), lalu `PHASE` naik F8 → **F9**.
