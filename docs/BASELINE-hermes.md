# Baseline hermes-agent

Baseline terukur dari `hermes-agent` (repo referensi, read-only) sebagai pembanding Damn-Bot. Semua rujukan `file:line` relatif terhadap root repo hermes-agent dan sudah dicek ulang (2026-10-17). Konversi token: **4 char/token** (`agent/model_metadata.py:2340` `CHARS_PER_TOKEN = 4`).

## 1. Skala codebase

| Metrik | Nilai |
|---|---|
| Python source | 972.645 LOC |
| Test | 1.442.817 LOC |
| `apps/desktop` | 850.355 LOC |
| `agent/` / `tools/` / `gateway/` | 141.805 / 126.545 / 116.193 LOC |
| Commit total / 30 hari terakhir | 55.582 / 17.438 |
| 500 commit terakhir | 314 `fix` vs 5 `feat` |
| Subcommand CLI | 76 |
| Adapter platform | 27 |
| Optional MCP / extras | 65 / 55 |
| `hermes_state*` | 55 modul / 24.520 LOC |
| Schema version DB | 31 (`hermes_state_common.py:284`) |

## 2. Token per API call

Sesi CLI default, model `gpt-4o`, konteks 128K.

| Komponen | Bytes | Token |
|---|---:|---:|
| System prompt — tier stable | 12.481 | 3.121 |
| System prompt — tier context | 13.413 | 3.354 |
| System prompt — tier volatile | 7.289 | 1.823 |
| Tool schema (19 schema dikirim) | 37.758 | 9.440 |
| **Floor tanpa AGENTS.md** (prompt 20.240 B / 5.060 tok + schema) | — | **14.500** |

Tool registry: 88 tool terdaftar di 33 toolset; 30 terpilih, 14 di-defer via `tool_search`, 19 schema benar-benar dikirim.

Schema terbesar:

| Tool | Bytes |
|---|---:|
| `delegate_task` | 4.671 |
| `manage_connections` | 4.526 |
| `skill_manage` | 3.725 |
| `memory` | 3.284 |
| `execute_code` | 3.092 |
| `terminal` | 2.741 |

## 3. Amplifikasi input tanpa cache

Model: fixed 14.500 tok per call + 4.000 tok per round-trip (RT) sebelumnya yang ikut dikirim ulang. n RT = n+1 call (call terakhir menjawab hasil tool). Kumulatif = Σ_{k=0}^{n} (14.500 + 4.000·k) = 14.500·(n+1) + 2.000·n·(n+1) — cocok dengan semua baris tabel.

| Round-trip | Total input token |
|---:|---:|
| 1 | 33.000 |
| 3 | 82.000 |
| 5 | 147.000 |
| 10 | 379.500 |
| 20 | 1.144.500 |
| 30 | 2.309.500 |

## 4. Default perilaku

| Parameter | Default | Rujukan |
|---|---|---|
| `max_turns` | 500 | `cli-config.yaml.example:1188` |
| Delegation `max_iterations` | 250 | `hermes_cli/config_defaults.py:1360` |
| Compression `threshold` | 0.50, dinaikkan ke ≥0.75 untuk window <512K | `hermes_cli/config_defaults.py:588`; `agent/context_compressor.py:2761-2765` (`_effective_threshold_percent`) |
| `protect_last_n` | 20 | `hermes_cli/config_defaults.py:602` |
| Summary `target_ratio` | 0.20 | `hermes_cli/config_defaults.py:594` |
| Tool result / turn budget / MCP result | 100.000 / 200.000 / 50.000 char | `tools/budget_config.py:13,14,20` |
| `proactive_prune_tokens` | 0 (off) | `hermes_cli/config_defaults.py:614` |
| `background_review.enabled` | `True` | `hermes_cli/config_defaults.py:810` |
| Memory nudge | tiap 10 turn | `agent/agent_init.py:1303` |
| Skill nudge | tiap 10 iterasi | `agent/agent_init.py:1387` |
| Biaya background review | "~30K tokens / event" | `agent/turn_finalizer.py:777-779` |
| Aux model | provider `"auto"`, model `""` (= model utama) | `hermes_cli/config_defaults.py:26` (`_aux`), blok `auxiliary` 737-815 |
| Memory cap | 2.200 (memory) + 1.375 (user) char | `tools/memory_tool.py:56` |

## 5. Reliabilitas

