# Agent Routre Configuration Guide

> **RULE: Every LLM request from pi MUST go via `routre` on `127.0.0.1:20128`.** No direct `opencode.ai` / `openrouter.ai` / `api.anthropic.com` / `api.openai.com` calls. This prevents auth drift, restores cache/RTK/ledger, and avoids the 500 bug below.

## 1. The Bug That Caused 500s

`muse-spark-1.2-contributor-free` (and other `opencode` models) only serve `/v1/responses` (OpenAI Responses API). `routre` was globally translating `responses → chat.completions` and always hitting `/v1/chat/completions` upstream → `opencode` returned `500 Internal server error` → provider put in cooldown (64s → 256s exponential) → `all_providers_failed`.

**Fix:** `routre` now detects native Responses upstreams (`base_url` contains `opencode.ai`) and does passthrough:

- `pipeline.go`: keep `responses` payload native; per-candidate `preparePayload` → if native, `cand.Payload(processed)` (no translate), else `ResponsesToOpenAI`
- `chat.go:relay/relayStream`: if `clientFmt==responses && isNativeResponsesBase(baseURL)` → `path=/v1/responses`, `streamRelay to=responses` (no SSE translation)
- Non-streaming `tryEval` and cache-hit: skip `OpenAIToResponses` wrapping for native (detect `"object":"response"`).

If you add a new `responses`-only provider, extend `isNativeResponsesBase`.

**Opencode session header (v0.4.4):** `opencode.ai` now requires `x-opencode-session` (stable per-conversation ID) from `09/06`. `routre` forwards the client's header when present, otherwise injects a stable gateway ID for every `opencode.ai` upstream (see `proxy/chat.go:opencodeSessionID` and `probe/probe.go:probeOpencodeSession`). No `config.json` change — `Go HTTP client` / `curl` UAs now pass.

## 2. Pi Side (`~/.pi/agent/models.json`)

This is the ONLY place pi's base URLs are overridden. Edit this file, then **restart pi** (resolved at startup).

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

- Add an entry for **every** provider pi uses (`opencode`, `openrouter`, `anthropic` ↔ Claude Code, `openai` ↔ ChatGPT/Codex, `google` ↔ Gemini, etc.). If only `baseUrl` is set (no `models`), pi keeps its built-in model catalog.
- Do NOT point any provider at `https://...` directly while `routre` is running.
- Verify: `cat ~/.pi/agent/models.json` → all `baseUrl` must be `http://127.0.0.1:20128/v1`.

## 3. Routre Side (`~/routre/config.json`)

```json
{
  "listen": "127.0.0.1:20128",
  "log_level": "debug",
  "forward_unknown": true,
  "tiers": [
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
- `kind: "openai" | "anthropic" | "gemini"` — `opencode` uses `openai` even though it speaks `responses` natively; the native check is on `base_url`.
- Keys in `~/routre/routre.env` (or env): `OPENCODE_API_KEY`, `OPENCODE_GO_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`. Must exist or provider returns `401`.
- After editing `config.json`: `~/routre/routre restart -config ~/routre/config.json` (or `systemctl --user restart routre`). Config also reloads on `SIGHUP`.

## 4. Verification (run after every change)

```bash
# pi is routed
cat ~/.pi/agent/models.json # all baseUrl == http://127.0.0.1:20128/v1
env | grep PI_ # PI_MODEL/PROVIDER shows active model

# routre is up
ps aux | grep routre
curl -s http://127.0.0.1:20128/healthz # → ok
~/routre/routre list --url http://127.0.0.1:20128 # all tiers up, no cooldown

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
- `status 401 (auth)` → missing `*_API_KEY` in `routre.env`.
- `status 400` + `openrouter rejected model` → model not in `openrouter` whitelist but `opencode-zen` in cooldown — wait or `routre restart`.
- No `pi` lines in `requests.jsonl` → pi `baseUrl` not pointing to `127.0.0.1:20128` → fix `models.json` + restart pi.

## 5. Common Agent Tasks

**Add a provider (e.g. Anthropic):**

1. Add `ANTHROPIC_API_KEY=sk-...` to `~/routre/routre.env`
2. Add tier entry in `config.json` (kind `anthropic`, `base_url https://api.anthropic.com`)
3. Add `"anthropic": {"baseUrl": "http://127.0.0.1:20128/v1"}` to `~/.pi/agent/models.json`
4. `go vet ./... && go build -o ./routre . && ./routre restart`
5. Verify with `/v1/messages` curl via routre.

**Add a model:** append to `providers[].models` + `routre restart` (or rely on `forward_unknown:true`).

**Pull new upstream models:** `~/routre/routre models sync --dry-run` → `... sync`.

## 6. Checklist Before Declaring Done

- [ ] `~/.pi/agent/models.json` all providers point to `127.0.0.1:20128`
- [ ] `~/routre/config.json` `forward_unknown:true`, all tiers present, keys in `routre.env`
- [ ] `go vet ./...` passes, `routre` rebuilt if `internal/proxy/*.go` changed
- [ ] `routre list` → all `up`, no cooldown
- [ ] `curl` `/v1/responses` and `/v1/chat/completions` via routre both 200
- [ ] `pi` request appears in `requests.jsonl` with `status:200`
