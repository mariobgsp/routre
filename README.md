# routre-cli

A low-RAM LLM gateway for coding agents: **automatic provider failover, RTK
token compression (≥90% on tool-heavy traffic), response caching, and a
per-agent token/cost ledger** — all in one static binary (~6.7 MiB, ~9 MiB
RAM idle) that runs as an always-on daemon on macOS, Windows, and Linux.

```bash
npm install -g routre-cli     # one command, no Go toolchain needed
routre-cli setup              # wizard: provider URLs + API keys
routre-cli serve              # gateway on 127.0.0.1:20128
routre-cli start              # start the daemon (systemd/launchd or detached)
routre-cli stop               # stop the daemon
routre-cli restart            # restart the daemon (keeps auto-start state)
routre-cli list               # connected providers + token/cost ledger
```

Point any OpenAI/Anthropic-compatible CLI (opencode, Claude Code, Codex,
Cursor, …) at `http://127.0.0.1:20128` via `OPENAI_BASE_URL` /
`ANTHROPIC_BASE_URL` and it gets failover + compression + caching for free.

---

## Why this exists

Built as a decision-driven spike from two research passes
([`research.md`](../research.md) and [`context.md`](../context.md)):

- **Not 9router** — functionally the right feature set, but it's a
  Node/Next.js gateway with ~80 MB idle RAM and a documented unbounded leak
  to ~4.8 GB in 3 days, plus unbenchmarked 20–65% savings claims.
- **Not LiteLLM/Portkey self-host** — Python/Postgres/Redis stacks sized in
  gigabytes.
- **Single static Go binary, stdlib only** — proven low-RAM pattern (Go ~5K
  QPS at ~11 ms proxy overhead), cross-compiled for 6 platforms (7 npm packages, incl. scoped win32).
- **Honest metrics** — the 90% claim is defined, gated, and reproducible
  (`routre-cli bench` fails the build if it regresses).

## Measured (this machine, 2026-08-15)

| Metric | Result | Target |
| --- | --- | --- |
| RTK tool-token reduction (bench, 5 tool-heavy payloads) | **91.5%** | ≥ 90% (aggregate **and** per-payload) |
| Worst per-payload tool reduction | 90.3% (tree-ls) | ≥ 90% |
| RTK payload-token reduction (whole request bodies) | **91.3%** | reported |
| Idle RSS (`scripts/measure-ram.sh`) | **9 MiB** | ≤ 100 MiB |
| Peak RSS under live opencode load (3 sessions) | 12.9 MiB | ≤ 200 MiB hard cap |
| Binary size (`CGO_ENABLED=0`, `-s -w`) | **6.7 MiB** | small |
| Tests | all pass (`go test ./...`) | — |
| OpenCode 1.18.15 e2e → gateway → upstream | answer delivered, exit 0 | — |
| RTK on a real 23.4 KB tool_result request | 5.4 KB sent upstream | fail-open |
| Cache on identical repeat request | served from cache, upstream untouched | — |
| Real OpenRouter paid round-trip (with your key) | 200, usage + cost recorded | — |

Reproduce:

```bash
make build test bench        # bench gates 90% (fails on regression)
./scripts/measure-ram.sh ./routre-cli ./config.example.json 30
```

---

## Install

### npm (macOS / Windows / Linux)

```bash
npm install -g routre-cli
routre-cli version
```

The package ships one static Go binary per platform via optional
`routre-cli`-scoped dependencies, plus a tiny Node launcher. No Go
toolchain, no runtime dependencies.

Platform packages (all published to the public registry):

| Platform | Package |
| --- | --- |
| linux x64 / arm64 | `routre-cli-linux-x64` / `routre-cli-linux-arm64` |
| darwin x64 / arm64 | `routre-cli-darwin-x64` / `routre-cli-darwin-arm64` |
| win32 x64 / arm64 | `@mariobgsp/routre-cli-win32-x64` / `@mariobgsp/routre-cli-win32-arm64` |

> The Windows packages are scoped (`@mariobgsp/...`): npm's spam detection
> rejects the unscoped `routre-cli-win32-*` names. On non-Windows hosts the
> launcher resolves the matching Unix package instead.

### From source (developers)

```bash
make build          # needs Go ≥ 1.22
./routre-cli version
```

### Build the npm distribution

```bash
make dist-npm       # cross-compiles all 6 platforms → npm/dist/*.tgz (7 packages)
```

### Publish to npm

Releases are published by CI on a `v*` tag push (see
`.github/workflows/publish.yml`, npm Trusted Publishing / OIDC — no token,
no 2FA on CI):

