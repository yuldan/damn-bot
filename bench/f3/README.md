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

### Build time (Damn-Bot side)

For each sample the driver extracts `git archive HEAD` into a private tree and
runs `go build -tags goolm ./cmd/damnbot` with an **empty** `GOCACHE`; this is
the clean build. It then appends one comment to `internal/loop/loop.go` and
rebuilds with the same cache; this is the incremental build. Times are seconds;
three pairs are recorded by default. The module cache is warm, matching
ENGINEERING E-62. The machine is a workstation, not Q14; `meta.loadavg` and CPU
are recorded so the numbers are not presented as reference-hardware gates.

### Error informativeness

**Blocked: human raters.** The required 60 common-error cases and two human
raters are not available. No rubric, percentage, or score is fabricated. The
artifact carries `status: blocked`, its reason, and the planned 12 groups ×
5 cases:

| Group | Example |
|---|---|
| config parse/unknown/type/range/invariant | bad TOML, unknown key, `max_turns=500` |
| credentials missing/invalid | missing provider key, keychain denied |
| provider transport | 401, 429, 5xx, timeout, network down |
| database | open/migrate/schema/permission/locked |
| filesystem/workspace | permission, symlink escape, disk full |
| exec/approval | shell denied, timeout, spawn failure |
| tool schema/args | bad args, unknown tool, invalid JSON |
| context budget | overflow, compaction failure |
| budget/pricing | cap reached, stale price |
| terminal/TUI | no TTY, unsupported TERM, resize |
| import/input | malformed Hermes/file input |
| ACP/stdio | bad frame, disconnect, oversized frame |

Each case must be agent-agnostic; a surface absent from Hermes/OpenClaw is
recorded as `n/a` in the denominator rather than scored zero. Two independent
raters score the binary rubric (cause + remedy), and disagreements are recorded,
not averaged away.

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
