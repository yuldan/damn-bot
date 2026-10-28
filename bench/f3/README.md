# F3 baseline harness

This directory measures the three dimensions that ADR-083 marks **F3**:
`First token di UI (TUI)`, `Error informatif`, and `Waktu build bersih /
inkremental`. It does not add P12/P13/P17/P18, setup, import, or ACP metrics to
the baseline: those already have F0 data or an `n/a` baseline.

## Run

```sh
bench/f3/run.sh
# optional: RUNS=20 BUILD_RUNS=3 OUT=bench/f3/results/damnbot-<sha>-F3.json

bench/f3/hermes.sh          # Hermes side (same PTY + SSE shape, both surfaces)
bench/f3/hermes_tui.sh      # builds the Ink TUI bundle hermes.sh measures
bench/f3/hermes_errors.sh   # supplementary Hermes failure captures
```

Suite bahan penilaian **Error informatif** dijalankan terpisah:
`bench/f3/run-errors.sh` (lihat [`errors/README.md`](errors/README.md)).

`run.sh` builds the real `damnbot` binary with `goolm,bench`, starts the Go
driver, and writes `bench/f3/results/damnbot-<measured-sha>-F3.json`. The
`bench` build tag only compiles `cmd/damnbot/bench_tui.go`, an environment
seam that points the TUI at a loopback fake provider; the normal `goolm` build
has no such path.

## Method

### First token, TUI (Damn-Bot side)

1. Start a loopback OpenAI-compatible SSE server. It records `T0` immediately
   before writing the first `content` frame containing `F3TOKEN`, and `T1` is
   the instant that token appears in the PTY output.
2. Run the real `damnbot` binary (no subcommand) under `script(1)`, with
   `TERM=xterm-256color`, a private `HOME`/`XDG_CONFIG_HOME`, and a minimal
   `config.toml`; wait for the TUI footer, type a prompt, and read the PTY.
3. The sample is `T1 - T0` in milliseconds. `N=20` samples are written to
   `samples_ms`; `p50_ms`/`p95_ms` use nearest-rank.

The seam is deliberately bench-only: production TUI has no fake-provider flag.
The artifact records `bench/f3/run.sh` and the exact binary tag.


### P12 first frame (Damn-Bot side)

The same PTY runner also measures **P12**: `T0` immediately before `cmd.Start()`
of the real binary (with the `script(1)`/`stty` wrapper) and `T1` when the first
frame marker `enter send` is visible. Twenty samples are recorded; `p95_ms`
must be `<= 100` or the driver exits non-zero (it writes the artefact first so
the failure is inspectable). `GOMAXPROCS=1` is measured in a second 20-sample
pass and reported as a non-gate number.

The `bench` build tag also emits `F3TRACE` lines (`config-done`, `app-built`,
`run-start`, and `ensure-start`) when `DAMNBOT_BENCH_TRACE=1`. The lazy-start
check fails if `ensure-start` (the first ctxdb/provider open) appears before the
frame marker; `config-done`/`app-built`/`run-start` must appear before it. A
missing trace also fails, so the proof cannot pass silently.

### Build time (Damn-Bot side)

For each sample the driver extracts `git archive HEAD` into a private tree and
runs `go build -tags goolm ./cmd/damnbot` with an **empty** `GOCACHE`; this is
the clean build. It then appends one comment to `internal/loop/loop.go` and
rebuilds with the same cache; this is the incremental build. Times are seconds;
three pairs are recorded by default. The module cache is warm, matching
ENGINEERING E-62. The machine is a workstation, not Q14; `meta.loadavg` and CPU
are recorded so the numbers are not presented as reference-hardware gates.

### Error informativeness

Bahan penilaian ada di [`errors/README.md`](errors/README.md): **60 kasus
nyata / 12 grup × 5**, capture tersanitasi, rubrik v1, dan gate mekanis
**60/60 exit ≠ 0, 0 kebocoran secret, 0 panic, 60/60 pesan tidak kosong**.
Gate itu tidak menilai kualitas pesan.

