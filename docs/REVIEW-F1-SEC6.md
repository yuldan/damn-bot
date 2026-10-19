# REVIEW-F1-SEC6 — Review keamanan `internal/exec/classify` (#41–#46)

**Status: lengkap.** Review independen pertama atas stack classify (sebelumnya belum direview; REVIEW-F1-SEC4 berhenti sebelum mulai). Reviewer tidak menulis kode yang direview. Tanggal: 2026-10-19.

Head yang direview: `#41` `11e5cfc`, `#43` `affd0e9`, `#44` `fa9f41c`, `#46` `d02bf38`. `go test ./internal/exec/classify` (seluruh paket) hijau; CI keempat PR hijau saat review. Semua eksekusi probe di Linux; dialek Windows dinilai lewat `Dialect=Pwsh/Cmd` deterministik (tanpa proses Windows).

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #41 | API types, label operand path, `Decide` | **approve** (CL5-4/5/6 MINOR) |
| #43 | parser bash/POSIX, walk, label simple command | **approve** (CL5-3 MINOR) |
| #44 | tabel label, wrapper, rekursi interpreter | **changes required** (CL5-2) |
| #46 | HTTP/remote, lexer PowerShell/cmd, `DirectArgv`/`MatchAllow`, acceptance | **changes required** (CL5-1) |

Tidak ada BLOCKER: model ADR-079 "classifier tidak pernah auto" **terbukti berlaku** (lihat "Diperiksa dan bersih"). Dua MAJOR di bawah adalah bypass label yang hanya menjadi eksekusi tanpa approval pada jalur `--unsafe-auto-shell` (yang secara eksplisit tetap menolak `destructive`/`unknown`).

## Temuan

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| CL5-1 | #46 | MAJOR | confirmed (classifier) | `cmd /c …` dan `cmd.exe /c …` tidak direkursi; perintah di dalamnya tidak dilabeli. `cmd /c rd /s /q C:\proj` (root workspace) = `mutating` → `AutoApprove` di mode `--unsafe-auto-shell`, sedangkan `rd /s /q C:\proj` = `destructive` → `denied` | `winlex.go:20-45`, `:60-80`; `wrap.go:16-20`; `command.go:21-60` |
| CL5-2 | #44 | MAJOR | confirmed (classifier) | Launcher yang tidak ada di `wrappers` tidak direkursi: `setsid`, `busybox sh -c`, `unshare`, `systemd-run`, `taskset`, `chroot` (sebagian tertangkap `outside_read` kebetulan). `setsid sh -c 'rm -rf ~'` = `mutating` → auto di `--unsafe-auto-shell`; `sh -c 'rm -rf ~'` = `destructive` | `wrap.go:16-18`, `:38-79`, `:131-180` |
| CL5-3 | #43 | MINOR | confirmed | `export LD_PRELOAD=/tmp/x; ls` = `mutating`, sedangkan `LD_PRELOAD=/tmp/x ls` dan `env LD_PRELOAD=…` = `unknown`. `riskyEnv` hanya memeriksa `CallExpr.Assigns` dan wrapper `env`, tidak argumen `export`/`declare`/`set` | `walk.go:139-170` |
| CL5-4 | #44 | MINOR | confirmed | `python -mpip install x` (bentuk tanpa spasi, valid) = `mutating`; `python -m pip install x` = `irreversible`. `uv pip install x` = `mutating` + `network`; `pip install x` = `irreversible`. Aturan "pip install di luar venv → irreversible" (ADR-106 butir 8) tidak konsisten | `wrap.go:145-170`; `rules.go` `networkKeys` |
| CL5-5 | #41 | MINOR | confirmed | `Decide.headless` mengembalikan `Denied` (bukan `OutsideWorkspace`) bila `outside_read` menyertai kelas non-`mutating`; SPEC §14.1 baris `outside_read` menyatakan headless → `outside_workspace`. Fail-closed, tetapi kode ≠ dokumen | `policy.go:88-103` |
| CL5-6 | #41 | MINOR | confirmed | Verify di mode `--unsafe-auto-shell` yang **tidak** ada di `shell_allow` mendapat `AutoApprove`; ADR-079 butir 5 berbunyi "auto hanya bila perintah verify persis ada di `shell_allow`". Test mengunci perilaku auto ini | `policy.go:88-96`; `policy_test.go:196-215` |
| CL5-7 | #41 | MINOR | confirmed | `sort -o /etc/passwd in` dilabeli `outside_read` (bukan tulis/destructive): `sort` tidak ada di `readCmds`/`writeCmds`, dan nilai `-o` diperlakukan sebagai operand baca. Efek tetap diblokir headless karena `outside_read` | `command.go:73-111`; `paths.go:15-45` |
| CL5-8 | #46 | MINOR | confirmed | Metadata host non-dotted (`curl http://2852039166/`, oktal/hex/IPv4-mapped) = `mutating` + `network`, bukan `irreversible`; auto di `--unsafe-auto-shell` | `http.go:84-104`, `:120-137` |

