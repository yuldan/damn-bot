# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## hermes-agent
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 868b9f244f5aa3ad238e494048046fe4f29c28ba
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`
- Isi: prompt sistem, tier konteks, dan skema tool hermes yang dirender (harness budget)

| File | SHA-256 |
|---|---|
| `context.txt` | d985ddcc74c931bac2cfaec402d53c149bfc71fe6d55d4f45078098c84638a4c |
| `meta.json` | 1700b29bb552c3b219e4895e661d4388eca0ce59c58e870ec4c3ab01a08f737c |
| `stable.txt` | 475d6f11faae9dc5913a5df9e69fb30b331c6d9903189e6119f51b450306f18f |
| `system_prompt.txt` | bb7e5e2c9bd809de47f7fd45a984f820dae00dbebf19353e56fec1a6c0180a49 |
| `tools_agent.json` | 83a827443909d037e5cc03635aeaf6540e9e3e1ee27f862b435270530d91287d |
| `tools_defs.json` | 6ad2ae494a27efe04cee7f8df409932e9a9293069a5d77c71379c2e4eb9f2e28 |
| `volatile.txt` | 8a472b0229cf1dedbff0f024542f6419acd137cd996c223f4817b8ae6c931a92 |
