# Open Questions — Damn-Bot

Hanya hal yang masih terbuka; **tidak ada yang memblokir freeze**. Nomor `Qn` dirujuk dari SPEC.md dan docs lain; nomor yang sudah diputuskan tidak dipakai ulang.

**Sudah diputuskan (lihat [`DECISIONS.md`](DECISIONS.md)):** Q5, Q11, Q17 (ADR-019/021/024); Q6–Q14 dan Q16–Q30 (ADR-032; Q6 diamandemen ADR-033 dan ADR-034); Q31–Q50 (ADR-033). Q15 masih terbuka (di bawah).

## Integrasi tern / omp — asumsi kerja, diverifikasi manual di F3

| # | Pertanyaan | Asumsi kerja | Status |
|---|---|---|---|
| Q1 | Bagaimana mendaftarkan agent ACP kustom di tern (`settings.json`? field apa)? | `damnbot acp` didokumentasikan sebagai command agent generik; diuji manual via UI Settings tern | Tidak memblokir freeze |
| Q2 | Palet warna & tipografi persis tern untuk `tern-dark`/`tern-light`? | Tema mendekati secara visual, tanpa klaim identik | Tidak memblokir freeze |
| Q3 | Pemetaan `--resume/--fork/--continue/--sessions/--model/--mode/--prompt` tern ke method ACP? | `session/load`, `set_mode`, `set_model`; fork ACP unstable bila tern memanggilnya | Tidak memblokir freeze |
| Q4 | Apakah tern mengirim MCP server lewat `session/new.mcpServers`, transport apa? | Terima stdio + http + sse | Tidak memblokir freeze |
| Q15 | Relasi Damn-Bot dengan `omp` yang dikelola tern? | Berdampingan; Damn-Bot = agent ACP biasa | Tidak memblokir freeze |

## Scorecard — terbuka sampai RC

| # | Pertanyaan | Asumsi kerja | Status |
|---|---|---|---|
| Q23-RC | Model frontier & anggaran untuk head-to-head penuh (SWE-bench Verified subset 100 × 3 run × 2 agent + E2–E10) dan scorecard mingguan | Ditetapkan user saat RC pertama F9 / saat scorecard mingguan pertama diaktifkan (ADR-032) | Tidak memblokir freeze |
