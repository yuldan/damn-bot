# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## damnbot (F5 acceptance smoke E1, `main` 965fd15)
- Sumber: https://github.com/yuldan/damn-bot
- Commit: 965fd1500916e93c52a6bd4238e8ef50cba9d330
- Lisensi: MIT, © 2026 Damn-Bot contributors — `LICENSE`
- Isi: rekaman trafik LLM hermes-agent lewat proxy rekam `bench/h2h` untuk smoke F5 10 task E1 (tanpa gate vs hermes, ADR-032); header `Authorization` diredaksi.

## hermes-agent @ `6bb377a51d`
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 6bb377a51d5c8e279a7a5e34d717d653d421d064
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`

| File | SHA-256 |
|---|---|
| `E1-104.hermes.proxy.jsonl.gz` | 2747b81e2dc22cd14370b9d99a7f4294563c3a90caaf718cc1f9269f9272d76e |
| `E1-105.hermes.proxy.jsonl.gz` | 8ec1b7bb1689432d213a05d3e8a1fe92ed2de6f45daf826117669289e00dca42 |
| `E1-106.hermes.proxy.jsonl.gz` | 2c9fd78465097a29b745b609da339d869fef24a1b92a5dce42ebb8c64e8114c5 |
| `E1-107.hermes.proxy.jsonl.gz` | 6f8e6d8cfc5dece9d4afcc7be6fd65ff5d5b1ea5cf4ae2f30708a6d02e30bcdf |
| `E1-110.hermes.proxy.jsonl.gz` | 6a639f657b86251b919a85b858a3df91d1bd23c5a645110bf37a65a605b9b717 |
| `E1-113.hermes.proxy.jsonl.gz` | 28331e362fae9a7e4a3288735d8b2d54f29d60ffe7c4d57ca54e1f05da40a575 |
| `E1-117.hermes.proxy.jsonl.gz` | 34fefbe6c4dbc9b4b175f5d4ae10efb7b5e41e610de869db6e2b61f1ddb7dfce |
| `E1-118.hermes.proxy.jsonl.gz` | 1f75ba5ed2d5b05843e2e71798f7a87c4fc099464d5c5f6538e302a4ee1499e7 |
| `E1-154.hermes.proxy.jsonl.gz` | 8d48323f4e006c10316a225939089c21ef035774b3a9c9f30fda65b52ad0b16b |
| `E1-157.hermes.proxy.jsonl.gz` | 60eddfb1fcb6cfc518e7a6a62606073c1cf5994842be77773c36e0243388c7af |
| `background.proxy.jsonl.gz` | 30e6fa98fb48c2b132824d1ac5e2243c0be9e9082ff32598d34d7687ca7f6c7f |
