#!/usr/bin/env python3
"""Hermes side of the F6 "Image preflight" dimension (ADR-065/ADR-083).

Measures the same 12 MP JPEG with the same preset as Damn-Bot's
BenchmarkImagePreflightStages: 2048 px longest side, 3.75 MB, JPEG q85.
Decode/resize/encode are split for diagnosis; total is the real Hermes
`_resize_image_for_vision` call. No LLM call and no network are made.

Usage:
  PYTHONPATH=<hermes-deps>:<hermes-checkout> \
    python3 hermes_image.py --hermes-dir <checkout> --out <json>
"""
import argparse
import base64
import json
import statistics
import sys
import tempfile
import time
from io import BytesIO
from pathlib import Path

WIDTH, HEIGHT = 4000, 3000
MAX_DIMENSION = 2048
MAX_BASE64_BYTES = 5_000_000  # 3.75 MB of file bytes, the Damn-Bot preset


def measure(hermes_dir: str, runs: int) -> dict:
    sys.path.insert(0, hermes_dir)
    from PIL import Image
    from tools.vision_tools import _resize_image_for_vision

    tmp = Path(tempfile.mkdtemp(prefix="hermes-f6-image-"))
    path = tmp / "12mp.jpg"
    gradient = Image.linear_gradient("L").resize((WIDTH, HEIGHT))
    image = Image.merge(
        "RGB",
        (gradient, gradient.rotate(90, expand=True).resize((WIDTH, HEIGHT)), gradient),
    )
    image.save(path, "JPEG", quality=80)

    decode, resize, encode, total = [], [], [], []
    _resize_image_for_vision(
        path, mime_type="image/jpeg",
        max_base64_bytes=MAX_BASE64_BYTES, max_dimension=MAX_DIMENSION,
    )  # warm imports and Pillow codecs; not counted
    for _ in range(runs):
        t0 = time.perf_counter()
        _resize_image_for_vision(
            path, mime_type="image/jpeg",
            max_base64_bytes=MAX_BASE64_BYTES, max_dimension=MAX_DIMENSION,
        )
        total.append((time.perf_counter() - t0) * 1000)

        t0 = time.perf_counter()
        opened = Image.open(path)
        opened.load()
        decode.append((time.perf_counter() - t0) * 1000)

        t0 = time.perf_counter()
        scaled = opened.resize((WIDTH // 2, HEIGHT // 2), Image.LANCZOS)
        resize.append((time.perf_counter() - t0) * 1000)

        t0 = time.perf_counter()
        buf = BytesIO()
        scaled.save(buf, "JPEG", quality=85)
        base64.b64encode(buf.getvalue())
        encode.append((time.perf_counter() - t0) * 1000)

    return {
        "status": "measured",
        "runs": runs,
        "decode_ms": statistics.median(decode),
        "resize_ms": statistics.median(resize),
        "encode_ms": statistics.median(encode),
        "total_ms": statistics.median(total),
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
        row = {"status": "not_measured", "reason": f"hermes image adapter: {type(exc).__name__}: {exc}"}
    row["machine"] = args.machine
    Path(args.out).write_text(json.dumps(row, indent=1) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
