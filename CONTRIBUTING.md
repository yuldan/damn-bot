# Contributing to Damn-Bot

Terima kasih. Repo ini **Go-only** (Go 1.26.x, `CGO_ENABLED=0`); tidak ada Node,
Python, pnpm, atau `npm ci`. Semua tool dev dipin di `tools/go.mod` dan
dipanggil lewat `go tool`, jadi satu toolchain cukup.

## Setup

```sh
git clone https://github.com/yuldan/damn-bot && cd damn-bot
make hooks   # git config core.hooksPath .githooks
make check   # gate yang sama dengan CI
```

`make check` adalah satu-satunya titik masuk lokal: lint, vet, test, test
timing, build matrix, ukuran, LOC, dependency (tidy/verify/vuln/licenses),
deadcode, dan coverage. Hook `pre-commit` menjalankan perintah yang sama
(`.githooks/pre-commit`); nightly CI menjalankan `make check` pada job `check`.

## Kebijakan dependency

Satu paragraf, dicek otomatis: semua dependency di-pin di `go.mod`/`go.sum`
dan `go mod tidy -diff` harus bersih; dependency baru wajib punya ADR dengan Δ
ukuran, dependency transitif, LOC yang dihemat, lisensi, dan tanggal rilis
(E-46..E-50, `docs/ENGINEERING.md` §4.4); modul baru/bump harus berumur ≥ 3
hari kecuali perbaikan `govulncheck` berlabel `security-hotfix` dengan dua
approver. `go.mod`, `deps.allow`, dan `docs/DECISIONS.md` berubah bersama.

## Pull request

- Satu PR = satu perubahan bermakna; judul `type(scope): ringkas`.
- Sertakan bukti perintah yang dijalankan dan nama tes bernama yang baru.
- `CHANGELOG.md` dan `docs/DECISIONS.md` (ADR) diperbarui bila perilaku,
  kontrak, dependency, atau config berubah.
- Jangan menambah `t.Skip` tanpa build tag/OS eksplisit atau `flaky: #<issue>`;
  baseline skip ada di `internal/archtest/testdata/skip-baseline.json`.

## Review

`docs/ENGINEERING.md` memuat standar wajib (E-xx) dan budget modul. Perubahan
yang melewati budget butuh ADR. Jangan menurunkan gate, target, atau tes agar
hijau; perbaiki penyebabnya.