| Metrik | Nilai | Rujukan |
|---|---|---|
| `except Exception` (termasuk `as e`) di `agent/`+`tools/`+`hermes_cli/` | 4.795 | grep |
| — di antaranya bare `except Exception:` | 3.069 | grep (ukur ulang) |
| `except BaseException` | 118 (63 bare `:`) | grep |
| `logger.debug` | 1.800 | grep |
| Default log level | `INFO` | `hermes_logging.py:384-385` (`setup_logging`) |
| Lapisan retry/fallback | ~26 | [tidak terverifikasi] (hitungan manual) |
| Fuzzy tool-name repair | `difflib.get_close_matches(..., cutoff=0.7)` | `agent/agent_runtime_helpers.py:2632-2679` (`repair_tool_call`, cutoff di 2678) |
| Validasi argumen tool | hanya `json.loads` | `agent/turn_tool_validation.py:171` |
| `response_format` / structured output | tidak ada | `agent/conversation_loop.py` (0 hit) |
| Key JSON duplikat | `"type": "string"` dua kali di schema `schedule` | `tools/cronjob_tools.py:1085-1086` |

## 6. Startup

| Metrik | Nilai | Rujukan |
|---|---|---|
| `import model_tools` | ≈ 2,1 s (sumber); 1,61–1,63 s cumulative (ukur ulang) | `python -X importtime` |
| └ `hermes_cli.config` | ≈ 1,28 s (sumber); 0,80 s (ukur ulang) | `python -X importtime` |
| Plugin discovery | 500–650 ms | komentar `hermes_cli/main.py:2774,2817,3301` |

## 7. Cara reproduksi

Jalankan dari root `hermes-agent`; `HERMES_HOME` diarahkan ke direktori sementara agar `~/.hermes` tidak tersentuh.

```bash
export HERMES_HOME=/tmp/hm_baseline PYTHONDONTWRITEBYTECODE=1
mkdir -p "$HERMES_HOME"

# Startup / import cost
python3 -X importtime -c "import model_tools" 2>&1 \
  | grep -E '\| +(model_tools|hermes_cli\.config)$'

# Hitungan exception handler & debug log (hanya *.py)
grep -rF "except Exception"     agent tools hermes_cli --include=*.py | wc -l
grep -rF "except Exception:"    agent tools hermes_cli --include=*.py | wc -l
grep -rF "except BaseException" agent tools hermes_cli --include=*.py | wc -l
grep -rF "logger.debug"         agent tools hermes_cli --include=*.py | wc -l

# Schema version & response_format
grep -nE "SCHEMA_VERSION\s*=" hermes_state*.py
grep -c response_format agent/conversation_loop.py
```

```python
# Ukuran tool schema yang dikirim per call
import json
import model_tools

defs = model_tools.get_tool_definitions(quiet_mode=True)
raw = json.dumps(defs)  # default ensure_ascii=True, sama seperti payload API
print(f"{len(defs)} schema, {len(raw)} B, ~{len(raw) // 4} tok")

# 6 schema terbesar
for size, name in sorted(
    ((len(json.dumps(d)), d["function"]["name"]) for d in defs), reverse=True
)[:6]:
    print(f"{name:20} {size}")
```

### Hasil ukur ulang (2026-10-17)

| Metrik | Sumber | Ukur ulang | Catatan |
|---|---:|---:|---|
| Tool schema dikirim | 19 | 19 | sama |
| Tool schema bytes (`json.dumps` default) | 37.758 | 37.765 | +7 B; `ensure_ascii=False` → 37.580 char / 37.654 B UTF-8 |
| `delegate_task` schema | 4.671 | 4.671 | sama |
| `except Exception` (`*.py`) | 4.795 | 4.795 | sama; bare `except Exception:` = 3.069 |
| `except BaseException` | 118 | 118 | bare `:` = 63 |
| `logger.debug` | 1.800 | 1.800 | sama |
| `import model_tools` | ≈ 2,1 s | 1,61–1,63 s | variasi cache/disk |
| `hermes_cli.config` | ≈ 1,28 s | 0,80 s | variasi cache/disk |

## 8. Target Damn-Bot

| Aspek | Hermes | Target Damn-Bot |
|---|---|---|
| Overhead per call | 14.500 tok | ≤ 5.000 tok |
| Max iterasi | 500 | 40 |
| Tool result inline | 100K char | 4K char (sisanya spill ke file) |
| Lapisan retry/fallback | ~26 | 1 kebijakan terpusat |
| Startup | 2,1 s | < 100 ms |
| Validasi tool call | `json.loads` | JSON Schema penuh |
| Background LLM call | default on | off (opt-in) |
