# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## tiktoken o200k_base
- Sumber: https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken (didistribusikan https://github.com/openai/tiktoken)
- Commit: — (berkas rank BPE tanpa versi; byte asli tidak diubah, hanya di-gzip)
- Lisensi: MIT, © 2022 OpenAI, Shantanu Jain — `third_party/manual/tiktoken-o200k.txt`
- Isi: berkas rank BPE `o200k_base`, di-gzip

| File | SHA-256 |
|---|---|
| `o200k_base.tiktoken.gz` | d94e3741f19100cc9d1fc6036ce651bb243d39cd48e2d56d46ed687c1ee80bfa |
