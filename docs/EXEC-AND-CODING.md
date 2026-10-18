# Damn-Bot — Eksekusi terminal & loop coding

Status: spesifikasi 1.0. Melengkapi [SPEC.md](../SPEC.md) §4.3, §5.1 (`shell`), §9.2 (`exec-backends`, `code`), §7.2 (`turn_result`), §14 (keamanan). Invariant baru: **P24** (0 orphan, 0 hang, spawn overhead). Mesin parsing/edit/outline ada di [CODE-TOOLS.md](CODE-TOOLS.md) (P23); pemulihan otomatis di [SELF-HEALING.md](SELF-HEALING.md) (P22). Angka akhir disalin ke [SCORECARD.md](SCORECARD.md) dan [MITIGATION-MATRIX.md](MITIGATION-MATRIX.md).

Konvensi: path hermes relatif terhadap root `hermes-agent`. `[INFERENCE]` = disimpulkan, belum diukur. `[tidak terverifikasi]` = bukti kode tidak ditemukan.

---

## 0. Pembanding hermes

| Aspek | hermes-agent (bukti) | Kelemahan | Damn-Bot (bagian) |
|---|---|---|---|
| Transport | Foreground memakai pipes, stdin `DEVNULL` (`tools/environments/local.py:1107-1112`). PTY hanya untuk `background=true, pty=true`, khusus backend lokal (`tools/process_registry.py:1168`, `tools/terminal_tool.py:1538`). | Perintah foreground yang butuh TTY gagal atau berperilaku berbeda. | PTY/pipe dipilih otomatis (A.1) |
| Sesi shell | Setiap call membuat bash baru (`tools/environments/local.py:1018-1020`). Persistensi disimulasikan dengan snapshot env + file CWD (`tools/environments/base.py:298-304`). | Alias, fungsi, dan job hilang antar call `[INFERENCE]`. | One-shot default + sesi persisten opsional (A.2) |
| Timeout | Default 180 s (`tools/terminal_tool.py:753`), maks foreground 600 s (`tools/terminal_tool.py:76`). Di atas 600 s perintah dipromosikan ke background, tidak ditolak (`tools/terminal_tool.py:1154-1155`). | Tidak ada idle timeout foreground `[tidak terverifikasi]`, jadi perintah yang menunggu input bisa diam sampai 600 s. | 120 s / maks 600 s + idle 60 s (A.4) |
| Klasifikasi error | String-match `"timeout" in str(e)` + retry `2^n` (`tools/terminal_tool.py:1280-1285`). | Rapuh, dan retry bisa mengulang efek samping. | Status typed, tanpa retry eksekusi (A.3, P6) |
| Ukuran output | 50.000 char (`tools/tool_output_limits.py:13`), head/tail 40/60 (`tools/environments/base_output.py:38-40`), spill ≤ 5.000.000 char (`tools/environments/base_output.py:35`). | Hingga ~12.500 tok per call `[INFERENCE: 4 char/tok]`. | ≤ 4.000 char inline, preview 800 (A.3) |
| CR/progress bar | `strip_ansi` hanya menghapus escape (`tools/ansi_strip.py:47-51`). CR→LF hanya di jalur tampilan (`tools/ansi_strip.py:69-70`). | Frame progress bar sampai ke model. | Collapse CR + dedupe `[×N]` (A.3) |
| Orphan | `killpg` TERM → 1 s → KILL + sweep descendant (`tools/environments/local.py:929-986`). Kode mengakui "a rare orphaned grandchild may leak" (`tools/environments/local.py:943-950`). | Orphan diterima sebagai risiko. | Marker env + subreaper/Job Object, 0 orphan (A.4) |
| Background | `MAX_PROCESSES=64` dengan LRU, TTL 30 menit (`tools/process_registry.py:62-63`). Notifikasi berupa pesan `[IMPORTANT: …]` yang di-inject (`tools/process_registry_notifications.py:463-464`). | Batas global longgar. Notifikasi memakan token prosa. | 8/sesi, 32 global, notice ≤ 40 tok (A.5) |
| Prompt interaktif | Tidak ada deteksi generik. Yang ada hanya guard regex dev-server (`tools/terminal_tool_guards.py:67-121`) dan prompt sudo (`tools/terminal_tool_sudo.py:460-461`). `PAGER=cat` (`tools/environments/base_session_env.py:156`). `CI`/`DEBIAN_FRONTEND` tidak diset `[tidak terverifikasi]`. | Hang pada `[y/N]`, editor, host key ssh. | Env non-interaktif + `setsid` + deteksi prompt (A.6) |
| Keamanan | Regex `HARDLINE_PATTERNS`/`DANGEROUS_PATTERNS` (`tools/approval_detection.py:107,228`). Perintah non-dangerous langsung disetujui (`tools/approval.py:1110-1112`). Dangerous auto-approve di konteks non-interaktif tanpa gateway (`tools/approval.py:1117`). Mode smart memakai LLM (`tools/approval_smart.py:88-146`). | Regex atas string mentah mudah diakali dengan quoting/`eval`. Auto-approve diam-diam. Approval smart memakai token. | Approval per perintah; auto hanya `shell_allow` argv-persis; classifier AST hanya label risiko (A.8, ADR-079) |
| Biaya guidance | `CODING_AGENT_GUIDANCE` 3.021 byte (`agent/coding_context.py:84-132`, diukur `wc -c`), ≈ 750 tok `[INFERENCE]`. Deskripsi tool `terminal` 869 char (`tools/terminal_tool.py:160-165`), ditambah deskripsi parameter panjang (`tools/terminal_tool.py:1520-1557`). | Overhead tetap per call. | Aturan coding ≤ 120 tok (B.1); schema `shell` ≤ 200 tok (A.2) |
| Verify-on-stop | Default OFF (`agent/verification_stop.py:53-79`). Jika aktif, memaksa iterasi LLM ekstra (≤ 2) lewat pesan user sintetis (`agent/turn_stop_gates.py:131-178`, `agent/verification_stop.py:161`). Catatan LSP terlambat ≤ 3 (`agent/turn_stop_gates.py:95`). | Default tanpa bukti; bila aktif, token bertambah. | Default `run` deterministik, 0 LLM call (B.6) |
| Deteksi verify | `scripts/run_tests.sh`, script `package.json`, pytest, target Makefile; maks 8 perintah (`agent/coding_context.py:52-58,484-510`). | Tidak memilih target yang terdampak; tanpa Go/Cargo/JVM `[tidak terverifikasi]`. | 12 toolchain + affected-target (B.2) |
| execute_code | Python saja (`tools/code_execution_tool.py:938-941`). Timeout 300 s, 50 tool call, stdout 50 KB (`tools/code_execution_tool.py:48-51`). Limit memori kernel lokal tidak ditemukan `[tidak terverifikasi]`. | Tanpa limit memori/jaringan. | Python/JS/Go/bash, limit OS (B.7) |

---

## A. Eksekusi terminal

Paket: `internal/exec` (core, backend `local`) dan `internal/modules/exec` (registry proses, backend lain; lazy P16). Semua pure Go, `CGO_ENABLED=0` (ADR-016). Runner tidak memakai `os/exec.Cmd.Wait`. Spawner memakai `syscall.ForkExec`/`StartProcess` plus satu goroutine reaper (A.4).

### A.1 Runner: PTY vs pipe

| Runner | Implementasi | Stdin | Stdout/stderr |
|---|---|---|---|
| `pipe` (default) | `ForkExec` + `Setsid` (sesi baru tanpa controlling TTY) | `stdin` argumen, atau `/dev/null` (`NUL` di Windows) | digabung dalam satu pipe, urutan terjaga |
| `pty` Unix | `github.com/creack/pty` + `Setsid` + `Setctty`; ukuran 120×40 | PTY master | PTY master (stderr menyatu) |
| `pty` Windows | ConPTY (`CreatePseudoConsole`, `golang.org/x/sys/windows`) di dalam Job Object | ConPTY | ConPTY |

Aturan pilih otomatis (deterministik, dari AST `mvdan.cc/sh/v3/syntax`; model tidak memilih):
1. `session` diset → `pty` (shell persisten interaktif).
2. Salah satu `argv[0]` termasuk daftar `tty_required` → `pty`. Daftar: `top`, `htop`, `vim`, `vi`, `nano`, `less`, `more`, `man`, `ssh` tanpa perintah remote, `python`/`python3`/`node`/`irb` tanpa argumen skrip, `docker run|exec` dengan `-t`/`-it`, `script`, `tmux`, `screen`.
3. `background:true` (F5, ADR-072) dan perintah adalah server/REPL interaktif menurut aturan 2 → `pty`.
4. Selain itu → `pipe`. Pipe lebih cepat (A.9), output bebas escape, dan tidak bisa membuka `/dev/tty` (A.6).

`Meta.runner` selalu melaporkan pilihan. Runner tidak menjalankan ulang perintah otomatis dengan mode lain, karena efek samping tidak boleh diulang.

### A.2 Shell, sesi, quoting, schema

**Deteksi shell** (sekali per proses, di-cache):

| OS | One-shot | Sesi persisten |
|---|---|---|
| Linux/macOS | `bash -c` bila ada di PATH, else `sh -c`. `$SHELL` tidak dipakai untuk one-shot agar semantik stabil. | `$SHELL` bila salah satu dari bash/zsh/fish; else bash; else sh. |
| Windows | `pwsh -NoProfile -NonInteractive -Command`, else `powershell` (sama), else `cmd /d /s /c` | pwsh/powershell, else cmd |

Konteks tier (SPEC §6) menulis shell one-shot dan shell sesi yang terdeteksi.

**One-shot (default):** setiap call adalah proses baru. `command` dikirim sebagai **satu elemen argv**, jadi tidak ada quoting ulang. `cwd` divalidasi sandbox (SPEC §14.2) lalu menjadi `Dir` proses.

**Sesi persisten (`session:"<nama>"`):**
- Shell dijalankan dengan `--norc --noprofile` (bash), `-f` (zsh), `--no-config` (fish), atau `-NoProfile` (pwsh).
- Ekspansi alias dimatikan (`shopt -u expand_aliases` / `unsetopt aliases`) agar classifier A.8 tidak bisa diakali alias.
- Pengiriman perintah:
  1. Tulis perintah ke file `<state_dir>/exec/sess/<id>/c<n>.<ext>` dengan mode 0600.
  2. Kirim ke shell: `. '<path>'` (bash/zsh/sh), `source '<path>'` (fish), `. '<path>'` (pwsh), atau `call "<path>"` (cmd). Path di-quote single-quote dengan escape `'\''` (pwsh: `''`). Isi perintah tidak pernah melewati quoting.
  3. Akhir perintah ditandai sentinel `printf '\036DB:%s:%d\036' <nonce128bit> $?` (fish: `$status`; pwsh: `$LASTEXITCODE`). Nonce acak per perintah, jadi output perintah tidak bisa memalsukan exit code. Runner juga membaca `cwd` hidup sesi lewat sentinel `pwd -P` setiap perintah (syarat `shell_allow`, A.8, ADR-079).
