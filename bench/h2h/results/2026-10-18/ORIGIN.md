# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## hermes-agent
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 868b9f244f5aa3ad238e494048046fe4f29c28ba
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`
- Isi: rekaman trafik LLM hermes-agent (memuat prompt dan skema tool hermes)

| File | SHA-256 |
|---|---|
| `E1-117.hermes.proxy.jsonl.gz` | bc4f9ea33bbc48c5af2f489060c83918f4ab54687d92cb3f561f27a6a4e91449 |
| `E1-124.hermes.proxy.jsonl.gz` | e01c318537e6f2c6c26abe16988e2c47d748c47599d97fb95b4436a95ab23fb9 |
| `E1-147.hermes.proxy.jsonl.gz` | cc0f2ad09b9f9b9ae02546f369a29308afcd7c5d3e910dfadac351d8c8e54dd4 |
| `E1-149.hermes.proxy.jsonl.gz` | 0f4a5317e9a45182a9b02d42a4ef5e670754db5424dd6bb61b1cc666ab204c02 |
| `E2-016.hermes.proxy.jsonl.gz` | debd75fd2895b309f8defb11755ebc440ca57339494fc5a33a6b48df840bd71c |
| `E2-039.hermes.proxy.jsonl.gz` | 8a971cc4ed68a705181418b53ce0a90c91fa9cdfeff105a0fda57a69435b98c0 |
| `E3-013.hermes.proxy.jsonl.gz` | 0db407c22eb6dddcaed4fcaa3f52a527b2c685474ddb6191a57eee9131daa724 |
| `E3-028.hermes.proxy.jsonl.gz` | fb80340783255bcbdff96f8fee20e1d5157bdeec31a0248b8953be62d4690d8a |
| `E6-010.hermes.proxy.jsonl.gz` | eea71ec917300d19ba02829b7c675db0901c6f2d263a89261e1f63bbf5e6853f |
| `E6-040.hermes.proxy.jsonl.gz` | e689620cbc0108a7f79584291370466a7d2a5b4bcbad0432a08dc7532a6455c9 |
| `background.proxy.jsonl.gz` | 30e6fa98fb48c2b132824d1ac5e2243c0be9e9082ff32598d34d7687ca7f6c7f |
