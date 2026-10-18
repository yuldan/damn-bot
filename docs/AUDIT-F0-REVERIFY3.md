# AUDIT-F0-REVERIFY3 — Verifikasi ulang putaran 4 (sempit)

Tanggal: 2026-10-18. Commit yang diverifikasi: `b65b7bc` (remediasi putaran 3, `36dc519..b65b7bc`: ADR-088..091, amandemen bertanggal `90dcaae`, `internal/ctxdb/lock_windows.go` + `lock_test.go`). Auditor sama dengan [`AUDIT-F0.md`](AUDIT-F0.md), [`AUDIT-F0-REVERIFY.md`](AUDIT-F0-REVERIFY.md), dan [`AUDIT-F0-REVERIFY2.md`](AUDIT-F0-REVERIFY2.md).

Cakupan sengaja dibatasi:
- NEW2-01..NEW2-06 dan item yang masih *partial*, yaitu AUD-020, NEW-05, NEW-10, dan NEW-15.
- Temuan BLOCKER/MAJOR baru akibat ADR-088..091, khususnya:
  - runner execve dan identitas binary yang sama;
  - aturan Windows untuk PATH/`.exe`;
  - aturan auto `execute_code`;
  - gerbang trust LSP;
  - pemetaan error LockFileEx.

Kriteria verdict dan disiplin severity sama dengan putaran sebelumnya.

## Ringkasan

| | Jumlah |
|---|---:|
| Item diverifikasi | 10 |
| closed | **10** |
| partial | 0 |
| not closed | 0 |
| Temuan baru BLOCKER | 0 |
| Temuan baru MAJOR | 0 |
| Temuan baru MINOR | 4 |

**Verdict gate ROADMAP F0 → F1 ("review penuh area non-persistence oleh auditor selesai; temuan BLOCKER/MAJOR ditutup lewat ADR"): TERPENUHI.**
- Semua BLOCKER dan MAJOR dari AUDIT-F0, REVERIFY, dan REVERIFY2 sudah ditutup lewat ADR Accepted. Teksnya diterapkan, dan test bernama tercantum di acceptance fase yang tepat.
- Remediasi putaran 3 tidak memunculkan BLOCKER atau MAJOR baru.
- MINOR yang tersisa sudah punya pemilik fase lewat ADR-078/086/091, ditambah empat MINOR baru di bawah. Sesuai definisi gate, MINOR tidak menahan F1, tetapi wajib ditutup sebelum acceptance fase pemiliknya.

### Perintah yang dijalankan

| Perintah | Hasil |
|---|---|
| `make check` @ `b65b7bc` | **EXIT 0**: lint 0 issues; test `-race` (ctxdb 169,5 s); test-timing (ctxdb 31,0 s); vet-cross 4/4; size ok; LOC ctxdb 2.857/3.100; tidy/verify/vuln/licenses ok |
| `make vet-cross` (terpisah) | lulus 4/4 (windows/darwin × amd64/arm64) |
| `go test -run TestFileLockSemantics -v ./internal/ctxdb/` (linux) | PASS |
| `GOOS=windows go test -c ./internal/ctxdb/` | compile ok. Test belum dieksekusi di Windows; acceptance-nya baru di matrix F1 #0 (docs/ROADMAP.md:113). |
| Pemeriksaan stdlib Go 1.26.9 (`os/file_windows.go:166`) | handle `os.OpenFile` hanya overlapped bila diberi `O_FILE_FLAG_OVERLAPPED` |

Status hijau GitHub Actions untuk `b65b7bc` diterima dari laporan pengguna.

## Verdict per item