### CL5-1 — `cmd /c` membatalkan label destructive (Windows)

Probe (deterministik, `Dialect=Cmd`, `Roots=["C:\\proj"]`):

```
cmd /c rd /s /q C:\proj   => mutating   flags=[]        -> Decide(unsafe) = auto
rd /s /q C:\proj          => destructive                -> Decide(unsafe) = denied
cmd /c diskpart           => mutating
cmd /c shutdown /s        => mutating
cmd.exe /c rd /s /q C:\proj => mutating
```

`cmd` bukan `interpreter`/`wrapper`, dan lexer `Cmd` memperlakukan `/c` sebagai option biasa; seluruh payload menjadi argumen biasa. Karena `cmd` adalah shell default Windows (ADR-105 butir 22), ini bukan konstruksi eksotis. Dampak: di headless `--approval auto --unsafe-auto-shell`, perintah yang menghapus workspace/disk menjalankan tanpa approval, melanggar aturan "destructive … tetap `denied`" (ADR-079 butir 4).

**Rekomendasi:** perlakukan `cmd`/`cmd.exe` (dan `command.com`) sebagai interpreter: `cmd /c <literal>` / `/k` direkursi dengan dialek `Cmd`, bentuk non-literal → `unknown`; atau naikkan `cmd /c …` ke `unknown` sampai isinya dianalisis. Tambah kasus `cmd /c rd /s /q <root>` ke `TestDialects`/`TestUnsafeAutoShellFlag`.

### CL5-2 — launcher tak terdaftar membatalkan label destructive (POSIX)

Probe (`Dialect=Bash`, `testEnv()`):

```
setsid sh -c 'rm -rf ~'                    => mutating
busybox sh -c 'rm -rf ~'                   => mutating
unshare -r sh -c 'rm -rf ~'                => mutating
systemd-run --user --scope sh -c 'rm -rf ~' => mutating
sh -c 'rm -rf ~'                           => destructive
```

`wrappers` hanya berisi `env nice nohup timeout stdbuf time command builtin exec xargs npx bunx pnpx uvx pipx`. Launcher standar lain tidak direkursi; payload berupa satu argumen kutipan sehingga tidak terlihat sebagai operand path. Dampak sama dengan CL5-1 pada mode `--unsafe-auto-shell`. `nohup` (yang terdaftar) justru aman.

**Rekomendasi:** tambahkan launcher umum (`setsid`, `busybox`, `unshare`, `chroot`, `taskset`, `systemd-run`, `ionice`, `flock`, `script`, `stdbuf` sudah ada) ke `wrappers` dengan aturan nilai opsi yang benar; untuk yang tidak dapat diurai → `unknown`. Tambah corpus negatif di `TestRuleNesting`.

## Diperiksa dan bersih

