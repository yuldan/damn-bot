# Asal file turunan pihak ketiga

Sidecar ADR-018 (b) / ADR-099: file di direktori ini berformat data sehingga tidak bisa memuat header asal. Setiap file turunan tercantum di bawah dengan SHA-256 byte-persis; diverifikasi `TestAttributionHeaders` (`internal/archtest`). Daftar repo: `docs/ATTRIBUTION.md`.

## JSON-Schema-Test-Suite
- Sumber: https://github.com/json-schema-org/JSON-Schema-Test-Suite (direktori `tests/draft2020-12/`)
- Commit: 55719286c33ae0d1823bfb65fea10338a19bc37c (2026-10-13)
- Lisensi: MIT, © 2012 Julian Berman — `third_party/manual/json-schema-test-suite.txt`
- Isi: satu file per keyword subset SPEC §4.4, plus `boolean_schema.json` dan `default.json`, tidak diubah. Grup yang memakai keyword di luar subset dilewati dan dihitung oleh `TestJSONSchemaTestSuite` (ADR-095 butir 6).

| File | SHA-256 |
|---|---|
| `additionalProperties.json` | b96bd533416386ac6421737b48c5c072870ab5dbea910585e9e8fa6305c7ca91 |
| `anyOf.json` | 7a6fe41602496c256334ffa3e6fb457b26ac23b97ca03cb16b724ad21a7d216d |
| `boolean_schema.json` | 9c8828d325b88a0c632fca22405d5481548b49fb0f9f5536ee401e4d66aa8582 |
| `const.json` | 83a148d2589cbd211e7e64b31763290f8869d763f513de4658e8d25f0fcc025e |
| `default.json` | db4a8382727f6c687a0fc7bb24347e010c3fcfabd1207150ce025e1a763f6094 |
| `enum.json` | 1abe7eb883bde4a78c62f08e484e16f32b72ca5c1be725aa54d85cc4546b5031 |
| `items.json` | d924a8171f1f1d04ff026a4e3491d1376297145563158543005b7073b69ab913 |
| `maxItems.json` | 59fa6236c0766aaac5fb7c9ef82e205c230efa94271485f614218291be7cbc5c |
| `maxLength.json` | ac43a3991586f2d2d1e7e37cad01bb1a7b1c5d9ad7b558d6029d24b41a04d1a9 |
| `maximum.json` | dfbc88ef4ede2966250fb2ad44076856d51481ea01ce679cca70efe47606ee90 |
| `minItems.json` | 9792cedc446be654836b42d0312eca12051e9e2f24cd5d95dcd3f3fc9adccd0a |
| `minLength.json` | 999e9731c57a1296e06e8b9b36b0531614ab7a6ac3c20db6fed50badbf3850ab |
| `minimum.json` | f6a29cb574b76621da23636a636cf12cb31b01646b89543e6ce1a813376439a3 |
| `pattern.json` | 60713a3b01957fad54f00ab30b378463b3e07b35242c945a9325f8d6ae7080fc |
| `properties.json` | 58f01566cfe3e1fc4fb851d3b2eab13768d65ba5dbc4e9f9387ceaf4244a0522 |
| `required.json` | 3e3900dd0e546c1cb4aaab6b24ea0e06a8f7f8c05b272dcc87e85332501ed42e |
| `type.json` | 4c5cbe6cbcd28af73761091367b20e07d0403847e236c06c31fc27061bd81192 |
