# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## damnbot (snapshot integrasi `f1-integration` 22cafba)
- Sumber: https://github.com/yuldan/damn-bot
- Commit: 22cafba
- Lisensi: MIT, © 2026 Damn-Bot contributors — `LICENSE`
- Isi: rekaman trafik LLM agent damnbot (`damnbot run` lewat proxy rekam `bench/h2h`) untuk smoke 10 task; header `Authorization` diredaksi

| File | SHA-256 |
|---|---|
| `background.proxy.jsonl.gz` | 30e6fa98fb48c2b132824d1ac5e2243c0be9e9082ff32598d34d7687ca7f6c7f |
| `E1-117.damnbot.proxy.jsonl.gz` | ec2f7108443e4655fd3b940949ffced4a534fac308bb78e51020f366a68a0215 |
| `E1-153.damnbot.proxy.jsonl.gz` | fbc6fdd7ce7c471d06cf626f952463a7cbdae42526e6da21892c0c62b220c2cd |
| `E1-157.damnbot.proxy.jsonl.gz` | 07521d0ab9d187a109b4b84783ba721a5ea19c421f53a8b7e8ab1237d16599e5 |
| `E1-158.damnbot.proxy.jsonl.gz` | b1d4069c941efe50f5abc2b8d505b1add4c22d0894ee981ae9c7d8356dc9e4a4 |
| `E2-016.damnbot.proxy.jsonl.gz` | 4a93a4c6b3fde8db5ca861ab368a3136ad8f811cdcf95d1ecefdf880a1f9f2b1 |
| `E2-039.damnbot.proxy.jsonl.gz` | 3f9288d5a2062dc971180c4064ddc57bdf6b22d32c2e5787b9fb6476dd2d7a36 |
| `E3-013.damnbot.proxy.jsonl.gz` | e91250b68efdd3c9596609cb1cd9184086ca0feefb711bfaf3b33ca878d2d0a5 |
| `E3-028.damnbot.proxy.jsonl.gz` | b6e9a8b2806dd4396b531857e76f2f8e944a9317243aec351aef3f2d225609e5 |
| `E6-010.damnbot.proxy.jsonl.gz` | c98f22848c805c91d981ba5551b776af68b1b55c71e3760227e8901db4e1031e |
| `E6-040.damnbot.proxy.jsonl.gz` | 21e0fa5a5ece94b278802f4c9bcd12678c08ef0199ffefff88530a16c5c2dd5b |