- **Tidak ada auto dari classifier:** `Decide` hanya `AutoApprove` lewat `allowAuto` (`AllowMatched` + semua kondisi) atau jalur `--unsafe-auto-shell` untuk kelas `mutating` tanpa flag. `TestShellNoClassifierAuto` (korpus ≥300 × 3 bentuk, `shell_allow` kosong) lulus 0 auto; `TestShellApprovalModel` 36 baris ADR-079 lulus; `TestUnsafeAutoShellFlag` lulus.
- **`shell_allow` argv-persis:** `DirectArgv` menolak seluruh metakarakter/ekspansi/glob/brace/redirect/assignment/heredoc/`time`/`coproc`/negasi/background dan karakter kontrol/non-print (termasuk NBSP, zero-width, bidi). `MatchAllow` membandingkan token persis, cwd di dalam root, argv0 tanpa separator/`:`, identitas binary sama dengan rekaman awal sesi, binary di luar root/cwd, `.bat/.cmd/.ps1/.com` ditolak. `TestShellAllowExactMatch` (≥60 kasus + Windows) lulus. `exec go test …`/`command go test …` hanya cocok bila entry benar-benar memuat token wrapper.
- **Plan mode:** auto hanya bila `shell_allow` cocok **dan** `NonMutating`; `git status` auto, `go test`, `ls`, `git push` ditolak. `TestPlanModeShell` lulus.
- **`deny_paths`/`outside_read`:** lexical + resolved, case-insensitive, glob/symlink lewat resolver; `cat ~/.ssh/id_rsa`, `.env`, `*.pem`, `/proc/*/environ` → `denied_path`; path luar root → `outside_read`/`destructive`. `TestOperandLabels`, `TestSymlinkAndGlobOperands`, `TestCwdRelative` lulus.
- **Label P26:** `git push`/publish/`gh` mutating/`ssh`/`nc` → `irreversible`; `stripe`/`solana`/`gh sponsor` → `monetary`; agent bypass flags → `delegated_agent_bypass`; `sudo`/`doas`/`pkexec`/`su`/`runas` → `privileged` + rekursi; `destructive` (rm root/.git/outside, git filter/reset/clean, mkfs, dd, shred, kill -1) terverifikasi di corpus.
- **Rekursi shell:** `bash|sh|zsh|dash|ksh -c '<literal>'`, `eval '<literal>'`, `su -c`, wrapper `env/nohup/timeout/nice/command/builtin/xargs`, `find -exec`; `$(…)`, backtick, `$'…'`, heredoc/pipe, `source`/`.` → `unknown`; `curl | sh` → `destructive`. `TestRuleNesting`, `TestParseLabels`, `TestNestingLimit` lulus.
- **Windows lexer:** alias PowerShell (`Remove-Item`/`del`/`type`/…), `rd /s /q C:\` destructive, `format D:` destructive + outside_read, `reg delete` destructive, `iwr | iex` destructive, `$env:SECRET` → `references_secret`, `-EncodedCommand` → unknown, `-Command` direkursi, `(…)`/`$(…)`/script block → unknown. `TestDialects` lulus. Celah CL5-1 di luar cakupan test ini.
- **HTTP:** method/body/`-d@file`/`-o` dan metadata dotted + host monetary diberi label; `loopback` tidak network. Clean kecuali CL5-8.
- **Secret:** `references_secret` untuk `$X`/`${X}`/`$env:X`/`%X%`, selalu `unknown`, dan `MatchAllow` tidak mungkin cocok karena `$`/`%` terlarang.
- **Paket murni:** tidak mengimpor paket internal; filesystem hanya lewat `PathResolver`. `BenchmarkClassify` 2 KiB < 1 ms (test lulus; tidak dijalankan bench penuh).

## Tidak dicakup

- Runtime Windows (lexer diuji deterministik, bukan `cmd.exe` nyata) dan macOS.
- Integrasi lapisan tool/approval: `Classify`/`Decide`/`MatchAllow` belum dipanggil kode non-test di stack ini, jadi tidak ada uji end-to-end "model → approval → eksekusi".
- Ekivalensi semantik penuh dengan bash/PowerShell (classifier bersifat best-effort per ADR-079; corpus di luar test tidak diuji).
- Fuzz/bench/perf dan corpus 300 perintah di luar test (test lulus; tidak dijalankan ulang dengan `-count` > 1 sesuai batas CPU).
- Interaksi `--unsafe-auto-shell` dengan tool layer (siapa yang boleh menyalakan flag) — di luar paket.

## Putaran 2 — re-review perbaikan (2026-10-20)

Head yang direview ulang: `#44` `2df9a7e` (`01dcdae`), `#46` `0551086` (`b606e8a`). `#41` `c12dfe9` dan `#43` `909212f` hanya merge main, tanpa perubahan kode baru; verdict putaran 1 tetap.

