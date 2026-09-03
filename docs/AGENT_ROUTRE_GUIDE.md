# Agent Routre Configuration Guide

> **RULE: Every LLM request from pi MUST go via `routre` on `127.0.0.1:20128`.** No direct `opencode.ai` / `openrouter.ai` / `api.anthropic.com` / `api.openai.com` / `api.commandcode.ai` calls. This prevents auth drift, restores cache/RTK/ledger, and avoids the 500 bug below.

## 1. The Bug That Caused 500s

`muse-spark-1.2-contributor-free` (and other `opencode` models) only serve `/v1/responses` (OpenAI Responses API). `routre` was globally translating `responses → chat.completions` and always hitting `/v1/chat/completions` upstream → `opencode` returned `500 Internal server error` → provider put in cooldown (64s → 256s exponential) → `all_providers_failed`.

**Fix:** `routre` now detects native Responses upstreams (`base_url` contains `opencode.ai`) and does passthrough:

- `pipeline.go`: keep `responses` payload native; per-candidate `preparePayload` → if native, `cand.Payload(processed)` (no translate), else `ResponsesToOpenAI`
- `chat.go:relay/relayStream`: if `clientFmt==responses && isNativeResponsesBase(baseURL)` → `path=/v1/responses`, `streamRelay to=responses` (no SSE translation)
- Non-streaming `tryEval` and cache-hit: skip `OpenAIToResponses` wrapping for native (detect `"object":"response"`).

If you add a new `responses`-only provider, extend `isNativeResponsesBase`.

**Opencode session header (v0.4.4):** `opencode.ai` now requires `x-opencode-session` (stable per-conversation ID) from `09/06`. `routre` forwards the client's header when present, otherwise injects a stable gateway ID for every `opencode.ai` upstream (see `proxy/chat.go:opencodeSessionID` and `probe/probe.go:probeOpencodeSession`). No `config.json` change — `Go HTTP client` / `curl` UAs now pass.

### 1b. Provider-qualified routing hijack (commandcode)

**Bug:** `commandcode/deepseek/deepseek-v4-flash` (provider-qualified) was still matched by `opencode-zen` via `freeVariantOf` tail matching (`deepseek-v4-flash` suffix → `deepseek-v4-flash-free`), so the request was sent to `opencode` with the wrong upstream model and returned `400 Model is unavailable`. Bare `deepseek/deepseek-v4-flash` also collided with the same free variant and never reached `commandcode`.

**Fix (`internal/router/router.go: Candidates`):**

- Detect `qualifiedFor` — if the requested model's first segment equals any configured provider name, the model is provider-qualified. All non-matching providers are now skipped (no `providerServes` / `freeVariantOf` checks).
- Qualified `commandcode/<model>` now isolates to the `commandcode` provider only, stripping the prefix and forwarding the bare upstream name verbatim.
- Tier order was changed to put `commandcode` first so bare `deepseek/*` models prefer `commandcode` over `opencode` free variants. For explicit routing, always use `commandcode/<model>`.

## 2. Pi Side (`~/.pi/agent/models.json`)

This is the ONLY place pi's base URLs are overridden. Edit this file, then **restart pi** (resolved at startup).

For built-in providers (opencode, openrouter, anthropic, openai) a `baseUrl` override is enough — pi keeps its built-in model catalog:

```json
{
  "providers": {
    "openrouter": { "baseUrl": "http://127.0.0.1:20128/v1" },
    "opencode":   { "baseUrl": "http://127.0.0.1:20128/v1" },
    "anthropic":  { "baseUrl": "http://127.0.0.1:20128/v1" },
    "openai":     { "baseUrl": "http://127.0.0.1:20128/v1" }
  }
}
```

For a **new custom provider** like `commandcode` (not built-in), you must also declare `api` and `models` so pi lists it in `/model` and `--list-models`:

```json
{
  "providers": {
    "commandcode": {
      "baseUrl": "http://127.0.0.1:20128/v1",
      "api": "openai-completions",
      "apiKey": "commandcode",
      "models": [
        { "id": "deepseek/deepseek-v4-flash", "contextWindow": 1048576, "maxTokens": 384000 },
        { "id": "deepseek/deepseek-v4-pro",    "contextWindow": 1048576, "maxTokens": 384000 },
        { "id": "gpt-5.6-luna" },
        { "id": "claude-sonnet-5" }
      ]
    },
    "openrouter": { "baseUrl": "http://127.0.0.1:20128/v1" },
    "opencode":   { "baseUrl": "http://127.0.0.1:20128/v1" }
  }
}
```

- Add an entry for **every** provider pi uses (`opencode`, `openrouter`, `anthropic` ↔ Claude Code, `openai` ↔ ChatGPT/Codex, `google` ↔ Gemini, `commandcode` ↔ CommandCode AI, etc.).
- `apiKey` can be a dummy (`commandcode`) — routre injects the real key from `~/routre/routre.env` (`COMMANDCODE_API_KEY`). Or set it to `$COMMANDCODE_API_KEY` and export it.
- Do NOT point any provider at `https://...` directly while `routre` is running.
- Verify: `cat ~/.pi/agent/models.json` → all `baseUrl` must be `http://127.0.0.1:20128/v1`. Then `pi --list-models | grep commandcode` must show models.

