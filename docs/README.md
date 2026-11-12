# Dokumentasi Damn-Bot

Indeks dokumen. Spesifikasi normatif tetap [SPEC.md](../SPEC.md); ADR ada di
[DECISIONS.md](DECISIONS.md).

## Memulai

- [VPS.md](VPS.md) — instalasi di VPS, service, dan operasi harian.
- [CONFIG-REFERENCE.md](CONFIG-REFERENCE.md) — referensi config tergenerasi.
- [reference/CLI.md](reference/CLI.md) — perintah dan flag tergenerasi.
- [reference/ENVIRONMENT.md](reference/ENVIRONMENT.md) — env config dan container.
- [reference/DEPLOYMENT.md](reference/DEPLOYMENT.md) — entrypoint, unit, bind.

## Arsitektur dan jaminan

- [ENGINEERING.md](ENGINEERING.md) — gate, budget, dan definition of done.
- [TRUST-MODEL.md](TRUST-MODEL.md) — model ancaman dan batas kepercayaan.
- [CROSS-CUTTING.md](CROSS-CUTTING.md) — kontrak lintas modul.
- [SELF-HEALING.md](SELF-HEALING.md) — supervisor dan pemulihan.

## Referensi tergenerasi

Halaman di bawah `docs/reference/` dibangkitkan dari kode oleh
`go generate ./cmd/damnbot` dan diperiksa gate `TestCLIDocsMatchHelp`,
`TestConfigDocSync`, `TestEnvDocSync`, dan `TestDeploymentDocSync`. Jangan sunting
manual: ubah kode lalu regenerasi.

## Terjemahan

Terjemahan disimpan di `docs/<locale>/`. Setiap halaman wajib memuat banner baris
pertama

```text
<!-- translation: source=../README.md sha256=<sha256 halaman sumber> -->
```

`TestTranslationBannerFresh` menghitung ulang sha sumber; banner yang tidak cocok
dilaporkan `STALE` dan menggagalkan gate. Halaman `docs/id/` adalah contoh
terjemahan halaman ini.
