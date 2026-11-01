# PROVIDERS — katalog provider LLM (hermes-agent + omp)

Sumber: hermes-agent `868b9f244f` (data PROV-001..PROV-080 di `docs/FEATURE-PARITY.md`, `cli-config.yaml.example`, `.env.example`) dan omp v18.11.3 (`/home/jelangkung/.local/bin/omp`). Keduanya dibaca saja.

## Ringkasan

- Total baris: **88**. Ada di hermes dan omp: **33**; hanya di hermes: **18**; hanya di omp: **37**. Ditambah 6 server lokal di bagian "Provider lokal".
- Profil hermes: 50 (49 dari registry + `custom`); provider omp yang teridentifikasi: 74 ID (termasuk ID lokal).

| Protokol | Jumlah baris (baris multi-protokol dihitung di tiap protokol) |
|---|---|
| openai-chat | 72 |
| anthropic-messages | 10 |
| openai-responses | 5 |
| gemini | 2 |
| bedrock | 1 |
| lainnya (cursor) | 1 |
| lainnya (factory) | 1 |

## Metode

```bash
# omp (read-only, tanpa jaringan)
omp --help                       # blok "Environment Variables" berisi daftar provider + env var
omp help                         # daftar subcommand: login, models, auth, token, config …
omp models --help; omp login --help; omp auth --help; omp config --help
strings -n 6 ~/.local/bin/omp > /tmp/omp_strings.txt
#  - pemetaan models.dev: baris `rt(…)` (openai-completions), `En(…)` (anthropic-messages), `In(…, api, …)`, `aT(…)` (xai responses)
#  - fungsi base URL: `case "ollama"|"vllm"|"litellm"|"opencode-*"|"meta"`
#  - usage provider OAuth: `id: "anthropic"|"openai-codex"|"github-copilot"|"google-gemini-cli"|"google-antigravity"|"kimi-code"|"cursor"`
sqlite3 'file:~/.omp/agent/models.db?mode=ro' 'select provider_id, models from model_cache'   # 37 provider, field api/baseUrl/reasoning/input
# ~/.omp/agent/config.yml & models.yml dibaca via Python dengan redaksi key/token/secret; ~/.config/omp tidak ada
# hermes: providers.list_providers() (lihat FEATURE-PARITY.md), cli-config.yaml.example:72-140, .env.example
```

Pemetaan protokol: hermes `chat_completions`→openai-chat, `codex_responses`→openai-responses, `anthropic_messages`→anthropic-messages, `bedrock_converse`→bedrock. omp `openai-completions`→openai-chat, `google-generative-ai`→gemini, `bedrock-converse-stream`→bedrock, `azure-openai-responses`→openai-responses (Azure).

## Katalog