```bash
git tag v0.1.0 && git push origin v0.1.0
```

Manual publishing (maintainer with an npm token, 2FA enabled):

```bash
NPM_TOKEN=<token with bypass-2FA> bash ./npm/publish.sh
```

Publish order matters: platform packages first (Unix, then the scoped
Windows packages), launcher last — the launcher's `optionalDependencies`
reference the platform packages.

---

## Quick start

```bash
routre-cli setup        # interactive: listen addr, providers, URLs, keys, prices
routre-cli check        # validate config + which API keys are set
routre-cli serve        # gateway on 127.0.0.1:20128
routre-cli start        # start the daemon (systemd/launchd, or detached process)
routre-cli start --autostart  # start + enable auto-start at boot/login
routre-cli stop         # stop the daemon
routre-cli stop --autostart   # stop + disable auto-start
routre-cli restart      # restart the daemon (keeps auto-start state)
```

`setup` writes two files next to `config.json`:

- `config.json` — providers, tiers, base URLs, models (no secrets)
- `routre-cli.env` — API keys, **0600 permissions**, auto-loaded by
  `serve`/`check`/`list` (no shell exports needed)

Manual equivalent (see `config.example.json`):

```bash
cp config.example.json config.json
export ANTHROPIC_API_KEY=... OPENAI_API_KEY=...   # or write router-cli.env
./routre-cli check -config config.json
./routre-cli serve -config config.json
```

