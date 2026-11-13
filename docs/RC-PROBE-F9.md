# Probe head-to-head RC F9 (10 task, `deepseek-v4.1-flash`)

Bukan gate penuh Q23-RC: ini probe 10 task F1 smoke (E1 4, E2 2, E3 2,
E6 2) dengan provider CoralBricks, model `deepseek-v4.1-flash`, harga
input US$0,45 / cached US$0,009 / output US$1,80 per 1M token. Kedua sisi
memakai harness `bench/h2h` yang sama, proxy rekam, dan seleksi task yang sama.

| Metrik | hermes | Damn-Bot | Catatan |
|---|---:|---:|---|
| Task lulus | 10/10 | 9/10 | E6-010 gagal: `curl -m 10` kena fault timeout 30 s lalu tool shell ditolak; E1/E2/E3 lulus |
| Biaya | US$0,083403 | US$0,035765 | 0,43× |
| Prompt token | 730.608 | 337.930 | 0,46× |
| Output token | 6.465 | 7.452 | 1,15× (kalah target ≤1,0× pada probe ini) |
| Cached token | 582.784 | 294.144 | — |
| Wall-clock median | 37,12 s | 10,40 s | 3,57× lebih cepat |
| Wall-clock total | 771,75 s | 112,03 s | — |

Artefak: `bench/h2h/results/2026-11-12-f9-rc-probe-hermes/` dan
`bench/h2h/results/2026-11-13-f9-rc-probe-damnbot/` (`summary.json` + rekaman
proxy ter-redaksi). Ledger `bench/h2h/results/f1-spend.json` naik dari
US$0,9220 ke US$1,1422 dari cap US$2,00.

## Perbaikan harness yang diperlukan

Probe pertama Damn-Bot 0/10 karena runner headless tidak punya izin setara
hermes `--yolo`. `bench/h2h/damnbot.go` sekarang menulis config task:

```toml
[skills]
trusted_projects = ["<workdir task>"]

[approval]
shell_allow = [["curl"]]
```

dan menjalankan `run --approval auto --unsafe-auto-shell --config <file>`.
Setelah perbaikan: E1/E2/E3 lulus, E6-040 lulus, E6-010 lulus saat dijalankan
terpisah (probe final gagal karena model memilih `curl -m 10` sebelum server
fault 30 s pulih). Tidak ada target yang diubah.

## Sisa blocker

- Bukan eval set penuh: E1 160, E2 40, E3 30, E4 30, E5 10×120 turn, E6 40,
  E7 50, E8 20, E9 50, E10 100; 3 run/task; container + urutan acak (ADR-075).
- Model/anggaran Q23-RC tetap keputusan owner; probe ini memakai model murah
  yang tersedia dan tidak diklaim sebagai model frontier.
- Soak 24 jam/1.000 turn dan VM bersih 10 Mbit/s tetap terpisah.
