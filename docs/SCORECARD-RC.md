# SCORECARD RC F9 — artefak terpublikasi (belum lulus P20)

Artefak mesin: [`bench/scorecard/results/2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json).
Gate: `TestScorecardGate` (`bench/scorecard/scorecard_test.go`); artefak dibangun
ulang oleh tes yang sama (`SCORECARD_UPDATE=1`). Basis: `8fab87144e`, hermes `868b9f244f`.

Status: **P20 BELUM lulus**. Artefak ini memuat 11 dimensi terukur dan 63
dimensi `not_measured`; **0 pelanggaran pada dimensi terukur**. Baris agregat F9
"Artefak rilis & scorecard" tetap `BELUM` (lihat
[`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) §1). Tidak ada target yang diturunkan.

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

Sumber: `bench/budget/results/hermes-868b9f244f.json`, `internal/assemble/testdata/p1/all-modules.request.json`,
`bench/f2/results/hermes-868b9f244f-F2.json`, `bench/f3/results/{damnbot-8fab87144e,damnbot-6d01e7a1d5,hermes-868b9f244f}-F3.json`,
`bench/f4/results/{damnbot-4c1e69ba,hermes-868b9f244f}-F4.json`,
`bench/speed/results/hermes-868b9f244f-F6.json`, `docs/FEATURE-PARITY.md`.

Buildtime memakai artefak `6d01e7a1d5` karena pengukuran build baru berjalan di
bawah nice/6-CPU host bersama (143,965 s) dan tidak sebanding dengan baseline
hermes 66,276 s; first token/startup memakai artefak final `8fab87144e`.

## Dimensi belum terukur

63 dimensi `not_measured` (rincian di artefak):

- Token input/output, biaya, cache, background token, task success, invalid tool
  call, klaim tanpa bukti, recall, fakta basi, routing skill, long-session, error
  recovery, overhead iterasi, wall-clock, prompt-injection: RC head-to-head penuh
  belum dijalankan (Q23-RC: model frontier & anggaran). Probe 10 task dengan
  `deepseek-v4.1-flash` ada di `bench/h2h/results/2026-11-1{2,3}-f9-rc-probe-*`.
- Crash/hang, kill -9, self-healing, peak RSS: artefak F9 hanya profil CI
  50 turn/50 kill, bukan soak 24 jam/1.000 turn/200 kill.
- Ukuran & waktu install dari nol, langkah setup, baris config: butuh VM bersih
  10 Mbit/s; host ini bukan VM RC dan tidak ada docker/root.
- Kualitas kode, keamanan, migrasi, inventaris platform/provider/locale/OS/skill/
  MCP, dan fitur pembeda: belum ada artefak angka RC.
- `Error informatif`: menunggu 2 penilai manusia.

## Speed parity

`TestF9SpeedParity` masih SKIP: belum ada `bench/speed/results/damnbot-*.json`.
Baseline hermes mengukur 163 operasi (80 CLI, 51 slash, 25 tool, 7 py); harness
Damn-Bot untuk operasi yang sama belum ada, jadi tidak ada angka yang dikarang.

## Regenerasi

```sh
cd bench
SCORECARD_UPDATE=1 go test -count=1 -run '^TestScorecardGate$' ./scorecard  # tulis ulang artefak
go test -count=1 ./scorecard                 # skip ber-alasan tanpa penanda rilis
DAMNBOT_RELEASE=1 go test -count=1 ./scorecard  # gagal: 63 dimensi belum terukur
```

`TestScorecardGate` membangun ulang artefak dan membandingkan setiap baris
dengan sumbernya; angka yang diedit tangan atau sumber yang berubah akan gagal
sebelum gate dievaluasi.
