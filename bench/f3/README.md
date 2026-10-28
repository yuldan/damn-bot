# F3 baseline harness

This directory measures the three dimensions that ADR-083 marks **F3**:
`First token di UI (TUI)`, `Error informatif`, and `Waktu build bersih /
inkremental`. It does not add P12/P13/P17/P18, setup, import, or ACP metrics to
the baseline: those already have F0 data or an `n/a` baseline.

## Run

```sh
bench/f3/run.sh
# optional: RUNS=20 BUILD_RUNS=3 OUT=bench/f3/results/damnbot-<sha>-F3.json
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

## Hermes side: unavailable

The F3 artifact field `hermes` is `unavailable` and `hermes_detail` carries the
reason and reproduction commands. This checkout has no runnable pinned Hermes:

- `~/.cache/damnbot/f2-inputs/hermes-src` exists, but has no `.git`, no
  verifiable commit, and `.python-version` is 3.14;
- the available interpreter is Python 3.12.3; `python3.14`, `pip`,
  `ensurepip`, and `uv` are absent;
- `hermes-deps` only contains `ruamel`, and `import hermes_cli.main` fails on
  `ModuleNotFoundError: dotenv`;
- Node 22 is present, but root/`ui-tui` `node_modules` are absent.

No Python/Node package install was attempted. The F0 numbers remain the
reference: `bench/speed/results/hermes-868b9f244f-nollm.json` and
`...-llm.json`. The F2 harness (`bench/f2/run.sh`) can still run its no-LLM
concurrency/token checks because it only imports `hermes_state`, but those are
not F3 dimensions.

When a pinned Hermes runtime is provisioned, the intended comparison is the
same PTY + timestamped SSE harness for `hermes chat`; choose and document
Python chat vs Ink TUI before measuring. Do not fill the field with an estimate.

## Scope and limitations

- The first-token number is the TUI render delta, not TTFT including the model;
  the fake provider answers immediately.
- The build numbers are local and noisy; use medians and compare only runs on
  the same machine. They are not a claim against Q14 or against Hermes.
- `bench/` is a separate Go module: run `cd bench && go test ./...`; the root
  module does not contain `./bench/...`.
- Artifact JSON contains no home paths or credentials.
