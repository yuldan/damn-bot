# SCORECARD RC F9 — artefak terpublikasi (P20 dire-fase ke F10, ADR-356)

Artefak mesin: [`bench/scorecard/results/2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json).
Gate: `TestScorecardGate` (`bench/scorecard/scorecard_test.go`); artefak dibangun
ulang oleh tes yang sama (`SCORECARD_UPDATE=1`). Basis: `8fab87144e`, hermes `868b9f244f`.

Status: **P20 dire-fase ke F10 (ADR-356)**. Owner menyetujui Opsi A pada 2026-11-13:
baris agregat F9 "Artefak rilis & scorecard" **tidak** diklaim `LULUS`; statusnya
`N/A — re-phased to F10 (ADR-356; P20 RC)` (lihat [`F9-ACCEPTANCE.md`](F9-ACCEPTANCE.md) §1
dan [`DECISIONS.md`](DECISIONS.md) ADR-356). Artefak ini memuat **43 dimensi terukur
dan 31 `not_measured`**, dengan **6 pelanggaran pada dimensi terukur**: fungsi
terpanjang 132 > 80, kompleksitas 40 > 15, retry 38 > 1, klasifikasi error string
1 > 0, flake 72% (18/25 nightly) > 0,1%, dan CI p90 32,62 > 12 menit. Bukti positif
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
| Klasifikasi error via string | 41 + 88 | 1 (`cron_store.go:88`) | 0 | **gagal** |
| Kebocoran goroutine | n/a | 0 (53 paket goleak) | 0 | lulus |
| Exec spawn overhead | n/a | p95 1,202 ms (mean 0,751) | <15 ms | lulus |
| Flake rate | n/a | 72% (18/25; 18/20 dieksekusi) | ≤0,1% | **gagal** |
| Waktu CI PR p90 | n/a | 32,62 menit (median 25,35) | ≤12 | **gagal** |

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

31 dimensi `not_measured` (rincian di artefak):

- Token input/output, biaya, cache, background token, task success, invalid tool
  call, klaim tanpa bukti, recall, fakta basi, routing skill, long-session, error
  recovery, overhead iterasi, wall-clock, prompt-injection: RC head-to-head penuh
  belum dijalankan (Q23-RC: model frontier & anggaran). Probe 10 task dengan
  `deepseek-v4.1-flash` ada di `bench/h2h/results/2026-11-1{2,3}-f9-rc-probe-*`.
- Crash/hang, kill -9, self-healing, peak RSS: artefak F9 hanya profil CI
  50 turn/50 kill, bukan soak 24 jam/1.000 turn/200 kill.
- Ukuran & waktu install dari nol, langkah setup, baris config: butuh VM bersih
  10 Mbit/s; host ini bukan VM RC dan tidak ada docker/root.
- Harness yang belum punya artefak terukur: code tools (korpus/host beda),
  proses yatim/perintah hang, edit salah lokasi, error tertelan, fallback tak
  terlihat.
- `Error informatif`: menunggu 2 penilai manusia.
- `Scorecard mingguan`: job ada, aktivasi setelah RC.

## Speed parity

`TestF9SpeedParity` masih SKIP/gagal: hanya **3 dari 163** operasi hermes yang punya
pengukuran Damn-Bot dengan protokol sama (`bench/speed/results/damnbot-8fab87144e.json`:
`TOOL-045` patch 5,238 ms, `TOOL-046` read 0,509 ms, `TOOL-047` search 10,519 ms; sumber
`bench/codetools/results/f1-7d0f6bd.json`). `TOOL-045` juga **regresi** terhadap hermes
3,091 ms. Sisa 160 ID belum diukur dengan protokol itu; tidak ada angka yang dikarang.

## Komitmen F10 (ADR-356)

Re-fase ini memindahkan penagihan P20 ke F10, bukan menutupnya. F10 wajib menutup:

1. **Seluruh 74 dimensi** SCORECARD §1.1–§1.8 (0 `not_measured`), termasuk RC
   head-to-head Q23-RC, soak 24 jam/1.000 turn/200 kill, VM bersih 10 Mbit/s,
   harness code-tools, `Error informatif` (2 penilai manusia), dan scorecard mingguan.
2. **Enam pelanggaran** sampai ambangnya: fungsi ≤80, kompleksitas ≤15, retry 1,
   klasifikasi error string 0, flake ≤0,1%, CI PR p90 ≤12 menit.
3. **P28 160 ID** sisanya dengan protokol `bench/speed`/`BenchmarkSpeedParity` yang sama,
   plus regresi `TOOL-045` (5,238 ms vs hermes 3,091 ms) ditutup.
4. Publikasi ulang artefak RC dan baris agregat `LULUS` hanya lewat PR F10 yang membuat
   `DAMNBOT_RELEASE=1 go test ./scorecard` dan `TestF9SpeedParity` hijau.

Artefak: [`bench/scorecard/results/2026-11-13-8fab87144e.json`](../bench/scorecard/results/2026-11-13-8fab87144e.json),
[`bench/speed/results/damnbot-8fab87144e.json`](../bench/speed/results/damnbot-8fab87144e.json),
`bench/scorecard/results/tooling-8fab87144e.json`. Keputusan: [`DECISIONS.md`](DECISIONS.md) ADR-356.

## Regenerasi

```sh
cd bench
SCORECARD_UPDATE=1 go test -count=1 -run '^TestScorecardGate$' ./scorecard  # tulis ulang artefak
go test -count=1 ./scorecard                 # skip ber-alasan tanpa penanda rilis
DAMNBOT_RELEASE=1 go test -count=1 ./scorecard  # gagal: 31 dimensi belum terukur + 6 pelanggaran
```

`TestScorecardGate` membangun ulang artefak dan membandingkan setiap baris
dengan sumbernya; angka yang diedit tangan atau sumber yang berubah akan gagal
sebelum gate dievaluasi.