| ID | ADR | Verdict | Bukti | Catatan |
|---|---|---|---|---|
| NEW2-01 (`allow_session` = jalur auto shell kedua) | 088 butir 1 | **closed** | SPEC.md:2559, :2596, :2602, :2761; docs/EXEC-AND-CODING.md:312; docs/ROADMAP.md (`TestShellNoAllowSession`) | `allow_session` tidak pernah ditawarkan untuk `shell` (juga `execute_code`); ACP `allow_always` → `allow_once`. Satu-satunya jalur auto shell tinggal `shell_allow`. Kontradiksi lama di EXEC:306 hilang. |
| NEW2-02 (`outside_read` SPEC vs EXEC) | 088 butir 2 | **closed** | SPEC.md:2586, :2612; docs/EXEC-AND-CODING.md:281, :307, :318, :324 | Kini satu definisi: label best-effort, fail-closed. Headless dan `--unsafe-auto-shell` → `outside_workspace`; output men-taint; tidak pernah auto, juga bila cocok `shell_allow`. Kalimat "tidak ada flag `outside_read`" tidak tersisa di dokumen normatif. |
| NEW2-03 (approval `execute_code`) | 089 butir 1, 3 | **closed** | SPEC.md:2591, :2602; docs/EXEC-AND-CODING.md:326, :522; docs/ROADMAP.md (`TestExecuteCodeApproval`, F5) | Baris §14.1 baru: `ask`/`auto-edit` selalu tanya. Mode `auto`/headless auto hanya untuk bahasa di `exec.code_auto_languages` (default `[]`) dengan syarat trust, taint, dan tulis tanpa approval. Bahasa shell diperlakukan sebagai `shell`. Definisi vulnerability diperluas. Catatan MINOR di NEW3-02. |
| NEW2-04 (LSP mengeksekusi kode workspace) | 089 butir 2, 3 | **closed** | docs/EXEC-AND-CODING.md §B.3/§B.4; docs/CODE-TOOLS.md §6; docs/TRUST-MODEL.md §1–§3; docs/ROADMAP.md (`TestLSPWorkspaceTrustGate`, F5) | Workspace untrusted: tanpa LSP/build script; operasi yang butuh LSP → `module_unavailable`; ada fallback tanpa eksekusi. Workspace trusted: build script/proc-macro dimatikan bila server mendukungnya; sisanya dicatat sebagai residu. |
| NEW2-05 (ctxdb Windows tidak bisa dibuka) | 090 | **closed** | internal/ctxdb/lock_windows.go; lock_test.go (`TestFileLockSemantics`); docs/ROADMAP.md:113; SPEC §11.11.1/§11.11.11 | LockFileEx shared/exclusive dengan `LOCKFILE_FAIL_IMMEDIATELY`, `fairLocks = true`. `ERROR_LOCK_VIOLATION` → `ErrDBBusy`; error lain diperlakukan sebagai error nyata. Acceptance F1 #0: suite ctxdb hijau di runner Windows. Kode ini belum pernah dieksekusi di Windows (diakui di Konsekuensi ADR-090), sehingga pembuktiannya ada di F1 #0. Lihat NEW3-01. |
| NEW2-06 (secret ref tanpa jalur eksekusi) | 088 butir 3, 4 | **closed** | docs/EXEC-AND-CODING.md:61-66, :337, :633 (EXC-15); docs/ROADMAP.md (`TestSecretRefScope`) | Runner punya mode (b) execve langsung. Secret hanya disuntikkan di mode (b) untuk argv0 ∈ `commands`; `bash -c`, sesi persisten, dan proses lain tidak pernah menerimanya. Referensi `$X` → `unknown`. Kasus scope masuk EXC-15. Catatan MINOR di NEW3-03. |
| AUD-020 (enum status) | 082 + 091 (NEW2-12) | **closed** | SPEC.md:537, :2218; grep `TestACPChildNoStdin` di dokumen normatif = 0 (hanya tabel ADR-091) | SIGTSTP live = `signaled` (`signal:"SIGTSTP"`); nama test tunggal `TestACPChildStdinIsolation`. |
| NEW-05 (secret ref `exec.env`) | 081 + 088 butir 4 | **closed** | lihat NEW2-06 | Scope `commands` kini punya jalur eksekusi dan test bernama. |
| NEW-10 (overflow volatile/`<runtime>`) | 082 + 091 (NEW2-13) | **closed** | docs/EXEC-AND-CODING.md:460; SPEC.md:563, :753 | Notice `user_changed` dirender ≤ 23 tok (path pertama + `+N more`), muat di slot volatile 23. Sub-cap `<runtime>` 40 dan volatile 100 tidak berubah. |
| NEW-15 (ctxdb lintas OS nyata) | 085 + 090 | **closed** | internal/ctxdb/env_darwin.go, env_windows.go, lock_windows.go; scenario_test.go (`TestPlatformNAList`); docs/ROADMAP.md:113 | Probe darwin/windows sudah nyata, Store bisa dibuka di Windows, dan `TestPlatformNAList` mencegah skip diam-diam. Eksekusi Windows menjadi acceptance F1 #0 yang eksplisit, dengan aturan "kegagalan = perbaikan F1 #0, bukan skip". |

