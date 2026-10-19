# REVIEW-F1-SEC4 — Review keamanan exec + exec/classify

**PARTIAL — review belum selesai.** Review dihentikan karena proyek dijeda oleh pemilik (2026-10-19). Belum ada PR exec/classify yang boleh di-merge berdasarkan dokumen ini.

Reviewer independen. Kode yang direview tidak diubah.

## Cakupan

| PR | Head | Status review |
|---|---|---|
| #39 exec process/PTY/Job Object | `5a9e9e8` | sebagian (Linux), **changes required** |
| #40 Runner | `6e508ff` | sebagian, **changes required** |
| #42 env + secret ref + one-shot | `afc32ff` | sebagian, **changes required** |
| #45 deteksi prompt/idle | `18ac833` | sebagian, **changes required** |
| #47 pipeline summary §A.3 | `e0c5c95` | sebagian, **changes required** |
| #52 SweepStale/TestNoOrphans | `ea6e8b5` | sebagian, **changes required** (MINOR saja) |
| #62 ConPTY | `31320bf` | statis saja, **changes required** |
| #41, #43, #44, #46 classify (shell_allow, policy, lexer Windows) | `d02bf38` (top) | **BELUM direview**: reviewer dihentikan sebelum ada hasil |
| Sesi persisten §A.2 | — | PR belum ada |
| #28 netx DLP @ `8ed7e04` (sign-off) | `8ed7e04` | oracle parsial (lihat bawah); **belum sign-off** |

## Temuan exec

Status menunjukkan cara temuan diperoleh: **confirmed** = dijalankan atau dibuktikan di kode; **suspected** = dari pembacaan kode, belum dieksploitasi.

| ID | PR | Severity | Status | Temuan | Bukti | Rekomendasi |
|---|---|---|---|---|---|---|
| EX39-1 | #39/#40 | MAJOR (melanggar P24 "0 hang") | confirmed | Runner PTY menggantung: `Run` tidak pernah kembali selama proses di luar pohon (`env -i setsid … &`, `sudo daemon &`) memegang slave PTY. Master creack/pty adalah fd blocking di luar poller Go, sehingga `closeOutput` tidak membuka blokir `syscall.Read`, dan cancel maupun `Runner.Close` ikut menggantung. Mode pipe tidak terdampak (574 ms). | `internal/exec/proc_unix.go:211-237`; `run.go:383-384`. Repro: `bash -c 'env -i setsid sleep 7003.1 & sleep 0.3; echo hi'` dengan `PTY:true`, `Timeout:5s` masih berjalan setelah lebih dari 10 s. | `unix.SetNonblock` + `os.NewFile` (master yang bisa di-poll), dan beri batas pada penantian `<-c.read`. Tambah test untuk slave yang ditahan proses lain. |
| EX42-1 | #42 | MAJOR | suspected | Secret ref disuntikkan hanya berdasarkan base name argv0. `./gh`, `/dir/gh`, atau `gh` yang di-resolve ke entri PATH absolut di dalam workspace (`.venv/bin`, `node_modules/.bin`) ikut menerima secret. Preview hanya menampilkan `gh …`, sehingga user tidak bisa membedakannya. | `env.go:242-249`; `oneshot.go:39-62`, `:97-119` | Suntikkan secret hanya bila argv0 tanpa separator dan path ter-resolve berada di luar root. Selain itu jalankan tanpa secret dan beri notice. Tambah kasus ke `TestSecretRefScope`. |
| EX42-2 / EX62-1 | #42, #62 | MAJOR | suspected | argv0 mode (b) yang mengandung separator dikembalikan relatif, padahal ADR-088 mensyaratkan absolut. ConPTY `CreateProcess` me-resolve path relatif terhadap cwd damnbot, bukan `Dir`, sehingga binary yang dijalankan berbeda dari yang dicek dan di-approve. | `oneshot.go:98-106`; `conpty_windows.go:82`, `:102` | Kembalikan path absolut yang sudah di-clean, dan tolak `Dir` relatif. |
| EX45-1 | #45 | MAJOR | suspected | `Result.Prompt` berisi output mentah target tanpa redaksi dan tanpa strip C0. Contoh: `printf "$TOKEN:"` → `Prompt` berisi secret, lalu dikirim ke model/UI. | `run.go:269-292`; `prompt.go:63-71` | Redaksi dan strip karakter kontrol di Summarize, atau wajibkan dan test redaksi di sisi pemanggil. |
| EX39-2 | #39 | MINOR | suspected | Windows: bila attach/resume gagal, `p.Wait()` dipanggil sebelum `Kill` pada proses yang masih suspended, sehingga spawn menggantung. | `proc_windows.go:69-72` | Kill dulu, lalu Wait. |
| EX39-3 | #39 | MINOR | confirmed | Dokumen penahanan usang. TRUST-MODEL §3 masih menyebut `cgroup.kill`, padahal implementasi tidak memakai cgroup. Residual escape lewat `env -i`+`setsid`, sudo, `systemd-run --user`, at/cron, dan WMI/schtasks belum tercatat. EXEC §A.4 belum diamandemen sesuai ADR-105. | `docs/TRUST-MODEL.md:41`; `docs/EXEC-AND-CODING.md:151`, `:158` | Perbarui baris residual dan tambahkan catatan amandemen. |
| EX40-1 | #40 | MINOR | confirmed | `systemd-run --user sleep` bertahan setelah call selesai; pgid maupun sweep marker tidak menangkapnya. | `run.go:369-401` | Dokumentasikan sebagai residual. |
| EX42-3 | #42 | MINOR | confirmed | Runner tidak menolak sendiri karakter kontrol, CR, atau NUL di argv mode (b); sepenuhnya bergantung pada classify. | `oneshot.go:39-56` | Tambahkan lapisan pertahanan. |
| EX42-4 | #42 | MINOR | confirmed | Allowlist env dicocokkan case-insensitive di Unix (`strings.ToUpper`), padahal GAP-SEC-02 menyebut nama persis. Tidak ditemukan kebocoran secret. | `env.go:110-131` | Gunakan nama persis. Tambahkan peringatan doctor untuk `LD_*`, `BASH_ENV`, dan sejenisnya di `env_allow`. |
| EX47-1 | #47 | MINOR | suspected | Satu OSC/DCS/CSI tanpa terminator menyembunyikan seluruh output sesudahnya dari model, sehingga target bisa menyembunyikan output. C0 lain tetap lolos. | `prompt.go:99-121`; `summarize.go:113-114` | Batasi panjang sequence tanpa terminator, dan buang sisa C0/C1. |
| EX47-2 | #47 | MINOR | suspected | Blob mentah diredaksi sebelum ANSI di-strip, sehingga secret yang dipisah kode SGR lolos redaksi. | `summarize.go:69` | Redaksi juga hasil strip, atau tandai blob sebagai tidak bisa dibaca model. |
| EX52-1 | #52 | MINOR | confirmed | `TestNoOrphans` hanya mengukur proses yang membawa marker. | `p24_unix_test.go:15-18`, `:44-76` | Dokumentasikan (lihat EX39-3). |

