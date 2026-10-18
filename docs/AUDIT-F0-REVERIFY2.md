# AUDIT-F0-REVERIFY2 — Verifikasi ulang putaran 3 (remediasi putaran 2)

Tanggal: 2026-10-18. Commit yang diverifikasi: `3d5fb8c` (remediasi `b30c63f..3d5fb8c`, ADR-079..087). Auditor sama dengan [`AUDIT-F0.md`](AUDIT-F0.md) dan [`AUDIT-F0-REVERIFY.md`](AUDIT-F0-REVERIFY.md); auditor tidak ikut menulis remediasi.

Kriteria verdict sama dengan putaran sebelumnya:
- **closed**: ADR menyelesaikan akar masalah, teksnya ada di semua dokumen sasaran, teks lama yang bertentangan sudah tidak ada, dan test bernama tercantum di acceptance fase yang tepat.
- **partial**: masih ada residu.
- **not closed**: belum ditangani.

Disiplin severity untuk temuan baru:
- **BLOCKER**: wajib diperbaiki sebelum kode F1 apa pun ditulis.
- **MAJOR**: wajib ditutup lewat ADR sebelum modul (atau Deliverable F1 #0) yang disentuhnya.
- **MINOR**: selain itu.

## Ringkasan

| | Jumlah |
|---|---:|
| Item yang diverifikasi: 42 BLOCKER/MAJOR AUDIT-F0 + NEW-01..NEW-20 REVERIFY = 62 | |
| — closed | 58 |
| — partial | 4 (AUD-020, NEW-05, NEW-10, NEW-15) |
| — not closed | 0 |
| Temuan baru putaran ini — BLOCKER | 0 |
| Temuan baru putaran ini — MAJOR | 6 (NEW2-01..06; NEW2-05 dan NEW2-06 adalah residu NEW-15 dan NEW-05) |
| Temuan baru putaran ini — MINOR | 21 (NEW2-07..27; beberapa baris menggabungkan sub-temuan) |

**Verdict gate F0 → F1: BELUM terpenuhi secara harfiah, tetapi tidak ada lagi BLOCKER.**
- Semua BLOCKER (AUD-001..006, NEW-01, NEW-02) sudah closed. Tidak ada temuan yang menghalangi penulisan kode F1 apa pun.
- Syarat ROADMAP §F0 ("temuan BLOCKER/MAJOR ditutup lewat ADR") belum terpenuhi karena masih ada 6 MAJOR terbuka (NEW2-01..06). Dua di antaranya adalah residu item partial NEW-15 dan NEW-05. Dua item partial lain (AUD-020, NEW-10) residunya MINOR (NEW2-12, NEW2-13).
- Empat dari enam MAJOR menyentuh modul F1: NEW2-05 menyentuh Deliverable F1 #0; NEW2-01, NEW2-02, dan NEW2-06 menyentuh `exec`. Dua lainnya (NEW2-03, NEW2-04) menyentuh F5. Satu putaran errata ADR yang kecil (ADR-079, ADR-081, ADR-085/087, ADR-076/055) cukup untuk menutup gate. Tidak ada desain yang perlu dirombak.

Perubahan dari putaran 2 adalah model shell ADR-079: tanpa auto dari classifier, satu-satunya jalur auto adalah `shell_allow` argv-persis. Model ini menutup seluruh kelas bypass classifier secara struktural. Jalur auto-eksekusi yang tersisa ada di luar `shell_allow`: `allow_session`, `execute_code`, dan LSP.

### Perintah yang dijalankan (read-only)

| Perintah | Hasil |
|---|---|
| `make check` @ `3d5fb8c` | **EXIT 0**. Lint 0 issues. Test `-race`: ctxdb 168,5 s; test-timing: ctxdb 32,6 s. vet-cross 4/4. size ok. LOC ctxdb 2.829/3.100. tidy/verify/vuln/licenses ok. |
| `make vet-cross` (bagian dari `check`, plus run terpisah oleh subagen) | lulus 4/4 (windows/darwin × amd64/arm64) |
| `cd bench && go test -count=1 ./...` | semua ok (h2h 3,6 s); `-race ./h2h/...` ok |
| `go test -run TestParityPhaseColumn ./internal/archtest`; `cd bench && go test -run TestNoLLMBaselineCoverage ./speed/` | PASS keduanya |
| Mutasi di salinan `/tmp` | TOOL-021 F1→F2 → FAIL. Ini menunjukkan jumlah per fase kini ditegakkan, jadi NEW-43 tertutup. Hapus 1 entri `not_measured` → FAIL. Ubah fase atau nama dimensi `not_measured` → **PASS** (NEW2-07). |
| `go test -count=3 -run 'TestMultiProcess\|TestPlatformNAList' ./internal/ctxdb/` (juga dengan `-race` dan `DAMNBOT_TIMING=1`) | PASS 3/3 |
| Eksperimen fairness di salinan `/tmp` | 16 dan 50 writer PASS. Dengan fsync +30 ms (strace inject), 4/8/16 writer PASS. Fairness aktif vs mati: 6,1–7,8 s vs 6,6–9,4 s. |
| Eksperimen parser mvdan v3.14.1 | `"go test ./...\r"`: argv versi mvdan berbeda dengan argv versi bash (NEW2-15) |

`scripts/ci-meta-test.sh` tidak saya jalankan ulang. Status hijau Actions untuk `3d5fb8c` diterima dari laporan pengguna. Setting branch protection `required_status_checks` ada di luar repo, sehingga tidak bisa diverifikasi.

## Verdict — item yang partial di REVERIFY (16)

| ID | ADR | Verdict | Bukti | Catatan |
|---|---|---|---|---|
| AUD-001 | 079 | closed | docs/DECISIONS.md:1939-1941; SPEC.md:2578-2579, :2590-2591; docs/CODE-TOOLS.md:409-416; docs/ROADMAP.md:115 | Allowlist `read_only` auto sudah dihapus. Baca/cari dilakukan lewat tool kode tersandbox. `deny_paths` di shell menjadi best-effort dan tercatat sebagai residu (docs/TRUST-MODEL.md:47). |
| AUD-004 | 065/083 | closed | docs/DECISIONS.md:1495, :2054-2060; bench/speed/results/hermes-868b9f244f-nollm.json (`not_measured` 27); docs/F0-REPORT.md:11; docs/SCORECARD.md:27; docs/ROADMAP.md:123, :136, :149, :161, :186, :217 | Baseline hermes per fase sudah masuk acceptance. Test penjaganya lemah (NEW2-07). |
| AUD-007 | 070/079 | closed | SPEC.md:2586, :2620-2622; docs/EXEC-AND-CODING.md:319; docs/ROADMAP.md:114-115 | Semua output shell di workspace untrusted kini men-taint. |
| AUD-008 | 079 | closed | docs/DECISIONS.md:1946; docs/TRUST-MODEL.md:45-46; docs/EXEC-AND-CODING.md:64-65, :71 | Bypass classifier sekarang hanya mengubah label. Sesi persisten jalan dengan `--norc` dan alias mati, dan tidak eligible setelah ada perintah manual. |
| AUD-010 | 079 | closed | SPEC.md:2427, :2591; docs/EXEC-AND-CODING.md:310-315, :414, :495; docs/ROADMAP.md:115 | Default `shell_allow = []` dan `auto_allow_verify` dihapus. Syarat trust/taint/cwd/sesi/tulis berlaku. Varian lewat `allow_session` dicatat di NEW2-01. |
| AUD-014 | 081 | closed | SPEC.md:115, :2612; docs/CROSS-CUTTING.md:24; docs/EXEC-AND-CODING.md:103; docs/ROADMAP.md:114 | Residu di NEW2-14. |
| AUD-017 | 079 | closed | SPEC.md:127, :2608, :2872; docs/TRUST-MODEL.md:9-12 | Boundary dibatasi pada tool kode dan netx. Aktor "konten repo/workspace" sudah ada. |
| AUD-018 | 082 | closed | SPEC.md:100, :379, :467; docs/ROADMAP.md:109, :113 | §4.2 kini punya cabang `content_filter`. |
| AUD-020 | 082 | **partial** | SPEC.md:536-538, :693, :2215, :2878; docs/EXEC-AND-CODING.md:128, :252, :365; docs/SCORECARD.md:150; docs/MITIGATION-MATRIX.md:181 | Enum `ExecStatus`/`RecoveryStatus` sudah tunggal. Masih ada dua residu (MINOR, NEW2-12): (a) SPEC:2215 (SIGTSTP) memakai `interrupted` untuk hasil live, padahal nilai itu milik `RecoveryStatus`; (b) `TestACPChildNoStdin` masih tertulis di SCORECARD:150 dan MITIGATION:181. |
| AUD-023 | 082 | closed | SPEC.md:563, :753; docs/EXEC-AND-CODING.md:189, :614; docs/MITIGATION-MATRIX.md:25; docs/ROADMAP.md:116 | Pembawa `user_changed` dan sub-cap `<runtime>` (6+14+20 = 40) sudah ditetapkan. |
| AUD-027 | 080 | closed | SPEC.md:998, :2662; docs/ROADMAP.md:54, :104 | Teks lama hanya tersisa di ADR-077, yang sudah bertanda amandemen. |
| AUD-034 | 085 | closed | .github/workflows/meta.yml:7-13, :41, :45-46; docs/ENGINEERING.md:531, :575 | meta berjalan di setiap PR, cek path dilakukan di dalam job (fail-closed), nightly menjalankan semua gate, dan meta masuk E-78. |
| AUD-037 | 085 | closed | bench/h2h/proxy.go:247-251; ledger.go:16, :36-54, :71-89; cmd/startup/main.go:111-116; cmd/h2h/main.go:91-92, :107; results/f1-spend.json | Ledger F1 tunggal dan wajib, dengan flock. Residu di NEW2-24. |
| AUD-039 | 085 | closed | docs/DECISIONS.md:2090; docs/ROADMAP.md:121-122 | `TestAgentIsolation` dan `TestRecordingsNoKey` menjadi prasyarat smoke F1. |
| AUD-041 | 084 | closed | SPEC.md:904, :1450, :2399, :2604, :2892; docs/ROADMAP.md:160 | Kolom nullable kini punya default turunan dan sumber kebenaran tunggal. Komentar migrasi basi tercatat di NEW2-27. |
| AUD-042 | 084 | closed | SPEC.md:886, :2269, :2599, :2756, :2865; docs/ROADMAP.md:173 | Tabel §11.11.10 sudah diperbarui. Field `origin` belum ada di daftar kanonik (NEW2-19). |

## Verdict — NEW-01..NEW-20 (REVERIFY)

| ID | ADR | Verdict | Bukti | Catatan |
|---|---|---|---|---|
| NEW-01 | 079 | closed | docs/DECISIONS.md:1939-1942; docs/EXEC-AND-CODING.md:617; SPEC.md:2594 | Ditutup secara struktural. `TestShellNoClassifierAuto` (≥ 300 perintah) masuk acceptance F1. |
| NEW-02 | 080 | closed | docs/DECISIONS.md:2005-2007; docs/ROADMAP.md:54, :104; SPEC.md:998, :2662 | Fase < `PHASE` wajib covered, = boleh pending, > wajib pending. Gate hijau di F1 #0. Celah kecil di NEW2-22. |
| NEW-03 | 079 | closed | docs/DECISIONS.md:1946, :1984; docs/TRUST-MODEL.md:45 | Filter git hanya berjalan setelah approval atau lewat `shell_allow`. Tulis auto ke `.git/config` memblokir `shell_allow`. |
| NEW-04 | 079 | closed | docs/EXEC-AND-CODING.md:310-311; docs/ROADMAP.md:115 | cwd hidup dibaca lewat sentinel `pwd -P`. |
| NEW-05 | 081 | **partial** | docs/DECISIONS.md:2021; SPEC.md:2397; docs/EXEC-AND-CODING.md:56, :194, :332, :619 | Scope `commands` sudah diputuskan. Runner A.2 belum punya jalur eksekusi langsung (semua one-shot lewat `bash -c`), dan tidak ada test bernama untuk scope itu. Lihat NEW2-06. |
| NEW-06 | 079 | closed | docs/DECISIONS.md:1943; docs/EXEC-AND-CODING.md:414, :495; SPEC.md:2579, :2592 | Residu `evidence_gap` di NEW2-21. |
| NEW-07 | 079 | closed | docs/TRUST-MODEL.md:12, :28; SPEC.md:2872 | Klaim "untrusted tidak menjalankan kode otomatis" masih dilanggar oleh LSP (NEW2-04). |
| NEW-08 | 081 | closed | docs/DECISIONS.md:2022; SPEC.md:115, :2612 | Lihat NEW2-14. |
| NEW-09 | 079 | closed | SPEC.md:2586, :2620, :2622; docs/ROADMAP.md:115 | |
| NEW-10 | 082 | **partial** | SPEC.md:753; docs/EXEC-AND-CODING.md:455 | Notice `user_changed` ≤ 40 tok masih lebih besar dari slot volatile ≤ 23 (MINOR, NEW2-13). |
| NEW-11 | 082 | closed | docs/ROADMAP.md:116 | |
| NEW-12 | 083 | closed | docs/ROADMAP.md:123, :136, :149, :161, :186, :217 | |
| NEW-13 | 085 | closed | .github/workflows/meta.yml:1-64; docs/ENGINEERING.md:531, :575 | |
| NEW-14 | 085 | closed | bench/h2h/cmd/startup/main.go:112; ledger_lock_unix.go:1-25; .gitignore:5-6 | |
| NEW-15 | 085 | **partial** | internal/ctxdb/env_darwin.go; env_windows.go:105-148; lock_other.go:10-18; store.go:171-175; maint.go:143-153; os_windows_test.go:16-17; scenario_test.go:40-78 | Probe darwin dan windows sudah nyata, dan `TestPlatformNAList` mencegah skip diam-diam. Namun `tryLock` di Windows selalu gagal, sehingga Store tidak bisa dibuka dan probe Windows tidak pernah tereksekusi. Lihat NEW2-05. |
| NEW-16 | 084 | closed | SPEC.md:2267, :2599, :2756, :2858; docs/ROADMAP.md:114 | ACP diakui sebagai kanal P26. Headless produksi → `denied`. Test F1 memakai approver palsu. |
| NEW-17 | 082 | closed | SPEC.md:126, :2726; docs/CODE-TOOLS.md:525, :562; docs/SCORECARD.md:42, :239 | Target `patch` ≥ 2× dengan durabilitas setara (≈ ≤ 5,8 ms p50) layak [INFERENCE: ukur ulang di Q14]. Ringkasan teks di NEW2-17. |
| NEW-18 | 082 | closed | docs/EXEC-AND-CODING.md:78-98; SPEC.md:693 | Schema `shell` per fase. |
| NEW-19 | 080 | closed | docs/ROADMAP.md:61, :63, :64; SPEC.md:2662 | Partisi STATE 16, WIN 17, TERM 10 cocok dengan katalog. |
| NEW-20 | 081 | closed | loc.budget:11-16; docs/ENGINEERING.md:315, :323-331; SPEC.md:279 | `exec` 3.500 + `exec/classify` 1.500. Σ `loc.budget` 33.750 = ENGINEERING §2.4, dan masih ≤ 47.000 (core 30.000 + codetools 4.500 + exec 7.000 + heal 5.500). |

## Verdict — item yang sudah closed di REVERIFY (26)

AUD-002, 003, 005, 006, 009, 011, 012, 013, 015, 016, 019, 021, 022, 024, 025, 026, 028, 029, 030, 031, 032, 033, 035, 036, 038, 040 diperiksa ulang terhadap regresi akibat ADR-079..087. Hasilnya **tetap closed**.
- ADR-079 mempertahankan butir ADR-069 yang relevan untuk AUD-009 (`irreversible`/`privileged`, P26) dan AUD-012 (baca di luar root lewat tool kode; symlink keluar root kini `outside_root`, sehingga NEW-22 ikut tertutup).
- AUD-011 tetap closed. Residu secret ref ada di NEW-05/NEW2-06.
- AUD-035/036: perubahan budget ADR-087 konsisten (lihat NEW-20). Satu catatan untuk AUD-040: approval `execute_code` sendiri tidak didefinisikan (NEW2-03). Akar masalah RPC `network:"deny"` tetap tertutup.

## Pemeriksaan khusus ADR-079 (jalur auto-eksekusi tersisa)

| Jalur | Hasil | Rujukan |
|---|---|---|
| `shell_allow` argv-persis | **Ketat.** Satu simple command tanpa metakarakter, ekspansi, assignment (termasuk `FOO=1 go test`), redirect, atau heredoc; argv0 tanpa path dan di-resolve ke binary saat sesi mulai; cwd di root; sesi persisten eligible hanya bila belum ada perintah manual. Diuji dengan mvdan: `time`, `!`, `{}`, `&`, redirect, dan multi-statement semuanya bukan simple command. Sisa: beda parse CR/NUL dan semantik Windows (NEW2-15, MINOR). | docs/DECISIONS.md:1941; SPEC.md:2591; docs/EXEC-AND-CODING.md:309 |
| `--unsafe-auto-shell` | **Terbatas.** Hanya flag `run` headless + `--approval auto`; tidak bisa diaktifkan lewat config, env, ACP, gateway, atau cron. Mati di workspace untrusted dan saat turn tainted. `destructive`/`unknown`/P26/`privileged`/`denied_path` tetap denied. Sisa: perilaku tanpa `--approval auto` dan penanda visibilitas belum ditetapkan (NEW2-08, MINOR). | SPEC.md:1003, :2592; docs/DECISIONS.md:1942 |
| verify | **Tertutup.** Auto hanya bila perintah verify persis ada di `shell_allow`. | docs/EXEC-AND-CODING.md:414, :495 |
| `allow_session` | **Terbuka (MAJOR, NEW2-01).** Syarat `shell_allow` tidak berlaku. | docs/EXEC-AND-CODING.md:306-307, :339 |
| hooks `pre_tool` | `allow` belum dinyatakan tidak pernah meng-approve (MINOR, NEW2-09). | SPEC.md:503, :670, :1188 |
| plan mode | Hanya `shell_allow` + `non_mutating`, membatasi, tidak pernah mengizinkan. Label `non_mutating` belum didefinisikan dan test fasenya belum sinkron (MINOR, NEW2-16). | docs/DECISIONS.md:1949; SPEC.md:2594 |
| gateway / cron | Status auto `shell_allow` vs P25 "selalu konfirmasi" ambigu (MINOR, NEW2-10). | SPEC.md:123, :903, :941, :2578 |
| `smart_approval`, `allow_destructive`, `destructive_allow` | Tidak berlaku untuk shell; dicadangkan tanpa efek. | SPEC.md:961, :2435, :2590 |
| `execute_code` (F5) | **Terbuka (MAJOR, NEW2-03).** | docs/EXEC-AND-CODING.md:508, :554 |
| LSP / codeintel (F5) | **Terbuka (MAJOR, NEW2-04).** | docs/EXEC-AND-CODING.md:421-424 |
| recovery redispatch | Urutan prioritas terhadap `shell_allow` belum dinyatakan (MINOR, NEW2-18). | docs/SELF-HEALING.md:368 |

## Pemeriksaan khusus ADR-087 (fairness writer + budget)

- **Mekanisme.** Penunggu memegang flock shared pada `run/writer.wait` selama `BEGIN`. Setelah commit, writer mencoba flock exclusive non-blocking; bila gagal, writer menunggu 150 ms sebelum transaksi berikutnya. Alasannya: 150 ms lebih besar dari jeda terpanjang busy handler SQLite (100 ms).
  - Crash safety aman: sinyalnya adalah flock, yang dilepas kernel saat kill -9.
  - Urutan lock tidak menambah deadlock (SPEC.md:2285).
- **Batas fairness.** Rumus `(N−1)×(hold+150 ms)` hanya dihitung untuk 4 proses. Untuk 50 klien (P19 SPEC.md:117, GAP-REL-03 F2), rumus itu sendiri sudah memberi ≥ 7,35 s, melebihi `busy_timeout` 5 s. Penunggu juga tidak FIFO satu sama lain.
  - Secara empiris (eksperimen auditor) 50 writer lulus, jadi rumusnya pesimistis. Klaimnya tetap belum dibuktikan oleh test yang mengasersi waktu tunggu (NEW2-23, MINOR, sebelum F2).
- **Interaksi dengan target lain** (NEW2-23, MINOR):
  - Di bawah kontensi berkelanjutan, Append dari writer yang baru commit tertunda ≥ 150 ms, padahal §17 "Append ≤ 10 ms p99" tidak mengecualikan kontensi ini.
  - Penunggu yang di-SIGSTOP men-throttle writer lain tanpa batas waktu.
  - Semua error `tryLock` diperlakukan sebagai kontensi.
- **Budget.**
  - `ctxdb` 2.800 → 3.100 memang diperlukan: terukur 2.829, headroom 271.
  - Σ `loc.budget` = ENGINEERING §2.4 = 33.750 ≤ 47.000, dan SPEC §3.2.4 tidak berubah. Konsisten, karena SPEC §3.2.4 adalah budget gabungan, sedangkan `loc.budget` mengatur alokasi per paket di dalamnya.
  - Perubahan dilakukan lewat ADR (commit `8a212e6` menyertakan DECISIONS.md). Namun adrgate E-82 secara mekanis tidak memicu pada `loc.budget` (NEW2-26).
  - Penanda amandemen di ADR-049/069/085 belum ada (NEW2-25).
- **Windows.** `fairLocks = false` dan `tryLock` selalu gagal, jadi fairness dan Store sama-sama tidak tersedia di Windows (NEW2-05, MAJOR).

## Temuan baru putaran 3

| ID | Severity | Area | Temuan | Bukti (file:line) | Rekomendasi |
|---|---|---|---|---|---|
| NEW2-01 | MAJOR | F1 `exec`: `allow_session` adalah jalur auto shell kedua | ADR-079 menyatakan `shell_allow` sebagai "satu-satunya jalur auto" dengan syarat 1–5 (cwd, eligibility sesi persisten, argv0/PATH, trust, tidak ada tulis tanpa approval). Namun `allow_session` (subject "perintah ter-normalisasi"; ACP `allow_always`) tetap meng-auto pengulangan shell non-P26 tanpa satu pun syarat itu, kecuali taint. Akibatnya skenario AUD-010 muncul lagi: `allow_session` untuk `go test ./...` di `auto-edit`, lalu file test ditulis auto, lalu `go test` jalan auto. EXEC:306 juga bertentangan dengan dirinya sendiri: `destructive` "tidak pernah auto, selalu tanya", tetapi `allow_session` "tetap berlaku untuk non-P26". Tabel 36 baris ADR-079 tidak punya baris `allow_session`. | docs/EXEC-AND-CODING.md:306-307, :339; SPEC.md:2556, :2596; docs/DECISIONS.md:1941, :1947, :1992 | Errata ADR-079: `allow_session` shell memakai kecocokan argv-persis yang sama dan wajib memenuhi syarat 1–5 `shell_allow`. `destructive`/`unknown` dikecualikan dari `allow_session`. Tambah baris ke `TestShellApprovalModel`. |
| NEW2-02 | MAJOR | F1 `exec/classify`: flag `outside_read` SPEC vs EXEC | SPEC §14.2 menyatakan "tidak ada flag `outside_read` untuk shell". EXEC §A.8 tetap mendefinisikan `outside_read` lengkap dengan kebijakannya (tanya; `outside_workspace` juga di bawah `--unsafe-auto-shell`), sumber taint, dan wrapper `<untrusted source="shell:outside_workspace">`. Perilakunya berbeda nyata: `cat ../x` dengan `--unsafe-auto-shell` jalan dan tidak men-taint menurut SPEC, tetapi ditolak dan men-taint menurut EXEC. | SPEC.md:2606; docs/EXEC-AND-CODING.md:264, :276, :302, :319; docs/DECISIONS.md:1940 | Satu errata ADR. Disarankan mempertahankan `outside_read` sebagai label best-effort beserta taint dan penolakan headless/unsafe, karena gagalnya fail-closed. Selaraskan SPEC §14.2/§14.5 dan ADR-079 butir 2. |
| NEW2-03 | MAJOR | F5 `execute_code`: approval dasar tidak didefinisikan | §14.1 tidak punya baris `execute_code`. EXEC §B.7 hanya menyebut "`network:"deny"` tanpa penegakan → dinaikkan ke destructive-level (tanya, termasuk di `auto`)", yang menyiratkan level dasar auto di mode `auto`. Akibatnya `execute_code{language:"bash"}` menjadi pintu belakang bagi invariant ADR-079 dan `deny_paths`. Definisi vulnerability (a) hanya mencakup tool `shell`. | docs/EXEC-AND-CODING.md:508, :554; SPEC.md:2572-2588; docs/TRUST-MODEL.md:12 | Sebelum F5: tambah baris §14.1. `execute_code` = tanya di semua mode interaktif; headless denied kecuali `--unsafe-auto-shell`; mati di workspace untrusted. Perluas vulnerability (a). Tambahkan test ke acceptance F5. |
| NEW2-04 | MAJOR | F5 codeintel: LSP menjalankan kode workspace tanpa trust/approval | Diagnostik pasca-edit memulai LSP secara otomatis. rust-analyzer menjalankan `build.rs`/proc-macro, jdtls menjalankan skrip Gradle, dan tsserver memuat plugin dari `node_modules`. Tidak ada gerbang workspace trust. Ini bertentangan dengan TRUST-MODEL:28 dan menjadi jalur auto-eksekusi di luar ADR-079. | docs/EXEC-AND-CODING.md:421-424; docs/CODE-TOOLS.md:354; docs/TRUST-MODEL.md:28 | Sebelum F5: di workspace untrusted, LSP yang mengeksekusi kode proyek mati atau jalan dengan setting tanpa build-script/plugin. Di workspace trusted, catat sebagai residu. Test masuk acceptance F5. |
| NEW2-05 | MAJOR | `ctxdb` Windows / matrix Deliverable F1 #0 (sisa NEW-15) | `tryLock` `!unix` selalu gagal, sehingga `Store.Open` → `waitSharedLock` → `db_busy`. Seluruh suite ctxdb akan merah di runner Windows yang dinyalakan F1 #0, termasuk S023/S044 yang menurut komentar "run". Probe Windows ADR-085 tidak pernah tereksekusi, dan fairness ADR-087 mati di Windows. LockFileEx disebut "kerja F1" tanpa deliverable, pemilik, maupun acceptance di ROADMAP. | internal/ctxdb/lock_other.go:10-18; store.go:171-175; maint.go:143-153; os_windows_test.go:16-17; docs/DECISIONS.md:2157; docs/ROADMAP.md:96-97 | ADR sebelum F1 #0. Tambahkan ke F1 #0: `tryLock` LockFileEx (shared/exclusive, `LOCKFILE_FAIL_IMMEDIATELY`) dan `fairLocks = true`, dengan acceptance "S023/S044 + `TestMultiProcess` hijau di runner Windows". Alternatifnya, nyatakan ctxdb platform-n/a Windows secara eksplisit sampai fase tertentu. Perbaiki komentar `os_windows_test.go`. |
| NEW2-06 | MAJOR | F1 `exec`: secret ref `exec.env` tanpa jalur eksekusi dan tanpa test (residu NEW-05) | Scope `commands` mensyaratkan eksekusi langsung tanpa interpreter, tetapi runner A.2 menjalankan semua one-shot lewat `bash -c`. Akibatnya implementer F1 harus mendesain sendiri jalur execve (quoting, PATH, PTY), atau fitur ini tidak bisa dipakai. EXC-15 `TestEnvAllowlist` mengizinkan `exec.env` tanpa kasus scope, sehingga penyuntikan ke semua spawn tetap lulus. Pelacak mengklaim "kasus secret ref" yang tidak ada di dokumen normatif. | docs/EXEC-AND-CODING.md:56, :194, :332, :619; docs/DECISIONS.md:2021; docs/AUDIT-F0-RESOLUTION.md:74 | Errata ADR-081 sebelum `exec`: definisikan jalur execve runner (dipakai juga oleh `shell_allow`, lihat NEW2-15), dan tambah `TestSecretRefScopedToCommands` ke acceptance F1. |
| NEW2-07 | MINOR | `TestNoLLMBaselineCoverage` | Hanya memeriksa jumlah. Mutasi fase atau nama dimensi lolos. Test ini juga tidak jalan di CI, karena Makefile `bench-budget` hanya menjalankan `./budget/...`. | bench/speed/nollm_test.go:28-39; Makefile:81-82 | Sematkan peta dimensi → fase. `bench-budget` → `cd bench && go test ./...` (AUD-075, F1 #0). |
| NEW2-08 | MINOR | `--unsafe-auto-shell` | Perilaku flag tanpa `--approval auto` belum didefinisikan. Tidak ada banner, event, atau field `unsafe_auto_shell` di hasil. | SPEC.md:1003, :2592; docs/EXEC-AND-CODING.md:317, :339 | Tanpa `--approval auto` → error penggunaan. Tambahkan field/event; masukkan ke `TestUnsafeAutoShellFlag`. |
| NEW2-09 | MINOR | Hooks `pre_tool` (F3) | Tidak ada pernyataan bahwa `allow` tidak pernah melewati approval. | SPEC.md:503, :670, :1188 | Errata §4.10: `allow` = lanjut ke kebijakan normal. |
| NEW2-10 | MINOR | Gateway/cron vs `shell_allow` (F4) | P25/§9.7 "selalu konfirmasi" vs §14.1 "cocok `shell_allow` = auto". Trust root `serve`/cron belum didefinisikan. | SPEC.md:123, :903, :941, :2578, :2593 | Nyatakan secara eksplisit sebelum F4. |
| NEW2-11 | MINOR | Reset syarat "tulis tanpa approval" | Menyetujui `ls` yang tidak terkait mereset penjaga ini; tulisan dari sesi lain tidak dihitung. | docs/EXEC-AND-CODING.md:315; docs/DECISIONS.md:1941, :1969 | Reset hanya lewat review daftar file yang ditulis auto. |
| NEW2-12 | MINOR | Sisa AUD-020 | SPEC:2215 memakai `interrupted` (milik `RecoveryStatus`) untuk hasil live SIGTSTP. `TestACPChildNoStdin` masih ada di SCORECARD:150 dan MITIGATION:181. | SPEC.md:2215; docs/SCORECARD.md:150; docs/MITIGATION-MATRIX.md:181 | Ganti ke `signaled` dan `TestACPChildStdinIsolation`. |
| NEW2-13 | MINOR | Sisa NEW-10 | Notice `user_changed` ≤ 40 tok, sedangkan slot volatile ≤ 23. | docs/EXEC-AND-CODING.md:455; SPEC.md:753 | Render ≤ 23 tok (path pertama + `+N more`). |
| NEW2-14 | MINOR | Redaksi stream: aturan mulai-tahan | Bila batas chunk jatuh di dalam prefix (`gh`\|`p_…`, `-----BEG`\|`IN`), matcher yang hanya maju kehilangan token. Lookbehind atas teks yang sudah dipancarkan tidak diwajibkan, padahal `TestStreamRedactSplit` menuntut 0 bocor di setiap offset. | docs/DECISIONS.md:2022; SPEC.md:2612 | Wajibkan lookbehind ≤ panjang prefix maksimum, dan definisikan "bocor". |
| NEW2-15 | MINOR | Parser `shell_allow` vs bash; Windows | mvdan memperlakukan CR sebagai spasi dan membuang NUL, sedangkan bash memasukkannya ke kata (diuji). Kecocokan argv-persis bergantung pada kesetaraan parser. Untuk pwsh/cmd, kecocokan belum didefinisikan; [INFERENCE] cmd.exe mencari `go.bat` di cwd sebelum PATH. "Binary yang sama" (path vs inode/hash) juga belum didefinisikan. | docs/DECISIONS.md:1941; docs/EXEC-AND-CODING.md:56, :267 | Entri `shell_allow` dieksekusi via execve argv (bukan `bash -c`). Tolak karakter kontrol. Batasi atau definisikan untuk Windows. Tambah kasus ke `TestShellAllowExactMatch`. |
| NEW2-16 | MINOR | Sinkronisasi test/fase shell | `TestPlanModeShell` tercantum sebagai F1 di SPEC/EXEC, tetapi F3 di ROADMAP. `non_mutating` tidak terdefinisi. ADR-070 tanpa penanda amandemen. Belum ada test bahwa kunci butir 9 tidak meng-auto shell. MITIGATION K-02 masih menggambarkan `smart_approval` aktif. `TestHeadlessDeniesDangerous` (S-15) tidak berfase. | SPEC.md:2594, :961; docs/EXEC-AND-CODING.md:265, :283-285, :323; docs/ROADMAP.md:115, :147; docs/DECISIONS.md:1703-1716; docs/MITIGATION-MATRIX.md:63 | Errata sinkronisasi. Definisikan `non_mutating`, atau tetapkan default "tidak pernah". Tambah `TestShellAutoKeysIgnored` (F1). |
| NEW2-17 | MINOR | Ringkasan P28 | SPEC:69 dan ROADMAP:212 ("≥ 5×/10×") tidak menyebut pengecualian patch 2×. SCORECARD:239 memasukkan "edit" ke kelompok 10×. | SPEC.md:69, :126; docs/ROADMAP.md:212; docs/SCORECARD.md:239 | Selaraskan wording. |
| NEW2-18 | MINOR | Recovery vs `shell_allow` | Belum dinyatakan bahwa gate pengulangan `non_idempotent` setelah `outcome_unknown` menang atas `shell_allow`. | docs/SELF-HEALING.md:368; SPEC.md:2578, :2597 | Nyatakan secara eksplisit dan tambah baris tabel ADR-079. |
| NEW2-19 | MINOR | Field `origin` (F5) | `origin` tidak ada di daftar field `approval_request` (SPEC:1711, :2260, :2857; SELF-HEALING:319; komentar `0005_recovery.sql`). Spike `approval_ref_test.go` belum memodelkan penolakan P26 untuk `mcp_serve`. | SPEC.md:1711, :2260, :2857; internal/ctxdb/approval_ref_test.go:17-26, :105-124 | Selaraskan sebelum F5. |
| NEW2-20 | MINOR | Pemetaan `origin` sesi non-interaktif | Belum ada nilai `origin` untuk cron, delegate, kanban, dan batch. | SPEC.md:865, :941, :1059 | Tetapkan pemetaannya (cron → `headless`, delegate → origin induk). |
| NEW2-21 | MINOR | Verify / `evidence_gap` | "unverified + `evidence_gap`" bertentangan dengan §7.2, yang mensyaratkan adanya klaim. | docs/DECISIONS.md:1943; SPEC.md:815, :2579 | "unverified (evidence_gap menurut §7.2)". |
| NEW2-22 | MINOR | Gate `PHASE` | Selama `PHASE = F1`, item F1 boleh pending, tetapi acceptance fase tidak mewajibkan `make phase-acceptance` hijau. Aturan "fase > PHASE wajib pending" melarang item yang selesai lebih awal. | docs/DECISIONS.md:2005-2007; docs/ROADMAP.md:54, :104 | Tambahkan bullet `phase-acceptance` per fase, dan izinkan covered lebih awal. |
| NEW2-23 | MINOR | ADR-087: batas dan interaksi | Lihat bagian khusus ADR-087: rumus untuk 50 klien > `busy_timeout`; tidak ada test batas tunggu; Append di bawah kontensi; penunggu SIGSTOP; semua error `tryLock` dianggap kontensi. | docs/DECISIONS.md:2156, :2163; internal/ctxdb/tx.go:157-201; lock_unix.go:15-31; SPEC.md:117, :583, :2350, :2687, :2875 | Sebelum F2: `TestWriterFairness` (asersi waktu tunggu maksimum, `DAMNBOT_TIMING`), analisis untuk 50 klien, catatan pengecualian di §17, bedakan ErrDBBusy dari error lain, dan skenario "penunggu SIGSTOP" di §11.11.13. |
| NEW2-24 | MINOR | Ledger h2h | Reservasi hanya di memori proses, sehingga dua driver bersamaan bisa melampaui cap sebesar reservasi in-flight. Tidak ada penanganan sinyal antara forward dan settle. `Ledger.Add` yang gagal tidak menghentikan proxy. Path ledger relatif ke cwd. | bench/h2h/proxy.go:213-226, :375-391; ledger.go:16, :36-54; cmd/h2h/main.go:107 | Sebelum smoke F1: `reserved_usd` disimpan di ledger di bawah flock, stop bila `Add` gagal, path di-resolve terhadap direktori modul. |
| NEW2-25 | MINOR | Penanda amandemen ADR | ADR-085 masih "dalam budget ctxdb 2.800". Status ADR-049 (2.800) dan ADR-069 ("exec 7.000") tanpa penanda ADR-087/081. Komentar `loc.budget` tidak mencatat ADR-087. ADR-068 baris SEC-14 tanpa penanda ADR-084. | docs/DECISIONS.md:1187, :1193, :1595, :1699, :2093; loc.budget:11, :16 | Tambah penanda gaya NEW-49. |
| NEW2-26 | MINOR | adrgate E-82 | Path pemicu tidak memuat `loc.budget` maupun tabel ENGINEERING §2.4, sehingga kenaikan budget per paket bisa merge tanpa ADR selama Σ ≤ 47.000. Kali ini prosesnya dipatuhi. | docs/ENGINEERING.md:579 | Tambahkan ke pr-meta F1 #0. |
| NEW2-27 | MINOR | Sinkronisasi kecil | Komentar `0003_gateway.sql:11` "(opsional)". ROADMAP:117 belum memuat pengecualian CT-34/35/36 seperti CODE-TOOLS:552. Acceptance F2 tanpa bullet backlog berfase F2 (NEW-44). Driver damnbot h2h (argv approval, `trusted_workspaces` workdir) belum ditetapkan, dan asimetri dengan hermes `--yolo` belum dicatat di SCORECARD. | internal/ctxdb/migrations/0003_gateway.sql:11; docs/ROADMAP.md:117, :130-136, :121; docs/CODE-TOOLS.md:552; docs/DECISIONS.md:1825, :2132; docs/SCORECARD.md:21, :71; bench/h2h/hermes.go:75 | Satu PR errata. |


## Area yang diperiksa tanpa temuan

- **Model ADR-079.**
  - `shell_allow = []` default.
  - `exec.auto_allow_verify` sudah tidak ada di dokumen normatif.
  - Larangan metakarakter konsisten (SPEC:2591, EXEC:309, DECISIONS:1941).
  - Shadowing alias/fungsi/rc dicegah.
  - `--unsafe-auto-shell` tidak punya jalur aktivasi selain flag `run`.
  - `smart_approval`/`allow_destructive`/`destructive_allow` dikecualikan dari shell di semua dokumen.
  - Tool kode menegakkan `deny_paths` dan `outside_root` termasuk symlink, dan walker grep/glob tidak mengikuti symlink.
  - Stable tier aturan 5 (SPEC:765).
  - Semua test ADR-079 F1 ada di ROADMAP:115.
- **PHASE.** Gate hijau di F1 #0, tidak vacuous saat penutupan fase (`make phase-acceptance`). Partisi EC STATE/WIN/TERM cocok dengan katalog.
- **Kontrak.** Enum `ExecStatus`/`RecoveryStatus` tunggal (kecuali NEW2-12). Sub-cap `<runtime>` = 40 dan volatile = 100. `content_filter` konsisten. CT-10 `patch` 2× setara durabilitas dinilai layak.
- **Persistence.**
  - Tidak ada perubahan DDL/migrasi di putaran 2.
  - Suite spike lulus.
  - Urutan lock ADR-087 tanpa deadlock baru.
  - `yieldUntil` dijaga `s.mu`.
  - Duty cycle I11 tetap ≤ 20% (S015 lulus dengan `DAMNBOT_TIMING=1`).
  - `TestPlatformNAList` memakai `t.Fatalf` dan golden per GOOS, tanpa `t.Skip`.
- **CI.**
  - `meta.yml` berjalan di setiap PR dengan base SHA benar; force-push → semua gate.
  - Path gate mencakup semua file yang dimutasi `ci-meta-test.sh`.
  - meta masuk E-78.
- **Budget.** Σ `loc.budget` = ENGINEERING §2.4 = 33.750 ≤ 47.000. SPEC §3.2.4 (Σ 184.900 / 48,4 MB) tidak berubah. ROADMAP F1 29.500 tetap. P1 5.000 / f1-core 4.000 tetap.
- **Ledger.** Tunggal dan wajib di `cmd/h2h` dan `cmd/startup`. Cap hanya bisa diturunkan. flock RMW dan penulisan atomik. `cached = 0` sah.
- **ADR-086.** NEW-21..52 semuanya punya fase atau status. AUD-047/048/053/064/068 kini punya keputusan konkret. ADR-078 tercakup di acceptance F1/F4/F5/F8.
- **Hasil perintah.** `make check` EXIT 0, `cd bench && go test ./...` ok, `make vet-cross` 4/4.

## Urutan penutupan yang disarankan

1. **Sebelum Deliverable F1 #0:** NEW2-05 (LockFileEx Windows, atau ctxdb dinyatakan n/a Windows lewat ADR); NEW2-26 bisa ikut di pr-meta.
2. **Sebelum `internal/exec`:** NEW2-01 (`allow_session`), NEW2-02 (`outside_read`), NEW2-06 + NEW2-15 (jalur execve untuk secret ref dan `shell_allow`).
3. **Sebelum F5:** NEW2-03 (`execute_code`), NEW2-04 (LSP).
4. **MINOR** sesuai fase di kolom Area. Dua yang paling murah: NEW2-12 dan NEW2-13.

Setelah butir 1–3 ditutup lewat ADR beserta teks terapannya, syarat gate ROADMAP §F0 terpenuhi. Re-verifikasi berikutnya cukup memeriksa keenam MAJOR itu.