### Context window: why a model shows 128K instead of 1M

**Root cause:** routre never tells pi the model's context window — `/v1/models` returns bare `{"id"}` entries and requests carry no window field. The window is a **pi-side** number. When a provider in `~/.pi/agent/models.json` declares a model with only `{ "id": ... }` (no metadata), pi falls back to its documented defaults: `contextWindow` **128000**, `maxTokens` **16384**. So a bare `deepseek/deepseek-v4-flash` entry shows `128K / 16.4K` in `pi --list-models` even though the upstream really supports 1M / 384K.

**Fix:** declare `contextWindow` and `maxTokens` on each custom model entry (match the real upstream numbers, visible via `pi --list-models` on the built-in opencode/openrouter catalog):

```json
{ "id": "deepseek/deepseek-v4-flash", "contextWindow": 1048576, "maxTokens": 384000 }
```

Notes:

- Settings live in `~/.pi/agent/models.json` (`settings.json` does NOT hold model metadata). Changes need a **pi restart** (resolved at startup) — SIGHUP/`/models` reload is not enough.
- Alternatively use `modelOverrides` on a built-in provider that keeps pi's catalog, e.g. `"opencode": { "modelOverrides": { "deepseek-v4-flash": { "contextWindow": 1048576, "maxTokens": 384000 } } }` (see pi `docs/models.md` § Per-model Overrides). Unknown ids are ignored; a custom `models` entry with the same id shadows the built-in.
- Verify with `pi --list-models | grep <model>` — the `context`/`max-out` columns must show `1M` / `384K`, not `128K` / `16.4K`.

## 3. Routre Side (`~/routre/config.json`)

```json
{
  "listen": "127.0.0.1:20128",
  "log_level": "debug",
  "forward_unknown": true,
  "tiers": [
    {
      "name": "commandcode",
      "providers": [{
        "name": "commandcode",
        "kind": "openai",
        "base_url": "https://api.commandcode.ai/provider/v1",
        "api_key_env": "COMMANDCODE_API_KEY",
        "models": [
          "deepseek/deepseek-v4-flash",
          "deepseek/deepseek-v4-flash-fast",
          "deepseek/deepseek-v4-pro",
          "claude-sonnet-5",
          "claude-opus-5",
          "gpt-5.6-luna",
          "gpt-5.6-sol",
          "gpt-5.6-terra"
        ]
      }]
    },
    {
      "name": "subscription",
      "providers": [{
        "name": "opencode-zen",
        "kind": "openai",
        "base_url": "https://opencode.ai/zen/v1",
        "api_key_env": "OPENCODE_API_KEY",
        "models": [
          "deepseek-v4-flash-free", "mimo-v2.5-free", "mimo-v2.5",
          "hy3-free", "hy3", "nemotron-3-ultra-free", "nemotron-3.5-lightning-free",
          "ling-3.0-flash-fin-free", "laguna-s-2.1-free",
          "muse-spark-1.2-contributor-free", "muse-spark-1.2"
        ]
      }]
    },
    {
      "name": "opencode-go",
      "providers": [{
        "name": "opencode-go",
        "kind": "openai",
        "base_url": "https://opencode.ai/zen/go/v1",
        "api_key_env": "OPENCODE_GO_API_KEY",
        "models": ["minimax-m3", "kimi-k3", "..."]
      }]
    },
    {
      "name": "openrouter",
      "providers": [{
        "name": "openrouter",
        "kind": "openai",
        "base_url": "https://openrouter.ai/api/v1",
        "api_key_env": "OPENROUTER_API_KEY",
        "models": ["openai/gpt-4o-mini", "anthropic/claude-haiku-4.5"]
      }]
    },
    {
      "name": "anthropic",
      "providers": [{
        "name": "anthropic",
        "kind": "anthropic",
        "base_url": "https://api.anthropic.com",
        "api_key_env": "ANTHROPIC_API_KEY",
        "models": ["claude-sonnet-4", "claude-haiku-4-5"]
      }]
    },
    {
      "name": "openai",
      "providers": [{
        "name": "openai",
        "kind": "openai",
        "base_url": "https://api.openai.com/v1",
        "api_key_env": "OPENAI_API_KEY",
        "models": ["gpt-4o", "gpt-4o-mini", "gpt-5.6-luna"]
      }]
    }
  ],
  "preferred_model": "opencode-zen/muse-spark-1.2-contributor-free",
  "request_log": "/home/mariobgsp/.routre/requests.jsonl"
}
```

**Rules:**

- `forward_unknown: true` (default) — unknown/new models forward to all providers with failover; `false` breaks zero-config.
- `kind: "openai" | "anthropic" | "gemini"` — `opencode` and `commandcode` use `openai` even though `opencode` speaks `responses` natively; the native check is on `base_url`.
- Tier order matters: `commandcode` is first so bare `deepseek/*` prefers `commandcode` over `opencode` free variants. Use `commandcode/<model>` for explicit routing regardless of order.
- Keys in `~/routre/routre.env` (or env): `OPENCODE_API_KEY`, `OPENCODE_GO_API_KEY`, `OPENROUTER_API_KEY`, `COMMANDCODE_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`. Must exist or provider returns `401`. For `commandcode`, the key is `user_...` from `api.commandcode.ai`.
- After editing `config.json`: `~/routre/routre restart -config ~/routre/config.json` (or `systemctl --user restart routre`). Config also reloads on `SIGHUP`.

