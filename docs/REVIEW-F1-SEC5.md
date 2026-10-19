# REVIEW-F1-SEC5 — Review keamanan exec stack (#39–#63)

**Status: lengkap.** Review independen penuh atas stack exec. Reviewer tidak menulis kode yang direview; kode tidak diubah. Tanggal: 2026-10-19.

Head yang direview (semua branch bertumpuk, belum di-merge): `#39` `5d3caf0`, `#40` `edc2ad5`, `#42` `c2084a0`, `#45` `7776696`, `#47` `75d53e7`, `#52` `682e5bb`, `#62` `d0f94a8`, `#63` `0ec101b`. Pemeriksaan runtime dilakukan di Linux (worktree terpisah); Windows/macOS dibaca statis dan di-cross-build (`GOOS=windows`, `GOOS=darwin`, `CGO_ENABLED=0`: hijau).

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #39 | lapisan proses per OS, PTY, Job Object, capture | **approve** (perbaikan EX39-1/39-2 terverifikasi; EX5-6 test quality) |
| #40 | `Runner`, timeout/cancel, kill order, marker | **changes required** (EX5-2) |
| #42 | env allowlist, secret ref, mode one-shot | **approve** (EX42-1/2/3/4 terverifikasi; EX5-5 MINOR) |
| #45 | deteksi prompt, idle timeout | **approve** (EX45-1 terverifikasi) |
| #47 | pipeline ringkasan §A.3 | **approve** (EX47-1/47-2 terverifikasi) |
| #52 | startup sweep, `TestNoOrphans`, benchmark | **approve** |
| #62 | ConPTY | **approve** (statis; runtime Windows tidak dijalankan) |
| #63 | sesi shell persisten §A.2 | **changes required — BLOCKER** (EX5-1; EX5-3, EX5-9) |

Ringkasan: **1 BLOCKER, 3 MAJOR, 5 MINOR.** Temuan MAJOR/MINOR lama EX39-1, EX39-2, EX42-1, EX42-2/EX62-1, EX45-1, EX47-1, EX47-2, EX52-1 sudah diperbaiki di head masing-masing; verifikasi ada di bawah.

## Temuan

`confirmed` = dijalankan/dibuktikan dengan repro; `static` = dibuktikan dari kode, belum dijalankan di OS tersebut.

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| EX5-1 | #63 | **BLOCKER** | confirmed (Linux) | `stdin` pada sesi ditulis ke input shell, bukan dibatasi ke perintah; sisa baris dieksekusi shell sebagai perintah baru tanpa approval terpisah | `session.go:78`, `:191-210`; `session_unix.go:38-40`; `session_windows.go:45-46` |
| EX5-2 | #40 (+#63) | MAJOR | confirmed (Linux) | `Runner.Close` menggantung bila `Stdin` lebih besar dari kapasitas pipe dan proses yang lolos (`env -i setsid`, foreground) memegang ujung baca stdin; writer tidak pernah ditutup di `finish` | `run.go:186-214`, `:405-422` |
| EX5-3 | #63 | MAJOR | static (Windows) | Sentinel sesi `cmd` menggemakan `%cd%` tanpa quoting; nama direktori ber-`&` menjalankan perintah `cmd` tambahan tanpa approval | `session_windows.go:37-41` |
| EX5-9 | #63 | MAJOR | confirmed (`-race`) | `session.killed` tidak disinkronkan: `Sessions.Close`/`drop` dan jalur timeout/cancel `wait` memanggil `kill()` bersamaan; dua `finish()` dapat berjalan dan salah satunya memblokir `<-c.exit`, menggantung shutdown | `session.go:148`, `:242`, `:371-380` |
| EX5-4 | #42 | MINOR | static | `withinRoot` memakai `filepath.Rel` leksikal (peka huruf pada Windows) dan tidak me-resolve symlink; `Root == ""` melewati pemeriksaan sepenuhnya (fail-open) | `env.go:266-292`; `oneshot.go:22-26`, `:86-90` |
| EX5-5 | #39 | MINOR | confirmed | `TestRunPTYEscapeeDoesNotHang` me-redirect stdout/stderr escapee ke `/dev/null`, sehingga escapee **tidak** memegang slave PTY; test tidak mereproduksi EX39-1 | `run_unix_test.go:217-218` |
| EX5-6 | #63 | MINOR | static | `stripFrames` membuang setiap output berbentuk frame (32 hex + kode), sehingga output perintah dapat menyembunyikan teksnya sendiri dari model/UI | `session.go:295-331` |
| EX5-7 | #63 | MINOR | static | file perintah sesi ditulis dengan `os.WriteFile` (mengikuti symlink) ke nama `cN.<ext>` yang dapat diprediksi; symlink yang ditanam perintah sebelumnya menimpa file di luar workspace | `session.go:199` |
| EX5-8 | #39 | MINOR | static | `Run` memercayai `Spec.Direct` tanpa memvalidasi bahwa argv berasal dari `classify.DirectArgv`; kontrak mode (b) hanya didokumentasikan | `oneshot.go:42-70`; `exec.go` `Spec` |