Sudah diperiksa dan benar:
- **Penahanan:** Setsid+Pdeathsig berjalan. 0 survivor untuk `setsid &`, `nohup` double-fork, trap TERM, `disown`, daemon yang menutup fd, `setpgid`, migrator fork-loop, dan `at`. Urutan kill TERM → grace 1–3 s → KILL ke `-pgid`.
- **Job Object:** `KILL_ON_JOB_CLOSE` tanpa `BREAKAWAY_OK`, dengan urutan `CREATE_SUSPENDED` → Assign → Resume.
- **Env deny-by-default:** `LD_PRELOAD`, `DYLD_*`, `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `SHELLOPTS`, `PS4`, `IFS`, `GIT_*`, `NODE_OPTIONS`, dan sejenisnya tidak diwarisi.
- **Mode (a):** tidak pernah menerima secret. pwsh berjalan dengan `-NoProfile -NonInteractive`; cmd dengan `/d`.
- **ConPTY:** `bInheritHandles=false`; handle ditutup.
- **Capture:** memori terbatas (8 MiB + 8 MiB).
- **Test:** `go test -race ./internal/exec/...` ok; `TestNoOrphans` `-count=3` ok; vet dan test-c untuk Windows/darwin ok.

Belum dicakup untuk exec:
- Eksekusi runtime di Windows/macOS (EX39-2, EX42-2, EX62-1 belum diuji di sana).
- Pengukuran waktu P24 secara empiris.
- Benchmark summary untuk input GiB/biner.
- Redaksi `OnOutput`/UI di luar `internal/exec`.
- Mutation testing.
- Apakah master PTY di darwin pollable.

## #28 netx DLP @ `8ed7e04` (parsial)

Oracle diferensial vs `main` dan vs `d2ee9d5` mencakup 490.000 input di 12 kelompok, termasuk splitter `%0A`/`%2E`/`%40`/`\u` untuk 65d50f7 dan batas join untuk 33f52b7. Hasilnya **0 regresi, 0 miss, 0 FP**. Ketiga bypass NETX-26 tetap diblok.

Belum selesai:
- `FuzzDiff` baru berjalan 81 s dari target 4 menit.
- `BenchmarkDLPScan` pinned dan `go test -race` belum dijalankan.
- CI saat dicek: job test (windows-11-arm, ubuntu-24.04-arm, macos-latest), `test-timing`, `coverage`, dan `pr-meta` **gagal**; `perf` hijau. Penyebab kegagalan belum diperiksa.

Temuan MINOR: tiga mutan lolos suite netx milik PR sendiri (`secretByte` tanpa lowercase, tanpa `plusSpans`, jendela dibagi dua). Tambahkan test secret ber-`+` dan secret yang dipisah `%41`.

**Belum sign-off.**

## Tindak lanjut saat proyek dilanjutkan

1. Review penuh stack classify #41/#43/#44/#46 (jalur auto tunggal `shell_allow`; policy; lexer cmd/pwsh).
2. Verifikasi perbaikan EX39-1, EX42-1, EX42-2/EX62-1, dan EX45-1.
3. Selesaikan sign-off #28: fuzz, race, dan penyebab CI merah.
4. Review sesi persisten §A.2 setelah PR-nya dibuka.