- cwd, env, fungsi, venv yang aktif, dan job bertahan antar call.
- Fungsi yang didefinisikan di sesi dicatat classifier (A.8), sehingga pemanggilannya diberi label maksimum isi fungsi. Sesi yang pernah menjalankan perintah yang tidak cocok `shell_allow` (termasuk definisi fungsi, `alias`, `trap`, assignment `PATH`/`LD_PRELOAD`/`GIT_*`, `printf -v PATH`, atau perintah lain yang di-approve manual) permanen tidak memenuhi syarat `shell_allow` sampai sesi ditutup (A.8, ADR-079 butir 3; menggantikan flag `tainted_session` ADR-069).
- Batas: 4 sesi per sesi agent. Sesi idle 30 menit di-reap.
- Timeout di sesi:
  1. SIGINT ke foreground pgrp TTY (`tcgetpgrp`).
  2. Tunggu 3 s.
  3. Shell dibunuh dan dibuat ulang; `Meta.session_reset=true`.

**Schema eager `shell` (final, menggantikan baris SPEC §5.1):**

```json
{"type":"function","function":{"name":"shell",
 "description":"Run a command non-interactively; output is summarized. background:true returns a process id. session keeps cwd/env across calls.",
 "parameters":{"type":"object","properties":{
  "command":{"type":"string","minLength":1,"maxLength":65536},
  "timeout_s":{"type":"integer","minimum":1,"maximum":600},
  "cwd":{"type":"string"},
  "background":{"type":"boolean"},
  "session":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},
  "stdin":{"type":"string","maxLength":65536}},
  "required":["command"],"additionalProperties":false}}}
```

- Deskripsi 128 char (≤ 160). Estimasi ≈ 150 tok `[INFERENCE]`; gate keras ≤ 200 tok o200k (`TestEagerSchemaTokens/shell`).
- Tidak ada parameter `pty`, `notify`, atau `idle_timeout`, karena perilaku itu ditentukan runtime. Bandingkan dengan 5 parameter background hermes (`tools/terminal_tool.py:1524-1557`).
- `timeout_s > 600` → `invalid_args` (P3). Perintah tidak dipromosikan diam-diam seperti di hermes. Hint error `use background:true` berlaku sejak F5 (ADR-072).
- `background` dan `session` bersamaan → `invalid_args`.
- Di build F1, `shell{background:true}` → `invalid_args` (hint "background not available; run in foreground with timeout_s"). Parameter `background` dan deskripsinya tetap di schema (0 tok perubahan); semantik background (A.5) datang di F5 (ADR-072).

### A.3 Streaming ke UI dan ringkasan ke model

**UI:**
- Byte mentah lewat redaksi streaming (SPEC §14.3, ADR-071). Penyaring hanya menahan sufiks terpanjang yang masih bisa menjadi awal pola atau nilai secret yang dikenal (pencocokan parsial, batas per pola); chunk tanpa kecocokan parsial dikirim segera (0 latensi tambahan, P17). Pola berekor tak terbatas (`Bearer \S+`) ditahan sampai terminator atau 4 KiB lalu diredaksi fail-closed; setelah `-----BEGIN … PRIVATE KEY-----` terlihat, output ditahan sampai END atau akhir stream lalu seluruh blok diredaksi. Test: `TestStreamRedactSplit`.
- Lalu masuk renderer terminal ringan (CR, `\x1b[K`, cursor-up untuk progress bar), di-throttle 30 fps.
- Chunk tidak disimpan per event. DB hanya menyimpan hasil final + blob log penuh.

**Model**, pipeline deterministik `internal/exec/summarize` (urutan tetap):
1. Decode UTF-8 (byte invalid → U+FFFD). Strip ANSI/OSC/DCS.
2. Collapse CR: per baris, hanya segmen setelah `\r` terakhir yang dipertahankan. Backspace diterapkan.
3. Collapse progress: baris berurutan dengan kunci identik (digit dinormalisasi ke `#`, spasi dirapatkan) → hanya baris terakhir + `[×N]`.
4. Dedupe: baris identik berurutan → satu baris + `[×N]`.
5. Redaksi secret (SPEC §14.3).
6. Hasil ≤ 4.000 char → inline. Jika lebih → spill ke `<data_dir>/blobs/<sha[0:2]>/<sha>` (log hasil langkah 1–5; log mentah ter-redaksi disimpan sebagai blob kedua, `Meta.raw_blob_sha`; kedua blob dirujuk lewat `blob_refs` owner `event`, SPEC §11.6) dengan preview 800 char:
   - **head 160 char**;
   - **baris error ≤ 240 char**: maks 3 baris unik dari bagian tengah yang cocok dengan `(?i)\b(error|fatal|panic|exception|traceback|failed|fail:|denied|not found)\b` atau `^\S+:\d+(:\d+)?: `, masing-masing dipotong 80 char;
   - **tail 400 char**;
   - path blob.

   Jatah head/error yang tidak terpakai dialihkan ke tail. Total preview tetap 800 char sesuai P5.
7. Agregat inline per turn 24.000 char (P5). Sesudahnya preview 400 char: head 80 + error 120 + tail 200.

Baris header selalu ada dan tidak dikompresi:

```text
[exit 1 · 2.341 ms · signal none · runner pipe · status exited]
```

`status ∈ {exited, signaled, timeout, idle_timeout, waiting_for_input, cancelled_confirmed, still_running, spawn_failed}` (`cancelled_confirmed`/`still_running` menggantikan `cancelled`, ADR-072; A.4). `Meta`: `exit_code` (null jika signaled), `signal`, `duration_ms`, `status`, `runner`, `bytes_raw`, `chars_model`, `lines_collapsed`, `truncated`, `blob_sha`, `raw_blob_sha`, `process_id` (F1: hanya untuk `still_running`, registry internal), `killed`, `session_reset`. Exit ≠ 0 tetap `OK=true` dengan status `exited`, karena perintah memang berjalan. `exec_failed` hanya untuk `spawn_failed`.

### A.4 Timeout, idle-timeout, cancel, 0 orphan

| Parameter | Default | Rentang | Sumber |
|---|---|---|---|
| `timeout_s` | 120 | 1–600 | argumen; config `exec.default_timeout_s` |
| idle-timeout | 60 s | 10–600 | config `exec.idle_timeout_s` |
| grace TERM→KILL | 3 s | 1–3 (> 3 ditolak validasi config, ADR-072) | config `exec.kill_grace_s` |

**Idle** (tidak berlaku untuk proses background) terpicu bila tiga kondisi berlaku bersamaan selama jendela idle:
- 0 byte output;
- CPU pohon proses < 1% satu core: Linux `/proc/<pid>/stat`; macOS `sysctl KERN_PROC` + `proc_pid_rusage` via `unix.Syscall`; Windows `JobObjectBasicAccountingInformation`;
- I/O < 64 KB: Linux `/proc/<pid>/io`; Windows job I/O accounting; macOS tanpa kriteria ini.

Akibatnya `go build` yang sunyi tetapi sibuk CPU tidak dibunuh, sedangkan `sleep 100` dibunuh setelah 60 s dengan hint `use background:true for long silent waits` (hint ini sejak F5, ADR-072).

