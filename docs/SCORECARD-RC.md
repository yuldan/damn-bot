# SCORECARD RC F9 — artefak terpublikasi (P20 dire-fase ke F10, ADR-356)

Artefak mesin: [`bench/scorecard/results/2026-11-15-6b63d78f16.json`](../bench/scorecard/results/2026-11-15-6b63d78f16.json).
Gate: `TestScorecardGate` (`bench/scorecard/scorecard_test.go`); artefak dibangun
ulang oleh tes yang sama (`SCORECARD_UPDATE=1`). Basis: `6b63d78f16` (pohon produk P20 setelah rebase ke `ee2647d7`; sumber soak 24 jam tetap `8fbafafa`, chaos `0001e301`, tooling `8fab87144e`, dan dicatat per baris), hermes `868b9f244f`.

Status: **P20 dire-fase ke F10 (ADR-356)**. Owner menyetujui Opsi A pada 2026-11-13:
baris agregat F9 "Artefak rilis & scorecard" **tidak** diklaim `LULUS`; statusnya
`N/A — re-phased to F10 (ADR-356; P20 RC)` (lihat [`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) §1
dan [`DECISIONS.md`](DECISIONS.md) ADR-356). Artefak P20 terbaru memuat **46 dimensi terukur
dan 28 `not_measured`**, dengan **5 pelanggaran pada dimensi terukur**: fungsi
terpanjang 132 > 80, kompleksitas 40 > 15, retry 38 > 1, flake 72% (18/25 nightly)
> 0,1%, dan CI p90 32,62 > 12 menit. Klasifikasi error via string kini **0**
(`cron_store.go` memakai `ctxdb.ErrDuplicate`, ditegakkan
`TestNoErrorStringClassification`). Tiga dimensi stabilitas terukur dari
soak/chaos: crash/hang 0, kill -9 0/0/0, dan self-healing 0,597 s. `Peak RSS`
tetap `not_measured`: artefak soak 24 jam/1.000 turn `8fbafafa` hanya merekam RSS
awal/akhir, bukan `VmHWM`; harness kini menulis `peak_kb` (VmHWM) untuk run
berikutnya, dan angka 98,5 MB dari soak 14,5 jam yang gagal (`9717027e`) tidak
diklaim. Bukti positif
tidak disembunyikan: first token 6,178 ms vs hermes 39,407 ms = **6,38×** (margin
inti ≥5× terpenuhi) dan 16 dimensi pembanding hermes lulus. Tidak ada target yang
diturunkan dan tidak ada angka yang dikarang.

## Dimensi terukur

Angka diambil ulang dari artefak fase (SHA-256 tercatat di `meta.sources_sha256`).

| Dimensi | hermes | Damn-Bot | Aturan | Hasil |
|---|---:|---:|---|---|
| Fixed overhead [I] | 16.843 tok | 2.809 tok | ≤ 0,35× | lulus (0,17×) |
| Startup [I] | 1.368,273 ms | 50,317 ms | ≤ 0,1× (10×) | lulus (27,2×) |
| First token di UI [I] | 39,407 ms | 6,178 ms | ≤ 0,2× (5×) | lulus (6,38×) |
| Idle RSS | 210,4 MB | 24,570 MB | ≤ 0,3× | lulus |
| CPU idle | 1,4 % | 0,233 % | ≤ hermes | lulus |
| Token skill per turn | 1,0 (rasio) | 0,0750 | ≤ 0,2× | lulus |
| Konkurensi | 0 error | 0 error | sama | lulus |
| Voice end-to-end | 1,597 ms | 0,076 ms | ≤ hermes | lulus |
| Image preflight | 453,536 ms | 265,000 ms | ≤ hermes | lulus vs hermes; target absolut 150 ms hanya sah di Q14 (ADR-323), tidak diklaim |
| Waktu build bersih | 66,276 s | 29,140 s | ≤ hermes | lulus (inkremental 4,756 s vs 1,698 s) |
| Feature parity [I] | 854 baris | 837 baris | = 837/837 | lulus |
| Provider | 50 profil | 88 profil | ≥ hermes | lulus |
| Bahasa UI | 17 locale | 18 locale | ≥ hermes | lulus |
| Skill tersedia | 211 | 211 | ≥ 211 | lulus |
| Platform chat | 30 | 30 | ≥ 30 | lulus |
| MCP | 65 | 65 | ≥ 65 | lulus |
| LOC per fitur | 1.162 | 192,5 | ≤ hermes | lulus |
| Rasio test:source | 1,48 | 0,93 | target 0,80–1,20 | lulus |
| Siklus import | 149 komentar | 0 | 0 | lulus |
| Lint / broad catch / global mutable | n/a | 0 / 0 / 0 | 0 | lulus |
| Dependency langsung | 55 | 24 | ≤ 28, CGO 0 | lulus |
| Kode mati | n/a | 0 | 0 | lulus |
| Secret redaction | n/a | 0 kebocoran | 0 | lulus |
| Akses gateway orang asing | n/a | 0 call | 0 | lulus |
| Supply chain (reachable) | n/a | 0 | 0 | lulus |
| Data at-rest | plaintext `.env` | 0 | 0 | lulus |
| Migrasi dari hermes | n/a | 5 fixture | ≥ 5 | lulus |
| Undo / preview / plan | n/a | 100% / 100% / 100% | 100% | lulus |
| Budget guard | n/a | 0 call setelah cap | 0 | lulus |
| Dukungan OS | n/a | 8 target | ≥ 8 | lulus |
| File terbesar non-test/test | 8.392 / 23.074 | 800 / 1.077 | ≤800 / ≤1.500 | lulus |
| Fungsi terpanjang / >80 | 616 baris / 716 | 132 baris / 6 | ≤80 / 0 | **gagal** (absolut) |
| Kompleksitas maks / >15 | 102 / 2.475 | 40 / 23 | ≤15 / 0 | **gagal** (absolut) |
| Implementasi retry | 169 | 38 fungsi (1 kebijakan) | 1 | **gagal** (absolut) |
| Klasifikasi error via string | 41 + 88 | 0 | 0 | lulus |
| Kebocoran goroutine | n/a | 0 (53 paket goleak) | 0 | lulus |
| Exec spawn overhead | n/a | p95 1,202 ms (mean 0,751) | <15 ms | lulus |
| Flake rate | n/a | 72% (18/25; 18/20 dieksekusi) | ≤0,1% | **gagal** |
| Waktu CI PR p90 | n/a | 32,62 menit (median 25,35) | ≤12 | **gagal** |
| Crash/hang rate [I] | 0 | 0 per 1.000 turn | 0 | lulus (soak 24 jam) |
| Pemulihan setelah `kill -9` [I] | 0 | 0/0/0 (200 kill) | 0 | lulus |
| Self-healing [I] | 10 s | 0,597 s maks (kill -9), 0,178 s reboot | ≤10 s | lulus |
| Peak RSS | 211,5 MB (peak idle F4) | `not_measured` | ≤0,5× | belum terukur; artefak soak hanya RSS akhir, `peak_kb` (VmHWM) baru dicatat run berikutnya |

Sumber: `bench/budget/results/hermes-868b9f244f.json`, `internal/assemble/testdata/p1/all-modules.request.json`,
`bench/f2/results/hermes-868b9f244f-F2.json`, `bench/f3/results/{damnbot-8fab87144e,damnbot-6d01e7a1d5,hermes-868b9f244f}-F3.json`,
`bench/f4/results/{damnbot-4c1e69ba,hermes-868b9f244f}-F4.json`,
`bench/speed/results/hermes-868b9f244f-F6.json`, `docs/FEATURE-PARITY.md`,
`bench/scorecard/results/tooling-8fab87144e.json` (goleak 53 paket, go/ast main
module, gocyclo, `gh api actions/runs` 30 hari).

Buildtime memakai artefak `6d01e7a1d5` karena pengukuran build baru berjalan di
bawah nice/6-CPU host bersama (143,965 s) dan tidak sebanding dengan baseline
hermes 66,276 s; first token/startup memakai artefak final `8fab87144e`.

## Dimensi belum terukur

28 dimensi `not_measured` (rincian di artefak):

- Token input/output, biaya, cache, background token, task success, invalid tool
  call, klaim tanpa bukti, recall, fakta basi, routing skill, long-session, error
  recovery, overhead iterasi, wall-clock, prompt-injection: RC head-to-head penuh
  belum dijalankan (Q23-RC: model frontier & anggaran). Probe 10 task dengan
  `deepseek-v4.1-flash` ada di `bench/h2h/results/2026-11-1{2,3}-f9-rc-probe-*`.
- Ukuran & waktu install dari nol, langkah setup, baris config: butuh VM bersih
  10 Mbit/s; host ini bukan VM RC dan tidak ada docker/root.
- Harness yang belum punya artefak terukur: code tools (korpus/host beda),
  proses yatim/perintah hang, edit salah lokasi, error tertelan, fallback tak
  terlihat.
- `Error informatif`: menunggu 2 penilai manusia.
- `Scorecard mingguan`: job ada, aktivasi setelah RC.

## Speed parity

`TestF9SpeedParity` masih SKIP/gagal: **96 dari 163** operasi hermes kini punya
pengukuran Damn-Bot dengan protokol yang sama
(`bench/speed/results/damnbot-6b63d78f16.json`; CLI cold start 1 warm-up + 5,
tools/assemble/gateway/TUI in-process 1 + 20). Tujuh baris masih **regresi**
terhadap hermes: `EXEC-012`, `TOOL-022`, `SKILL-002`, `SKILL-016`, `TOOL-076`,
`TOOL-048`, dan `TOOL-045` (3,09 ms). **67 ID** menunggu parity dan tetap
`not_measured` dengan alasan FEATURE-PARITY (ADR-350); tidak ada angka hermes yang
disalin.

## Komitmen F10 (ADR-356, ledger ADR-357)

Re-fase ini memindahkan penagihan P20 ke F10, bukan menutupnya. Ledger
eksekusinya ada di [`F10-ACCEPTANCE.md`](F10-ACCEPTANCE.md); F10 wajib menutup:

1. **Seluruh 74 dimensi** SCORECARD §1.1–§1.8 (0 `not_measured`), termasuk RC
   head-to-head Q23-RC, soak 24 jam/1.000 turn/200 kill, VM bersih 10 Mbit/s,
   harness code-tools, `Error informatif` (2 penilai manusia), dan scorecard mingguan.
2. **Enam pelanggaran** sampai ambangnya: fungsi ≤80, kompleksitas ≤15, retry 1,
   klasifikasi error string 0, flake ≤0,1%, CI PR p90 ≤12 menit.
3. **P28 67 ID** yang menunggu parity dengan protokol `bench/speed` yang sama,
   plus tujuh regresi terukur (termasuk `TOOL-045`) ditutup.
4. Publikasi ulang artefak RC dan baris agregat `LULUS` hanya lewat PR F10 yang membuat
   `DAMNBOT_RELEASE=1 go test ./scorecard` dan `TestF9SpeedParity` hijau.

Artefak: [`bench/scorecard/results/2026-11-15-6b63d78f16.json`](../bench/scorecard/results/2026-11-15-6b63d78f16.json),
[`bench/speed/results/damnbot-6b63d78f16.json`](../bench/speed/results/damnbot-6b63d78f16.json),
`bench/scorecard/results/tooling-8fab87144e.json`. Keputusan: [`DECISIONS.md`](DECISIONS.md) ADR-356.

## Regenerasi

```sh
cd bench
SCORECARD_UPDATE=1 go test -count=1 -run '^TestScorecardGate$' ./scorecard  # tulis ulang artefak
go test -count=1 ./scorecard                 # skip ber-alasan tanpa penanda rilis
DAMNBOT_RELEASE=1 go test -count=1 ./scorecard  # gagal: 28 dimensi belum terukur + 5 pelanggaran
```

`TestScorecardGate` membangun ulang artefak dan membandingkan setiap baris
dengan sumbernya; angka yang diedit tangan atau sumber yang berubah akan gagal
sebelum gate dievaluasi.
