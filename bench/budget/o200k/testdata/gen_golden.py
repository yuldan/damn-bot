"""Bangkitkan golden.json untuk o200k_test.go dari tiktoken (referensi Python).

Pemakaian (sekali; butuh tiktoken di venv sementara, bukan dependency repo):
    python3 -m venv /tmp/f0venv && /tmp/f0venv/bin/pip install tiktoken
    TIKTOKEN_CACHE_DIR=/tmp/f0_tok /tmp/f0venv/bin/python bench/budget/o200k/testdata/gen_golden.py
Kasus: string sulit (Unicode, whitespace, kontraksi, angka, CJK, emoji) + seluruh file teks/JSON
di bench/budget/testdata/**. Untuk string: id token; untuk file: jumlah token.
"""

import json
import pathlib

import tiktoken

ROOT = pathlib.Path(__file__).resolve().parents[2]  # bench/budget
enc = tiktoken.get_encoding("o200k_base")

STRINGS = [
    "", " ", "  ", "\n", "\n\n\n", " \n ", "\t\tx", "a  b", "a   \n  b", "hello world",
    "Hello, World!", "I'm you're they've he'll we'd it's DON'T I'M", "123 4567 89012345",
    "x=1;y=22;z=333", "foo/bar\n/baz", "  leading", "trailing  ", "mixed\r\nline\rendings",
    "Ünïcödé façade naïve", "日本語のテキストです。", "中文文本，测试。", "한국어 텍스트",
    "emoji 😀👍🏽 family 👨‍👩‍👧", "ÀB ǅungla ʰmod", "zero\u200bwidth", "nbsp\u00a0here\u3000ideo",
    '{"type": "object", "properties": {"a": {"type": "string"}}}', "<|endoftext|> literal",
    "CamelCaseIdentifier snake_case_name kebab-case", "    def f(x):\n        return x  # c\n",
    "!!!???...,,,", " ?!", "a\u0301e\u0301", "Ⅻ ½ ²³ ٣٤٥", "\u2028sep\u2029",
]


def main() -> None:
    cases = [{"text": s, "ids": enc.encode(s, disallowed_special=())} for s in STRINGS]
    files = []
    for p in sorted((ROOT / "testdata").rglob("*")):
        if p.suffix in (".txt", ".json") and p.is_file():
            text = p.read_text(encoding="utf-8")
            files.append({"path": str(p.relative_to(ROOT)), "count": len(enc.encode(text, disallowed_special=()))})
    out = {"tiktoken": tiktoken.__version__, "strings": cases, "files": files}
    (pathlib.Path(__file__).parent / "golden.json").write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")


if __name__ == "__main__":
    main()
