# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## hermes-agent
- Sumber: https://github.com/NousResearch/hermes-agent
- Commit: 868b9f244f5aa3ad238e494048046fe4f29c28ba
- Lisensi: MIT, © 2025 Nous Research — `third_party/manual/hermes-agent.txt`
- Isi: frontmatter `SKILL.md` hermes-agent

| File | SHA-256 |
|---|---|
| `hermes-skills.tsv` | 1eba40d8d8582c643ba89f177fa9e1a2eb73842360bc6e12dcae8dd87b533672 |

## SWE-bench Verified
- Sumber: https://huggingface.co/datasets/princeton-nlp/SWE-bench_Verified
- Commit: c104f840cc67f8b6eec6f759ebc8b2693d585d4a (revisi dataset)
- Lisensi: MIT (proyek SWE-bench) — `third_party/manual/swe-bench.txt`
- Isi: `instance_id` saja

| File | SHA-256 |
|---|---|
| `swe-bench-verified.ids` | 1720416028335d5dc1a094e43b6405d22753ee94f72d45cce8578b895d18c0c9 |

## Terminal-Bench core v0.1.1
- Sumber: https://github.com/laude-institute/terminal-bench (registry.json)
- Commit: d28711d0da2675d0bb1d56de45ae5df6082438a3
- Lisensi: Apache-2.0 — `third_party/manual/terminal-bench.txt`
- Isi: ID task saja

| File | SHA-256 |
|---|---|
| `terminal-bench-core-0.1.1.ids` | b6f0ec52cae6932cf22d2967a71b28bd73d4d004f8d7ce5653f911f7d28af4f5 |