### EX5-1 (BLOCKER) — stdin sesi menjadi perintah shell

`Sessions.Exec` menerima `stdin` dan `session` sekaligus (SPEC §5.1/EXEC §A.2 tidak melarang kombinasinya). `run` menulis baris perintah (`. <file>; printf sentinel`) lalu menulis `stdin` mentah ke master PTY/pipe yang sama. Jika perintah yang di-approve tidak membaca stdin, shell membaca sisa byte itu sebagai baris perintah berikutnya.

Repro (Linux, dijalankan; status `exited`, lalu file muncul):

```go
ss := r.NewSessions(dir, env)
ss.Exec(ctx, "s", "true", []byte("touch /tmp/pwned\n"), 5*time.Second)
// 200 ms kemudian: /tmp/pwned ada
```

Perintah `touch` berjalan tanpa approval kedua; preview hanya menunjukkan `true` (+ `stdin` sebagai input perintah). Ini melanggar definisi vulnerability ADR-079 butir 8(a): perintah shell berjalan tanpa approval eksplisit. Di Windows dampaknya sama (stdin pipe dibaca `cmd`/`pwsh` sebagai baris berikutnya).

**Rekomendasi (wajib):** tolak `stdin` non-kosong pada `Sessions.Exec` (`ErrSpec`, `invalid_args` di tool layer), atau kirim stdin lewat file dan redirect yang terikat hanya pada perintah. Tambah regression test `TestSessionStdinIsNotShellInput` (assert file tidak dibuat + output tidak berisi efek).

### EX5-2 (MAJOR) — `Runner.Close` menggantung pada stdin holder yang lolos

`spawn` membuat `inW` dan menjalankan writer, tetapi `call` tidak menyimpan `inW`; `finish` hanya menutup output. Bila writer memblokir di `Write` (pipe penuh) dan proses yang memegang ujung baca lolos dari pgid + kehilangan marker, `g.Wait()` di `Runner.Close` menunggu selamanya. `Run` sendiri kembali (P24.2 per-call tetap), tetapi `Close`/shutdown menggantung dan goroutine bocor.

Repro (dijalankan): `Stdin` 1 MiB, `PTY:false`, `sh -c 'env -i setsid sleep 9973'`, `Timeout:1s`. `Run` kembali `timeout`; `Close` tidak kembali >4 s. Pada Linux skema `stdin` ≤ 65.536 B menyamai kapasitas pipe (64 KiB) sehingga jalur schema tidak selalu memblokir; pada macOS kapasitas pipe lebih kecil dari 65.536 B `[INFERENCE]`, jadi batas schema dapat memicunya. API `Spec.Stdin` sendiri tidak membatasi ukuran.

**Rekomendasi:** simpan `inW` di `call`, tutup di `finish` dan di jalur `spawn` gagal; writer men-select `r.stop`/`ctx` dan berhenti. Tambah test `Close` dengan escapee pemegang stdin.

### EX5-3 (MAJOR) — injeksi cwd pada sentinel sesi `cmd`

`session_windows.go:40` membentuk `echo \x1eDB:<nonce>:<code>:%cd%\x1e`. Ekspansi `%cd%` terjadi sebelum `cmd` mem-parse baris, sehingga `&`, `|`, `<`, `>` di nama direktori menjadi operator. Direktori `x&calc.exe&rem` (valid di NTFS) membuat sentinel menjalankan `calc.exe` saat perintah sesi berikutnya selesai — tanpa approval perintah itu. Nama direktori berasal dari repo (mis. `cd sub` yang di-approve), jadi model/prompt-injection dapat memicunya.