| PR | Verdict putaran 2 |
|---|---|
| #41 | **approve** (tidak berubah; CL5-5/6/7 MINOR tetap) |
| #43 | **approve** (tidak berubah; CL5-3 MINOR tetap) |
| #44 | **changes required** — CL5-2 inti fixed, tetapi CL5-10 (MAJOR) masih lolos |
| #46 | **changes required** — CL5-1 inti fixed, tetapi CL5-9 (MAJOR) masih lolos |

### Perbaikan inti yang terverifikasi

**CL5-1 (cmd /c) — fixed untuk payload langsung.** `cmdShell` (`winlex.go:237-260`) dipanggil dari `command.go:43-45`, mem-parse `/c`/`/k` dengan lexer Cmd.

| Perintah (Dialect Cmd, root `C:\proj`) | Label |
|---|---|
| `cmd /c rd /s /q C:\proj` | destructive |
| `cmd /s /c "rd /s /q C:\proj"` | destructive |
| `cmd /c diskpart`, `cmd /c shutdown /s /t 0` | destructive |
| `cmd /c cmd /c rd /s /q C:\proj` | destructive |
| `cmd /c powershell -Command "Remove-Item -Recurse C:\proj"` | destructive |

**CL5-2 (launcher) — fixed untuk bentuk uji.** `wrappers` (`wrap.go:18-20`) + `launcherArgs`/`tasksetArgs` (`wrap.go:60-180`).

| Perintah | Label |
|---|---|
| `setsid sh -c 'rm -rf ~'` | destructive |
| `busybox sh -c 'rm -rf ~'` | destructive |
| `taskset -c 0-3 sh -c 'rm -rf ~'` / `taskset 0x3 rm -rf ~` | destructive |
| `unshare -m sh -c 'rm -rf ~'` | destructive |
| `systemd-run rm -rf ~` / `systemd-run --user --scope -- rm -rf ~` | destructive |
| `systemd-run --user --scope rm -rf ~`, `setsid --unknown …`, `busybox --list`, `taskset -p 1234` | unknown (fail-closed) |

`go test ./internal/exec/classify` (seluruh paket) hijau.

### Temuan residual

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| CL5-9 | #46 | MAJOR | confirmed | Builtin `cmd` yang menjalankan perintah tidak direkursi: `cmd /c start rd /s /q C:\proj`, `cmd /c call rd …`, `cmd /c if exist x rd …` = `mutating` → `AutoApprove` di `--unsafe-auto-shell`; `cmd /c rd …` = `destructive`/denied | `winlex.go:237-260`; probe di bawah |
| CL5-10 | #44 | MAJOR | confirmed | `unshare --propagation private sh -c 'rm -rf ~'` = `mutating` karena `--propagation` salah dimasukkan sebagai bool padahal mengambil nilai; `busybox ash/hush -c 'rm -rf ~'` dan `toybox sh -c 'rm -rf ~'` = `mutating` (shell applet tidak dikenali) | `wrap.go:16, 99-101`; probe di bawah |

Probe persis:

```
cmd /c start rd /s /q C:\proj                    => mutating   (auto di unsafe)
cmd /c call rd /s /q C:\proj                     => mutating
cmd /c if exist x rd /s /q C:\proj               => mutating
cmd /c for %i in (x) do rd /s /q C:\proj         => unknown   (aman karena parens)
unshare --propagation private sh -c 'rm -rf ~'   => mutating
unshare --propagation=private sh -c 'rm -rf ~'   => unknown    (aman)
busybox ash -c 'rm -rf ~'                        => mutating
busybox hush -c 'rm -rf ~'                       => mutating
toybox sh -c 'rm -rf ~'                          => mutating
```

