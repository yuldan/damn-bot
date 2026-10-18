# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## gitleaks
- Sumber: https://github.com/gitleaks/gitleaks (`config/gitleaks.toml`, tag v8.28.0; sha256 berkas c3bee40d87bb4689e3f5ffa5cc0315f7e6e4d43f4e9388397fca59f29407b432)
- Commit: 4fb43823ef3d152d239e92d7d5cb04783b548062
- Lisensi: MIT, © 2019 Zachary Rice — `third_party/manual/gitleaks.txt`
- Isi: sampel positif sintetis dalam format setiap rule gitleaks (bukan secret asli; badan PEM dari byte acak); `«»` di dalam prefix literal dibuang loader test (ADR-096).

| File | SHA-256 |
|---|---|
| `gitleaks-v8.28.0.tsv` | b337386bf368436f30b2f6c94903c7875e32f8b1c3cf4bd4738c0018ecdcd016 |
