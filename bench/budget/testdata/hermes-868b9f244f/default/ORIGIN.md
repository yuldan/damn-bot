# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## hermes-agent
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 868b9f244f5aa3ad238e494048046fe4f29c28ba
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`
- Isi: prompt sistem, tier konteks, dan skema tool hermes yang dirender (harness budget)

| File | SHA-256 |
|---|---|
| `context.txt` | 717e4c372e37e438f039bba3ac8fcc1c038086ad540a284d4c6d7783d11f1fa7 |
| `meta.json` | a63c169fd8fc0a4bbfbcde9c8f3ef6beddafd90e939e164d8c82eddb31d47579 |
| `stable.txt` | 475d6f11faae9dc5913a5df9e69fb30b331c6d9903189e6119f51b450306f18f |
| `system_prompt.txt` | 69e6da7c5bed158a810f01a02849ea64b948344bcb5d3521f09704b309e0e114 |
| `tools_agent.json` | 83a827443909d037e5cc03635aeaf6540e9e3e1ee27f862b435270530d91287d |
| `tools_defs.json` | 6ad2ae494a27efe04cee7f8df409932e9a9293069a5d77c71379c2e4eb9f2e28 |
| `volatile.txt` | 8a472b0229cf1dedbff0f024542f6419acd137cd996c223f4817b8ae6c931a92 |
