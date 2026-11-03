# F6 baseline harness

This directory produces `bench/speed/results/hermes-868b9f244f-F6.json`, the
comparative baseline for the two dimensions ADR-065/ADR-083 mark **F6**:

- `voice_end_to_end` — end-of-speech → first TTS call, and first LLM sentence →
  first TTS call. Targets: overhead ≤ 200 ms, streaming TTS start ≤ 300 ms.
- `image_preflight` — 12 MP decode → resize → encode. Target: total ≤ 150 ms
  **on the Q14 reference machine** (SPEC §17 / SCORECARD §1.2).

No LLM is called. Voice uses a timestamped fake LLM and a fake 10 ms TTS;
playback is a no-op. The image dimension is local CPU work on a generated
4000×3000 JPEG with the documented preset (2048 px longest side, 3.75 MB,
JPEG q85).

## Run

```sh
HERMES_DIR=/opt/hermes-agent \
HERMES_PYTHON=$HOME/.cache/damnbot/hermes-venv/bin/python \
  bench/f6/run.sh

# optional overrides
RUNS=5 MACHINE="Q14" OUT=bench/speed/results/hermes-868b9f244f-F6.json bench/f6/run.sh
```

`HERMES_PYTHON` must see the Hermes dependencies (Pillow for the image side).
If the checkout or Pillow is unavailable, the sidecar records
`status: not_measured` with the reason instead of inventing a number.

## How to fill the artefact

`run.sh` collects four inputs and `cmd/f6measure` assembles them:

| input | producer |
|---|---|
| Damn-Bot image stages | `BenchmarkImagePreflightStages` (`internal/media/preflight`) |
| Damn-Bot voice | `TestVoicePipelineLatency` (`internal/voice`), `F6-VOICE-LATENCY` line |
| Hermes image | `hermes_image.py` (real `_resize_image_for_vision`) |
| Hermes voice | `hermes_voice.py` (real sentence → TTS speaker path) |

The artefact is complete when both dimensions list `status: measured` for both
agents. A pass requires every target to be met; for `image_preflight` it also
requires `machine: "Q14"` on both rows. A fast workstation therefore yields
`verdict: not_reference` (or `fail` when over target), never `pass`.

Regenerate after measuring; do not hand-edit numbers. Commit the JSON together
with the harness change so `TestF6HermesBaseline` can validate it offline.

## F6 E3 smoke

The paid E3 smoke uses the existing h2h harness with ten deterministic E3
tasks and a separate `F6` ledger phase (cap US$2):

```sh
cd bench
go run ./h2h/cmd/h2h -smoke f6e3 -hermes <checkout> \
  -upstream <base-url> -model <cheap-model> \
  -price-in <usd> -price-cached <usd> -price-out <usd> \
  -out h2h/results/<date>-f6-e3-smoke
```

It is opt-in: `H2H_LIVE_F6=1` makes the unit test fail (rather than skip) when
no key is available. F6 spending is isolated from the F1/F5 phases in
`h2h/results/f1-spend.json`.
