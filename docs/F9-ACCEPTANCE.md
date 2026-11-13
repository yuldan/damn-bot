# F9-ACCEPTANCE — Ledger acceptance F9 (hardening & rilis 1.0)

Status: **ledger hidup** untuk `PHASE=F9`. Status per baris ada pada kolom `Status`; setiap baris agregat §1 tetap `BELUM` sampai semua anggotanya tertutup (Gate parity kini `LULUS`, lihat §1). Kriteria dan angka tidak berubah dari fase sebelumnya kecuali baris yang eksplisit berstatus `LULUS`. Satu baris hanya boleh berubah menjadi `LULUS` pada PR yang benar-benar menambahkan tes bernama di kolom "Tes yang harus ada" (dan/atau artefak yang disebut) lalu tes/artefak itu lulus, atau menjadi `N/A` beralasan yang memenuhi §7. Baris agregat §1 hanya `LULUS` setelah semua anggotanya tertutup.

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
| GAP-SWEEP 118/118 | 118/118 requirement SPEC §22.1 terverifikasi dengan resolusi ADR-033/ADR-034; tabel SPEC §22.1 ≡ MITIGATION-MATRIX §10 | `TestF9GapSweep118` (`internal/archtest/gap_sweep_test.go`) | BELUM |
| EDGE-CASES 166/166 | 166/166 entri EDGE-CASES `covered` atau `n/a` beralasan; tidak ada `pending:F9` | `TestF9EdgeCases166` (`internal/testutil/phase/f9_edgecases_test.go`), `TestEdgeCaseCatalogSync` (`internal/testutil/phase/acceptance_test.go:37`) | BELUM |
| OS matrix lengkap | 8 target rilis kanonik (E-39) build/test/runner sesuai CROSS-CUTTING §7; linux/arm dan android/arm64 menjalankan suite inti (ctxdb/exec/acp/headless) di QEMU, bukan hanya cross-compile | `TestF9OSMatrix` + `TestNightlyOSMatrixWired` (`internal/archtest/osmatrix_test.go`), `make build-matrix`, job `os-matrix-qemu` tier `full` (nightly via `ci-full`) dengan `scripts/os-matrix-qemu.sh`, job `test`/`sqlite-engine` tier `full` | BELUM |
| Soak 24 jam + 1.000 turn + chaos penuh | soak 24 jam stabil (0 leak/0 crash), 1.000 turn, chaos penuh (kill -9, 50 klien, restart gateway) hijau | `TestF9Soak24h`, `TestF9ChaosFull`, `TestNightlyChaosWired` (`internal/archtest/repometa_test.go:307`) | BELUM |
| Artefak rilis & scorecard | `TestScorecardGate` semua dimensi ≥ hermes dan dimensi inti menang; speed parity (a)/(b) P28; baseline hermes dimensi ADR-065/083 diukur dengan harness sama; artefak scorecard RC dipublikasikan; invariant P1–P29 hijau; `TestChangelogPerTag` | `TestScorecardGate`, `TestF9SpeedParity` (`bench/speed/f9_test.go`), `BenchmarkSpeedParity/<ID>`, invariant P1–P29, artefak `bench/speed/results/` + `bench/h2h/results/` | BELUM |

Catatan 2026-11-13 (scorecard RC): `TestScorecardGate` (`bench/scorecard/scorecard_test.go`) dan artefak [`bench/scorecard/results/2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json) sudah ada; ringkasan di [`SCORECARD-RC.md`](SCORECARD-RC.md). Baris ini tetap `BELUM`: gate mencatat **43 dimensi terukur dengan 2 pelanggaran** (flake 3,33%, CI p90 23,5 menit) (First token di UI 6,178 vs 39,407 ms = 6,38× setelah render 120 fps), 31 dimensi `not_measured` (Q23-RC, soak penuh, VM instalasi, artefak keamanan/kualitas), dan `TestF9SpeedParity` belum punya artefak sisi Damn-Bot (`bench/speed/results/damnbot-*.json`). Angka dan alasan tidak diturunkan; tidak ada klaim lulus.

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
| GAP-OPS-05 | satu format arsip portabel (sama dengan backup SEC-01) untuk config + skill + memori `approved`, tanpa secret; resolusi konflik `keep-local`/`take-remote`/`both`; impor melewati threat scan + karantina | `TestArchiveRoundTrip`, `TestArchiveNoSecret`, `TestRedaction` | BELUM | P2 |
| GAP-INF-03 | image `FROM scratch`/distroless static; ukuran ≤ binary full + 2 MB (≤ 54 MB) dan layer terkompresi ≤ 23 MB; varian `-full` opsional; build multi-arch memakai binary rilis yang sama; healthcheck REL-17 | `TestDockerImageSize`, `TestDockerHealthcheck` | LULUS | P2 |
| GAP-INF-10 | referensi flag/config/env/entrypoint/unit/bind dibangkitkan dari kode; link checker offline; 0 flag docs hantu; sha sumber terjemahan + banner basi | `TestDocsLinkCheck`, `TestCLIDocsMatchHelp`, `TestConfigDocSync` | BELUM | P2 |
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

Pemakaian yang **ditolak**: `N/A` telanjang, `N/A` tanpa alasan (gate menolak < 8 karakter), `N/A` untuk menyembunyikan tes yang gagal, `N/A` pada baris parity yang tesnya bisa dibuat, dan `N/A` pada baris agregat yang anggotanya belum tertutup.

## 8. Angka

- Baris data ledger: **15** = 5 parity (UI 1, OPS 4) + 8 GAP + 0 EC + 2 carryover F8.
- Baris agregat §1: **6**. Total baris ledger: **21**.
- Parity global: **837/837** penyebut gate (854 baris − 17 `dropped-with-approval`) = **653 `LULUS` + 184 `N/A` re-phased (ADR-350)**, 0 `planned` di F1–F9 setelah PR backfill F3/F4–F5 dan installer F9 bergabung; agregat parity `LULUS` di main `4aad646f` (`TestParityPhaseColumn` + `TestF9ParityDeferred184` hijau; audit `scripts/parity-audit.sh`: total=854, planned=0, LULUS=653, N/A=184, dropped=17).
- GAP F9 resmi: **7** (SEC-19/22/28, OPS-03/05, INF-03/10) + GAP-INF-02 (ADR-193) = **8**; GAP-SWEEP total **118/118**.
- EDGE-CASES: **166/166**; EC F9: **0**.
- OS matrix: **8** target E-39 = linux/{amd64,arm64,arm}, darwin/{amd64,arm64}, windows/{amd64,arm64}, android/arm64 (6 berrunner + 2 QEMU).
- Budget F9: **0 LOC Go produk**; `maxModuleLOC` tetap **132.000** dan `maxCoreLOC` tetap sesuai ADR-344.
- Ledger F8 ditutup pada `9c14575e` (167 baris data: 165 `LULUS` + 2 `N/A`; 7 agregat `LULUS`), lalu `PHASE` naik F8 → **F9**.
