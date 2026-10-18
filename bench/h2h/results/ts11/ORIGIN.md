# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## hermes-agent
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 868b9f244f5aa3ad238e494048046fe4f29c28ba
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`
- Isi: rekaman trafik LLM hermes-agent (memuat prompt dan skema tool hermes)

| File | SHA-256 |
|---|---|
| `2026-10-18.proxy.jsonl.gz` | feda0d78a27b977670240056f68399c3651aad37291d5e838728b6724e496caa |