| ID | Provider | Protokol | Base URL default | Auth | Ada di hermes? (path) | Ada di omp? (bukti) | Fitur khusus | Catatan |
|---|---|---|---|---|---|---|---|---|
| LLM-001 | Actual Computer (`actual`) | openai-chat | https://api.actual.inc/v1 | API key: `ACTUAL_API_KEY` | ✓ `plugins/model-providers/actual/__init__.py:80` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: actual-computer, actualcomputer, aci |
| LLM-002 | ai-gateway (`ai-gateway`) | openai-chat | https://ai-gateway.vercel.sh/v1 | API key: `AI_GATEWAY_API_KEY` | ✓ `plugins/model-providers/ai-gateway/__init__.py:22` | ✓ `vercel-ai-gateway`: `omp --help` blok Environment Variables; strings: base URL/ID | tool calling (umum) | alias: vercel, vercel-ai-gateway, ai_gateway, aigateway |
| LLM-003 | alibaba (`alibaba`) | openai-chat | https://dashscope-intl.aliyuncs.com/compatible-mode/v1 | API key: `DASHSCOPE_API_KEY` | ✓ `plugins/model-providers/alibaba/__init__.py:23` | ✗ tidak ditemukan (strings/models.db) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: dashscope, alibaba-cloud, qwen-dashscope, aliyun |
| LLM-004 | Alibaba Cloud DashScope (China) (`alibaba-cn`) | openai-chat | https://dashscope.aliyuncs.com/compatible-mode/v1 | API key: `DASHSCOPE_API_KEY`, `DASHSCOPE_CN_BASE_URL` | ✓ `plugins/model-providers/alibaba/__init__.py:28` | ✗ tidak ditemukan (strings/models.db) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: dashscope-cn, alibaba-cloud-cn |
| LLM-005 | Alibaba Cloud (Token Plan) (`alibaba-token-plan`) | openai-chat | https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1 | API key: `ALIBABA_TOKEN_PLAN_API_KEY`, `ALIBABA_TOKEN_PLAN_BASE_URL` | ✓ `plugins/model-providers/alibaba/__init__.py:36` | ✗ tidak ditemukan (strings/models.db) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: dashscope-token-plan |
| LLM-006 | Alibaba Cloud (Token Plan, China) (`alibaba-token-plan-cn`) | openai-chat | https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1 | API key: `ALIBABA_TOKEN_PLAN_CN_API_KEY`, `ALIBABA_TOKEN_PLAN_API_KEY`, `ALIBABA_TOKEN_PLAN_CN_BASE_URL` | ✓ `plugins/model-providers/alibaba/__init__.py:44` | ✗ tidak ditemukan (strings/models.db) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: dashscope-token-plan-cn |
| LLM-007 | Alibaba Cloud (Coding Plan) (`alibaba-coding-plan`) | openai-chat | https://coding-intl.dashscope.aliyuncs.com/v1 | API key: `ALIBABA_CODING_PLAN_API_KEY`, `DASHSCOPE_API_KEY`, `ALIBABA_CODING_PLAN_BASE_URL` | ✓ `plugins/model-providers/alibaba-coding-plan/__init__.py:23` | ✓ `alibaba-coding-plan`: strings: `rt("alibaba-coding-plan", "alibaba-coding-plan", …)`; models.db cache: 12 model (reasoning 9, vision 5) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: alibaba_coding, alibaba-coding, dashscope-coding |
| LLM-008 | Alibaba Cloud (Coding Plan, China) (`alibaba-coding-plan-cn`) | openai-chat | https://coding.dashscope.aliyuncs.com/v1 | API key: `ALIBABA_CODING_PLAN_CN_API_KEY`, `ALIBABA_CODING_PLAN_API_KEY`, `DASHSCOPE_API_KEY`, `ALIBABA_CODING_PLAN_CN_BASE_URL` | ✓ `plugins/model-providers/alibaba-coding-plan/__init__.py:32` | ✗ tidak ditemukan (strings/models.db) | cache_control di wire OpenAI (`agent/agent_runtime_helpers.py:1886`) | alias: alibaba-coding-cn, dashscope-coding-cn |
| LLM-009 | anthropic (`anthropic`) | anthropic-messages | https://api.anthropic.com | API key: `ANTHROPIC_API_KEY`, `ANTHROPIC_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN` | ✓ `plugins/model-providers/anthropic/__init__.py:50` | ✓ `anthropic`: strings: `En("anthropic", "anthropic", …)`; models.db cache: 28 model (reasoning 22, vision 28) | prompt caching (cache_control), extended thinking, vision, tool calling | alias: claude, claude-oauth, claude-code |
| LLM-010 | arcee (`arcee`) | openai-chat | https://api.arcee.ai/api/v1 | API key: `ARCEEAI_API_KEY` | ✓ `plugins/model-providers/arcee/__init__.py:7` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: arcee-ai, arceeai |
| LLM-011 | Azure Foundry (`azure-foundry`) | openai-chat | — (user-supplied) | API key: `AZURE_FOUNDRY_API_KEY`, `AZURE_FOUNDRY_BASE_URL` | ✓ `plugins/model-providers/azure-foundry/__init__.py:8` | ✓ `azure`: strings: `In("azure", "azure", …)`; models.db cache: 43 model (reasoning 34, vision 38) | tool calling (umum) | alias: azure, azure-ai-foundry, azure-ai |
| LLM-012 | bedrock (`bedrock`) | bedrock | https://bedrock-runtime.us-east-1.amazonaws.com | IAM (AWS SDK) | ✓ `plugins/model-providers/bedrock/__init__.py:12` | ✓ `amazon-bedrock`: models.db cache: 219 model (reasoning 159, vision 168); `omp --help` blok Environment Variables | Converse API, prompt caching (Claude), IAM | alias: aws, aws-bedrock, amazon-bedrock, amazon |
| LLM-013 | CommandCode (`commandcode`) | openai-chat | https://api.commandcode.ai/provider/v1 | API key: `COMMANDCODE_API_KEY`, `COMMANDCODE_BASE_URL` | ✓ `plugins/model-providers/commandcode/__init__.py:74` | ✓ `commandcode`: models.db cache: 94 model (reasoning 60, vision 62); `omp --help` blok Environment Variables | tool calling (umum) | alias: commandcode-chat |
| LLM-014 | CommandCode (Anthropic) (`commandcode-anthropic`) | anthropic-messages | https://api.commandcode.ai/provider/v1 | API key: `COMMANDCODE_API_KEY`, `COMMANDCODE_ANTHROPIC_BASE_URL` | ✓ `plugins/model-providers/commandcode/__init__.py:89` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: commandcode-claude |
| LLM-015 | copilot (`copilot`) | openai-chat | https://api.githubcopilot.com | GitHub token/OAuth: `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | ✓ `plugins/model-providers/copilot/__init__.py:39` | ✓ `github-copilot`: strings: `rt("github-copilot", "github-copilot", …)`; models.db cache: 55 model (reasoning 53, vision 53) | multi-protokol per model (chat/responses/anthropic di omp) | alias: github-copilot, github-models, github-model, github |
| LLM-016 | copilot-acp (`copilot-acp`) | openai-chat | acp://copilot | proses eksternal (ACP) | ✓ `plugins/model-providers/copilot-acp/__init__.py:51` | ✗ tidak ditemukan (strings/models.db) | multi-protokol per model (chat/responses/anthropic di omp) | alias: github-copilot-acp, copilot-acp-agent |
| LLM-017 | custom (`custom`) | openai-chat | — (user-supplied) | API key | ✓ `plugins/model-providers/custom/__init__.py:96` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: ollama, local, vllm, llamacpp, llama.cpp, llama-cpp |
| LLM-018 | DeepInfra (`deepinfra`) | openai-chat | https://api.deepinfra.com/v1/openai | API key: `DEEPINFRA_API_KEY`, `DEEPINFRA_BASE_URL` | ✓ `plugins/model-providers/deepinfra/__init__.py:54` | ✓ `deepinfra`: strings: base URL/ID | tool calling (umum) | alias: deep-infra, deepinfra-ai |
| LLM-019 | DeepSeek (`deepseek`) | openai-chat | https://api.deepseek.com/v1 | API key: `DEEPSEEK_API_KEY` | ✓ `plugins/model-providers/deepseek/__init__.py:55` | ✓ `deepseek`: strings: `rt("deepseek", "deepseek", …)`; models.db cache: 4 model (reasoning 4, vision 3) | reasoning_content wajib di-replay saat tool call (omp compat `requiresReasoningContentForToolCalls`; hermes #21944) | alias: deepseek-chat, deep-seek |
| LLM-020 | Fireworks AI (`fireworks`) | openai-chat | https://api.fireworks.ai/inference/v1 | API key: `FIREWORKS_API_KEY` | ✓ `plugins/model-providers/fireworks/__init__.py:30` | ✓ `fireworks`: strings: `rt("fireworks-ai", "fireworks", …)` | tool calling (umum) | alias: fireworks-ai, fw |
| LLM-021 | gemini (`gemini`) | openai-chat | https://generativelanguage.googleapis.com/v1beta | API key: `GOOGLE_API_KEY`, `GEMINI_API_KEY` | ✓ `plugins/model-providers/gemini/__init__.py:41` | ✓ `google`: strings: `In("google", "google", …)`; models.db cache: 50 model (reasoning 38, vision 49) | vision, reasoning (thinking level); adapter native opsional | alias: google, google-gemini, google-ai-studio |
| LLM-022 | GMI Cloud (`gmi`) | openai-chat | https://api.gmi-serving.com/v1 | API key: `GMI_API_KEY`, `GMI_BASE_URL` | ✓ `plugins/model-providers/gmi/__init__.py:8` | ✓ `gmi-cloud`: strings: base URL/ID | tool calling (umum) | alias: gmi-cloud, gmicloud |
| LLM-023 | HuggingFace (`huggingface`) | openai-chat | https://router.huggingface.co/v1 | API key: `HF_TOKEN` | ✓ `plugins/model-providers/huggingface/__init__.py:7` | ✓ `huggingface`: strings: `rt("huggingface", "huggingface", …)`; models.db cache: 79 model (reasoning 60, vision 29) | tool calling (umum) | alias: hf, hugging-face, huggingface-hub |
| LLM-024 | kilocode (`kilocode`) | openai-chat | https://api.kilo.ai/api/gateway | API key: `KILOCODE_API_KEY` | ✓ `plugins/model-providers/kilocode/__init__.py:7` | ✓ `kilo`: strings: `rt("kilo", "kilo", …)`; models.db cache: 625 model (reasoning 379, vision 265) | tool calling (umum) | alias: kilo-code, kilo, kilo-gateway |
| LLM-025 | kimi-coding (`kimi-coding`) | openai-chat | https://api.moonshot.ai/v1 | API key: `KIMI_API_KEY`, `KIMI_CODING_API_KEY` | ✓ `plugins/model-providers/kimi-coding/__init__.py` | ✓ `moonshot`: strings: `rt("moonshotai", "moonshot", …)`; models.db cache: 17 model (reasoning 7, vision 8) | cache_control via OpenRouter (hermes `agent/agent_runtime_helpers.py:1812`), schema tool Moonshot (`agent/moonshot_schema.py`) | alias: kimi, moonshot, kimi-for-coding |
| LLM-026 | kimi-coding-cn (`kimi-coding-cn`) | openai-chat | https://api.moonshot.cn/v1 | API key: `KIMI_CN_API_KEY` | ✓ `plugins/model-providers/kimi-coding/__init__.py` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: kimi-cn, moonshot-cn |
| LLM-027 | LM Studio (`lmstudio`) | openai-chat | — (user-supplied) | API key: `LM_API_KEY`, `LM_BASE_URL` | ✓ `plugins/model-providers/lmstudio/__init__.py:41` | ✓ `lm-studio`: strings: base URL/ID | tool calling (umum) |  |
| LLM-028 | Meta Model API (`meta-ai`) | openai-responses | https://api.meta.ai/v1 | API key: `MODEL_API_KEY`, `META_API_KEY`, `META_MODEL_API_KEY`, `META_BASE_URL` | ✓ `plugins/model-providers/meta-ai/__init__.py:58` | ✓ `meta / muse-code`: strings: base URL/ID | tool calling (umum) | alias: meta, muse, muse-spark, model-api, msl |
| LLM-029 | minimax (`minimax`) | anthropic-messages | https://api.minimax.io/anthropic | API key: `MINIMAX_API_KEY` | ✓ `plugins/model-providers/minimax/__init__.py:41` | ✓ `minimax`: strings: `En("minimax", "minimax", …)`; models.db cache: 8 model (reasoning 8, vision 1) | Anthropic-compatible endpoint | alias: mini-max |
| LLM-030 | minimax-cn (`minimax-cn`) | anthropic-messages | https://api.minimaxi.com/anthropic | API key: `MINIMAX_CN_API_KEY` | ✓ `plugins/model-providers/minimax/__init__.py:46` | ✓ `minimax-cn`: strings: `En("minimax-cn", "minimax-cn", …)`; models.db cache: 8 model (reasoning 8, vision 1) | Anthropic-compatible endpoint | alias: minimax-china, minimax_cn |
| LLM-031 | MiniMax (OAuth) (`minimax-oauth`) | anthropic-messages | https://api.minimax.io/anthropic | OAuth | ✓ `plugins/model-providers/minimax/__init__.py:52` | ✗ tidak ditemukan (strings/models.db) | Anthropic-compatible endpoint | alias: minimax_oauth, minimax-portal, minimax-global, minimax-oauth-io |
| LLM-032 | Nebius Token Factory (`nebius-token-factory`) | openai-chat | https://api.tokenfactory.nebius.com/v1 | API key: `NEBIUS_API_KEY`, `NEBIUS_TOKEN_FACTORY_API_KEY`, `NEBIUS_BASE_URL` | ✓ `plugins/model-providers/nebius-token-factory/__init__.py:35` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: nebius, nebius-tokenfactory, nebius-tf, token-factory, tokenfactory |
| LLM-033 | Nous Research (`nous`) | openai-chat | https://inference-api.nousresearch.com/v1 | OAuth device code: `NOUS_API_KEY` | ✓ `plugins/model-providers/nous/__init__.py:70` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: nous-portal, nousresearch |
| LLM-034 | NovitaAI (`novita`) | openai-chat | https://api.novita.ai/openai/v1 | API key: `NOVITA_API_KEY`, `NOVITA_BASE_URL` | ✓ `plugins/model-providers/novita/__init__.py:8` | ✓ `novita`: strings: base URL/ID | tool calling (umum) | alias: novita-ai, novitaai |
| LLM-035 | NVIDIA NIM (`nvidia`) | openai-chat | https://integrate.api.nvidia.com/v1 | API key: `NVIDIA_API_KEY` | ✓ `plugins/model-providers/nvidia/__init__.py:28` | ✓ `nvidia`: strings: `rt("nvidia", "nvidia", …)`; models.db cache: 173 model (reasoning 55, vision 38) | tool calling (umum) | alias: nvidia-nim, nim, build-nvidia, nemotron |
| LLM-036 | ollama-cloud (`ollama-cloud`) | openai-chat | https://ollama.com/v1 | API key: `OLLAMA_API_KEY` | ✓ `plugins/model-providers/ollama-cloud/__init__.py:39` | ✓ `ollama-cloud`: strings: `In("ollama-cloud", "ollama-cloud", …)`; models.db cache: 53 model (reasoning 39, vision 22) | tool calling (umum) | alias: ollama_cloud |
| LLM-037 | openai-codex (`openai-codex`) | openai-responses | https://chatgpt.com/backend-api/codex | OAuth | ✓ `plugins/model-providers/openai-codex/__init__.py:7` | ✓ `openai-codex`: strings: base URL/ID; strings: usage provider `id: "openai-codex"` | Responses API, reasoning items, prompt_cache_key | alias: codex, openai_codex |
| LLM-038 | opencode-zen (`opencode-zen`) | openai-chat | https://opencode.ai/zen/v1 | API key: `OPENCODE_ZEN_API_KEY` | ✓ `plugins/model-providers/opencode-zen/__init__.py:120` | ✓ `opencode-zen`: strings: `rt("opencode", "opencode-zen", …)`; `omp --help` blok Environment Variables | tool calling (umum) | alias: opencode, opencode_zen, zen |
| LLM-039 | opencode-go (`opencode-go`) | openai-chat | https://opencode.ai/zen/go/v1 | API key: `OPENCODE_GO_API_KEY` | ✓ `plugins/model-providers/opencode-zen/__init__.py:126` | ✓ `opencode-go`: strings: `rt("opencode-go", "opencode-go", …)`; `omp --help` blok Environment Variables | tool calling (umum) | alias: opencode_go, go, opencode-go-sub |
| LLM-040 | OpenRouter (`openrouter`) | openai-chat | https://openrouter.ai/api/v1 | API key: `OPENROUTER_API_KEY` | ✓ `plugins/model-providers/openrouter/__init__.py:208` | ✓ `openrouter`: `omp --help` blok Environment Variables; strings: base URL/ID | prompt caching per model (Claude/Kimi cache_control), reasoning, vision, tool calling | alias: or |
| LLM-041 | qwen-oauth (`qwen-oauth`) | openai-chat | https://portal.qwen.ai/v1 | OAuth: `QWEN_API_KEY` | ✓ `plugins/model-providers/qwen-oauth/__init__.py:68` | ✓ `qwen-portal`: strings: `rt("qwen-portal", "qwen-portal", …)`; models.db cache: 2 model (reasoning 0, vision 1) | tool calling (umum) | alias: qwen, qwen-portal, qwen-cli |
| LLM-042 | Ramp Router (`router`) | openai-responses | https://api.router.com/v1 | API key: `RAMP_ROUTER_API_KEY`, `ROUTER_API_KEY`, `RAMP_ROUTER_BASE_URL` | ✓ `plugins/model-providers/router/__init__.py:290` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: ramp-router, ramp, router.com |
| LLM-043 | Solstice (`solstice`) | openai-chat | https://generativelanguage.googleapis.com/v1alpha | OAuth | ✓ `plugins/model-providers/solstice/__init__.py:59` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: solstice-oauth |
| LLM-044 | stepfun (`stepfun`) | openai-chat | https://api.stepfun.ai/step_plan/v1 | API key: `STEPFUN_API_KEY` | ✓ `plugins/model-providers/stepfun/__init__.py:7` | ✓ `stepfun`: `omp --help` blok Environment Variables; strings: base URL/ID | tool calling (umum) | alias: step, stepfun-coding-plan |
| LLM-045 | Tencent TokenHub (`tencent-tokenhub`) | openai-chat | — (user-supplied) | API key: `TOKENHUB_API_KEY`, `TOKENHUB_BASE_URL` | ✓ `plugins/model-providers/tencent-tokenhub/__init__.py:38` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) |  |
| LLM-046 | Upstage Solar (`upstage`) | openai-chat | https://api.upstage.ai/v1 | API key: `UPSTAGE_API_KEY`, `UPSTAGE_BASE_URL` | ✓ `plugins/model-providers/upstage/__init__.py:51` | ✗ tidak ditemukan (strings/models.db) | tool calling (umum) | alias: solar |
| LLM-047 | vertex (`vertex`) | openai-chat | https://aiplatform.googleapis.com | IAM (Google ADC) | ✓ `plugins/model-providers/vertex/__init__.py:27` | ✓ `google-vertex`: strings: `In("google-vertex", "google-vertex", …)`; models.db cache: 36 model (reasoning 30, vision 33) | Gemini + Claude via Vertex | alias: google-vertex, vertex-ai, gcp-vertex, vertexai |
| LLM-048 | xai (`xai`) | openai-responses | https://api.x.ai/v1 | API key: `XAI_API_KEY` | ✓ `plugins/model-providers/xai/__init__.py:8` | ✓ `xai`: strings: `aT("xai", "xai", …)`; models.db cache: 35 model (reasoning 16, vision 20) | Responses API, x_search, reasoning | alias: grok, x-ai, x.ai |
| LLM-049 | xiaomi (`xiaomi`) | openai-chat | https://api.xiaomimimo.com/v1 | API key: `XIAOMI_API_KEY` | ✓ `plugins/model-providers/xiaomi/__init__.py:7` | ✓ `xiaomi`: strings: `rt("xiaomi", "xiaomi", …)`; models.db cache: 9 model (reasoning 9, vision 5) | reasoning_content wajib saat tool call (omp compat) | alias: mimo, xiaomi-mimo |
| LLM-050 | Z.AI (GLM) (`zai`) | openai-chat | https://api.z.ai/api/paas/v4 | API key: `GLM_API_KEY`, `ZAI_API_KEY`, `Z_AI_API_KEY` | ✓ `plugins/model-providers/zai/__init__.py:74` | ✓ `zai`: strings: `En("zai", "zai", …)`; models.db cache: 18 model (reasoning 18, vision 6) | thinking format zai, endpoint coding plan | alias: glm, z-ai, z.ai, zhipu |
| LLM-051 | Custom OpenAI-compatible (`custom`) | openai-chat | — (base_url wajib) | API key opsional (`key_env`/`api_key`/`key_cmd`) | ✓ `plugins/model-providers/custom/__init__.py` | ✓ `models.yml` providers.<name> (baseUrl, api, apiKey, models[]) | tergantung server | alias hermes: ollama, local, vllm, llamacpp, llama.cpp |
| LLM-052 | `abliteration` | openai-chat | https://api.abliteration.ai/v1 | ABLITERATION_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-053 | `aiand` | openai-chat | https://api.aiand.com/v1 | ? | ✗ | ✓ strings: base URL/ID | — | hanya di omp |
| LLM-054 | `bedrock-mantle` | openai-chat | https://bedrock-mantle.{region}.api.aws/openai/v1 | ? | ✗ | ✓ strings: base URL/ID | — | hanya di omp |
| LLM-055 | `cerebras` | openai-chat | https://api.cerebras.ai/v1 | CEREBRAS_API_KEY | ✗ | ✓ strings: `rt("cerebras", "cerebras", …)`; models.db cache: 8 model (reasoning 4, vision 2) | — | hanya di omp |
| LLM-056 | `charm-hyper` | openai-chat | https://hyper.charm.land/v1 | CHARM_HYPER_API_KEY | ✗ | ✓ models.db cache: 22 model (reasoning 17, vision 12); `omp --help` blok Environment Variables | — | hanya di omp |
| LLM-057 | `cline-pass` | openai-chat | https://api.cline.bot/api/v1 | CLINE_API_KEY | ✗ | ✓ strings: `rt("cline-pass", "cline-pass", …)`; `omp --help` blok Environment Variables | — | hanya di omp |
| LLM-058 | `cloudflare-ai-gateway` | anthropic-messages, openai-chat | https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/anthropic | CLOUDFLARE_AI_GATEWAY_API_KEY | ✗ | ✓ strings: `En("cloudflare-ai-gateway", "cloudflare-ai-gateway", …)`; strings: `rt("cloudflare-workers-ai", "cloudflare-ai-gateway", …)` | — | hanya di omp |
| LLM-059 | `coralbricks` | openai-chat | https://inference.coralbricks.ai/v1 | ? | ✗ | ✓ models.db cache: 2 model (reasoning 2, vision 1) | — | hanya di omp |
| LLM-060 | `coreweave` | openai-chat | https://api.inference.wandb.ai/v1 | ? | ✗ | ✓ strings: `rt("wandb", "coreweave", …)` | — | hanya di omp |
| LLM-061 | `cursor` | lainnya (cursor) | https://api2.cursor.sh | CURSOR_ACCESS_TOKEN (OAuth); OAuth (`omp login`) | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-062 | `factory-droid` | lainnya (factory) | https://api.factory.ai/api/llm/a | ? | ✗ | ✓ models.db cache: 50 model (reasoning 50, vision 43); strings: base URL/ID | — | hanya di omp |
| LLM-063 | `google-antigravity` | gemini | (OAuth) | OAuth (`omp login`) | ✗ | ✓ strings: base URL/ID; strings: usage provider `id: "google-antigravity"` | — | hanya di omp |
| LLM-064 | `google-gemini-cli` | gemini | cloudcode-pa.googleapis.com | OAuth (`omp login`) | ✗ | ✓ strings: base URL/ID; strings: usage provider `id: "google-gemini-cli"` | — | hanya di omp |
| LLM-065 | `groq` | openai-chat | https://api.groq.com/openai/v1 | GROQ_API_KEY | ✗ | ✓ strings: `rt("groq", "groq", …)`; models.db cache: 20 model (reasoning 9, vision 4) | — | hanya di omp |
| LLM-066 | `helmcode` | openai-chat? | ? | HELMCODE_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-067 | `kimi-code` | openai-chat | api.kimi.com | OAuth (`omp login`) | ✗ | ✓ strings: base URL/ID; strings: usage provider `id: "kimi-code"` | — | hanya di omp |
| LLM-068 | `minimax-code` | anthropic-messages, openai-chat | https://api.minimax.io/anthropic | ? | ✗ | ✓ strings: `En("minimax-coding-plan", "minimax-code", …)`; models.db cache: 10 model (reasoning 10, vision 2) | — | hanya di omp |
| LLM-069 | `minimax-code-cn` | anthropic-messages, openai-chat | https://api.minimaxi.com/anthropic | ? | ✗ | ✓ strings: `En("minimax-cn-coding-plan", "minimax-code-cn", …)`; models.db cache: 10 model (reasoning 10, vision 2) | — | hanya di omp |
| LLM-070 | `mistral` | openai-chat | https://api.mistral.ai/v1 | MISTRAL_API_KEY | ✗ | ✓ strings: `rt("mistral", "mistral", …)`; models.db cache: 41 model (reasoning 12, vision 21) | — | hanya di omp |
| LLM-071 | `nanogpt` | openai-chat | https://nano-gpt.com/api/v1 | ? | ✗ | ✓ strings: `rt("nano-gpt", "nanogpt", …)`; models.db cache: 1158 model (reasoning 604, vision 411) | — | hanya di omp |
| LLM-072 | `openai` | openai-responses | https://api.openai.com/v1 | OPENAI_API_KEY | ✗ | ✓ strings: `In("openai", "openai", …)`; models.db cache: 72 model (reasoning 48, vision 63) | — | hanya di omp |
| LLM-073 | `sakana` | openai-chat | https://api.sakana.ai/v1 | ? | ✗ | ✓ strings: base URL/ID | — | hanya di omp |
| LLM-074 | `siliconflow` | openai-chat | https://api.siliconflow.com/v1 | ? | ✗ | ✓ strings: `rt("siliconflow", "siliconflow", …)` | — | hanya di omp |
| LLM-075 | `siliconflow-cn` | openai-chat | https://api.siliconflow.cn/v1 | ? | ✗ | ✓ strings: `rt("siliconflow-cn", "siliconflow-cn", …)` | — | hanya di omp |
| LLM-076 | `singularityapi-dev` | openai-chat | https://api.singularityapi.dev/v1 | SINGULARITYAPI_DEV_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-077 | `singularityapi-tech` | openai-chat | https://api.singularityapi.tech/v1 | SINGULARITYAPI_TECH_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-078 | `synthetic` | openai-chat | https://api.synthetic.new/openai/v1 | ? | ✗ | ✓ strings: `rt("synthetic", "synthetic", …)` | — | hanya di omp |
| LLM-079 | `together` | openai-chat | https://api.together.xyz/v1 | ? | ✗ | ✓ strings: `rt("togetherai", "together", …)`; models.db cache: 39 model (reasoning 27, vision 13) | — | hanya di omp |
| LLM-080 | `umans` | anthropic-messages | (konstanta `sd`) | UMANS_AI_CODING_PLAN_API_KEY | ✗ | ✓ strings: `En("umans-ai", "umans", …)`; `omp --help` blok Environment Variables | — | hanya di omp |
| LLM-081 | `venice` | openai-chat | https://api.venice.ai/api/v1 | ? | ✗ | ✓ strings: `rt("venice", "venice", …)`; models.db cache: 174 model (reasoning 133, vision 96) | — | hanya di omp |
| LLM-082 | `wafer` | openai-chat | https://pass.wafer.ai/v1 | WAFER_SERVERLESS_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-083 | `xiaomi-token-plan-ams` | openai-chat | https://token-plan-ams.xiaomimimo.com/v1 | ? | ✗ | ✓ strings: `rt("xiaomi-token-plan-ams", "xiaomi-token-plan-ams", …)`; models.db cache: 6 model (reasoning 6, vision 4) | — | hanya di omp |
| LLM-084 | `xiaomi-token-plan-cn` | openai-chat | https://token-plan-cn.xiaomimimo.com/v1 | ? | ✗ | ✓ strings: `rt("xiaomi-token-plan-cn", "xiaomi-token-plan-cn", …)`; models.db cache: 7 model (reasoning 7, vision 5) | — | hanya di omp |
| LLM-085 | `xiaomi-token-plan-sgp` | openai-chat | https://token-plan-sgp.xiaomimimo.com/v1 | ? | ✗ | ✓ strings: `rt("xiaomi-token-plan-sgp", "xiaomi-token-plan-sgp", …)`; models.db cache: 6 model (reasoning 6, vision 4) | — | hanya di omp |
| LLM-086 | `yolo-auto` | openai-chat | https://yolo-auto.com/v1 | YOLO_AUTO_API_KEY | ✗ | ✓ `omp --help` blok Environment Variables; strings: base URL/ID | — | hanya di omp |
| LLM-087 | `zenmux` | anthropic-messages, openai-chat | (konstanta `Ei`) | ? | ✗ | ✓ strings: `rt("zenmux", "zenmux", …)`; models.db cache: 295 model (reasoning 223, vision 192) | — | hanya di omp |
| LLM-088 | `zhipu-coding-plan` | openai-chat | https://open.bigmodel.cn/api/coding/paas/v4 | ? | ✗ | ✓ strings: `rt("zhipuai-coding-plan", "zhipu-coding-plan", …)` | — | hanya di omp |

