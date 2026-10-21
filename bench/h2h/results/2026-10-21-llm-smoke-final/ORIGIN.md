# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

`summary.json` adalah keluaran asli harness `bench/h2h` (bukan file turunan pihak ketiga); glob `docs/ATTRIBUTION.md` hanya mencakup `*.proxy.jsonl.gz`, sehingga summary tidak diberi baris SHA di tabel ini.

## damnbot (smoke final `main` 3a2a10d)
- Sumber: https://github.com/yuldan/damn-bot
- Commit: 3a2a10d
- Lisensi: MIT, © 2026 Damn-Bot contributors — `LICENSE`
- Isi: rekaman trafik LLM agent damnbot (`damnbot run` lewat proxy rekam `bench/h2h`) untuk smoke final 10 task pada `main`; header `Authorization` diredaksi

| File | SHA-256 |
|---|---|
| `E1-117.damnbot.proxy.jsonl.gz` | b7ee675c9cf9d80f4133ea76112dcc0a97aae1c1f240e3a10fd355cc9f37b9d9 |
| `E1-153.damnbot.proxy.jsonl.gz` | 3afdd1b7abc7cd53f444e7757c6524a10e952c93aa399ee5cf9838d86dc9e8ec |
| `E1-157.damnbot.proxy.jsonl.gz` | 9e038a26e1f5911fc94dff0b6aa4d5e684e60edda5a341031710d7648e1de729 |
| `E1-158.damnbot.proxy.jsonl.gz` | dc3de79ee9a9a0f9d015a08daf6007642097a335f49acb200cb7de456fbbd36a |
| `E2-016.damnbot.proxy.jsonl.gz` | 92f9c142e320c822a11be5d8dd7ba6afd3be123d4d43334350ab814b4cf4ae94 |
| `E2-039.damnbot.proxy.jsonl.gz` | 23a74699f46ace8e366e0431b2e1c1bedf330a3a4cb4287cee0bb540b4f466cb |
| `E3-013.damnbot.proxy.jsonl.gz` | 3f93010719ca9a63eca046b2d0f35591028512ffb4f8bd7e99e37766eace6697 |
| `E3-028.damnbot.proxy.jsonl.gz` | afb1c69582165f85cc23d8acd7716326cceae2eb1a9ab7639847b60f1d135147 |
| `E6-010.damnbot.proxy.jsonl.gz` | 062e2aef7b65bcadf9d8a6185a93814392e47b18210cb4183c94bbc8aa87a165 |
| `E6-040.damnbot.proxy.jsonl.gz` | ffbd9c90efab8e3dfc7cf11a7daf767df9c2c7630224f6ac5417011267349118 |
| `background.proxy.jsonl.gz` | 30e6fa98fb48c2b132824d1ac5e2243c0be9e9082ff32598d34d7687ca7f6c7f |
