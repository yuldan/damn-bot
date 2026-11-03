#!/usr/bin/env python3
"""Hermes side of the F6 "Voice end-to-end" dimension (ADR-065/ADR-083).

Runs Hermes' real sentence -> TTS speaker path with a timestamped fake LLM
(sentence queue) and a fake 10 ms TTS provider; playback is a no-op. The
reported overhead starts at end-of-speech and ends when the first sentence
reaches TTS, matching TestVoicePipelineLatency on the Damn-Bot side. No LLM
call and no audio device are used.

Usage:
  PYTHONPATH=<hermes-deps>:<hermes-checkout> \
    python3 hermes_voice.py --hermes-dir <checkout> --out <json>
"""
import argparse
import json
import queue
import statistics
import sys
import threading
import time
from pathlib import Path

TTS_DELAY_S = 0.010


def measure(hermes_dir: str, runs: int) -> dict:
    sys.path.insert(0, hermes_dir)
    from tools import tts_streaming, tts_tool, voice_mode
    from tools.tts_tool_speaker import stream_tts_to_speaker

    # Hermes resolves these seams at call time, so patching the modules is the
    # same seam its tests use. A streamer would change the measured path.
    tts_streaming.resolve_streaming_provider = lambda *_a, **_k: None
    tts_tool._load_tts_config = lambda: {}
    synthesis = []

    def fake_tts(*, text, output_path, **_kwargs):
        synthesis.append(time.perf_counter())
        time.sleep(TTS_DELAY_S)
        Path(output_path).write_bytes(b"fake-mp3")
        return json.dumps({"file_path": output_path})

    tts_tool.text_to_speech_tool = fake_tts
    voice_mode.play_audio_file = lambda _path: None

    def one() -> tuple[float, float]:
        del synthesis[:]
        text = queue.Queue()
        stop = threading.Event()
        done = threading.Event()
        worker = threading.Thread(
            target=stream_tts_to_speaker,
            args=(text, stop, done),
            daemon=True,
        )
        worker.start()
        end_of_speech = time.perf_counter()
        first_sentence = end_of_speech
        text.put("Satu. ")
        text.put("Dua. ")
        text.put("Tiga.")
        text.put(None)
        if not done.wait(10):
            stop.set()
            raise RuntimeError("speaker pipeline timed out")
        worker.join(5)
        if not synthesis:
            raise RuntimeError("no TTS synthesis observed")
        return (
            (synthesis[0] - end_of_speech) * 1000,
            (synthesis[0] - first_sentence) * 1000,
        )

    one()  # warm imports/thread pools; not counted
    overhead, tts_start = [], []
    for _ in range(runs):
        o, t = one()
        overhead.append(o)
        tts_start.append(t)
    return {
        "status": "measured",
        "runs": runs,
        "overhead_ms": statistics.median(overhead),
        "tts_start_ms": statistics.median(tts_start),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--hermes-dir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--machine", default="workstation (not Q14)")
    args = ap.parse_args()
    try:
        row = measure(args.hermes_dir, max(1, args.runs))
    except Exception as exc:  # noqa: BLE001 - the artefact records the reason
        row = {"status": "not_measured", "reason": f"hermes voice adapter: {type(exc).__name__}: {exc}"}
    row["machine"] = args.machine
    Path(args.out).write_text(json.dumps(row, indent=1) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