Dua rater LLM independen sudah memberi **skor proxy** (`errors/scores-proxy.json`);
angka itu **bukan ratifikasi manusia, bukan gate**, dan tidak masuk
`errors/summary.json`. `LULUS` final untuk dimensi ini tetap menunggu penilai
manusia. Karena itu field `error_informatif` pada artefak F3 baseline tetap
`blocked` sampai ratifikasi manusia tersedia; jangan menggantinya dengan skor
proxy. Ambiguitas rubrik dicatat di `errors/rubric-notes.md` (usulan v1.1);
`rubric.json` v1 tidak diubah agar skor A/B tetap sebanding.

## Hermes side (868b9f244f)

The F3 artifact field `hermes` is `measured` and the numbers live in
[`results/hermes-868b9f244f-F3.json`](results/hermes-868b9f244f-F3.json).
Reproduce both surfaces with `bench/f3/hermes.sh` (`RUNS=`, `TUI_RUNS=`,
`BUILD_RUNS=`, `TUI_BUILD_RUNS=` override the sample counts). The checked-in
artifact was produced in two passes so the earlier CLI numbers survive
verbatim: the first pass measured the classic REPL and the Python install on
2026-10-28, and the second added the TUI with

```sh
SURFACE=tui REUSE=bench/f3/results/hermes-868b9f244f-F3.json bench/f3/hermes.sh
```

`--surface cli|tui|both` and `--reuse <artifact>` are the underlying flags.

Runtime provisioning is private and does not touch the system Python:

```sh
# python-build-standalone 3.14.10+20261026, x86_64-unknown-linux-gnu,
# install_only_stripped (sha256 4d3c16c59ed0802a3d1d34d8bce504b15ceeaff39d62dadecc6a9fa803412aed)
tar -xzf cpython-3.14.10+20261026-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz \
  -C ~/.cache/damnbot/py314
~/.cache/damnbot/py314/python/bin/python3.14 -m venv ~/.cache/damnbot/hermes-venv
cd ~/.cache/damnbot/f2-inputs/hermes-src
~/.cache/damnbot/hermes-venv/bin/pip install -e .
```