### Point a coding agent at it

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:20128   # Claude Code
export OPENAI_BASE_URL=http://127.0.0.1:20128      # Codex / opencode / etc.
```

opencode: add a provider to `opencode.json`:

```jsonc
"provider": {
  "llmrouter": {
    "npm": "@ai-sdk/openai-compatible",
    "name": "LLMRouter",
    "options": { "baseURL": "http://127.0.0.1:20128/v1" },
    "models": { "hy3": { "name": "Hy3", "modelID": "tencent/hy3" } }
  }
}
```

Endpoints: `POST /v1/chat/completions`, `POST /v1/messages`,
`GET /v1/models`, `GET /v1/status`, `GET /v1/usage`, `GET /healthz`.

### Connect every model (opencode-go, opencode zen, OpenRouter)

The repo ships `config.all.json` — a ready config that exposes **501
models** through one endpoint, already verified live:

| Provider | Base URL | Models | Key env |
| --- | --- | --- | --- |
| `opencode-go` | `https://opencode.ai/zen/go/v1` | 26 (minimax, kimi, glm, deepseek, qwen, hy3, gpt-5.6-luna, …) | `OPENCODE_GO_API_KEY` (from opencode's `auth.json`) |
| `opencode-zen` | `https://opencode.ai/zen/v1` | 62 (claude-fable-5, claude-opus-5, gemini, gpt-5.x, grok, free tier, …) | `OPENCODE_GO_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | 413 (all OpenRouter models) | `OPENROUTER_API_KEY` |

```bash
cp config.all.json config.json
# routre-cli.env: OPENCODE_GO_API_KEY=<from ~/.local/share/opencode/auth.json>
#                OPENROUTER_API_KEY=<your key>
routre-cli serve
curl http://127.0.0.1:20128/v1/models          # 501 models
# use any model as <provider>/<model> in your agent, e.g.:
#   opencode-zen/claude-fable-5  opencode-go/hy3  openrouter/deepseek/deepseek-chat
```

Tier order in `config.all.json`: `opencode-go` → `opencode-zen`
(subscription), `openrouter` (fallback) — so if a model is missing or a
provider 5xx/401s, the gateway fails over automatically. Refresh the
catalog at any time:

```bash
# opencode models
curl -H "Authorization: Bearer $OPENCODE_GO_API_KEY" https://opencode.ai/zen/go/v1/models
curl -H "Authorization: Bearer $OPENCODE_GO_API_KEY" https://opencode.ai/zen/v1/models
# openrouter models (with pricing)
curl https://openrouter.ai/api/v1/models
```

### Validate without a paid key

```bash
./cmd/mock-upstream/mock-upstream -addr 127.0.0.1:19999   # mock provider
# config with base_url "http://127.0.0.1:19999/v1"
MOCK_KEY=x ./routre-cli serve -config config-mock.json
opencode run --model <provider>/<model> "hello"
```

---

## Commands

| Command | Purpose |
| --- | --- |
| `routre-cli setup [-config f]` | interactive wizard (providers, URLs, API keys) |
| `routre-cli serve [-config f] [-port :p]` | run the gateway in the foreground |
| `routre-cli start [-config f] [--autostart]` | start the daemon (systemd/launchd, or detached process) |
| `routre-cli stop [-config f] [--autostart]` | stop the daemon (+ disable auto-start) |
| `routre-cli restart [-config f]` | restart the daemon (keeps auto-start state) |
| `routre-cli check [-config f]` | validate config + API keys |
| `routre-cli list [-config f] [-url http://127.0.0.1:20128]` | connected providers + token/cost ledger |
| `routre-cli logs [-n 50] [-f] [-config f]` | tail the per-request log |
| `routre-cli bench [-config f] [-target 90]` | RTK token-reduction benchmark (gated) |
| `routre-cli version` | print version |

### `start` / `stop` / `restart` — daemon lifecycle

- The gateway is managed through the OS service manager when installed as
  one — **systemd** (system or `--user` scope, auto-detected) or **launchd**
  on macOS (see `deploy/`). Without an installed service, `start` spawns a
  detached `serve` background process logging to
  `~/.routre-cli/daemon.log` and waits for the port to come up; `stop`
  finds the process listening on the configured port and sends SIGTERM
  (graceful shutdown, usage saved first).
- `start --autostart` / `stop --autostart` also enable / disable
  boot/login auto-start (`systemctl enable` / `disable`, `launchctl load` /
  `unload -w`). Auto-start requires an installed service.
- `restart` keeps the current auto-start state.

### `list` — everything connected, per agent, with totals

```text
== configured providers ==
  [subscription] openrouter  openai  key ok  models=tencent/hy3,... cost n/a

== live gateway ==
  openrouter     up

== token & cost ledger ==
  source: live

  codex
    requests: 2
    consumed: 58 tokens (18 in + 40 out)
    saved:    3351 tokens (rtk 2308 + cache 1043)
    cost:     n/a (no prices configured)   saved: n/a
    by provider/model:
      codex/tencent/hy3  2 req  58 tok  saved 3351

  opencode
    requests: 4
    consumed: 112 tokens (62 in + 50 out)
    saved:    28 tokens (rtk 0 + cache 28)
    cost:     $0.000021   saved: $0.000000

  TOTAL
    requests: 6
    consumed: 170 tokens   saved: 3379 tokens (95.2%)
    cost:     $0.000021   saved: $0.000000
```

Usage is attributed per coding agent by User-Agent, persisted to
`~/.routre-cli/usage.json` (survives restarts), and works offline from the
persisted file when the gateway is down. Costs come from provider-reported
usage (OpenRouter reports real `usage.cost`) or from `price_in` /
`price_out` in the config (USD per 1M tokens).

---

## Features

### Automatic failover

- Providers are configured in **tiers** (`subscription` → `cheap` → `free`),
  tried in order; within a tier, providers are tried in order.
- Failures (5xx, 429, 401/403, network errors) fail over to the next
  provider; the failed one enters an **exponential cooldown** (2 s base →
  30 min cap). Success resets. Cooldowns are per provider — one failing
  provider never cools down the others.
- **Streaming requests fail over too**: an upstream 5xx/429 answered
  before the first stream byte is treated like a non-streaming failure
  (cooldown + next provider); only after the first byte does a stream
  abort stop the request (no duplicated output). Client-caused errors
  (400/404/422, e.g. context-length) are surfaced, not retried.
- **Honest error identity**: `model_not_found` (503) is only returned when
  no configured provider (and no fallback) can serve the model; when every
  provider that could serve it is in cooldown, the gateway returns
  `providers_unavailable` (503) with a `Retry-After` header instead — the
  remedy is waiting, not editing the config.
- The gateway **holds the provider API keys** (from `api_key_env` /
  `routre-cli.env`) and injects them upstream — a client's `Authorization`
  header is a placeholder and is never forwarded.
- `routre-cli list` shows live per-provider cooldown state.

### RTK token compression (≥90% on tool-heavy traffic)

Heuristic compression of `tool_result` content — no local LM, no network
calls:

| Filter | Rule |
| --- | --- |
| git-diff | 10 changed lines/hunk cap + 80/30 head/tail trim |
| git-log | dedup + 50/15 trim |
| grep | dedup + 80/40 trim |
| tree / ls / find / git-status | dedup |
| build-output | dedup + 50/25 trim |
| read-numbered / search-list | dedup |
| smart-truncate (fallback) | head 120 / tail 60 |

Safety contract: **fail-open** (malformed JSON passes through), **never
grows** a payload, 500 B–10 MiB window, per-request safe. The `bench`
command measures reduction on 5 realistic tool-heavy payloads and gates
**both the aggregate (91.5%) and the worst per-payload (90.3%)** at ≥90%.

### Response cache

- Exact-match LRU keyed by SHA-256 of the **processed** body (post-RTK),
  default 512 entries / 1 h TTL / 8 MiB max entry — ~free RAM.
- Non-streaming responses only; streamed responses are never cached.
- Optional `prefix_order` moves system messages first for stable keys and
  stable upstream prompt-cache prefixes.
- Cache hits record their token savings in the ledger (`cache saved`).

### Always-on daemon

- `deploy/routre-cli.service` + `deploy/routre-cli.socket` (systemd;
  socket activation → ~0 MB idle) and `deploy/dev.routrecli.daemon.plist`
  (launchd for macOS). `MemoryMax=200M` hard guard.
- **SIGHUP reloads config + env** without dropping connections (SIGINT/
  SIGTERM = graceful shutdown, usage saved first).
- `routre-cli start [--autostart]`, `routre-cli stop [--autostart]` and
  `routre-cli restart` manage the daemon through systemd (system or
  `--user` scope) or launchd; without an installed service they fall back
  to a detached background process logging to `~/.routre-cli/daemon.log`
  and wait for the configured port to come up.
- `--autostart` on `start` runs `systemctl enable` / `launchctl load -w`;
  on `stop` it runs `systemctl disable` / `launchctl unload -w`.

---

## Configuration

```jsonc
{
  "listen": "127.0.0.1:20128",
  "rtk":   { "enabled": true, "min_bytes": 500, "max_bytes": 10485760 },
  "cache": { "enabled": true, "max_entries": 512, "ttl_seconds": 3600, "prefix_order": false },
  "tiers": [
    { "name": "subscription", "providers": [
      { "name": "openrouter", "kind": "openai",
        "base_url": "https://openrouter.ai/api/v1",
        "api_key_env": "OPENROUTER_API_KEY",
        "models": ["tencent/hy3"],
        "price_in": 0, "price_out": 0 }   // USD per 1M tokens; 0 = unknown
    ]}
  ]
}
```

| Field | Meaning |
| --- | --- |
| `kind` | `openai` or `anthropic` (dialect translation for cross-kind fallback) |
| `api_key_env` | env var holding the key — loaded from `routre-cli.env` or shell |
| `price_in` / `price_out` | USD per 1M tokens for cost reporting (optional) |
| `tiers` order | fallback order; keep subscription/cheap/free |

---

## Layout

```text
main.go                  CLI (setup/serve/check/start/stop/restart/list/bench/version)
bench.go                 RTK benchmark + 90% gate
setup.go                 interactive setup wizard
start.go                 daemon start/restart (systemd/launchd/detached spawn)
stop.go                  daemon stop (systemd/launchd/port scan + SIGTERM)
list.go                  providers + per-agent token/cost ledger
internal/config/         JSON config + routre-cli.env + SIGHUP reload
internal/router/         tiers, failover, cooldowns (exponential backoff)
internal/rtk/            token compression (12 filters + autodetect)
internal/cache/          exact-match LRU + prefix ordering
internal/proxy/          HTTP gateway, SSE relay, key injection, translation
internal/usage/          token/cost ledger (persisted to ~/.routre-cli/)
internal/tokenize/       token estimator (benchmark instrument)
internal/mock/           mock upstream (tests + keyless e2e)
benchdata/               tool-heavy request bodies for the bench gate
scripts/measure-ram.sh   RSS/peak/growth measurement
deploy/                  systemd unit+socket, launchd plist
npm/                     npm distribution (7 packages: 4 Unix + 2 scoped win32 + launcher)
```

---

## Known gaps (full detail in SPEC.md)

- Cross-kind **streaming** translation returns 501; non-streaming cross-kind
  translation is lossy (tools dropped, `tool_call_id` links lost).
- Token estimates are an approximation (≈4 bytes/token) — a benchmark
  instrument, not billing-grade (tiktoken integration is planned).
- 90% is measured on tool-result tokens; output tokens are never
  compressed, so real-session savings depend on the tool-traffic mix
  (this is exactly what `routre-cli list` shows you).
- 401/403 token refresh is not implemented (cooldown + failover applies).

## License

MIT — see [LICENSE](LICENSE). (The RTK filter approach is a clean-room
reimplementation of the MIT-licensed 9router `open-sse/rtk` ideas.)

---

See [`SPEC.md`](SPEC.md) for the full decision record, metric definitions,
failover policy table, and validation roadmap.