## Pemeriksaan khusus ADR-088..091

| Area | Hasil | Rujukan |
|---|---|---|
| Runner execve mode (b) | Argv = hasil parse classifier untuk satu simple command; tanpa interpreter. Semantik `shell_allow` tidak lagi bergantung pada kesetaraan parser mvdan vs bash (NEW2-15 tertutup). Karakter kontrol, CR, dan NUL tidak pernah cocok. `shell_allow` hanya untuk one-shot (amandemen butir 5), sehingga eligibility sesi persisten dihapus. Celah kecil: perilaku mode (b) untuk perintah ber-secret-ref yang mengandung ekspansi/glob belum ditetapkan (NEW3-03, MINOR). | docs/EXEC-AND-CODING.md:61-66, :314-320 |
| Identitas "binary yang sama" | Syaratnya path absolut sama **dan** (dev, inode, size, mtime) sama dengan saat sesi mulai. Penggantian binary (inode baru) terdeteksi. Penimpaan in-place yang menjaga size/mtime butuh tulis ke path di luar workspace. Itu hanya mungkin lewat perintah yang sudah di-approve, jadi masuk residu TOCTOU yang tercatat (TRUST-MODEL §3). Entri `PATH` sesi di dalam root (mis. `node_modules/.bin`) belum dilarang secara eksplisit; syarat 5 (tidak ada tulis tanpa approval) memitigasinya (NEW3-04, MINOR). | docs/EXEC-AND-CODING.md:65, :314 |
| Windows PATH/`.exe` | Hanya argv0 yang di-resolve ke `.exe` lewat `PATH` sesi. cwd tidak pernah dicari; `.bat`/`.cmd` tidak pernah cocok. `CreateProcess` tanpa `cmd.exe`. Ini menutup jalur `go.bat` di cwd. | docs/EXEC-AND-CODING.md:66 |
| `execute_code` auto | Opt-in eksplisit (`code_auto_languages = []`) plus syarat trust/taint/tulis. Untuk bahasa shell, aturan `shell` berlaku. Lihat NEW3-02. | SPEC.md:2591 |
| Gerbang trust LSP | Lihat NEW2-04. | — |
| Pemetaan error LockFileEx | `ERROR_LOCK_VIOLATION` → `ErrDBBusy` sudah benar. `ERROR_IO_PENDING` → `ErrDBBusy` tidak bisa terjadi, karena `os.OpenFile`/`os.Root.OpenFile` tanpa `O_FILE_FLAG_OVERLAPPED` menghasilkan handle sinkron (Go 1.26.9 `os/file_windows.go:166`); pemetaannya tidak berbahaya. Error lain → error nyata, sesuai NEW2-23 untuk Windows. Pelepasan lock lewat `Close` saja: lihat NEW3-01. | internal/ctxdb/lock_windows.go |

## Temuan baru (semua MINOR)

