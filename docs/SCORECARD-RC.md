# SCORECARD RC F9 — artefak terpublikasi (belum lulus P20)

Artefak mesin: [`bench/scorecard/results/2026-11-12-aeb4dc65.json`](../bench/scorecard/results/2026-11-12-aeb4dc65.json).
Gate: `TestScorecardGate` (`bench/scorecard/scorecard_test.go`);
artefak dibangun ulang oleh tes yang sama (`SCORECARD_UPDATE=1`). Basis: `aeb4dc65`, hermes `868b9f244f`.

Status: **P20 BELUM lulus**. Artefak ini jujur memuat 11 dimensi terukur dan 63
dimensi `not_measured`; gate juga mencatat **1 pelanggaran terukur**. Baris
agregat F9 "Artefak rilis & scorecard" tetap `BELUM` (lihat
[`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) §1). Tidak ada target yang diturunkan.

## Dimensi terukur

Angka diambil ulang dari artefak fase (SHA-256 tercatat di `meta.sources_sha256`).

| Dimensi | hermes | Damn-Bot | Aturan | Hasil |
|---|---:|---:|---|---|
| Fixed overhead [I] | 16.843 tok | 2.809 tok | ≤ 0,35× | lulus |
| Startup [I] | 1.368,273 ms | 42,507 ms | ≤ 0,1× (10×) | lulus (32,2×) |
| First token di UI [I] | 39,407 ms | 10,032 ms | ≤ 0,2× (5×) | **GAGAL** (3,93×) |
| Idle RSS | 210,4 MB | 24,570 MB | ≤ 0,3× | lulus |
| CPU idle | 1,4 % | 0,233 % | ≤ hermes | lulus |
| Token skill per turn | 1,0 (rasio) | 0,0750 | ≤ 0,2× | lulus |
| Konkurensi | 0 error | 0 error | sama | lulus |
| Voice end-to-end | 1,597 ms | 0,076 ms | ≤ hermes | lulus |
| Image preflight | 453,536 ms | 265,000 ms | ≤ hermes | lulus vs hermes; target absolut 150 ms hanya sah di Q14 (ADR-323), tidak diklaim |
| Waktu build bersih | 66,276 s | 29,140 s | ≤ hermes | lulus (inkremental 4,756 s vs 1,698 s) |
| Feature parity [I] | 854 baris | 837 baris | = 837/837 | lulus |

Sumber: `bench/budget/results/hermes-868b9f244f.json`, `internal/assemble/testdata/p1/all-modules.request.json`,
`bench/f2/results/hermes-868b9f244f-F2.json`, `bench/f3/results/{damnbot-6d01e7a1d5,hermes-868b9f244f}-F3.json`,
`bench/f4/results/{damnbot-4c1e69ba,hermes-868b9f244f}-F4.json`,
`bench/speed/results/hermes-868b9f244f-F6.json`, `docs/FEATURE-PARITY.md`.

## Pelanggaran dan dimensi belum terukur

1. **First token di UI [I] kalah margin.** 39,407 ms / 10,032 ms = 3,93×, syarat
   ≥ 5×. Nilai absolut ≤ 30 ms terpenuhi, tetapi aturan menang inti tidak.
   Tidak ada angka pengganti yang dipakai.
2. **63 dimensi `not_measured`.** Rinciannya ada di artefak; kelompok utamanya:
   - Token input/output, biaya, cache, task success, invalid tool call, klaim
     tanpa bukti, recall, fakta basi, routing skill, long-session, error
     recovery, overhead iterasi, wall-clock, prompt-injection: RC head-to-head
     penuh belum dijalankan karena **Q23-RC** (model frontier & anggaran) masih
     terbuka (`docs/OPEN-QUESTIONS.md`).
   - Crash/hang, kill -9, self-healing, peak RSS: artefak F9 yang ada hanya
     profil CI 50 turn/50 kill, bukan soak 24 jam/1.000 turn/200 kill.
   - Ukuran & waktu install dari nol, langkah setup, baris config: butuh VM
     bersih 10 Mbit/s; host ini bukan VM RC dan tidak ada docker/root.
   - Kualitas kode, keamanan, migrasi, inventaris platform/provider/locale/OS/
     skill/MCP, dan fitur pembeda: belum ada artefak angka RC.
3. **Speed parity (a) belum punya artefak sisi Damn-Bot.**
   `TestF9SpeedParity` (`bench/speed/f9_test.go`) masih skip karena
   `bench/speed/results/damnbot-*.json` tidak ada; baseline hermes mengukur 163
   operasi (80 CLI, 51 slash, 25 tool, 7 py). Tidak ada angka yang dikarang.
4. **Baseline hermes dimensi ADR-065/083 berstatus F9** (install dari nol, VM
   bersih 10 Mbit/s) belum diukur; artefak nollm mencatat `install_time_s`
   workstation, bukan VM RC.

## Regenerasi

```sh
cd bench
SCORECARD_UPDATE=1 go test -count=1 -run '^TestScorecardGate$' ./scorecard  # tulis ulang artefak
go test -count=1 ./scorecard                 # skip ber-alasan tanpa penanda rilis
DAMNBOT_RELEASE=1 go test -count=1 ./scorecard  # gagal: P20 belum lulus
```

`TestScorecardGate` membangun ulang artefak dan membandingkan setiap baris
dengan sumbernya; angka yang diedit tangan atau sumber yang berubah akan gagal
sebelum gate dievaluasi.
