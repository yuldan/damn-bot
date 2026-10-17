# HANDOFF — F0 otomatis (tanpa pengawasan)

Brief untuk agent yang bekerja sendiri semalaman. Baca seluruhnya sebelum mulai.

## Sumber kebenaran

- `SPEC.md` (Spec 1.0, FROZEN) + `docs/*.md`. Spec **tidak boleh diubah**. Bila spec salah/kontradiktif: catat di `docs/F0-REPORT.md` bagian "Temuan spec", lanjutkan dengan asumsi paling konservatif, jangan edit spec.
- Fase yang dikerjakan: **hanya F0** (`docs/ROADMAP.md` §F0). Jangan mulai F1.
- `/home/jelangkung/Dokumen/hermes-agent` read-only. Jalankan Python hermes hanya dengan `HERMES_HOME=/tmp/hm_f0 PYTHONDONTWRITEBYTECODE=1`.

## Urutan kerja

1. Scaffold Go module (`go.mod`, layout `cmd/`, `internal/`, `bench/` sesuai `docs/ENGINEERING.md` §2), Makefile, `.golangci.yml`, CI workflow (lint, test -race, size-check, LOC, deps allowlist, govulncheck, go-licenses).
2. **Persistence spike** (prioritas utama): `internal/ctxdb` dengan DDL §11 (0001–0008), lease/fencing, purge, blob gc, journal recovery; skenario adversarial SPEC §11.11.13 #1–#78 + `docs/REVIEW-1.0.md` §"Verifikasi ADR-046" #79–#86 sebagai test Go; `TestKill9Chaos`, `TestMultiProcess`.
3. `bench/budget` (harness token P1, tokenizer o200k offline di `bench/go.mod` terpisah).
4. `bench/speed` + baseline hermes tanpa LLM (startup, schema bytes, overhead token, RSS, grep vs `rg`).
5. Korpus code tools C1–C3 (`docs/CODE-TOOLS.md` §10).
6. Errata backlog ADR-047 (`docs/DECISIONS.md`): kerjakan sebagai catatan + test bila menyangkut kode F0; perubahan spec dicatat sebagai usulan ADR di laporan, bukan diterapkan.

Lewati (butuh kredensial/biaya): smoke LLM 10 task, head-to-head hermes berbasis LLM. Tandai "menunggu user" di laporan.

## Aturan

- Commit git kecil per deliverable dengan pesan jelas (`f0: ...`). Jangan force-push, jangan rewrite history, jangan hapus file yang bukan buatanmu.
- Setiap klaim "selesai" wajib bukti perintah yang benar-benar dijalankan (`go test ./...`, benchmark) + exit code di laporan.
- Tidak boleh: memanggil API LLM berbayar, mengirim pesan ke platform chat, `sudo`, mengubah file di luar `/home/jelangkung/Dokumen/Damn-Bot` dan `/tmp`, menginstal paket sistem. Dependency Go hanya dari daftar `docs/ENGINEERING.md` §4.
- Macet > 3 percobaan pada satu masalah: catat di laporan (gejala, yang sudah dicoba), pindah ke item berikutnya.
- Jangan tandai test sebagai skip/xfail untuk membuat CI hijau. Test gagal yang jujur lebih baik dari test palsu yang lulus.

## Laporan pagi

Perbarui `docs/F0-REPORT.md` setelah setiap deliverable:

| Bagian | Isi |
|---|---|
| Status | per deliverable F0: selesai / sebagian / belum / menunggu user |
| Bukti | perintah + exit code + ringkasan output |
| Skenario persistence | #1–#86: lulus / gagal / belum ditulis |
| Temuan spec | kontradiksi atau lubang spec + usulan ADR |
| Blocker | apa yang butuh keputusan user |
