# Atribusi aset pihak ketiga

Daftar file di repo yang disalin atau diturunkan dari proyek pihak ketiga (ADR-018 (c), ENGINEERING E-50). Teks lisensi lengkap ada di `third_party/manual/*.txt`. Semua file di bawah adalah data benchmark/test; tidak ada yang di-embed di binary `damnbot`.

Header asal ADR-018 (b) untuk file data = sidecar `ORIGIN.md` di direktori file itu (sumber, commit, lisensi, SHA-256 per file; ADR-099). Glob kolom File = himpunan yang diperiksa `TestAttributionHeaders` (`**` = nol atau lebih segmen).

| File | Asal | Lisensi | Notice |
|---|---|---|---|
| `bench/budget/testdata/hermes-868b9f244f/**` | hermes-agent @ `868b9f244f` (prompt sistem, konteks, skema tool) | MIT, © 2025 Nous Research | `third_party/manual/hermes-agent.txt` |
| `bench/h2h/results/**/*.proxy.jsonl.gz` | rekaman trafik LLM hermes-agent @ `868b9f244f` (memuat prompt dan skema tool hermes) | MIT, © 2025 Nous Research | `third_party/manual/hermes-agent.txt` |
| `bench/evals/sources/hermes-skills.tsv` | frontmatter `SKILL.md` hermes-agent @ `868b9f244f` | MIT, © 2025 Nous Research | `third_party/manual/hermes-agent.txt` |
| `bench/budget/o200k/o200k_base.tiktoken.gz` | `o200k_base.tiktoken` (OpenAI, didistribusikan tiktoken), tidak diubah, di-gzip | MIT, © 2022 OpenAI, Shantanu Jain | `third_party/manual/tiktoken-o200k.txt` |
| `bench/evals/sources/swe-bench-verified.ids` | `instance_id` SWE-bench Verified (hanya ID) | MIT (proyek SWE-bench) | `third_party/manual/swe-bench.txt` |
| `bench/evals/sources/terminal-bench-core-0.1.1.ids` | ID task Terminal-Bench core v0.1.1 (hanya ID) | Apache-2.0 | `third_party/manual/terminal-bench.txt` |
