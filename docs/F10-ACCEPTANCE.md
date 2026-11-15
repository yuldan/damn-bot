# F10-ACCEPTANCE — Ledger closure P20, parity 184, dan regresi F9

Status: **ledger hidup** untuk fase F10 (ADR-357) pada `origin/main` `0001e301`.
`PHASE` tetap `F9`; F10 adalah fase closure pasca-1.0 dan tidak menaikkan file
`PHASE` sebelum ledger, parser, dan gate-nya siap. Status per baris ada di kolom
`Status`; setiap baris agregat §1 baru `LULUS` setelah semua anggotanya tertutup,
kecuali `N/A — <alasan teknis> (ADR owner)` yang disetujui pemilik.

Sumber normatif: [`ROADMAP.md`](ROADMAP.md) §F10, [`SCORECARD.md`](SCORECARD.md)
§1.1–§1.8 dan §4, [`SCORECARD-RC.md`](SCORECARD-RC.md),
[`FEATURE-PARITY.md`](FEATURE-PARITY.md) (184 baris ADR-350),
[`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) (lima agregat yang harus tetap `LULUS`),
dan [`DECISIONS.md`](DECISIONS.md) ADR-347/ADR-350/ADR-356/ADR-357.

Aturan gate:

- **Penutupan.** Baris hanya `LULUS` bila PR yang sama menambah tes bernama/bukti
  yang dapat dijalankan dan menulis buktinya di kolom `Catatan`. `BELUM` berarti
  belum ada bukti, bukan gagal.
- **Gate mesin.** `TestF10LedgerClosed` menolak `BELUM`, `N/A` telanjang
  (< 8 karakter alasan), inventaris P20 yang tidak berjumlah 74, baris parity
  pinned yang belum tertutup, dan regresi agregat F9. Gate **nonaktif** selama
  `PHASE=F9` dan `DAMNBOT_F10_RELEASE` belum diset; aktif bila penanda itu
  diset atau `PHASE` menjadi `F10`. `TestF10LedgerGateRules` adalah kontrol
  negatifnya.
- **Parity.** `TestF10ParityPinned184` mem-pin 184 ID literal ADR-350; baris
  boleh kembali `LULUS` dengan tes di kolom `Catatan`, atau menjadi
  `N/A — <alasan> (ADR owner)`. `N/A — re-phased to F10 (ADR-350)` tetap sah
  selama ledger hidup dan **ditolak** saat gate penutupan aktif
  (`TestF10ParityClosed184`).
- **Tanpa penurunan target.** Tidak boleh mengubah ambang SCORECARD, menambah
  `t.Skip` tanpa alasan, memakai `N/A` tanpa prasyarat yang benar-benar tidak
  dapat disediakan repo, atau menurunkan denominator 74/163/184/837.

## 1. Gate F10 (G1–G6)

| ID | Gate | Definisi lulus | Tes/perintah | Status |
|---|---|---|---|---|
| G1 | P20 dimensi | 74/74 terukur; 0 `not_measured`; 0 pelanggaran; dimensi inti menang dengan margin | `cd bench && DAMNBOT_RELEASE=1 go test -count=1 ./scorecard`; `TestScorecardGate` | BELUM |
| G2 | P28 speed parity | 163/163 operasi `measured` dengan protokol yang sama; target kelas (a)/(b) SCORECARD §3; tidak ada operasi lebih lambat; regresi `TOOL-045` tertutup | `cd bench && DAMNBOT_RELEASE=1 go test -count=1 ./speed`; `TestF9SpeedParity` + `BenchmarkSpeedParity/<ID>` | BELUM |
| G3 | Parity 184 | 184/184 tertutup: `LULUS` dengan tes atau `N/A — <alasan> (ADR owner)`; 0 `planned`, 0 status re-fase ADR-350; 837/837 penyebut gate | `TestF10ParityPinned184`, `TestF10ParityClosed184`, `TestParityPhaseColumn`, `scripts/parity-audit.sh` | BELUM |
| G4 | Regresi F9 | Lima agregat F9 yang sudah `LULUS` tidak turun: parity 837/837, GAP-SWEEP 118/118, EDGE-CASES 166/166, OS matrix 8/8, soak/chaos 24 jam | `DAMNBOT_RELEASE=1 make phase-acceptance`; `TestF9LedgerClosed`, `TestF9GapSweep118`, `TestF9EdgeCases166`, `TestF9OSMatrix`, `TestF9SpeedParity` | LULUS |
| G5 | Release invariant | P1–P29 hijau; `TestChangelogPerTag`; `make lint loc size-check deadcode` 0 temuan; tidak ada `t.Skip` baru tanpa alasan | suite invariant + `make lint loc size-check deadcode` | BELUM |
| G6 | Artefak | Scorecard RC, speed, dan h2h dipublikasikan dengan SHA-256 sumber dan dapat diregenerasi pada commit yang sama | `SCORECARD_UPDATE=1 go test -run TestScorecardGate ./scorecard`; artefak `bench/*/results/` | BELUM |

## 2. Inventory P20 (74 dimensi)

Denominator tertutup 74 dimensi SCORECARD §1.1–§1.8; artefak awal
[`2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json)
memuat **37 `LULUS`, 6 pelanggaran, 31 `not_measured`** (semua pelanggaran dan
`not_measured` berstatus `BELUM` di ledger ini). Angka, target, dan alasan ada di
[`SCORECARD-RC.md`](SCORECARD-RC.md); ledger ini tidak menyalin target.

| ID | Dimensi | § | Status | Catatan |
|---|---|---|---|---|
| P20-01 | Akurasi routing skill | 1.1 | BELUM | Q23-RC |
| P20-02 | Background token | 1.1 | BELUM | Q23-RC |
| P20-03 | Biaya per task | 1.1 | BELUM | Q23-RC |
| P20-04 | Cache hit rate | 1.1 | BELUM | Q23-RC |
| P20-05 | Fakta basi | 1.1 | BELUM | Q23-RC |
| P20-06 | Fixed overhead | 1.1 | LULUS | artefak scorecard |
| P20-07 | Klaim selesai tanpa bukti | 1.1 | BELUM | Q23-RC |
| P20-08 | Long-session stabilitas | 1.1 | BELUM | Q23-RC |
| P20-09 | Recall memori lintas sesi ("ga pikun") | 1.1 | BELUM | Q23-RC |
| P20-10 | Task success rate | 1.1 | BELUM | Q23-RC |
| P20-11 | Token input per task | 1.1 | BELUM | Q23-RC |
| P20-12 | Token output per task | 1.1 | BELUM | Q23-RC |
| P20-13 | Token skill per turn | 1.1 | LULUS | artefak scorecard |
| P20-14 | Tool call invalid / halusinasi | 1.1 | BELUM | Q23-RC |
| P20-15 | Tool error recovery | 1.1 | BELUM | Q23-RC |
| P20-16 | CPU idle | 1.2 | LULUS | artefak scorecard |
| P20-17 | Code tools | 1.2 | BELUM | harness korpus/host |
| P20-18 | Exec spawn overhead | 1.2 | LULUS | artefak scorecard |
| P20-19 | First token di UI | 1.2 | LULUS | 6,38× |
| P20-20 | Idle RSS | 1.2 | LULUS | artefak scorecard |
| P20-21 | Image preflight | 1.2 | LULUS | Q14 bukan gate |
| P20-22 | Overhead agent per iterasi | 1.2 | BELUM | Q23-RC |
| P20-23 | Peak RSS | 1.2 | BELUM | harness VmHWM (serve+api-server) siap; run 1 jam/1.000 turn tertunda host (ADR-359) |
| P20-24 | Startup | 1.2 | LULUS | 27,2× |
| P20-25 | Task wall-clock end-to-end | 1.2 | BELUM | Q23-RC |
| P20-26 | Voice end-to-end | 1.2 | LULUS | artefak scorecard |
| P20-27 | Crash/hang rate | 1.3 | BELUM | soak |
| P20-28 | Edit salah lokasi | 1.3 | BELUM | harness oracle |
| P20-29 | Error tertelan | 1.3 | BELUM | harness injeksi |
| P20-30 | Fallback tak terlihat | 1.3 | BELUM | harness event |
| P20-31 | Konkurensi | 1.3 | LULUS | artefak scorecard |
| P20-32 | Pemulihan setelah `kill -9` | 1.3 | BELUM | soak/chaos |
| P20-33 | Proses yatim / perintah hang | 1.3 | BELUM | harness |
| P20-34 | Self-healing | 1.3 | BELUM | soak/chaos |
| P20-35 | Baris config minimum | 1.4 | BELUM | VM 10 Mbit/s |
| P20-36 | Error informatif | 1.4 | BELUM | 2 penilai manusia |
| P20-37 | Langkah setup | 1.4 | BELUM | VM 10 Mbit/s |
| P20-38 | Migrasi dari hermes | 1.4 | LULUS | 5 fixture |
| P20-39 | Ukuran install | 1.4 | BELUM | VM 10 Mbit/s |
| P20-40 | Waktu install dari nol | 1.4 | BELUM | VM 10 Mbit/s |
| P20-41 | Bahasa UI | 1.5 | LULUS | 18 locale |
| P20-42 | Dukungan OS | 1.5 | LULUS | 8 target |
| P20-43 | Feature parity | 1.5 | LULUS | 837/837 (lihat G3) |
| P20-44 | MCP | 1.5 | LULUS | 65 |
| P20-45 | Platform chat | 1.5 | LULUS | 30 |
| P20-46 | Provider | 1.5 | LULUS | 88 |
| P20-47 | Skill tersedia | 1.5 | LULUS | 211 |
| P20-48 | Akses gateway orang asing | 1.6 | LULUS | 0 call |
| P20-49 | Data at-rest | 1.6 | LULUS | 0 plaintext |
| P20-50 | Prompt-injection suite | 1.6 | BELUM | Q23-RC |
| P20-51 | Secret redaction | 1.6 | LULUS | 0 kebocoran |
| P20-52 | Supply chain | 1.6 | LULUS | 0 reachable |
| P20-53 | Broad catch (`except Exception` / error diabaikan) | 1.7 | LULUS | 0 |
| P20-54 | Dependency langsung / CGO | 1.7 | LULUS | 24 ≤ 28 |
| P20-55 | File terbesar (non-test / test) | 1.7 | LULUS | 800/1.077 |
| P20-56 | Flake rate | 1.7 | BELUM | 72% > 0,1% |
| P20-57 | Fungsi terpanjang / > 80 baris | 1.7 | BELUM | 132 > 80 |
| P20-58 | Implementasi retry | 1.7 | BELUM | 38 > 1 |
| P20-59 | Kebocoran goroutine | 1.7 | LULUS | 53 paket |
| P20-60 | Klasifikasi error via string | 1.7 | BELUM | 1 > 0 |
| P20-61 | Kode mati | 1.7 | LULUS | 0 |
| P20-62 | Kompleksitas siklomatik maks / > 15 | 1.7 | BELUM | 40 > 15 |
| P20-63 | LOC per fitur | 1.7 | LULUS | 192,5 |
| P20-64 | Rasio test:source | 1.7 | LULUS | 0,93 |
| P20-65 | Siklus import / komentar "hindari circular import" | 1.7 | LULUS | 0 |
| P20-66 | State global mutable | 1.7 | LULUS | 0 |
| P20-67 | Temuan lint | 1.7 | LULUS | 0 |
| P20-68 | Waktu CI PR | 1.7 | BELUM | p90 32,62 > 12 |
| P20-69 | Waktu build bersih / inkremental | 1.7 | LULUS | 29,140 s |
| P20-70 | Budget guard | 1.8 | LULUS | 0 call |
| P20-71 | Plan mode | 1.8 | LULUS | 100% |
| P20-72 | Preview aksi berbahaya | 1.8 | LULUS | 100% |
| P20-73 | Scorecard mingguan | 1.8 | BELUM | job + satu run |
| P20-74 | Undo universal | 1.8 | LULUS | 100% |

## 3. Parity F10 (184 baris ADR-350)

Daftar ID literal, aturan status, dan closure gate ada di ADR-350 (daftar
`F9-DEFERRED-184`) dan ADR-357. `TestF10ParityPinned184` menjaga 184 ID; baris
boleh kembali `LULUS` lewat tes bernama, tetapi tidak boleh hilang, berubah fase
(`F1`–`F5`), atau menjadi `dropped-with-approval`. Denominator gate tetap 837
(854 − 17 dropped), dan `TestParityPhaseColumn` tidak berubah.

| ID | Item | Jumlah | Tes | Status |
|---|---|---|---|---|
| F10-PARITY-184 | Parity F1–F5 dire-fase ADR-350 (121 deferred + 63 residual) | 184 | `TestF10ParityPinned184`, `TestF10ParityClosed184` | BELUM |

Progres wave 1/B3 (2026-11-15): **2 baris `LULUS`** — CLI-033 dan OPS-025;
**182 baris** masih terbuka. Bukti nama tes dan path ada di kolom Catatan
[`FEATURE-PARITY.md`](FEATURE-PARITY.md); baris agregat ini tetap `BELUM` sampai
184/184 tertutup.

Rincian per prefix (tidak menambah baris parity berfase F10):

| Prefix | Total | 121 belum ada | 63 residual |
|---|---:|---:|---:|
| CLI | 32 | 22 | 10 |
| SLASH | 57 | 48 | 9 |
| PROV | 27 | 8 | 19 |
| EXEC | 17 | 10 | 7 |
| PLUG | 11 | 9 | 2 |
| CORE | 8 | 4 | 4 |
| TSET | 7 | 2 | 5 |
| UI | 6 | 5 | 1 |
| OPS | 5 | 4 | 1 |
| MEM | 4 | 3 | 1 |
| SKILL | 4 | 3 | 1 |
| AUTO | 4 | 3 | 1 |
| TOOL | 1 | 0 | 1 |
| GW | 1 | 0 | 1 |
| **Total** | **184** | **121** | **63** |

## 4. Regresi F9 (lima agregat `LULUS`)

Lima agregat yang sudah `LULUS` di [`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) §1
tidak boleh turun. Baris keenam F9 (artefak rilis & scorecard) adalah pekerjaan
G1–G6 di ledger ini, bukan regresi.

| ID | Agregat F9 | Tes/bukti | Status |
|---|---|---|---|
| F9-REG-1 | Gate parity 837/837 | `TestParityPhaseColumn`, `TestF10ParityPinned184`, `scripts/parity-audit.sh` | LULUS |
| F9-REG-2 | GAP-SWEEP 118/118 | `DAMNBOT_RELEASE=1 go test -run '^TestF9GapSweep118$' ./internal/archtest` | LULUS |
| F9-REG-3 | EDGE-CASES 166/166 | `TestF9EdgeCases166`, `TestEdgeCaseCatalogSync` | LULUS |
| F9-REG-4 | OS matrix lengkap 8/8 | `TestF9OSMatrix`, `TestNightlyOSMatrixWired`, run `43403236417` | LULUS |
| F9-REG-5 | Soak 24 jam + 1.000 turn + chaos penuh | artefak `bench/f9/results/soak-acceptance-24h-1000t-8fbafafa-F9.json`, `chaos-200kills-8fbafafa-F9.json` | LULUS |

## 5. Pengecualian eksternal (harus ditutup atau `N/A` dengan persetujuan owner)

Prasyarat yang tidak dapat disediakan repo **tidak otomatis** menjadi `N/A`.
Baris di bawah tetap `BELUM` sampai bukti tersedia atau owner menyetujui
`N/A — <alasan teknis ≥ 8 karakter> (ADR-357)`; persetujuan wajib menyebut
prasyarat, dimensi/baris yang terkena, dan komitmen agar target tidak turun.

| ID | Prasyarat eksternal | Terkena | Status |
|---|---|---|---|
| EXT-1 | VM bersih 10 Mbit/s (network shaping) | P20-35, P20-37, P20-39, P20-40 | BELUM |
| EXT-2 | Dua penilai manusia independen | P20-36 | BELUM |
| EXT-3 | Model frontier + anggaran Q23-RC | P20-01..P20-05, P20-07..P20-12, P20-14, P20-15, P20-22, P20-25, P20-50 | BELUM |

## 6. Perintah bukti

```sh
# Pin dan gate ledger F10 (gate penutupan nonaktif selama PHASE=F9).
go test ./internal/archtest -run 'TestF10|TestParityPhaseColumn' -count=1
go test ./internal/testutil/phase -count=1

# Aktifkan gate penutupan: sengaja gagal selama masih ada BELUM / parity terbuka.
DAMNBOT_F10_RELEASE=1 go test ./internal/archtest -run 'TestF10LedgerClosed|TestF10ParityClosed184' -count=1

# Regresi F9 tetap berlaku.
DAMNBOT_RELEASE=1 make phase-acceptance
DAMNBOT_RELEASE=1 go test -count=1 -run 'TestF9LedgerClosed|TestF9LedgerGateRules' ./internal/archtest

# Gate P20/P28 (gagal sampai closure; jangan dilonggarkan).
cd bench && DAMNBOT_RELEASE=1 go test -count=1 ./scorecard ./speed
```

## 7. Definisi `N/A` F10

`N/A` bukan `LULUS`. Bentuk yang diterima gate: `N/A — <alasan teknis ≥ 8
karakter> (ADR owner)`. Pemakaian sah hanya bila:

- prasyarat eksternal benar-benar tidak dapat disediakan repo (VM, penilai
  manusia, model/anggaran) **dan** ADR owner menyetujui, atau
- baris parity sudah diekuivalenkan/dibuktikan dengan tes bernama sehingga
  `N/A` bukan lagi jalur yang tepat (dalam hal ini statusnya `LULUS`).

`N/A — re-phased to F10 (ADR-350)` sah selama ledger hidup, tetapi ditolak
`TestF10ParityClosed184` saat gate penutupan aktif. Ditolak: `N/A` telanjang,
alasan < 8 karakter, `N/A` untuk menyembunyikan tes yang gagal, dan `N/A` tanpa
ADR owner.

## 8. Angka

- Gate: **6** (G1–G6); satu `LULUS` (G4), lima `BELUM`.
- P20: **74** dimensi = 37 `LULUS` + 6 pelanggaran + 31 `not_measured`
  (artefak `8fab87144e`). Pelanggaran: fungsi 132 > 80, kompleksitas 40 > 15,
  retry 38 > 1, klasifikasi error string 1 > 0, flake 72% > 0,1%, CI p90
  32,62 > 12 menit.
- P28 speed: **3/163** operasi terukur (`TOOL-045` 5,238 ms vs hermes 3,091 ms
  = regresi; `TOOL-046` 0,509 ms; `TOOL-047` 10,519 ms).
- Parity: **184** ID pinned (121 + 63); 837/837 penyebut gate = 653 `LULUS` +
  184 `N/A` re-fase saat awal F10.
- Regresi F9: **5** agregat `LULUS`.
- Pengecualian eksternal: **3** kelompok (`EXT-1`..`EXT-3`), semuanya `BELUM`.
- Budget: **0 LOC Go produk** untuk fondasi ini; plafon `maxModuleLOC` dan
  `maxCoreLOC` tidak berubah. Satu baris infra `testutil` naik 200 → 202
  (parser F10 terukur 202; infra di luar Σ core), TOTAL tidak berubah. Alokasi
  LOC fitur F10 wajib diukur per slice dan disetujui ADR sebelum PR fitur; tidak
  ada angka LOC fitur yang dikarang di sini.

## 9. Definisi selesai F10

F10 selesai hanya bila semua benar pada commit rilis yang sama:

- [ ] G1: 74/74 dimensi terukur, 0 `not_measured`, 0 pelanggaran, inti menang.
- [ ] G2: 163/163 operasi speed `measured`, target kelas terpenuhi, `TOOL-045`
      tidak lagi regresi.
- [ ] G3: 184/184 parity tertutup (`LULUS` bertes atau `N/A` ber-ADR), 0
      `planned`, 0 status re-fase ADR-350, 837/837.
- [ ] G4: lima agregat F9 tetap `LULUS`; `make phase-acceptance` hijau.
- [ ] G5: P1–P29, `TestChangelogPerTag`, `lint/loc/size-check/deadcode` hijau.
- [ ] G6: artefak RC/speed/h2h dipublikasikan dengan SHA-256 dan dapat
      diregenerasi.
- [ ] Tidak ada target/ambang diturunkan dan tidak ada `N/A` tanpa persetujuan
      owner; `PHASE` baru boleh naik ke F10 setelah ledger dan gate hijau.