**Identitas pohon proses.** Setiap anak mewarisi env `DAMNBOT_EXEC_ID=<ulid>`. Anak juga diberi:
- Linux: `Setsid` (pgid = pid), `Pdeathsig=SIGKILL`. Spawn dilakukan dari goroutine `LockOSThread` yang tidak pernah exit, agar pdeathsig tidak terpicu oleh thread yang mati. Proses damnbot menjadi `PR_SET_CHILD_SUBREAPER` (`unix.Prctl`) saat runner pertama dipakai (lazy). Cucu yang lepas via `setsid`/double-fork tetap ter-reparent ke damnbot.
- macOS: `Setsid`; tanpa subreaper.
- Windows: Job Object dengan `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, tanpa `BREAKAWAY_OK`. Proses dibuat `CREATE_SUSPENDED`, dimasukkan ke job, baru di-resume, sehingga tidak ada celah race.

**Urutan kill** (timeout, idle, cancel user, `process_kill` (F5), akhir sesi):
1. `kill(-pgid, SIGTERM)` (Windows: `TerminateJobObject` langsung, karena Windows tidak punya TERM yang setara untuk pohon proses).
2. Tunggu `exec.kill_grace_s` (default 3 s, rentang 1–3, ADR-072). P24 "≤ `timeout_s` + 4 s" tetap berlaku.
3. `kill(-pgid, SIGKILL)`.
4. Sweep: semua pid yang env-nya memuat `DAMNBOT_EXEC_ID` yang sama (Linux `/proc/*/environ`, macOS `KERN_PROCARGS2`) atau anak hasil reparent subreaper → `SIGKILL`. Identitas dicek lewat marker, bukan pid saja, sehingga aman dari PID reuse yang dikhawatirkan hermes (`tools/environments/local.py:943-950`).
5. Reaper `wait4(-1, WNOHANG)` pada SIGCHLD me-reap semua zombie, termasuk anak yatim hasil reparent.

**Cancel (ADR-072, REL-06).** 1 s setelah cancel = tenggat **pengembalian hasil**. Pohon proses yang terbukti mati dalam 1 s → `tool_result{status:"cancelled_confirmed", by:"user"}`. Yang belum terbukti mati → `tool_result{status:"still_running", by:"user"}` + `process_id` registry internal (F1); urutan kill berlanjut di background dan hasilnya dicatat sebagai `system_notice{kind:"process_exit"}`. Re-dispatch tool `non_idempotent` setelah `still_running` wajib menunggu `cancelled_confirmed` atau konfirmasi user (P22/REL-06).

**Crash damnbot (`kill -9`):**
- Linux: pdeathsig membunuh leader; cucu yang lepas ditangkap oleh sweep saat start berikutnya.
- Windows: job close → semua anak mati.
- macOS: sweep saat start berikutnya dengan marker + `<state_dir>/exec/live/<pid>.json`.

Sweep startup dijalankan lazy pada pemakaian `shell` pertama, atau oleh `damnbot doctor`.

### A.5 Proses background (F5, ADR-072)

Di build F1, `shell{background:true}` → `invalid_args` (A.2). Background, `process_list/output/wait/send/kill`, dan pemindahan foreground → background datang di **F5** bersama TOOL-077/EXEC-009 (ADR-064, ADR-072). Registry proses internal (untuk `still_running`, A.4) tetap F1. Spesifikasi F5:

`shell{background:true}` → registry di modul `exec`. Tool deferred (schema lewat `tool_search`):

| Tool | Argumen (`*` required) | Hasil |
|---|---|---|
| `process_list` | — | ≤ 40 entri `{id, command ≤ 80 char, status, pid, started, exit_code, runtime_ms}` |
| `process_output` | `id*`; `since` (offset byte, default = offset terakhir dibaca); `tail_lines` 1–500; `grep` (RE2) | output baru melalui pipeline A.3 |
| `process_wait` | `id*`; `timeout_s` 1–600 (default 60) | status final atau `still_running` |
| `process_send` | `id*`; `text*` ≤ 4.096 char; `enter` bool (default true) | hanya runner `pty` atau pipe dengan stdin terbuka |
| `process_kill` | `id*`; `signal` enum[`TERM`,`INT`,`KILL`] (default `TERM` → urutan A.4) | status final |

Batas dan siklus hidup:
- Maks 8 proses hidup per sesi agent dan 32 global per proses damnbot. Melebihi → `too_large` + hint `process_kill`. Tidak ada LRU yang membunuh diam-diam.
- Log bergulir: `<state_dir>/exec/bg/<id>.log`, ring 8 MiB. Saat proses selesai, log difinalisasi menjadi blob (sha) dan direferensikan di event.
- Entri selesai disimpan 30 menit, maks 64.
- Proses background bertahan melewati compaction dan `/new` di proses damnbot yang sama (parity opsi survive hermes, `tools/terminal_tool.py:1557`). Proses mati saat damnbot exit (A.4).

Notifikasi selesai:
- Proses exit → event `system_notice` (payload `{kind:"process_exit", id, exit_code, signal, runtime_ms, blob_sha}`).
- Notice dikirim ke model pada request LLM berikutnya, baik di turn berjalan setelah batch tool saat ini maupun di turn berikutnya. Tidak ada LLM call baru (P11).
- Teks notice ≤ 40 tok, contoh: `bg p3 "npm run dev" exited 1 after 12,3 s; see process_output p3`. Maks 3 notice per request; sisanya `+N more (process_list)`.
- UI menampilkan notifikasi seketika.

### A.6 Prompt interaktif tanpa hang

**Env non-interaktif** (ditambahkan ke env anak; nilai yang di-set user lewat `exec.env` — map nama → nilai untuk anak, bukan pewarisan dari induk, ADR-071 — menang, kecuali baris yang ditandai *keras*):

| Var | Nilai | Alasan |
|---|---|---|
| `PAGER`, `GIT_PAGER`, `MANPAGER`, `SYSTEMD_PAGER` | `cat` | pager tidak menahan proses |
| `GIT_TERMINAL_PROMPT` | `0` (keras) | git tidak meminta kredensial |
| `GIT_SSH_COMMAND` | `ssh -o BatchMode=yes` (jika tidak diset user) | ssh gagal cepat daripada bertanya |
| `CI`, `NONINTERACTIVE`, `PIP_NO_INPUT` | `1` | mode non-interaktif npm/brew/pip/dll. |
| `DEBIAN_FRONTEND` | `noninteractive` | apt/dpkg |
| `EDITOR`, `VISUAL`, `GIT_EDITOR`, `GIT_SEQUENCE_EDITOR` | `false` | editor gagal eksplisit (exit ≠ 0); hint `use -m / --no-edit`. `true` ditolak karena bisa menerima isi editor kosong tanpa jejak. |
| `GIT_MERGE_AUTOEDIT` | `no` | merge tanpa editor |
| `PYTHONUNBUFFERED` | `1` | output streaming |
| `TERM` | `dumb` (pipe) / `xterm-256color` (pty) | — |
| `DAMNBOT_EXEC_ID`, `DAMNBOT=1` | id / `1` | marker A.4 (keras) |

**Runner pipe:** proses tanpa controlling TTY (`setsid`), jadi `open("/dev/tty")` gagal `ENXIO`, dan stdin = `/dev/null` memberi EOF. Akibatnya sudo, ssh password, dan `read -p` gagal cepat, bukan hang. Ini jaminan struktural, bukan heuristik.

**Runner pty dan pipe ber-stdin**, heuristik prompt. Hasil `waiting_for_input` terpicu bila:
- output diam ≥ 1,5 s;
- baris parsial terakhir tanpa `\n` (≤ 200 char) cocok dengan salah satu pola:

| Kelas | Pola (RE2, case-insensitive) |
|---|---|
| `secret` | `(password\|passphrase\|passcode\|pin\|token)[^:\n]{0,40}:\s*$`, `^\[sudo\] ` |
| `confirm` | `\[(y/n\|y/N\|Y/n\|yes/no)\]\s*[:?]?\s*$`, `\((y/n\|yes/no)\)\s*[:?]?\s*$`, `continue connecting \(yes/no` |
| `pager` | `^\(END\)$`, `--More--`, `^:$` |
| `text` | `\?\s*$` atau `:\s*$` dengan baris ≤ 120 char, `press (any key\|enter\|return)`, `username for '` |

- Di Linux, sinyal tambahan dari `/proc/<pid>/wchan` ∈ {`n_tty_read`, `wait_woken`, `pipe_read`} dapat memicu tanpa pola, setelah diam 5 s.

Hasil:

```json
{"status":"waiting_for_input","prompt":"Do you want to continue? [Y/n]","input_kind":"confirm","killed":true}
```

- **F1 (ADR-072):** pohon proses foreground yang terdeteksi menunggu input di-kill (urutan A.4) lalu dikembalikan `status:"waiting_for_input"` dengan prompt yang tertangkap, `killed:true`, tanpa `process_id`.
- **F5:** proses foreground dipindahkan ke registry background dan dihitung terhadap batas A.5 (hasil memuat `process_id`). Proses tetap hidup untuk `process_send`, dan di-kill jika tidak ada input 10 menit. Jika batas A.5 penuh → kill + status `waiting_for_input` tanpa `process_id`.
- `input_kind:"secret"` (F5): `process_send` dari model → `denied`. Input hanya lewat UI (TUI/web/ACP memberi field input tersembunyi; nilai tidak masuk transcript, event hanya mencatat `secret_input:true`).

### A.7 Backend (modul lazy)

Kontrak `internal/exec.Backend`, diimplementasikan oleh setiap backend:

```go
// Backend menjalankan perintah di lingkungan target. Implementasi wajib
// lazy (P16): koneksi/daemon dibuka pada Exec pertama, bukan saat Register.
type Backend interface {
    ID() string
    Caps() Caps                                // PTY, Signals, SharesFS, ResourceLimits, Network
    Exec(ctx context.Context, s Spec) (Proc, error) // Spec: argv, cwd, env, stdin, pty, limits
    Sweep(ctx context.Context, execID string) (killed int, err error) // 0 orphan per backend
    Close(ctx context.Context) error           // membunuh semua Proc milik backend
}
```

| Backend | Status | Transport | `SharesFS` | Kill pohon proses | Parity |
|---|---|---|---|---|---|
| `local` | core | A.1–A.4 | ya | pgid + subreaper/Job | `tools/environments/local.py` |
| `docker` | lazy | Docker Engine API via HTTP di unix socket/named pipe (client minimal sendiri, tanpa SDK; P21); `exec` dengan `setsid` di container | bind mount workspace di path identik | `kill -- -pgid` di container; `Close` = stop container milik sesi | EXEC-002 |
| `ssh` | lazy | `golang.org/x/crypto/ssh`; host key wajib known_hosts (tanpa TOFU diam-diam) | tidak: sinkron manifest sha (push file berubah sebelum exec, pull file berubah sesudahnya; konflik → `stale_read`) | `setsid` remote + `kill -- -pgid` lewat channel kedua | EXEC-007 |
| `singularity` | lazy | CLI `apptainer`/`singularity exec` di runner lokal | bind mount | pgid lokal | EXEC-003 |
| `modal`, `daytona`, `vercel` | lazy | HTTP API resmi masing-masing (`internal/netx`) | tidak: sinkron manifest | API terminate sandbox | EXEC-004…006 |

- Detail inventaris: [FEATURE-PARITY.md](FEATURE-PARITY.md) baris EXEC-001…015.
- Backend non-lokal yang tidak memiliki salah satu `Caps` (mis. PTY) mengembalikan `module_unavailable` dengan instruksi, tanpa fallback diam-diam ke `local`.
- Test kontrak bersama `TestBackendContract/<id>` (spawn, timeout, kill pohon, 0 orphan via `Sweep`, exit code, sinyal) wajib lulus per backend. Backend cloud memakai fake server HTTP di CI dan diuji nyata di job nightly berkredensial.

### A.8 Keamanan

**Klasifikasi = label risiko, bukan jalur auto (ADR-069, ADR-079).** `internal/exec/classify` mem-parse perintah dengan `mvdan.cc/sh/v3/syntax` (dialek bash; dialek POSIX untuk `sh`). Keluaran = `Verdict{class, flags, reasons}`. Label (urutan ketat approval): `mutating` < `destructive` = `unknown` < `irreversible` (kelas P26: `irreversible`, `monetary`, `delegated_agent_bypass`). Flag: `denied_path`, `outside_read`, `privileged`, `network`. Setiap simple command dinilai; label memakai kelas tertinggi dari semua simple command, dan flag selalu berlaku. Label hanya menentukan bentuk prompt (preview, larangan `allow_session` untuk P26) dan penolakan headless; label **tidak pernah** menghasilkan auto-approve, di mode apa pun (ADR-079 butir 1–2). Tidak ada kelas `read_only` dan tidak ada allowlist `read_only` bawaan: membaca/mencari file dilakukan lewat tool kode tersandbox (`read_file`, `grep`, `glob`) yang menegakkan root + `deny_paths` lewat `openat2(RESOLVE_BENEATH)`/verifikasi fd (SPEC §14.2, [CODE-TOOLS.md](CODE-TOOLS.md) §8); stable tier menyatakan aturan ini kepada model (ADR-079).
- Label tambahan `non_mutating` (best-effort) hanya dipakai untuk **membatasi** di plan mode (butir "Plan mode" di bawah), tidak pernah untuk mengizinkan (ADR-079 butir 11).
- Gagal parse → `unknown` (tanya), tetapi tidak ditolak permanen.
- PowerShell/cmd memakai lexer token sendiri. Struktur yang tidak dikenali → minimal `mutating`; pola destruktif tetap dicek per token.

Aturan rekursi:
- `bash|sh|zsh -c '<literal>'`, `eval <s>`, `xargs <cmd>`, `find -exec <cmd>`, `sudo|doas <cmd>`, `env <cmd>`, `nice <cmd>`, `nohup <cmd>`, `timeout <n> <cmd>`: perintah di dalamnya ikut diklasifikasi.
- `$(…)` atau variabel di posisi `argv[0]` → `unknown`.
- `ssh host '<s>'` → `irreversible` (butir irreversible di bawah); isi `<s>` tetap discan pola destruktif.

**Operand path = label best-effort (ADR-069, ADR-079 butir 2).** Untuk setiap simple command, operand path yang ter-resolve statis (argumen posisi perintah file, `-f`/`--file`/`--ignore-file`/`--exclude-from`, redirect `<`/`>`/`>>`, starting point `find`, pathspec dan `-C` git; ekspansi `~` dan `$HOME` saja, `..`, glob terhadap filesystem, lalu symlink per komponen seperti SPEC §14.2) dipakai untuk label:
- cocok `deny_paths` → flag `denied_path` → error `denied_path` di langkah 5 SPEC §4.3, **tidak bisa di-approve** di mode apa pun;
- di luar root (bukan direktori blob) → flag `outside_read` (baca) atau kelas minimal `destructive` (tulis) → preview menandai path, `allow_session` hanya untuk himpunan path persis; headless → `outside_workspace`; output dibungkus `<untrusted source="shell:outside_workspace">`;
- operand yang tidak bisa di-resolve statis (variabel selain `HOME`, `$(…)`, backtick, `<(…)`, `cd` sebelum operand relatif) → `unknown`.

Ketiadaan label ini **tidak** menjamin apa pun (rekursi lewat symlink, filter git, dan bentuk lain tidak terdeteksi); shell bukan boundary path, kontrolnya approval (ADR-079 butir 8).

**Interpreter (ADR-069).** argv0 ∈ {`sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`, `python*`, `node`, `deno`, `bun`, `perl`, `ruby`, `php`, `lua`, `pwsh`, `powershell`, `osascript`} dengan program dari stdin, pipe, heredoc/here-string, parameter `stdin` schema, atau string `-c`/`-e` non-literal → `unknown`. `-c '<literal>'` tetap direkursi. `source`/`.` → `unknown` (menjalankan isi file). Sesi persisten (A.2) yang pernah menjalankan perintah yang tidak cocok `shell_allow` (mis. `alias`, definisi fungsi, `trap`, `export PATH=…`, `printf -v PATH …`, atau perintah lain yang di-approve manual) **permanen** tidak memenuhi syarat `shell_allow` sampai sesi ditutup (ADR-079 butir 3; menggantikan flag `tainted_session` ADR-069).

| Label | Contoh (cocok → label) |
|---|---|
| `mutating` | Default untuk semua perintah lain, termasuk perintah baca (`ls`, `cat`, `grep`, `git status`/`diff`/`log`; ADR-079): build, test, `go list`/`go vet`, `git add/commit/checkout <branch>/merge/rebase`, `rm` di dalam workspace (bukan root workspace), redirect ke file di workspace, `npm run`, `make`. Instal dependency lokal proyek (`npm install\|ci`, `pnpm i`, `yarn`, `pip install` di venv dalam root, `go mod download`, `cargo fetch`) dan klien HTTP GET ke host non-loopback = `mutating` + flag `network` (ADR-069). |
| `destructive` | `rm -r`/`-rf`/`-fr` dengan target di luar root (setelah resolve operand path) atau = root workspace atau `.git`; tulis ke path di luar root (ADR-069); `git push --force`/`-f`/`+refspec`/`--delete`/`--mirror` (sekaligus `irreversible` → diperlakukan `irreversible`, preview memuat keduanya); `git reset --hard`; `git clean -f*`; `git checkout -- .`/`git restore .` (membuang perubahan worktree); `git branch -D`; `git stash drop/clear`; `git filter-branch`/`filter-repo`; `curl\|wget … \| sh\|bash\|zsh\|python\|node\|iex`; `mkfs*`, `dd of=/dev/*`, `fdisk`, `parted`, `wipefs`, `shred`, redirect ke `/dev/sd*`/`/dev/nvme*`; `chmod -R`/`chown -R` pada `/` atau di luar workspace; fork bomb (fungsi yang memanggil diri sendiri via pipe + `&`); `kill -9 -1`; `shutdown`/`reboot`/`halt`/`poweroff`; `crontab -r`; `docker system prune -a`, `docker volume rm`; `kubectl delete`; `terraform destroy`; tulis ke `/etc`, `/usr`, `/boot`, `C:\Windows`; Windows: `Remove-Item -Recurse` di luar workspace, `rd /s`, `del /s`, `format`, `diskpart`, `reg delete`, `iwr … \| iex`. |
| `irreversible` (P26, ADR-069) | `git push` (semua bentuk), `git send-email`; `npm\|pnpm\|yarn publish`, `npm unpublish\|deprecate\|dist-tag`, `cargo publish\|yank`, `twine upload`, `gem push\|yank`, `poetry publish`, `uv publish`, `docker push`, `helm push`; `gh`/`glab` yang mengubah state remote (`release`, `pr create\|merge\|close\|comment`, `issue create\|comment\|close`, `repo create\|delete\|edit`, `secret set`, `workflow run`, `api` dengan metode non-GET atau field `-f`/`-F`); klien HTTP (`curl`, `wget`, `http`, `xh`) dengan metode/body mutating (`-X` ≠ GET/HEAD, `-d*`, `--data*`, `-F`, `-T`, `--json`, `--post-*`, `--method`) atau host metadata/link-local (169.254.0.0/16, `fd00:ec2::254`, `metadata.google.internal`); `ssh`, `scp`, `sftp`, `rsync` ke host remote, `nc`/`ncat`/`socat`/`telnet`; package manager sistem (`apt`, `apt-get`, `dnf`, `yum`, `pacman`, `zypper`, `apk`, `brew`, `port`, `snap`, `flatpak`, `choco`, `winget`, `scoop`) yang memasang/menghapus, dan `pip install` di luar venv di dalam root. |

- **`network`:** output perintah berflag `network` dibungkus `<untrusted source="shell:network">` dan men-taint turn, juga di workspace trusted (ADR-069, ADR-070, ADR-079 butir 6). Egress subprocess **tidak** melewati gerbang DLP/SSRF `netx`.
- **`privileged`:** `sudo`/`doas`/`su`/`pkexec`/`runas` → flag `privileged` (perintah di dalamnya tetap direkursi): tanya per perintah di **semua** mode termasuk `auto`, preview argv lengkap; `allow_session` hanya untuk subject persis; headless tanpa approver → `denied`; tidak pernah lewat `shell_allow`, `--unsafe-auto-shell`, `destructive_allow`, `allow_destructive`, atau `smart_approval` (SEC-14, ADR-069, ADR-079).

**Kebijakan** (sama dengan baris `shell` di SPEC §14.1; ADR-079 butir 3–4): setiap perintah `shell` **ditanya** di `ask`, `auto-edit`, dan `auto` interaktif, kecuali cocok `shell_allow` (butir di bawah). Label menentukan bentuk prompt dan penolakan headless; label tertinggi + semua flag berlaku.

| Kondisi / label | `ask` (default) | `auto-edit` | `auto` | Headless tanpa approver | Headless `--approval auto --unsafe-auto-shell` |
|---|---|---|---|---|---|
| cocok `shell_allow` (semua syarat di bawah) | auto | auto | auto | auto | auto |
| `mutating` (dengan/tanpa `network`) | tanya | tanya | **tanya** | `denied` | auto bila workspace trusted dan turn tidak tainted; selain itu `denied` |
| `destructive` | tanya + preview | tanya + preview | **tanya + preview** | `denied` | `denied` |
| `unknown` | tanya + preview | tanya + preview | **tanya + preview** | `denied` | `denied` |
| `irreversible`/P26 | tanya + preview (tanpa `allow_session`) | sama | sama | `denied` | `denied` |
| flag `denied_path` | error `denied_path` | error | error | error | error |
| flag `outside_read` | tanya (`allow_session` hanya himpunan path persis) | tanya | tanya | `outside_workspace` | `outside_workspace` |
| flag `privileged` | tanya per perintah + preview argv | tanya per perintah | **tanya per perintah** | `denied` | `denied` |
| turn ter-taint (ADR-070, ADR-079 butir 6), termasuk perintah yang cocok `shell_allow` dan verify | tanya, tanpa `allow_session` | tanya | **tanya** | `denied` | `denied` |

- **Kunci auto non-shell (ADR-079 butir 9).** `exec.allow_destructive`, `exec.destructive_allow`, dan `smart_approval` **tidak berlaku untuk `shell`**: satu-satunya jalur auto shell adalah kecocokan persis `shell_allow`, dan perintah berlabel `destructive`, `irreversible`, `monetary`, atau `delegated_agent_bypass` tidak pernah auto walau cocok (selalu tanya + preview; headless → `denied`). Kunci itu dicadangkan untuk operasi destruktif tool file (desain F1), default tanpa efek sampai ADR F1 mendefinisikannya. `allow_session` (approval user untuk subject yang persis sama) tetap berlaku untuk non-P26.
- `allow_session` **tidak pernah** berlaku untuk kelas P26 (`irreversible`, `monetary`, `delegated_agent_bypass`). Perintah `destructive` sekaligus `irreversible` diperlakukan sebagai `irreversible` (ADR-069).
- **Plan mode (ADR-079 butir 11, SPEC §18.3).** Selama plan mode, `shell` hanya diizinkan bila perintah cocok persis `shell_allow` **dan** classifier memberi label `non_mutating`; perintah lain ditolak. `non_mutating` adalah label best-effort yang hanya dipakai untuk **membatasi** (tidak pernah mengizinkan di luar `shell_allow`). Test: `TestPlanModeShell`.
- **`shell_allow` = satu-satunya jalur auto (ADR-079 butir 3).** Default `shell_allow = []`. Entri = daftar token argv **persis** (contoh `["go", "test", "./..."]`); tidak ada pencocokan prefix. Perintah cocok hanya bila string `command` di-parse menjadi **tepat satu** simple command tanpa metakarakter/ekspansi apa pun (tanpa `|`, `;`, `&`, `&&`, `||`, subshell, `$`, backtick, `~`, glob `*?[`, brace, redirect, assignment `VAR=`, heredoc, parameter `stdin`) dan token-tokennya sama persis dengan satu entri. argv0 entri harus nama tanpa path yang di-resolve lewat `PATH` sesi ke binary yang sama saat sesi dimulai; argv0 relatif/`./x` tidak pernah cocok. Syarat tambahan, semuanya wajib:
  1. `cwd` call (one-shot) atau `cwd` hidup sesi persisten (dibaca runner lewat sentinel `pwd -P` setiap perintah, A.2) berada di dalam root;
  2. sesi persisten hanya pernah menjalankan perintah yang cocok `shell_allow` (perintah lain yang di-approve manual membuat sesi itu permanen tidak memenuhi syarat);
  3. workspace trusted (ADR-069 butir 7) dan turn tidak tainted (ADR-070);
  4. label bukan `destructive`/`unknown`/P26 dan tanpa flag `privileged` (`denied_path` selalu error);
  5. tidak ada file yang ditulis tanpa approval eksplisit di **sesi** ini sejak approval shell manual terakhir.

  Syarat tidak terpenuhi → baris label di tabel (tanya / `denied`).
- **`damnbot run --unsafe-auto-shell` (ADR-079 butir 4).** Hanya headless `--approval auto` dengan flag eksplisit ini: perintah berlabel `mutating` (dengan/tanpa `network`) berjalan tanpa approval, hanya bila workspace trusted dan turn tidak tainted; `destructive`, `unknown`, P26, `privileged`, dan `denied_path` tetap `denied`. Help flag dan dokumentasi menyatakan risikonya: "runs arbitrary shell commands chosen by the model without confirmation; use only in a disposable container". Ini satu-satunya padanan mode yolo hermes; `auto` interaktif tetap bertanya.
- **Workspace trust (ADR-069).** Saat root dibuka pertama kali secara interaktif: prompt "trust workspace <path>?". Keputusan disimpan per path kanonik di config `security.trusted_workspaces`. Headless `run`: untrusted kecuali path ada di `security.trusted_workspaces`. Workspace untrusted: AGENTS.md dan context file dibungkus `<untrusted source="workspace">` (data, bukan aturan), konten file workspace men-taint turn (ADR-070), jalur auto `shell_allow` (termasuk verify) dan `--unsafe-auto-shell` mati, setiap output shell dibungkus `<untrusted source="workspace">` dan men-taint turn (ADR-079 butir 6), hook proyek tidak berjalan (F3).
- **Taint turn (ADR-070, ADR-079 butir 6).** Sumber taint shell: output berflag `network` (juga di workspace trusted) atau `outside_read`, dan **setiap** output shell di workspace untrusted. Taint aktif sejak `tool_result` itu di-commit sampai `turn_end`, untuk semua iterasi berikutnya dalam turn. Selama taint aktif berlaku baris "turn ter-taint" di tabel; `user_message` berikutnya memulai turn tanpa taint.
- Beda dengan hermes: tidak ada auto-approve diam-diam di konteks non-interaktif (`tools/approval.py:1117`), tidak ada auto-approve perintah "non-dangerous" (`tools/approval.py:1110-1112`), dan tidak ada LLM call untuk klasifikasi.
- **Boundary dan vulnerability (ADR-079 butir 8, mengoreksi ADR-069 butir 9).** Sandbox path = boundary untuk tool kode (CODE-TOOLS §8). Shell **bukan** boundary path; kontrol shell = approval. Classifier tidak mendeteksi skrip yang ditulis lalu dijalankan (`./x.sh`, isi file tidak dianalisis); karena label tidak pernah memberi auto, ini tidak membuka jalur eksekusi tanpa approval. Vulnerability = (a) perintah shell yang berjalan tanpa approval eksplisit, kecuali cocok `shell_allow` atau jalur `--unsafe-auto-shell` yang terdokumentasi; (b) tool kode yang mengakses luar root/`deny_paths` tanpa verdict yang benar; (c) egress tool built-in yang lolos DLP `netx`. Efek perintah yang sudah di-approve user (termasuk filter git, rekursi symlink) dan TOCTOU antara preview dan eksekusi = sisa risiko (TRUST-MODEL §3). Isolasi kuat = backend container (SPEC §14.2, OPEN-QUESTIONS Q10).

**Test acceptance F1 (ADR-079, menggantikan tabel dan test classifier ADR-069; ADR-070):** `TestShellApprovalModel` (36 baris tabel ADR-079, semua baris: label dan kebijakan per `ask`/`auto-edit`/`auto`/headless/headless `--unsafe-auto-shell` persis), `TestShellAllowExactMatch` (≥ 60 kasus: setiap metakarakter/ekspansi/redirect/assignment/pipeline × entri cocok → tidak auto), `TestShellAllowCwdTracked`, `TestShellNoClassifierAuto` (EXC-13: ≥ 300 perintah korpus, mode `ask`/`auto-edit`/`auto`, `shell_allow` kosong → 0 dieksekusi tanpa approval), `TestUnsafeAutoShellFlag`, `TestVerifyNeedsShellAllow`, `TestShellOutputUntrustedWorkspace`, `TestCodeToolsSymlinkOutsideRoot` (CODE-TOOLS §8), `TestNoAutoClassReadonlyDecision`, `TestPlanModeShell` (ADR-079 butir 10–11); tetap dari ADR-069/070: `TestShellIrreversibleAlwaysAsk`, `TestSudoAlwaysAsk`, `TestWorkspaceTrustGate`, `TestReadOutsideWorkspaceAsks`, `TestTaintForcesApproval`. Konteks default tabel ADR-079: `shell_allow = [["go","test","./..."]]`, workspace trusted, turn bersih, tidak ada tulis auto di sesi.

**Sandbox cwd:**
- `cwd` harus berada di dalam root sesi (SPEC §14.2), else `outside_workspace`.
- `cd` di dalam perintah tidak dibatasi OS di backend local dan tidak dilacak statis oleh label (`cd ~ && cat .ssh/id_rsa` → `unknown`). `cwd` hidup sesi persisten dibaca lewat sentinel `pwd -P` dan dipakai untuk syarat `shell_allow` (ADR-079 butir 3). Shell bukan boundary path; kontrolnya approval (ADR-079 butir 8).

**Env anak (ADR-071):**
- Deny-by-default: hanya var di allowlist GAP-SEC-02 (SPEC §22.1) yang diwariskan dari env induk, ditambah `exec.env_allow` (daftar **nama persis** var induk, tanpa glob) dan nilai `exec.env` yang di-set (A.6).
- Kunci provider hanya diwariskan bila namanya tercantum eksplisit di `exec.env_allow`.

**Redaksi:** SPEC §14.3 (termasuk pola ADR-071: blok PEM multi-baris fail-closed, Stripe, Google, GitLab, Hugging Face, npm, JWT, userinfo URL) diterapkan pada stream UI, ringkasan model, blob, dan log.

**Audit:** setiap perintah, termasuk yang dijalankan runtime (B.6), meninggalkan jejak di DB konteks:
- event `tool_dispatch` dengan payload `exec: {class, flags, reasons[≤5], argv0[], backend, runner, cwd, session}` (`Verdict`, ADR-069);
- event `tool_result` dengan payload `{exit_code, signal, status, duration_ms, bytes_raw, blob_sha, raw_blob_sha}` (konvensi kunci ref blob `*_blob_sha`, SPEC §11.6);
- row `approvals` (`subject` = perintah ter-normalisasi; `decision` `auto_allowlist` untuk cocok `shell_allow`, `auto_mode` untuk jalur `--unsafe-auto-shell`; nilai `auto_class_readonly` tetap di DDL, dicadangkan, dan tidak pernah ditulis, `TestNoAutoClassReadonlyDecision`, ADR-079 butir 10) untuk setiap keputusan;
- verify runtime memakai event `verify_run` (sudah ada di daftar `events.kind`, SPEC §11.2).

### A.9 Latensi

Overhead spawn = wall time dari `Run` dipanggil sampai hasil final siap, dikurangi runtime perintah. Diukur dengan `/bin/true` / `cmd /c exit 0`, dikurangi waktu `true` yang diukur langsung via `ForkExec`.

| Benchmark | Target p95 | Platform |
|---|---|---|
| `BenchmarkShellSpawnPipe` | < 15 ms | linux/amd64, darwin/arm64 |
| `BenchmarkShellSpawnPTY` | < 15 ms | linux, darwin |
| `BenchmarkShellSpawnWindows` (pipe dan ConPTY) | < 40 ms | windows/amd64 `[INFERENCE: ConPTY + job]` |
| `BenchmarkShellSessionExec` (sesi persisten, sentinel) | < 5 ms | linux, darwin |
| `BenchmarkClassify` (perintah 2 KB) | < 1 ms | semua |
| `BenchmarkSummarize1MB` (pipeline A.3) | < 15 ms | semua |

Job `perf` (Deliverable F1 #0, runner tetap, ADR-073) menegakkan benchmark di atas; ambang ×1,5 di runner bersama hanya untuk metrik waktu absolut (SPEC §17, ADR-077). Acceptance F1 spawn p95 < 15 ms diukur di mesin referensi Q14.

### A.10 Invariant P24

| ID | Invariant | Angka | Verifikasi |
|---|---|---|---|
| P24.1 | 0 orphan | 0 proses bermarker `DAMNBOT_EXEC_ID` hidup ≤ 1 s setelah kill/timeout/akhir sesi (Linux/Windows); 0 setelah start berikutnya (macOS, pasca crash) | `TestNoOrphans`: fixture `setsid sleep 1000 &`, double-fork daemon, `nohup … &`, `disown`, `trap '' TERM`, `docker run -d`-style detach di backend fake, `kill -9` damnbot di tengah perintah (200 iterasi, mengikuti `TestKill9Chaos`). Matrix CI linux/macos/windows. |
| P24.2 | 0 hang | setiap call `shell` kembali ≤ `timeout_s` + `exec.kill_grace_s` (≤ 3 s, ADR-072) + 1 s; perintah yang menunggu input kembali ≤ 2 s setelah prompt tampil (pty) atau gagal cepat (pipe) | `TestInteractiveCorpus`: 40 fixture (apt tanpa `-y`, `git commit` tanpa `-m`, ssh host baru, `sudo`, `npm init`, `python -c 'input()'`, `read -p`, `less`, `vim`, `mysql -p`, `git rebase -i`, …) di runner pipe dan pty. Assert durasi + status ∈ {`exited`, `waiting_for_input`}. |
| P24.3 | Spawn overhead | p95 < 15 ms (Unix), < 40 ms (Windows) | A.9 benchmark, gate PR job perf |

Test cancel (ADR-072): `TestCancelConfirmed` (REL-06, F1: hasil ≤ 1 s setelah cancel, status `cancelled_confirmed` atau `still_running`), `TestCancelConfirmBeforeRedispatch` (F1: re-dispatch `non_idempotent` setelah `still_running` menunggu `cancelled_confirmed` atau konfirmasi user), `TestACPChildStdinIsolation` (REL-13, F3). Bagian test P24/EXC-09/EXC-11 yang membutuhkan `process_*` (background, `process_send`, reap waiting-for-input) adalah acceptance F5 (ADR-072).

Acceptance F1 P24 (ADR-077): `TestNoOrphans` (200 iterasi), `TestInteractiveCorpus` (40 fixture × pipe/pty), `BenchmarkShellSpawn*`, `TestCancelConfirmed`, `TestCancelConfirmBeforeRedispatch` (REL-13 di F3; Windows di matrix).

---

## B. Coding

### B.1 Loop coding sebagai aturan stable tier

Loop understand → plan → edit → verify → report ditulis sebagai blok aturan yang ditambahkan ke stable tier SPEC §6. Blok ini dihitung di dalam cap 1.200 tok; ≤ 120 tok, gate `TestStableTierBudget/coding`. Aturan yang sudah ada (baca sebelum edit, laporkan yang terverifikasi) tidak diulang (`TestPromptLint`).

```text
Coding:
11. Locate code with grep, glob or repo_map before reading whole files.
12. For changes touching several files, record a short plan with todo first.
13. Make the smallest edit that solves the task; match the surrounding style.
14. After editing, run the narrowest verify command from <workspace>; fix failures before answering.
15. Do not commit, push or delete branches unless the user asked.
```

- Estimasi ≈ 75 tok `[INFERENCE]`, sekitar 0,1× `CODING_AGENT_GUIDANCE` hermes yang 3.021 byte (`agent/coding_context.py:84-132`).
- Aturan berhenti setelah ~3 percobaan lint di hermes digantikan batas iterasi P2 dan perilaku [SELF-HEALING.md](SELF-HEALING.md).
- Edit memakai `edit_file{path, tag, edits[]}` (anchored, tag 8 hex dari header `read_file`/`grep`) dan `write_file{…, tag}` sesuai [CODE-TOOLS.md](CODE-TOOLS.md) §5.1, §5.2, §5.6.

### B.2 Deteksi toolchain dan verify terdampak

Deteksi berjalan di `internal/code/toolchain`:
- deterministik, tanpa LLM, tanpa spawn kecuali `go list` saat memilih target terdampak;
- membaca marker di root workspace + ancestor setiap file yang berubah (maks 8 level);
- di-cache per sesi dan diinvalidasi fsnotify pada file marker;
- target ≤ 20 ms untuk workspace 10.000 file (`BenchmarkToolchainDetect`).

| Marker | Toolchain | Verify penuh | Verify terdampak (file berubah → target) |
|---|---|---|---|
| `go.mod` | go | `go build ./... && go test ./...`, `go vet ./...` | dir berubah → paket. `go list -deps -test -json ./...` di-invert → paket + reverse-deps di modul. ≤ 20 paket → `go test <pkgs>`, else penuh. |
| `Cargo.toml` (+ `[workspace]`) | cargo | `cargo test --workspace`, `cargo clippy` (jika terpasang) | `Cargo.toml` terdekat → crate → `cargo test -p <crate>` |
| `package.json` + `pnpm-lock.yaml` / `yarn.lock` / `bun.lockb`\|`bun.lock` / `package-lock.json` | pnpm / yarn / bun / npm (lockfile menang; tanpa lockfile → npm) | `<pm> run test` / `lint` / `typecheck` bila script ada | jest: `<pm> exec jest --findRelatedTests <files>`; vitest: `<pm> exec vitest related <files> --run` |
| `tsconfig.json` | tsc | `<pm> exec tsc --noEmit -p <tsconfig>` | tsconfig terdekat dari file berubah |
| `.eslintrc*` / `eslint.config.*` | eslint | `<pm> exec eslint .` | `eslint <files>` |
| `pyproject.toml` (`[tool.pytest]`, `[tool.ruff]`), `pytest.ini`, `setup.cfg`, `ruff.toml` | pytest, ruff | `pytest -q`, `ruff check .` | file `tests/**/test_*.py` berubah → langsung. `pkg/mod.py` → `test_mod.py`/`mod_test.py` di tree test. Tidak ketemu → penuh. `ruff check <files>` |
| `Makefile` (target `test`/`check`/`lint`) | make | `make test` (prioritas `check` > `test` > `lint`) | — (penuh) |
| `CMakeLists.txt` | cmake | `cmake --build build && ctest --test-dir build` | — |
| `pom.xml` | maven | `mvn -q test` | modul terdekat → `mvn -q -pl <mod> -am test` |
| `build.gradle(.kts)` / `settings.gradle*` | gradle (`./gradlew` bila ada) | `./gradlew test` | `:<project>:test` |
| `scripts/run_tests.sh` | custom | perintah itu sendiri (parity `agent/coding_context.py:52-58`) | — |

- Workspace snapshot (B.4) menampilkan ≤ 2 perintah verify.
- Pemilihan terdampak dipakai oleh B.6 dan disarankan ke model lewat `Meta.verify_hint` pada hasil edit.
- Perintah verify (agent maupun runtime) mengikuti approval A.8: auto hanya bila perintah itu persis ada di `shell_allow` dengan semua syaratnya; selain itu interaktif → tanya, headless → tidak dijalankan (B.6). Tidak ada `exec.auto_allow_verify` (ADR-079 butir 5).
- Akurasi diuji `TestToolchainCorpus`: 30 fixture repo, 100% perintah sesuai golden.

### B.3 Diagnostik setelah edit

Setiap `edit_file`/`write_file` yang sukses pada file kode menambahkan `Meta.diagnostics`. Diagnostik deterministik, tanpa LLM. Client LSP sama dengan tool deferred `code_diagnostics{path}` ([CODE-TOOLS.md](CODE-TOOLS.md) §6.3), jadi tidak ada proses language server kedua.

1. **Sintaks lokal** (selalu): Go via `go/parser`; bahasa lain via tree-sitter WASM (node `ERROR`/`MISSING`, ADR-015); JSON/TOML/YAML via parser Go. Target ≤ 30 ms p95 untuk file 5.000 baris (`BenchmarkPostEditSyntax`).
2. **LSP** (modul lazy `code-intel`, `code.lsp = "auto"`):
   - Server terpasang (gopls, rust-analyzer, pyright/basedpyright, typescript-language-server, jdtls, clangd) di-start saat edit pertama pada bahasa itu.
   - `didChange` versioned, lalu tunggu `publishDiagnostics` untuk versi tersebut maks 1.500 ms.
   - Lewat batas: `diagnostics_pending:true`. Diagnostik terlambat ditempelkan ke tool result berikutnya pada file yang sama, atau ke `turn_result`.
   - Tidak pernah memaksa iterasi tambahan (beda dengan hermes, `agent/turn_stop_gates.py:95`).
3. **Delta:** hanya diagnostik baru dibanding baseline sebelum edit, diurutkan error → warning, lalu jarak ke region edit.

Format tiap baris: `path:line:col: error|warning: msg [source]`. Cap 400 tok (o200k) inline; sisanya di-spill sesuai P5 dengan `diagnostics_more:N` + path. Syntax error yang tidak terdeteksi pada korpus `TestPostEditDiagCorpus` (200 edit rusak, 6 bahasa) = 0.

### B.4 Repo map dan workspace snapshot

**Tool deferred** `repo_map{focus? ≤ 200 char, path?, max_tokens 200–1000 (default 600)}`:
- Simbol (definisi + referensi) diambil dari `go/ast` / tree-sitter tags (mesin di [CODE-TOOLS.md](CODE-TOOLS.md)).
- Graf file→file berbobot jumlah referensi diperingkat dengan PageRank personalisasi (damping 0,85, 20 iterasi).
- Vektor personalisasi:
  - file yang disebut di `focus`/history turn: bobot 10;
  - file yang diedit sesi ini: 5;
  - hit BM25 nama simbol terhadap term `focus`: 1–3.
- Output: per file `path` lalu signature simbol teratas (indent 2), dipotong tepat di `max_tokens` (o200k), sehingga cap keras 1.000 tok tidak bisa dilampaui.
- Cache: `<data_dir>/index/<ws-hash>/repomap.bin`, diinvalidasi fsnotify per file + verifikasi (mtime, size).
- Target: cold ≤ 1,5 s @ 10.000 file, warm ≤ 50 ms p95 (`BenchmarkRepoMap`). Relevansi: file gold patch masuk top-10 pada ≥ 70% task subset SWE-bench B.C (`TestRepoMapRecall`, nightly).

**Workspace snapshot** (konteks tier SPEC §6, dibekukan saat sesi mulai):
- ≤ 150 tok, bagian dari cap 1.100;
- isi: nama root, branch + upstream ahead/behind, jumlah file dirty, bahasa dominan (≤ 3), toolchain + ≤ 2 perintah verify, ≤ 8 direktori top-level;
- ekuivalen dengan snapshot hermes (`agent/coding_context.py:538-592`) tetapi tanpa daftar commit/context files; commit terbaru tersedia lewat `git log` bila dibutuhkan;
- gate `TestWorkspaceSnapshotBudget`.

### B.5 Git awareness

- **Ringkas:** runtime membaca `git status --porcelain=v2 -b -z` sendiri (hanya untuk snapshot/notice) dan merendernya ≤ 20 baris + `+N more`. Model memakai `shell` biasa untuk diff (ditanya kecuali persis cocok `shell_allow`, A.8, ADR-079); output diff melewati A.3.
- **Perubahan user di luar agent:**
  - Pada awal turn, runtime membandingkan sha file yang pernah dibaca/diedit sesi ini (`file_snapshots.after_sha`, sha baca terakhir) dengan isi disk.
  - Perbedaan → `system_notice` `{kind:"user_changed", paths[≤10]}` (≤ 40 tok).
  - Edit ke file tersebut dengan tag lama → `stale_read` (tag check [CODE-TOOLS.md](CODE-TOOLS.md) §5.2). Model wajib `read_file` ulang untuk mendapat tag baru. Agent tidak pernah menimpa perubahan user.
- **Checkpoint:**
  - `file_snapshots` (before/after sha per mutasi) adalah sumber rollback yang otoritatif, dengan atau tanpa git.
  - Saat kumulatif perubahan turn melewati ≥ 5 file atau ≥ 300 baris berubah, dan `git.checkpoint_refs = true` (default), runtime membuat ref `refs/damnbot/checkpoints/<turn_id>` berisi status worktree **sebelum turn**:
    1. Temp `GIT_INDEX_FILE`.
    2. `read-tree HEAD` + `add -A`.
    3. File yang disentuh turn di-`update-index --cacheinfo` ke blob `before_sha` (`hash-object -w`).
    4. `write-tree`.
    5. `commit-tree`.
  - Index dan worktree user tidak disentuh. Durasi ≤ 300 ms p95 di repo 10.000 file (`BenchmarkCheckpointRef`).
  - Ref dibersihkan setelah 14 hari (`damnbot doctor gc`).
- **Rollback satu perintah:** `damnbot undo --turn <id>` / `damnbot undo [N]` (`--force` untuk menimpa perubahan user) / TUI `/undo`:
  - Memulihkan `before_sha` setiap file di turn tersebut (`before_sha NULL` → file dihapus).
  - File dengan sha disk ≠ `after_sha` (diubah lagi sesudahnya) ditolak per file dengan laporan konflik, kecuali `--force`.
  - Hasil dicatat sebagai event `system_notice{kind:"rollback"}`.
  - Target 50 file ≤ 200 ms (`TestRollback`, `BenchmarkRollback50`).
- **Commit hanya bila diminta:** aturan B.1 #15. `git commit` = `mutating` (tanya di `ask`/`auto-edit`/`auto` kecuali persis cocok `shell_allow`, ADR-079), `git push --force` = `destructive` + `irreversible` (A.8). Runtime tidak pernah membuat commit di branch user; checkpoint hanya ref di namespace `refs/damnbot/`.

### B.6 Tanpa klaim tanpa bukti

`turn_result` mendapat field baru yang dibangun runtime, bukan model:

```json
"verification": [
  {"command": "go test ./internal/x/...", "exit_code": 0, "duration_ms": 4120,
   "source": "agent", "after_last_edit": true, "blob_sha": "…"}
]
```

`source ∈ {agent, runtime}`. Entri mencakup setiap perintah `shell` yang cocok dengan perintah verify B.2 (penuh atau terdampak, atau perintah test/build/lint yang dikenali classifier) serta setiap verify runtime.

`agent.verify_on_stop`:

| Mode | Perilaku | Biaya LLM |
|---|---|---|
| `off` | tidak menjalankan apa pun; `evidence_gap` tetap dihitung | 0 |
| `run` (default) | Saat turn akan berakhir `completed`: jika `files_changed` berisi file non-prosa (bukan `*.md`, `*.txt`, `*.rst`, `docs/**`) dan tidak ada entri `verification` dengan `exit_code=0` dan `after_last_edit=true`, runtime menjalankan ≤ 2 perintah verify terdampak (B.2). Timeout total `verify.timeout_s` 300 s. Event `verify_run`, hasil ke `verification[]` dengan `source:"runtime"`. Jawaban model tidak diubah; UI/footer menampilkan status. | 0 |
| `repair` (opt-in) | Seperti `run`. Jika gagal: 1 iterasi LLM tambahan, dihitung terhadap batas 40 (P2) dan budget. Iterasi menerima `system_notice` berisi ringkasan kegagalan ≤ 1.500 char (A.3). Setelah edit perbaikan, verify dijalankan ulang sekali. Tidak ada repair kedua. | ≤ 1 iterasi |

- Verify runtime mengikuti approval A.8 (ADR-079 butir 5; `exec.auto_allow_verify` dihapus): auto hanya bila perintah verify persis ada di `shell_allow` dengan semua syaratnya. Selain itu interaktif (`ask`/`auto-edit`/`auto`) → tanya; headless → tidak dijalankan, jawaban ditandai `unverified` + `evidence_gap`. Headless `--unsafe-auto-shell` menjalankan verify berlabel `mutating` bila workspace trusted dan turn tidak tainted (A.8). Test: `TestVerifyNeedsShellAllow`.
- Default `run` berlaku di semua surface. Gateway hanya menampilkan baris footer (bandingkan hermes yang mematikan verify di messaging, `agent/verification_stop.py:53-79`).
- `evidence_gap` (SPEC §7.2) dihitung **setelah** verify. Klaim verifikasi di jawaban tanpa entri `verification` sukses `after_last_edit` → `true`. Turn yang mengubah kode, gagal verify, dan jawabannya mengklaim sukses → `evidence_gap:true` + `verification_failed:true`.
- Test:
  - `TestVerifyOnStopRun`: 0 `llm_call` tambahan, 1 `verify_run`;
  - `TestVerifyOnStopRepair`: tepat ≤ 1 iterasi tambahan;
  - `TestEvidenceGapAfterVerify`.

### B.7 `execute_code`

Schema deferred:

```text
execute_code{language* enum[python,javascript,typescript,go,bash,starlark], code* ≤ 100.000 char, timeout_s 1–300 (default 30; starlark 1–30, > 30 → invalid_args tanpa clamp, ADR-055), stdin ≤ 65.536 char, network enum[deny,allow] (default deny), persist bool}
```

**Interpreter sistem** (tidak di-bundle):

| Bahasa | Interpreter |
|---|---|
| python | `python3`/`py -3` |
| javascript | `node`, else `bun`, else `deno run` |
| typescript | `bun`, else `deno`, else `node --experimental-strip-types` (Node ≥ 22,6) |
| go | `go run` di modul temp (`go.mod` minimal, `GOFLAGS=-mod=mod`, `GOPROXY` dari env user) |
| bash | `bash` |
| starlark | embedded (`go.starlark.net`), dijalankan di proses anak `damnbot __starlark`; tidak butuh interpreter sistem (GAP-COST-10, SPEC §9.2) |

Interpreter tidak ditemukan → `module_unavailable` + instruksi instalasi.

**Isolasi direktori:**
- cwd = `<state_dir>/exec/code/<id>/` (0700), dihapus setelah selesai kecuali `persist`.
- `TMPDIR`/`HOME` diarahkan ke sana; `DAMNBOT_WORKSPACE` = root sesi (akses baca/tulis tetap lewat path absolut).
- `code.cwd = "workspace"` memberi parity `code_execution.mode: project` hermes ([FEATURE-PARITY.md](FEATURE-PARITY.md) EXEC-012). `damnbot import hermes` memetakannya otomatis.

**Env:** allowlist (`PATH`, `LANG`, `LC_*`, `TZ`, `TERM`, `SYSTEMROOT`, `COMSPEC`, `PATHEXT`, `GOPATH`, `GOCACHE`, `GOPROXY`) + `DAMNBOT_*`. Tidak ada secret. Parity dengan scrub hermes (`tools/code_execution_env.py:25-97`), tetapi memakai allowlist murni.

**Panggilan tool dari skrip** (parity `tools/code_execution_tool.py:43-45`):
- Socket RPC `DAMNBOT_TOOL_SOCKET` (unix socket 0600; Windows named pipe ber-ACL user).
- Modul klien kecil ditulis ke cwd: `damnbot_tools.py`, `damnbot_tools.mjs`; starlark memakai builtin `tool(name, **args)` lewat pipe JSON-RPC privat ke proses induk.
- Tool yang diizinkan: `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `web_fetch`, `shell` (tanpa `background`/`session`). Binding `tool()` starlark hanya ke 7 tool ini; tool lain → `invalid_args` (ADR-055).
- Setiap call melewati pipeline penuh SPEC §4.3 (schema, sandbox, approval) dan tercatat sebagai event anak.
- Maks 50 call per eksekusi; melebihi → `too_large`.
- **`network = "deny"` mencakup RPC (ADR-076):** RPC/binding ke `web_fetch` → `denied{reason:"network_deny"}`; `shell` lewat RPC dijalankan dengan penegakan jaringan yang sama (netns/sandbox-exec), dan bila penegakan tidak tersedia di OS itu → `denied`. Test F5: `TestExecuteCodeRPCNetworkDeny`.

**`persist:true`** (python, javascript):
- Interpreter long-lived per sesi dengan driver embedded (frame panjang+payload lewat pipe). State variabel bertahan.
- Idle 30 menit → reap (parity idle kernel `tools/code_execution_tool.py:687`). Maks 2 kernel per sesi.

**Limit:**

| Limit | Linux | macOS | Windows |
|---|---|---|---|
| Waktu | `timeout_s` + kill A.4 (starlark maks 30 s, ADR-055) | sama | sama |
| Memori 512 MiB (`code.memory_mb`, 64–4.096) | cgroup v2 sub-group delegasi user (`memory.max`, `pids.max=256`) bila writable; else shim `damnbot __exec-shim` `setrlimit` (`RLIMIT_AS` untuk python/bash; `--max-old-space-size` untuk node; `GOMEMLIMIT` + `RLIMIT_DATA` untuk go; starlark: proses anak `damnbot __starlark` `RLIMIT_AS` 256 MiB) | shim `setrlimit` (`RLIMIT_DATA`, `RLIMIT_NOFILE=256`, `RLIMIT_FSIZE=64 MiB`, `RLIMIT_CPU`); starlark: shim `setrlimit` `RLIMIT_DATA` 256 MiB untuk `damnbot __starlark`, tanpa `ulimit -v` (ADR-055) | Job Object `JOB_OBJECT_LIMIT_JOB_MEMORY`, `ActiveProcessLimit=64`; starlark: Job Object 256 MiB (ADR-055) |
| Jaringan `deny` | `CLONE_NEWUSER\|CLONE_NEWNET` via `SysProcAttr.Cloneflags` (loopback saja) | `sandbox-exec` profil `(deny network*)` | tidak ada penegakan |
| Output | pipeline A.3 + P5 | sama | sama |

- Shim = binary damnbot sendiri yang di-re-exec. Shim memasang limit lalu `execve` interpreter, jadi tetap pure Go.
- Penegakan yang tidak tersedia dilaporkan eksplisit: `Meta.isolation = {memory: "cgroup"|"rlimit"|"job"|"none", network: "netns"|"sandbox-exec"|"none"}`.
- `network:"deny"` tanpa penegakan → approval dinaikkan ke `destructive`-level (tanya, termasuk di `auto`). Konsekuensinya tidak pernah diam-diam.
- Test `TestExecuteCodeLimits` per OS: alokasi 1 GiB → OOM/`exit≠0` terlapor; `socket.connect` ke 1.1.1.1 gagal saat `deny` (Linux, macOS); 51 tool call → `too_large`.

---

## C. Evaluasi (SCORECARD, gate 1.0)

Harness `damnbot-eval` (F0, SCORECARD) menjalankan Damn-Bot dan hermes pada kondisi yang sama:
- model sama (di-pin di `eval/model.toml`, termasuk versi provider);
- temperatur 0 bila didukung;
- cap budget per task identik (iterasi 40, wall 30 menit);
- container bersih per task;
- hermes memakai config default yang hash-nya dicatat di artefak.

**Set terkunci** (daftar ID di repo; SHA-256 file dicatat di SCORECARD; perubahan butuh ADR):

| Set | File | Isi | Seleksi deterministik |
|---|---|---|---|
| SWE-bench Verified | `eval/sets/swebench-verified-100.txt` | 100 instance_id | urut `sha256("damnbot-1.0:" + instance_id)`, ambil 100 pertama dengan stratifikasi proporsional per repo (pembulatan largest-remainder) |
| Terminal-Bench | `eval/sets/terminal-bench-50.txt` | 50 task id (Terminal-Bench 2.0) | aturan hash yang sama, stratifikasi per kategori |
| Internal multi-bahasa | `eval/sets/internal-60/` | 60 task: 10 per Go, Python, TypeScript, Rust, Java, shell. Per bahasa: 3 bugfix, 2 fitur, 2 refactor, 1 build-fix, 2 trap (prompt interaktif, server background, output > 1 MB, perubahan user di tengah task) | ditulis tim; tiap task punya test tersembunyi + `expect.json` (file yang boleh berubah, perintah verify) |

**Metrik per task**, run n = 3 per agent per task:

| Metrik | Definisi | Target Damn-Bot |
|---|---|---|
| pass rate | test tersembunyi lulus / task, rata-rata 3 run | ≥ hermes (gate) |
| tokens/task | input + output (termasuk cached), dari ledger / log usage hermes | ≤ 0,6× hermes (gate) |
| wall time/task | start → stop agent | median ≤ 0,5× hermes; tidak ada task > 1,10× ([SCORECARD.md](SCORECARD.md) §3.2) |
| hang rate | run yang mencapai wall cap tanpa stop reason, atau tool call yang melewati `timeout_s` + 4 s | 0 |
| orphan rate | run dengan ≥ 1 proses turunan agent masih hidup 5 s setelah agent exit (scan container) | 0 |
| wrong-location edit rate | edit tercatat yang `before_sha`-nya ≠ sha isi terakhir yang dibaca model, atau yang menimpa perubahan user (task trap) / total edit | 0 |
| claim-without-evidence rate | turn final dengan `evidence_gap=true`. Hermes dinilai dengan detector yang sama atas jawaban + log tool-nya. | 0 |

**Uji statistik:**
- Pass rate: selisih berpasangan per task (rata-rata 3 run), paired bootstrap 10.000 resample atas task, CI 95%.
- Tokens/task: rasio rata-rata geometrik per task, CI 95% bootstrap + Wilcoxon signed-rank.
- Gate rilis 1.0 (P20, job `scorecard` di RC menjalankan `TestScorecardGate`, V-20/ADR-054), per set dan gabungan:
  1. estimasi pass rate Damn-Bot ≥ hermes dan batas bawah CI selisih ≥ −2 pp;
  2. rasio tokens/task ≤ 0,60 dan batas atas CI ≤ 0,65;
  3. hang, orphan, wrong-location, dan claim-without-evidence = 0 di seluruh 630 run Damn-Bot (210 task × 3).

---

## D. Ringkasan angka

| ID | Metrik | Target | Verifikasi |
|---|---|---|---|
| P24.1 | Proses orphan | 0 (≤ 1 s pasca kill; macOS: start berikutnya) | `TestNoOrphans`, matrix linux/macos/windows |
| P24.2 | Hang `shell` | 0; kembali ≤ `timeout_s` + 4 s; prompt terdeteksi ≤ 2 s | `TestInteractiveCorpus` (40 fixture) |
| P24.3 | Spawn overhead p95 | < 15 ms Unix, < 40 ms Windows | `BenchmarkShellSpawnPipe`, `BenchmarkShellSpawnPTY`, `BenchmarkShellSpawnWindows` |
| EXC-01 | Schema eager `shell` | ≤ 200 tok, deskripsi ≤ 160 char | `TestEagerSchemaTokens/shell` |
| EXC-02 | Timeout default / maks | 120 s / 600 s | `TestShellTimeout` |
| EXC-03 | Idle-timeout | 60 s (10–600), CPU < 1%, I/O < 64 KB | `TestShellIdleTimeout` |
| EXC-04 | Grace TERM→KILL; cancel | 3 s (1–3, > 3 ditolak config); hasil cancel ≤ 1 s, status `cancelled_confirmed`/`still_running` (ADR-072) | `TestKillSequence`, `TestCancelConfirmed`, `TestCancelConfirmBeforeRedispatch` |
| EXC-05 | Output inline / preview | ≤ 4.000 char / 800 (160 head + 240 error + 400 tail) | `TestShellSummary`, `TestSpill` |
| EXC-06 | Baris error tersimpan di preview | 100% (≤ 3 baris) pada korpus | `TestShellSummaryCorpus` |
| EXC-07 | Pipeline ringkasan 1 MB | < 15 ms p95 | `BenchmarkSummarize1MB` |
| EXC-08 | Sesi persisten exec | < 5 ms p95; maks 4 sesi; idle reap 30 menit | `BenchmarkShellSessionExec`, `TestShellSessionLimits` |
| EXC-09 | Proses background (F5, ADR-072); F1: `background:true` → `invalid_args` | 8/sesi, 32 global, log ring 8 MiB, retensi 30 menit/64 | `TestProcessLimits` (F5) |
| EXC-10 | Notice proses selesai | ≤ 40 tok, ≤ 3/request, 0 LLM call | `TestProcessNotice`, `TestNoBackgroundCalls` |
| EXC-11 | Waiting-for-input | F1: pohon di-kill, `killed:true`, tanpa `process_id`; F5: reap 10 menit (ADR-072) | `TestInteractiveCorpus` (F1), `TestWaitingInputReap` (F5) |
| EXC-12 | Classify perintah 2 KB | < 1 ms p95 | `BenchmarkClassify` |
| EXC-13 | Shell tanpa approval (ADR-079) | 0 perintah dieksekusi tanpa approval di mode `ask`/`auto-edit`/`auto` dengan `shell_allow` kosong, pada korpus ≥ 300 perintah (termasuk obfuscation quoting/`eval`/`sh -c`); auto hanya untuk kecocokan argv-persis `shell_allow` (≥ 60 kasus metakarakter/ekspansi/redirect/assignment/pipeline → tidak auto); 36 baris tabel ADR-079 persis; output shell di workspace untrusted dibungkus + men-taint | `TestShellNoClassifierAuto`, `TestShellApprovalModel`, `TestShellAllowExactMatch`, `TestShellAllowCwdTracked`, `TestShellOutputUntrustedWorkspace`, `TestWorkspaceTrustGate` |
| EXC-14 | Destructive/irreversible/privileged/baca di luar root di mode `auto`; headless `--unsafe-auto-shell`; verify | 100% tanya interaktif, tanpa pengecualian (ADR-069, ADR-079); headless `--unsafe-auto-shell`: hanya `mutating` di workspace trusted + turn tidak tainted, selain itu `denied`; verify auto hanya bila persis di `shell_allow` (ADR-079) | `TestDestructiveAlwaysAsk`, `TestShellIrreversibleAlwaysAsk`, `TestSudoAlwaysAsk`, `TestReadOutsideWorkspaceAsks`, `TestUnsafeAutoShellFlag`, `TestVerifyNeedsShellAllow` |
| EXC-15 | Env anak (GAP-SEC-02) | 0 var di luar allowlist SEC-02 + `exec.env_allow` + `exec.env` (ADR-071) | `TestEnvAllowlist` |
| EXC-16 | Kontrak backend | 100% test kontrak lulus per backend | `TestBackendContract/<id>` |
| COD-01 | Aturan coding stable tier | ≤ 120 tok (dalam 1.200) | `TestStableTierBudget/coding` |
| COD-02 | Deteksi toolchain | ≤ 20 ms @ 10.000 file; 100% golden 30 fixture | `BenchmarkToolchainDetect`, `TestToolchainCorpus` |
| COD-03 | Diagnostik sintaks pasca-edit | ≤ 30 ms p95 @ 5.000 baris; 0 miss pada 200 edit rusak | `BenchmarkPostEditSyntax`, `TestPostEditDiagCorpus` |
| COD-04 | Diagnostik LSP | tunggu ≤ 1.500 ms; cap 400 tok inline | `TestLSPDiagnosticsDelta` |
| COD-05 | Repo map | cap 1.000 tok (default 600); cold ≤ 1,5 s, warm ≤ 50 ms @ 10.000 file; recall top-10 ≥ 70% | `BenchmarkRepoMap`, `TestRepoMapRecall` |
| COD-06 | Workspace snapshot | ≤ 150 tok | `TestWorkspaceSnapshotBudget` |
| COD-07 | Checkpoint ref | ambang ≥ 5 file / ≥ 300 baris; ≤ 300 ms p95; GC 14 hari | `BenchmarkCheckpointRef`, `TestCheckpointThreshold` |
| COD-08 | Rollback | 50 file ≤ 200 ms; 0 timpa perubahan user tanpa `--force` | `TestRollback`, `BenchmarkRollback50` |
| COD-09 | Verify-on-stop `run` | 0 LLM call tambahan; ≤ 2 perintah; timeout 300 s | `TestVerifyOnStopRun` |
| COD-10 | Verify-on-stop `repair` | ≤ 1 iterasi ekstra (dalam 40) | `TestVerifyOnStopRepair` |
| COD-11 | execute_code | timeout 30 s (1–300), memori 512 MiB, ≤ 50 tool call, kernel persisten ≤ 2, idle 30 menit; starlark: `timeout_s` 1–30 (> 30 → `invalid_args`), 256 MiB, binding hanya 7 tool §B.7 (ADR-055) | `TestExecuteCodeLimits` |
| EVL-01 | Pass rate vs hermes | ≥ hermes; CI bawah selisih ≥ −2 pp | job `scorecard`, paired bootstrap |
| EVL-02 | Tokens/task vs hermes | ≤ 0,60× (CI atas ≤ 0,65×) | job `scorecard`, Wilcoxon |
| EVL-03 | Wall time/task | median ≤ 0,5× hermes; tidak ada task > 1,10× (SCORECARD §3.2) | job `scorecard` |
| EVL-04 | Hang / orphan / wrong-location / claim-without-evidence | 0 / 0 / 0 / 0 dari 630 run | job `scorecard` |
| EVL-05 | Set eval terkunci | 100 SWE-bench Verified + 50 Terminal-Bench + 60 internal, n = 3 | SHA-256 file set di SCORECARD |