**Rekomendasi:** jangan menggemakan `%cd%`; tulis cwd ke file terpisah dengan mekanisme yang tidak di-parse ulang (mis. delayed expansion yang tidak me-reparse nilai, atau minta shell menulis `cd > temp` dengan path tetap), dan tolak cwd yang memuat metakarakter `cmd` sampai ada jalur aman. Tambah test cwd `a&calc&rem` di runner Windows.

### EX5-9 (MAJOR) — data race pada teardown sesi

`session.kill` memeriksa/menulis `s.killed` tanpa mutex (`session.go:371-380`). `Sessions.Close`/`drop` memanggilnya tanpa memegang `s.mu` (`:110`, `:148`), sedangkan `session.wait` memanggilnya pada `ctx.Done()` (`:242`). Pada shutdown, call sesi yang sedang berjalan dan/atau dibatalkan dapat memanggil `kill()` dua kali. `go test -race` dengan `Sessions.Close` konkuren + cancel melaporkan `WARNING: DATA RACE` (read `session.go:372` vs write `:375`). Akibatnya dua `finish()` dapat berjalan; yang kedua dapat memblokir `<-c.exit` (`run.go:409-411`), sehingga `Close` menggantung.

**Rekomendasi:** lindungi `killed` (dan seluruh teardown) dengan `s.mu` atau `sync.Once`; jangan panggil `kill` dari `drop` tanpa mengambil `s.mu`/menunggu command selesai. Tambah test race `Close` + cancel.

### MINOR

- **EX5-4:** `withinRoot`/`allowedBinary` membandingkan string path. Pada Windows, root `C:\ws` dan PATH `C:\WS\bin` tidak dianggap "di dalam root" (`filepath.Rel` peka huruf), dan symlink di luar root ke binary di dalam root juga lolos; `OneShot.Root == ""` mematikan pemeriksaan. Secret ref dapat mencapai binary yang dikontrol workspace pada kombinasi ini. Rekomendasi: `EvalSymlinks` + perbandingan case-insensitive per komponen; wajibkan `Root` (fail-closed) untuk injeksi secret.
- **EX5-5:** test regresi EX39-1 tidak memegang slave. Reviewer menguji bentuk asli (`env -i setsid sleep … &` tanpa redirection): `leader-exits` 360 ms, `timeout` 1,27 s, `Close` saat Run hidup 0,59 s — perbaikan `pollableMaster` benar. Test perlu diperbaiki agar mengunci regresi.
- **EX5-6:** `stripFrames` menyapu frame valid tanpa nonce; perintah dapat membungkus output dengan frame palsu dan menyembunyikannya dari model/UI. Batasi ke nonce yang dikenal (nonce perintah saat ini + sebelumnya).
- **EX5-7:** gunakan `O_CREATE|O_EXCL|O_NOFOLLOW` (atau `openat` relatif ke dir sesi) untuk file perintah.
- **EX5-8:** validasi `Direct` di `Prepare` (atau dokumentasikan sebagai kontrak yang harus dipenuhi caller dan tambah assert di lapisan tool).

## Diperiksa dan bersih