## 4. Verification (run after every change)

```bash
# pi is routed
cat ~/.pi/agent/models.json # all baseUrl == http://127.0.0.1:20128/v1
pi --list-models | grep commandcode # must show commandcode models
env | grep PI_ # PI_MODEL/PROVIDER shows active model

# routre is up
ps aux | grep routre
curl -s http://127.0.0.1:20128/healthz # → ok
~/routre/routre list --url http://127.0.0.1:20128 # all tiers up, no cooldown

# commandcode via routre (qualified — always routes to commandcode)
curl -s http://127.0.0.1:20128/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"commandcode/deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":32}' | jq .choices

# pi via routre to commandcode (custom provider)
pi --provider commandcode --model deepseek/deepseek-v4-flash -p "say hi in 3 words"
# or explicitly qualified:
pi --provider commandcode --model commandcode/deepseek/deepseek-v4-flash -p "hi"

# responses native passthrough (must be 200, not 500)
curl -s -N http://127.0.0.1:20128/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"muse-spark-1.2-contributor-free","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":32,"stream":true}' | head

curl -s http://127.0.0.1:20128/v1/responses \
  -H "Content-Type: application/json" \
  -d '{"model":"muse-spark-1.2-contributor-free","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":32}' | jq .model

# chat path still works for chat models
curl -s http://127.0.0.1:20128/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"max_tokens":16}' | jq .choices

# pi went via routre
tail -1 ~/.routre/requests.jsonl | jq  # client:"pi (...)" status:200

# logs
journalctl --user -u routre --since "5 min ago" | tail
cat ~/.routre/daemon.log | tail -20
```

**Failure signatures:**

- `status 500 (server)` + `cooldown_remaining_seconds` → upstream 500, likely `responses`→`chat` mistranslation or bad `max_output_tokens` (<16 for muse-spark).
- `status 401 (auth)` → missing `*_API_KEY` in `routre.env` (for commandcode, check `COMMANDCODE_API_KEY=user_...`).
- `status 400` + `Model is unavailable` on `commandcode/<model>` → model not in `commandcode` tier's `models` list or typo; `bare deepseek/...` without `commandcode/` prefix may still hit `opencode` free variant — use qualified `commandcode/<model>` for explicit routing.
- `status 400` + `openrouter rejected model` → model not in `openrouter` whitelist but `opencode-zen` in cooldown — wait or `routre restart`.
- `pi --provider commandcode` → `Unknown provider` → `~/.pi/agent/models.json` missing `commandcode` entry with `api`+`models` — add it and retry `pi --list-models`.
- No `pi` lines in `requests.jsonl` → pi `baseUrl` not pointing to `127.0.0.1:20128` → fix `models.json` + restart pi.

## 5. Common Agent Tasks

**Add a provider (e.g. Anthropic or CommandCode):**

1. Add `ANTHROPIC_API_KEY=sk-...` (or `COMMANDCODE_API_KEY=user_...`) to `~/routre/routre.env`
2. Add tier entry in `config.json` (kind `openai` for commandcode, `base_url https://api.commandcode.ai/provider/v1`)
3. For built-in providers: add `"anthropic": {"baseUrl": "http://127.0.0.1:20128/v1"}` to `~/.pi/agent/models.json`. For new custom providers like `commandcode`, add full entry with `api` + `models` (see §2).
4. `go vet ./... && go build -o ./routre . && ./routre restart`
5. Verify with `curl /v1/chat/completions` via routre and `pi --provider <name> --model <model> -p "hi"`.

**Add a model:** append to `providers[].models` + `routre restart` (or rely on `forward_unknown:true`). For `commandcode`, also add `{ "id": "<model>" }` to `~/.pi/agent/models.json` `commandcode.models`.

**Pull new upstream models:** `~/routre/routre models sync --dry-run` → `... sync`.

## 6. Checklist Before Declaring Done

- [ ] `~/.pi/agent/models.json` all providers point to `127.0.0.1:20128` and `commandcode` has `api`+`models`
- [ ] `~/routre/config.json` `forward_unknown:true`, all tiers present (`commandcode` first), keys in `routre.env`
- [ ] `go vet ./...` passes, `routre` rebuilt if `internal/proxy/*.go` or `internal/router/*.go` changed
- [ ] `routre list` → all `up`, no cooldown
- [ ] `curl` `/v1/chat/completions` via `commandcode/<model>` and bare `deepseek/...` via routre both 200
- [ ] `pi --list-models | grep commandcode` shows models
- [ ] `pi --provider commandcode --model deepseek/deepseek-v4-flash -p "hi"` appears in `requests.jsonl` with `status:200`
- [ ] `curl` `/v1/responses` via routre 200