| ID | Severity | Area | Temuan | Bukti | Rekomendasi |
|---|---|---|---|---|---|
| NEW3-01 | MINOR | ctxdb Windows (F1 #0) | `unlockFile` hanya menutup handle. Dokumentasi Win32 LockFileEx menyatakan lock milik handle yang ditutup memang dilepas OS, tetapi waktunya "depends upon available system resources". `TestFileLockSemantics` (lock eksklusif segera setelah close) dan probe fairness ADR-087 bisa flaky di runner Windows. [INFERENCE: belum dieksekusi di Windows] | internal/ctxdb/lock_windows.go (`unlockFile`); internal/ctxdb/lock_test.go | Panggil `windows.UnlockFileEx` sebelum `Close` di `unlockFile`. Tutup bersama acceptance matrix Windows F1 #0. |
| NEW3-02 | MINOR | `execute_code` headless (F5) | Headless auto untuk bahasa di `code_auto_languages` hanya butuh `--approval auto`, tanpa flag setara `--unsafe-auto-shell`. Padahal kode Python/JS bisa menjalankan subprocess dan membaca file tanpa sandbox fs. Gerbangnya lebih lemah dari shell, walau tetap opt-in config eksplisit. | SPEC.md:2591; docs/DECISIONS.md:2205 | Sebelum F5: mensyaratkan `--unsafe-auto-shell` (atau flag `--unsafe-auto-code`) untuk auto headless, atau mencatatnya secara eksplisit sebagai residu di TRUST-MODEL §3. |
| NEW3-03 | MINOR | Runner mode (b) untuk secret ref (F1 `exec`) | Mode (b) mengeksekusi argv hasil parse tanpa ekspansi. Belum ditetapkan apa yang terjadi bila perintah ber-argv0 ∈ `commands` mengandung glob/`~`/ekspansi: ditolak, `unknown`, atau dijalankan literal tanpa ekspansi (yang mengubah semantik dibanding bash). | docs/EXEC-AND-CODING.md:63, :337 | Errata sebelum `exec`: perintah ber-secret-ref yang bukan simple command literal → jalankan mode (a) **tanpa** secret, dengan notice, atau tolak (`invalid_args`). Tambah kasus ke `TestSecretRefScope`. |
| NEW3-04 | MINOR | `shell_allow` dan entri `PATH` di dalam root (F1 `exec`) | Resolusi argv0 lewat `PATH` sesi tidak melarang entri `PATH` yang berada di dalam root (mis. `./node_modules/.bin`, `bin/`). Binary di sana bisa ditulis oleh tool kode. Syarat 5 dan cek identitas (inode/mtime) memitigasi, tetapi aturan eksplisit belum ada. | docs/EXEC-AND-CODING.md:65, :314 | Errata sebelum `exec`: argv0 `shell_allow` yang di-resolve ke path di dalam root (atau di bawah entri `PATH` relatif) tidak pernah cocok. Tambah kasus ke `TestShellAllowExactMatch`. |

## Area yang diperiksa tanpa temuan

- Penerapan teks ADR-088..091 di SPEC §5.1, §13.3, §14.1, §14.2, §18.2; EXEC §A.2, §A.6, §A.8, §B, §D; CODE-TOOLS §6; TRUST-MODEL; ROADMAP F1/F5 dan F1 #0. Test bernama berikut tercantum di ROADMAP: `TestShellNoAllowSession`, `TestSecretRefScope`, `TestShellAllowExactMatch`, `TestExecuteCodeApproval`, `TestLSPWorkspaceTrustGate`, `TestFileLockSemantics`.
- Opsi **edit** di §18.2 memvalidasi ulang perintah hasil edit lewat schema dan classifier, lalu menjalankannya sekali. Ini bukan jalur auto.
- ADR-091 memberi fase pada setiap NEW2-07..27. Yang ditutup sekarang adalah NEW2-12, 13, 15, dan 25, dan teksnya ada.
- LOC: ctxdb 2.857 ≤ 3.100 (ADR-087). Tidak ada dependency baru (`golang.org/x/sys/windows` sudah ada di `deps.allow`).
- Hasil perintah: `make check` EXIT 0, vet-cross 4/4, `TestFileLockSemantics` PASS di linux, dan build test windows ter-compile.

## Catatan untuk F1

Gate F0 → F1 terpenuhi. Yang tetap mengikat di F1:
- Acceptance Deliverable F1 #0: matrix Windows ctxdb (ADR-090) plus NEW3-01.
- MINOR backlog ADR-078/086/091 yang berfase F1, plus NEW3-03 dan NEW3-04 (sebelum `internal/exec`).
- Smoke LLM F1 (ADR-062/075/085).