- **PTY pollable (EX39-1):** `unix.Dup` setelah `Fd()` terakhir, `SetNonblock`, `os.NewFile`; `Close` membatalkan `Read`. Diverifikasi dengan escapee yang benar-benar memegang slave, termasuk `Runner.Close` saat call hidup (0 hang). `TestProcPTYCloseUnblocksRead` lulus.
- **Leader tersuspend (EX39-2):** `proc_windows.go:98-104` memanggil `p.Kill()` sebelum `p.Wait()` pada kegagalan attach. Lulus cross-build; runtime Windows tidak dijalankan.
- **Secret ref (EX42-1):** injeksi hanya untuk argv0 tanpa separator yang di-resolve ke binary yang identitasnya (path absolut + dev/inode/size/mtime) sama dengan command scope dan di luar root; `TestSecretRefScope` lulus untuk bare/absolute/`./env`/workspace PATH shadow. MINOR EX5-4 di atas.
- **argv0 relatif (EX42-2/EX62-1):** `lookPath` mengembalikan path absolut yang di-clean; `Dir` dibuat absolut; ConPTY `CreateProcess` menerima path absolut. `TestLookPath` lulus.
- **Env allowlist (EX42-4):** nama persis di Unix; `Path`/`Home`/`Http_Proxy` tidak diwarisi; `SSH_AUTH_SOCK` hanya dengan `pass_ssh_agent`; proxy userinfo dibuang; `GIT_TERMINAL_PROMPT=0`, marker, dan `DAMNBOT=1` keras. **Konfirmasi:** `http_proxy` huruf kecil sekarang dibuang; ini konsisten dengan GAP-SEC-02 ("nama persis") dan lebih ketat — bukan regresi keamanan. Konsekuensi fungsional (curl hanya membaca `http_proxy`) dicatat, bukan temuan.
- **Prompt (EX45-1):** `Prompt` di-redaksi dengan redactor sesi dan hanya rune printable + tab/newline; `TestPromptRedactedAndSanitized` lulus.
- **Ringkasan (EX47-1/47-2):** escape tanpa terminator dibatasi `maxEscape`, C0/C1 dibuang, blob mentah memakai bentuk ANSI-strip yang di-redaksi ulang; `TestSummarizeUnterminatedEscape`, `TestSummarizeSplitSecretNotLeaked` lulus.
- **Penahanan:** kill order TERM → grace 1–3 s → KILL → sweep marker; `waitDead` + `drainAfterExit` 250 ms; `TestRunStragglersKilled`, `TestRunSetsidEscapeSwept`, `TestRunTimeoutIgnoresTERM` lulus. Escapee yang menghapus marker + keluar pgid (`env -i … setsid`, sudo, `systemd-run`, at/cron) tetap residu dan sudah didokumentasikan di TRUST-MODEL §3 dan EXEC §A.4 (EX39-3/EX40-1 ditutup).
- **Cancel:** hasil ≤ ~1,3 s (`cancelled_confirmed`/`still_running`), kill berlanjut di background; `TestRunCancelConfirmed` lulus.
- **Capture:** head 8 MiB + ring 8 MiB, `BytesRaw` total, tidak ada alokasi per chunk; test capture lulus.
- **Job Object (statis):** `KILL_ON_JOB_CLOSE`, tanpa `BREAKAWAY_OK`, `CREATE_SUSPENDED` → assign → resume, `TerminateJobObject`; handle creation ConPTY dipertahankan sampai `CreateProcess` (fix EX62-1) dan ditutup setelahnya; `bInheritHandles=false`.
- **Sesi (selain temuan):** batas 4 sesi, nama tervalidasi, dir 0700/file 0600, alias/line-editing mati, sentinel nonce 128-bit per perintah, timeout SIGINT → 3 s → kill/recreate, env sesi tanpa secret (`TestSessionSecretRefScope`), `SessionReset`, `TestSessionTimeout`, `TestSessionAliasesOff`, `TestSessionPersistentState` lulus.
- **Suite:** `go test ./internal/exec` target (PTY, env, secret, prompt, summarize, session, cancel, straggler) hijau; `-race` dengan `Sessions.Close` konkuren menemukan EX5-9 (repro terpisah, bukan bagian suite); cross-build Windows/darwin/linux hijau. `TestNoOrphans` 200 iterasi dan `TestInteractiveCorpus` tidak dijalankan lokal (dibiarkan ke CI sesuai batas CPU).

## Tidak dicakup

- Runtime Windows dan macOS (ConPTY, Job Object, wmic/schtasks, `%cd%`) — hanya statis/cross-build. EX5-3 perlu repro di runner Windows.
- Stress race lebih luas (Close vs semua jalur sesi) — hanya satu skenario `-race` yang dijalankan.
- `TestNoOrphans` penuh, `TestInteractiveCorpus` 40×2, fuzz, benchmark, dan stress konkurensi (batas CPU malam ini).
- Integrasi lapisan tool/approval (`internal/modules/exec`, dispatch) belum ada di stack ini; `Decide`/`MatchAllow` dipakai lewat test saja. Temuan EX5-1/EX5-3 akan menjadi vulnerability penuh hanya bila tool layer mengizinkan kombinasi parameter tersebut (schema saat ini mengizinkan `session` + `stdin`).
- TOCTOU preview→eksekusi dan perubahan PATH/filesystem antara cek dan spawn tetap residu TRUST-MODEL §3.