## Provider lokal

| Server | Hermes | omp | Base URL default | Protokol | Catatan |
|---|---|---|---|---|---|
| Ollama | `custom` dengan alias `ollama` (`plugins/model-providers/custom/__init__.py`); `/v1` ditambahkan otomatis (`agent/auxiliary_client.py:5078`, #106010); auto-detect `agent/agent_init_ollama.py` | provider `ollama`, base `http://127.0.0.1:11434` (strings: fungsi base-URL `case "ollama"`), wire `ollama-chat` native | `http://127.0.0.1:11434` (hermes: `…/v1`) | openai-chat (hermes) / ollama native (omp) | omp punya pesan khusus untuk "malformed tool-call JSON" dari llama.cpp/Ollama |
| llama.cpp | alias `llamacpp`/`llama.cpp` → `custom`; metadata `agent/model_metadata_llamacpp.py`; runtime lokal `local_runtime` (default off) | `llama.cpp` dalam daftar discovery lokal (strings: `"ollama" \| "llama.cpp" \| "lm-studio" \| "openai-models-list" \| "proxy" \| "litellm" \| "apple-foundation-models"`) | user-supplied (umumnya `http://127.0.0.1:8080/v1`) [INFERENCE] | openai-chat | |
| vLLM | alias `vllm` → `custom`; `cli-config.yaml.example:114` menyebut bug streaming tool-call vLLM (#72901) | `case "vllm"` → `http://127.0.0.1:8000/v1` | `http://127.0.0.1:8000/v1` (omp) | openai-chat | hermes punya `model.streaming: false` sebagai escape hatch |
| LM Studio | provider first-class `lmstudio` (`plugins/model-providers/lmstudio/__init__.py`), `LM_API_KEY` opsional, default `http://127.0.0.1:1234/v1` (`cli-config.yaml.example:99`) | `lm-studio`, `lm-studio-local` (strings) | `http://127.0.0.1:1234/v1` | openai-chat | `agent/lmstudio_reasoning.py` menangani reasoning |
| LiteLLM proxy | via `custom` | `case "litellm"` → `LITELLM_BASE_URL` atau `http://localhost:4000/v1` | `http://localhost:4000/v1` | openai-chat | |
| Apple Foundation Models | ✗ | `apple-foundation-models` (strings) | lokal macOS | lainnya | hanya di omp |

## Skema konfigurasi provider CUSTOM (field minimal)

Kunci yang dibaca hermes (`hermes_cli/runtime_provider_custom.py`, `hermes_cli/config_providers.py`; hitungan `.get("…")`) dan kunci di `~/.omp/agent/models.yml` (`providers.<name>`; field model `id, contextWindow, maxTokens, reasoning, input, thinking`):

| Field Damn-Bot (usulan) | Wajib? | Hermes | omp | Keterangan |
|---|---|---|---|---|
| `name` | ya | `name` / key `providers.<key>` | key `providers.<name>` | ID unik yang dirujuk sebagai `provider/model` |
| `base_url` | ya | `base_url` | `baseUrl` | Untuk alias lokal, tambahkan `/v1` bila tidak ada (pelajaran hermes #106010) |
| `api` (protokol) | ya (default `openai-chat`) | `api_mode` / `api` | `api` (`openai-completions`, `openai-responses`, `anthropic-messages`, …) | Menentukan adapter wire |
| `api_key_env` / `api_key` / `key_cmd` | tidak (server lokal) | `key_env`, `api_key_env`, `api_key`, `key_cmd` | `apiKey` | Server lokal butuh placeholder non-kosong (`agent/auxiliary_client.py:2850`) |
| `credentials[]` (`id`, `secret`) | tidak | — (pool beberapa key) | — | SPEC §4.1/ADR-278: 1..16 entri, `id` unik, `secret` hanya `keychain:<id>`; saling eksklusif dengan tiga field di atas |
| `models[]` | ya (minimal 1) | `models`, `model`, `default_model` | `models[]` | |
| `models[].context_length` | disarankan | `context_length` | `contextWindow` | Override per model wajib dihormati (#83324) |
| `models[].max_tokens` | disarankan | — | `maxTokens` | |
| `models[].capabilities` (reasoning, vision/input, tools) | disarankan | `capabilities` | `reasoning`, `input`, `thinking` | Gate vision sebelum mengirim gambar (#108349) |
| `extra_headers` | tidak | `extra_headers` (nilai dianggap secret, tidak di-log) | — | |
| `extra_body` | tidak | `extra_body` | (compat `extraBody` di katalog) | |
| `tls` (`ssl_verify`, `ssl_ca_cert`) | tidak | `ssl_verify`, `ssl_ca_cert` | env `NODE_EXTRA_CA_CERTS` | |
| `session_affinity_header` | tidak | `session_affinity_header` | — | Untuk proxy yang session-aware |
| `streaming` | tidak (default true) | `model.streaming` | — | Escape hatch untuk server dengan streaming tool-call yang rusak (#72901) |

## Rekomendasi default (berbasis bukti)

| Aspek | Hermes | omp | Usulan Damn-Bot |
|---|---|---|---|
| Pemilihan provider | `model.provider: "auto"`, yaitu auto-detect dari kredensial (`cli-config.yaml.example:77-108`); `model` kosong di `hermes_cli/config_defaults.py` sehingga dipilih lewat `hermes setup`/`hermes model` | `--model` fuzzy (`provider/model`); `--provider` ditandai "legacy; prefer --model" (`omp --help`) | Auto-detect dari kredensial + referensi `provider/model` |
| Contoh model default | `default: "anthropic/claude-opus-4.6"` dengan `base_url: https://openrouter.ai/api/v1` (`cli-config.yaml.example:75`, `:112`) | Config lokal pengguna: `modelRoles.default = anthropic/claude-opus-5-5` (`~/.omp/agent/config.yml`; ini pilihan pengguna, bukan default pabrik) | Jangan hardcode model; minta pengguna memilih di setup |
| Provider inti | daftar di `cli-config.yaml.example` dibuka dengan openrouter, nous, anthropic, openai-codex | blok "# Core Providers" `omp --help`: Anthropic, OpenAI, Gemini, GitHub Copilot | Prioritas 1.0: openai-chat (generik + OpenRouter), anthropic-messages, openai-responses, gemini; bedrock/vertex setelahnya |
| Auxiliary model | `auxiliary.*.provider: auto` ("auto (recommended)", `hermes_cli/main_provider_setup.py:288`) | role `smol`/`slow`/`plan` (`--smol`, `PI_SMOL_MODEL`) | Role model terpisah untuk tugas ringan (title, compression) |
| Fallback | `fallback_providers` kosong, opt-in | `retry.modelFallback` + `fallbackChains` di config pengguna | Chain fallback opt-in dengan pemulihan ke primer |

## Hal yang tidak bisa ditentukan

- Base URL default beberapa provider omp tersimpan di konstanta terminifikasi (`wd`, `sd`, `Ud`, `$r`, `Ei`, `Nye`, `sst`) sehingga nilainya tidak bisa dipastikan tanpa menjalankan omp.
- Tabel omp disusun dari pemetaan models.dev di binary (43 entri), cache `models.db` (37 provider yang pernah di-refresh), env var di `omp --help`, dan string URL. Provider omp yang tidak meninggalkan jejak di sumber-sumber itu bisa terlewat.
- Fitur khusus per model (caching, structured output) tidak diverifikasi per provider; angka reasoning/vision omp diambil dari cache lokal dan bisa usang.
- `omp models` (ls/refresh) sengaja tidak dijalankan karena berpotensi memanggil jaringan atau menulis cache.
- Protokol `helmcode` dan `meta / muse-code` di omp belum pasti (ditandai `?`).
- Nama provider custom di `~/.omp/agent/models.yml` adalah konfigurasi pribadi dan tidak dimasukkan ke katalog. Hanya skemanya yang dipakai.
- Untuk structured output (`response_format`), hermes menerjemahkannya ke field native (`agent/auxiliary_client.py:1775`); dukungan per provider tidak diinventaris.
