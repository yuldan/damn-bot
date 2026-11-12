# SECURITY — kebijakan keamanan Damn-Bot

Kebijakan ini berlaku untuk rilis resmi Damn-Bot (`github.com/yuldan/damn-bot`) dan menjelaskan versi yang didukung, SLA respons, kanal privat (GHSA + email PGP), serta proses CVE. Batas keamanan, aktor, dan residu yang diakui ada di [`docs/TRUST-MODEL.md`](docs/TRUST-MODEL.md); bila dokumen ini berbeda dengan [`SPEC.md`](SPEC.md), SPEC berlaku.

## 1. Versi yang didukung

Damn-Bot memakai versi semver `MAJOR.MINOR.PATCH`. `N` adalah minor stabil terbaru; `N-1` adalah satu minor sebelumnya.

| Versi | Dukungan | Patch keamanan |
|---|---|---|
| `N` (minor stabil terbaru) | Didukung penuh | Semua perbaikan keamanan, termasuk critical |
| `N-1` (satu minor sebelumnya) | Didukung | Perbaikan critical/high; backport best-effort untuk medium/low |
| `N-2` dan lebih lama | Tidak didukung | Tidak ada patch; pengguna wajib naik ke `N` atau `N-1` |

Selama baru ada satu minor stabil (1.0), `N-1` mengacu pada minor stabil terakhir yang dipublikasikan sebelum `N`. Patch keamanan selalu diterbitkan untuk `N` dan, bila dampaknya critical/high, untuk `N-1`.

## 2. SLA respons

| Tahap | Target | Definisi |
|---|---|---|
| Ack | **72 jam** | Balasan pertama dari maintainer yang mengonfirmasi laporan diterima dan membuka kanal privat |
| Triage | **7 hari** | Severity (CVSS), komponen terdampak, versi terdampak, dan rencana perbaikan dikonfirmasi ke pelapor |
| Fix critical | **30 hari** | Patch tersedia di rilis `N` (atau `N-1` bila perlu); perbaikan high ditargetkan ≤ 90 hari, medium/low pada minor berikutnya |

Severity critical mengikuti CVSS ≥ 9.0 atau dampak setara: eksekusi kode tanpa approval, bypass sandbox tool kode/`deny_paths`, egress tool built-in yang melewati DLP `internal/netx`, atau bypass P25 (lihat `docs/TRUST-MODEL.md` §1). SLA dihitung sejak laporan diterima pada kanal privat dan dapat diperpanjang hanya bila pelapor menyetujui penundaan tertulis.

## 3. Kanal privat

Jangan pernah melaporkan kerentanan lewat issue, PR, diskusi, atau komentar publik.

- **GitHub Security Advisory (GHSA, privat):** <https://github.com/yuldan/damn-bot/security/advisories/new>. Ini kanal utama: laporan, diskusi, patch, dan publikasi advisory berlangsung di ruang privat sampai embargo berakhir.
- **Email:** `yuldan@users.noreply.github.com` (alamat maintainer yang tercatat di riwayat commit repo). Sertakan versi, komponen, langkah reproduksi, dan dampak.
- **PGP:** enkripsi isi laporan dengan kunci publik maintainer di <https://github.com/yuldan.gpg>. Bila kunci belum tersedia di URL itu, minta fingerprint melalui GHSA sebelum mengirim detail sensitif; jangan mengirim proof-of-concept ke kanal tanpa enkripsi.

## 4. Proses CVE

1. **Terima dan verifikasi.** Laporan masuk lewat GHSA/email PGP; maintainer mereproduksi pada versi `N`/`N-1` dan menetapkan severity.
2. **Minta CVE.** CVE ID diminta melalui GitHub Security Advisory (GitHub adalah CNA) atau, bila kanal itu tidak tersedia, melalui MITRE CNA. ID dicatat di advisory privat dan tidak dipublikasikan sebelum embargo berakhir.
3. **Perbaiki dan uji.** Patch + tes regresi disiapkan di ruang privat; `CHANGELOG.md` bagian `### Security` menyebut CVE/komponen tanpa detail eksploitasi sebelum rilis.
4. **Rilis dan publikasi.** Patch dirilis untuk `N` dan `N-1` sesuai SLA; GHSA dipublikasikan dengan CVE ID, versi terdampak/terperbaiki, CVSS, dan kredit pelapor (kecuali meminta anonim).
5. **Disclosure.** Embargo default **90 hari** sejak laporan diterima atau sampai patch tersedia, mana yang lebih dulu; perpanjangan hanya atas kesepakatan pelapor dan maintainer.

## 5. Cakupan

In-scope: bug yang melanggar `docs/TRUST-MODEL.md` §1 — (a) shell/`execute_code` berjalan tanpa approval di luar jalur auto terdokumentasi, (b) tool kode mengakses luar root/`deny_paths`, (c) egress tool built-in melewati DLP `internal/netx`, (d) bypass P25, serta kebocoran secret/credential.

Out-of-scope: kompromi root/kernel atau akses fisik, efek samping perintah yang sudah di-approve pengguna, bug pada heuristik (classifier shell, threat scan, redaksi pola) yang tidak melewati boundary, dan laporan yang hanya memakai versi `N-2` atau lebih lama.

Kami tidak akan menuntut pelapor yang mengikuti kebijakan ini dan tidak merusak data, layanan, atau privasi pengguna lain.