`pip install -e .` is the editable path the checkout's own `setup.py` documents;
a non-editable wheel is refused by that file's build guard (`RuntimeError:
Building wheels or sdists for hermes-agent is not supported.`). The first
install took **37 s** with an empty pip cache on this workstation. All 45 core
pins resolved on PyPI; no native build failed, and `pip check` reports
`No broken requirements found.` `hermes --version` reports
`Hermes Agent vunknown (2026.9.24)` because the vendored tree has no `.git` and
no install stamp, so the commit identity still comes from the F0 artefact
(`868b9f244f`), not from the binary. `hermes chat --help` exits 0 with no
network: under `strace -f -e trace=network` it creates a socket but issues no
`connect`.

The Ink TUI needs a Node build; the vendored checkout ships neither
`node_modules` nor `hermes_cli/tui_dist`:

```sh
bench/f3/hermes_tui.sh    # npm ci --workspace ui-tui; npm run build; publish tui_dist
```

`hermes --tui` then loads `hermes_cli/tui_dist/entry.js`. The launcher resolves
Node through PM by default; that path tried to provision its own Node under
`HERMES_HOME` and failed (its binary needs `libatomic.so.1`, and the
non-interactive `sudo apt-get` cannot authenticate), so the harness pins the
system Node explicitly with `HERMES_NODE=/usr/bin/node` and
`HERMES_PYTHON=$HERMES_VENV/bin/python`. This is a workstation-provisioning
limitation, not a TUI requirement.

### First token (Hermes side)

Method: the same timestamped loopback OpenAI-compatible SSE server as the
Damn-Bot side, on both Hermes surfaces. `T0` is immediately before the fake
provider writes the first `content` frame carrying `F3TOKEN`; `T1` is the
instant `F3TOKEN` appears in the PTY. The classic REPL gets the prompt typed,
then Enter 300 ms later (it treats an Enter within 50 ms of the last text
change as a pasted newline); the TUI submits with one Enter once its composer
frame is painted. `N=20`, nearest-rank. No real provider or credential is used,
so the run costs nothing.

| surface | first token p50 | first token p95 | cold | notes |
| --- | --- | --- | --- | --- |
| **Ink TUI** (`hermes --tui --yolo`) | **39.4 ms** | **46.9 ms** | 56.0 ms | fresh Node app + Python gateway per sample; only OS caches warm |
| classic REPL (`hermes chat --cli --yolo`) | **148.7 ms** | **192.9 ms** | 210.1 ms | 20 samples reuse one warm detached gateway |

The TUI sample is about 3.8× lower than the classic REPL on this harness. Both
surfaces go through the Python gateway; the measured difference is the render
path — Ink repaints each streamed delta, while the classic REPL routes the same
text through its rich console. That attribution comes from the code paths, not
from per-component instrumentation.

#### Comparison with Damn-Bot

| surface | first frame | first token p95 | start command |
| --- | --- | --- | --- |
| Damn-Bot TUI (reference) | p95 **44.4 ms** (P12, `b9247ef4`; **50.8 ms** in the checked-in `#386` artifact) | p95 **17.5 ms** (**18.4 ms** in the `#382` artifact) | `damnbot` |
| Hermes Ink TUI | p95 **1518.4 ms** (chrome frame; first byte 109.2 ms) | p95 **46.9 ms** | `hermes --tui --yolo` |
| Hermes classic REPL | — (first byte p95 2725.9 ms; prompt ready p95 7081.0 ms) | p95 **192.9 ms** | `hermes chat --cli --yolo` |

The Damn-Bot reference row is `docs/F3-ACCEPTANCE.md` (rerun `b9247ef4`, local,
load-affected); the parenthesised values are the earlier checked-in artifact
(`bench/f3/results/damnbot-6d01e7a1d5-F3.json`, PR `#382`/`#386`). The frame
markers are not identical: Damn-Bot gates on its `enter send` footer, while
Hermes's first frame is Ink's first painted chrome frame (`summoning hermes`).

### Startup / first frame (Hermes side)

TUI; `T0` is before `pty.fork`/`exec` of `hermes --tui`:

- first PTY byte (Ink's initial mouse-mode reset): p50 **94.9 ms**, p95
  **109.2 ms**;
- first painted chrome frame: p50 **1368.3 ms**, p95 **1518.4 ms**;
- composer ready (intro + `Available Tools`): p50 **1834.4 ms**, p95
  **1968.8 ms**;
- cold run (fresh `HERMES_HOME`, first session + gateway): first byte
  **95.7 ms**, first frame **1379.0 ms**, prompt ready **7373.1 ms**.

Classic REPL, unchanged from the CLI pass:

- first byte: p50 **2617.2 ms**, p95 **2725.9 ms**;
- prompt ready: p50 **6767.8 ms**, p95 **7081.0 ms**;
- cold run (starts the gateway): first byte **2134.8 ms**, prompt ready
  **15759.0 ms**.

### Build/install time (Hermes side)

Python packaging has no compile step, so the Python "build" is dependency
installation. Both surfaces share this interpreter and gateway:

- **clean:** fresh `python -m venv` + `pip install --no-cache-dir -e <hermes-src>`
  (all dependencies downloaded from PyPI; venv creation included). Three
  samples: **66.28 s, 245.95 s, 41.15 s**; p50 **66.28 s**, p95 **245.95 s**.
  The 245.95 s sample is a PyPI/network stall on the workstation; the spread is
  real and is why the median, not the minimum, is reported.
- **incremental:** same venv, one comment appended to
  `hermes_cli/version_info.py`, `pip install --no-cache-dir --no-deps -e
  <hermes-src>` (rebuild the editable wheel and console scripts). Three
  samples: **4.84 s, 4.54 s, 4.76 s**; p50 **4.76 s**, p95 **4.84 s**.

The Ink TUI adds a Node build. `bench/f3/hermes_tui.sh` runs the two commands
and publishes the bundle; Node **v22.23.3** and npm **10.9.9** were used:

- install: `npm ci --workspace ui-tui --no-audit --no-fund` — **5.899 s,
  6.100 s, 6.426 s**; p50 **6.100 s**, p95 **6.426 s**. The workspace selector
  matters: a root `npm ci` also installs `apps/desktop`/`tests-js`, whose
  `node-pty` has no linux-x64 prebuild here and needs `g++` (absent); that run
  failed after 48.2 s. The TUI does not use `node-pty`.
- bundle: `cd ui-tui && npm run build` (`scripts/build/tui.mjs`, esbuild →
  `ui-tui/dist/entry.js`, 3.9 MB) — **0.693 s, 0.687 s, 0.717 s**; p50
  **0.693 s**, p95 **0.717 s**.
- publish: `cp -a ui-tui/dist hermes_cli/tui_dist` plus
  `{"type":"module"}` as `hermes_cli/tui_dist/package.json`, the same layout
  `scripts/build/agent.py` plants. Nothing under `node_modules`/`tui_dist` is
  committed.

These numbers do **not** include the official `install.sh`/PM path (uv, Node,
ripgrep, FFmpeg, optional extras); they measure the documented editable
developer install plus the TUI workspace build, on this workstation's network.

### Error informativeness (supplementary)

`bench/f3/hermes_errors.sh` captures **8** common Hermes failures — config
missing, malformed config, missing credentials, unknown provider, unknown model
(404 `model_not_found` from a loopback server), connection refused, DNS
failure, and an unknown CLI flag. Raw stdout/stderr is stored sanitized in
[`results/hermes-errors-868b9f244f.json`](results/hermes-errors-868b9f244f.json);
paths become `$HERMES_HOME`/`$HOME`, session ids are redacted, and no real
credentials are used. Cases that produce no user-facing error before the
deadline are marked `timed_out` and include a `gateway_log_tail` (for example,
connection-refused retries are visible only in `logs/errors.log`). This is
**not** the 60-case Damn-Bot suite, not a score, and not ratified: human raters
are still required.

## Scope and limitations

- The first-token number is the render delta, not TTFT including the model;
  the fake provider answers immediately.
- Both Hermes surfaces are measured. The TUI requires a Node build
  (`bench/f3/hermes_tui.sh`) and Node 22; the launcher's PM-managed Node was
  unusable here (`libatomic.so.1`), so `HERMES_NODE` is pinned to the system
  Node. The TUI samples each start a fresh Node app and Python gateway, while
  the classic samples reuse one warm detached gateway. The cold runs are
  reported separately.
- The TUI's "first frame" is Ink's first painted chrome frame
  (`summoning hermes`); the first PTY byte is earlier and recorded separately.
  This marker is not the same as Damn-Bot's `enter send` P12 marker.
- Hermes's vendored tree has no `.git`, so the `868b9f244f` identity is inherited
  from the F0 artefact and cannot be re-derived from the checkout here.
- The build numbers are local and noisy; use medians and compare only runs on
  the same machine. They are not a claim against Q14 or against Hermes. The
  Hermes clean samples include PyPI download variance; the TUI install samples
  reuse npm's cache after the first sample.
- `bench/` is a separate Go module: run `cd bench && go test ./...`; the root
  module does not contain `./bench/...`.
- Artifact JSON contains no home paths or credentials. Home-relative paths are
  written as `$DAMNBOT_CACHE`/`$HERMES_SRC`/`$HERMES_VENV` placeholders.