Keduanya melanggar aturan "destructive/unknown tetap `denied` di `--unsafe-auto-shell`" (ADR-079 butir 4). Dampaknya hanya pada flag headless eksplisit itu; mode interaktif tetap bertanya dan headless biasa tetap `denied`, tetapi untuk acceptance F1 ini masih MAJOR.

**Rekomendasi.** #46: perlakukan `start`/`call` sebagai wrapper (parse opsi `/b /d /wait …`, title, lalu perintah) atau `unknown` sampai isinya dianalisis; `if`/`for`/`do`/`goto` → `unknown` bila tidak dapat diparse. #44: pindahkan `--propagation` ke `unshareValued`; tambahkan `ash`/`hush` ke `interpreters` dan `toybox` (atau applet shell `busybox`) ke `wrappers`; untuk applet `busybox` yang tidak dikenal, pilih `unknown` alih-alih `mutating`. Tambah kasus di `TestRuleLabels`/`TestRuleNesting` dan `TestUnsafeAutoShellFlag`.

### Tidak dicakup putaran 2

- Runtime Windows (`cmd.exe` nyata) dan macOS; label diuji deterministik lewat `Dialect`.
- Tabel opsi lengkap `start`/`call`/`if`/`for`, `unshare`, dan seluruh applet `busybox`/`toybox`.
- Integrasi tool/approval end-to-end; CL5-9/CL5-10 hanya menjadi eksekusi tanpa approval lewat `--unsafe-auto-shell`.

## Putaran 3 — final narrow check (2026-10-20)

Head: `#44` `e080f4b`, `#46` `16b1871`. `#41`/`#43` tidak berubah.

| PR | Verdict putaran 3 |
|---|---|
| #41, #43 | **approve** (tidak berubah; MINOR lama) |
| #44 | **approve** — CL5-10 fixed; tidak ada bypass baru untuk bentuk yang diminta |
| #46 | **changes** — CL5-9 hanya sebagian; `start` dengan title non-kosong atau `/d`/`/machine` masih lolos |

### #44 — CL5-10 terverifikasi

| Perintah | Label |
|---|---|
| `unshare --propagation shared sh -c 'rm -rf ~'` | destructive |
| `unshare --propagation=shared sh -c 'rm -rf ~'` | unknown (fail-closed) |
| `busybox ash -c 'rm -rf ~'`, `busybox hush …`, `busybox sh …` | destructive |
| `busybox su -c 'rm -rf ~'` | destructive (privileged) |
| `busybox --install -s` | unknown |
| `toybox sh -c 'rm -rf ~'`, `toybox rm -rf ~`, `toybox ash -c …` | destructive |

`--propagation` kini valued (`wrap.go:99-101`), `ash`/`hush` masuk `interpreters`, `toybox` masuk `wrappers`; suite classify hijau. Catatan di luar CL5-10: `busybox awk 'BEGIN{system(…)}'` tetap `mutating` (celah interpreter generik yang sudah ada, bukan regresi).

### #46 — CL5-9 masih bocor lewat bentuk `start` standar

Bentuk yang diminta lulus:

| Perintah | Label/decision |
|---|---|
| `cmd /c start /wait rd /s /q C:\proj` | destructive/denied |
| `cmd /c start "" /wait rd …` | destructive/denied |
| `cmd /c start /affinity 1 rd …` | destructive/denied |
| `cmd /c start cmd /c rd …` | destructive/denied |
| `cmd /c cmd /c cmd /c rd …` | destructive/denied |
| `cmd /c call rd …` | destructive/denied |
| `cmd /c call :label` | unknown/denied |
| `if errorlevel`, `if exist`, `if defined` | unknown/denied |
| `for /r …`, `for /d …`, `goto` | unknown/denied |

Namun tiga bentuk `start` yang umum masih `mutating` → `AutoApprove` di `--unsafe-auto-shell`:

```
cmd /c start "title" rd /s /q C:\proj            => mutating (auto)
cmd /c start /d C:\proj rd /s /q C:\proj         => mutating (auto)
cmd /c start /machine x64 rd /s /q C:\proj       => mutating (auto)
```

Penyebab: `cmdBuiltin` (`winlex.go:258-283`) hanya melewati title kosong (`""`) dan hanya mengonsumsi nilai `/node` dan `/affinity`; `/d <dir>` dan `/machine <arch>` juga mengambil nilai, dan title non-kosong (`start "Judul" prog`) dianggap argv0. `start "title" /wait rd …` kebetulan `denied` karena flag `outside_read`, tetapi bentuk tanpa `/wait` auto.

**Rekomendasi:** pertahankan status "quoted" pada token lexer (atau aturan konservatif: token pertama `start` yang bukan opsi dan diikuti token lain = title), konsumsi nilai `/d` dan `/machine`, dan `unknown` untuk opsi `start` yang tidak dikenal. Tambah kasus title/`/d`/`/machine` ke `TestUnsafeAutoShellCmdBuiltins`/`TestRuleLabels`.

Tidak ada temuan baru #44; suite `go test ./internal/exec/classify` hijau.

### Tidak dicakup putaran 3

- Runtime `cmd.exe` nyata; semantik title/opsi `start` diuji via `Dialect` deterministik.
- Seluruh tabel opsi `start` (mis. `/node`, `/affinity` sudah; kombinasi lain) dan applet `busybox`/`toybox` di luar shell.

## Putaran 4 — final verification (2026-10-20)

Head: `#46` `55f5cf3`; `#44` `e080f4b` tidak berubah.

| PR | Verdict putaran 4 |
|---|---|
| #44 | **approve** (tidak berubah) |
| #46 | **changes** — title ter-quote masih lolos lewat jalur `cmd /c` |

### Terverifikasi fixed

| Perintah | Label |
|---|---|
| `cmd /c start /d C:\proj rd …` | destructive |
| `cmd /c start /machine x64 rd …` | destructive |
| `cmd /c start /node n1 rd …` | destructive |
| `cmd /c start /affinity 1 rd …` | destructive |
| `cmd /c start /wait "title" rd …` | **mutating (masih bocor)** |
| `cmd /c start "title" rd …` | **mutating (masih bocor)** |
| `cmd /c start /unknown rd …`, `/title`, `/d"C:\proj"`, `/machine:x64` | unknown |
| `start "title" rd …` (dialek Cmd, tanpa `cmd /c`) | destructive |
| `cmd /c start "" /d C:\proj rd`, `/b /d`, `/b`, `/high`, `/wait` | destructive |

### CL5-9 residual

`cmdBuiltin` sekarang benar menangani title ter-quote dan opsi bernilai jika `arg.quoted` tersedia. Namun `cmdShell` memanggil `s.inline(rest, Cmd)`, dan `inline` melakukan `strings.Join(argStrings(args), " ")` (`wrap.go:217-226`) sehingga flag `quoted` hilang saat re-parse. Akibatnya:

```
start "title" rd /s /q C:\proj        (dialek Cmd langsung) => destructive
cmd /c start "title" rd /s /q C:\proj (re-parse via inline)  => mutating -> auto di unsafe
cmd /c start /wait "title" rd …                              => mutating -> auto
```

Ini persis bentuk yang diminta diverifikasi (quoted-title) dan masih menjadi eksekusi tanpa approval di `--unsafe-auto-shell`.

**Rekomendasi:** di `cmdShell`, untuk `rest` yang bukan satu argumen ter-quote utuh, panggil `s.command(rest, stdinNone)` (mempertahankan `arg.quoted`) alih-alih `inline`; hanya re-lex string bila seluruh command dibungkus satu quote. Atau buat `inline` yang sadar `quoted` (re-quote token ber-quoted) dengan jalur khusus untuk command utuh ter-quote. Tambah `cmd /c start "title" rd …` ke `TestUnsafeAutoShellCmdBuiltins`.

### Tidak dicakup putaran 4

- Runtime `cmd.exe` nyata; semantik `start` dengan title/opsi diuji deterministik via `Dialect`.
