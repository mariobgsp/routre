# routre-cli

A low-RAM LLM gateway for coding agents. One static binary (~6.7 MiB, ~9 MiB
RAM idle) that gives every OpenAI/Anthropic-compatible CLI — opencode,
Claude Code, Codex, Cursor, … — automatic provider failover, RTK token
compression (≥90% on tool-heavy traffic), response caching, and a
per-agent token/cost ledger.

```bash
npm install -g routre-cli     # one command, no Go toolchain needed
routre-cli setup              # wizard: provider URLs + API keys
routre-cli serve              # gateway on 127.0.0.1:20128
routre-cli start              # start the daemon (systemd/launchd or detached)
routre-cli list               # connected providers + token/cost ledger
```

Point any agent at `http://127.0.0.1:20128` via `OPENAI_BASE_URL` /
`ANTHROPIC_BASE_URL` — failover, compression, and caching come for free.

---

## Table of contents

- [Why this exists](#why-this-exists)
- [Install](#install)
- [Quick start](#quick-start)
- [How it works](#how-it-works)
  - [Automatic failover](#automatic-failover)
  - [RTK token compression](#rtk-token-compression--90-on-tool-heavy-traffic)
  - [Response cache](#response-cache)
  - [Token & cost ledger](#token--cost-ledger)
  - [Always-on daemon](#always-on-daemon)
- [Configuration](#configuration)
- [Commands](#commands)
- [Benchmarks](#benchmarks)
- [Project layout](#project-layout)
- [Known gaps](#known-gaps)
- [License](#license)

---

## Why this exists

Built as a decision-driven spike from two research passes
([`research.md`](../research.md) and [`context.md`](../context.md)):

- **Not 9router** — right feature set, but Node/Next.js with ~80 MB idle RAM
  and a documented unbounded leak (~4.8 GB in 3 days), plus unbenchmarked
  20–65% savings claims.
- **Not LiteLLM/Portkey self-host** — Python/Postgres/Redis stacks sized in
  gigabytes.
- **Single static Go binary, stdlib only** — proven low-RAM pattern (Go ~5K
  QPS at ~11 ms proxy overhead), cross-compiled for 6 platforms (7 npm
  packages, incl. scoped win32).
- **Honest metrics** — the 90% claim is defined, gated, and reproducible:
  `routre-cli bench` fails the build if it regresses.

---

## Install

### npm (macOS / Windows / Linux) — recommended

```bash
npm install -g routre-cli
routre-cli version
```

The npm package ships one static Go binary per platform (via optional
dependencies) plus a tiny Node launcher. No Go toolchain, no runtime
dependencies.

| Platform | Package |
| --- | --- |
| linux x64 / arm64 | `routre-cli-linux-x64` / `routre-cli-linux-arm64` |
| darwin x64 / arm64 | `routre-cli-darwin-x64` / `routre-cli-darwin-arm64` |
| win32 x64 / arm64 | `@mariobgsp/routre-cli-win32-x64` / `@mariobgsp/routre-cli-win32-arm64` |

> The Windows packages are scoped (`@mariobgsp/…`) because npm's spam
> detection rejects the unscoped `routre-cli-win32-*` names.

### From source (developers)

```bash
make build          # needs Go ≥ 1.22
./routre-cli version
```

### Build / publish the npm distribution

```bash
make dist-npm       # cross-compiles all 6 platforms → npm/dist/*.tgz (7 packages)
NPM_TOKEN=<token> bash ./npm/publish.sh
```

Releases are also published by CI on a `v*` tag push (see
`.github/workflows/publish.yml`, npm Trusted Publishing / OIDC — no token
on CI). Publish order matters: platform packages first (Unix, then scoped
Windows), launcher last — the launcher's `optionalDependencies` reference
the platform packages.

---

## Quick start

```bash
routre-cli setup        # interactive: listen addr, providers, URLs, keys, prices
routre-cli check        # validate config + which API keys are set
routre-cli serve        # gateway on 127.0.0.1:20128
routre-cli start --autostart  # start daemon + enable boot/login auto-start
routre-cli stop         # stop the daemon
routre-cli list         # providers + token/cost ledger
```

`setup` writes two files next to `config.json`:

- `config.json` — providers, tiers, base URLs, models (no secrets)
- `routre-cli.env` — API keys, **0600 permissions**, auto-loaded by
  `serve` / `check` / `list` (no shell exports needed)

### Point a coding agent at it

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:20128   # Claude Code
export OPENAI_BASE_URL=http://127.0.0.1:20128      # Codex / opencode / etc.
```

Endpoints: `POST /v1/chat/completions`, `POST /v1/messages`,
`GET /v1/models`, `GET /v1/status`, `GET /v1/usage`, `GET /healthz`.

### Connect everything (opencode-go, opencode zen, OpenRouter)

The repo ships `config.all.json` — a ready config exposing **501 models**
through one endpoint, verified live:

| Provider | Base URL | Models | Key env |
| --- | --- | --- | --- |
| `opencode-go` | `https://opencode.ai/zen/go/v1` | 26 (minimax, kimi, glm, deepseek, qwen, hy3, …) | `OPENCODE_GO_API_KEY` |
| `opencode-zen` | `https://opencode.ai/zen/v1` | 62 (claude-fable-5, gemini, gpt-5.x, grok, free tier, …) | `OPENCODE_GO_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | 413 (all OpenRouter models) | `OPENROUTER_API_KEY` |

```bash
cp config.all.json config.json
# routre-cli.env:
#   OPENCODE_GO_API_KEY=<from ~/.local/share/opencode/auth.json>
#   OPENROUTER_API_KEY=<your key>
routre-cli serve
curl http://127.0.0.1:20128/v1/models          # 501 models
# use any model as <provider>/<model>, e.g.:
#   opencode-zen/claude-fable-5  opencode-go/hy3  openrouter/deepseek/deepseek-chat
```

Tier order: `opencode-go` → `opencode-zen` (subscription), `openrouter`
(fallback). If a model is missing or a provider 5xx/401s, the gateway
fails over automatically.

### Validate without a paid key

```bash
./cmd/mock-upstream/mock-upstream -addr 127.0.0.1:19999   # mock provider
# config with base_url "http://127.0.0.1:19999/v1"
MOCK_KEY=x ./routre-cli serve -config config-mock.json
opencode run --model <provider>/<model> "hello"
```

---

## How it works

### Automatic failover

- Providers are configured in **tiers** (`subscription` → `cheap` → `free`)
  and tried in order; within a tier, providers are tried in order.
- Failures (5xx, 429, 401/403, network errors) fail over to the next
  provider; the failed one enters an **exponential cooldown** (2 s base →
  30 min cap). Success resets. Cooldowns are per provider — one failing
  provider never cools down the others.
- **Transient blips are retried first**: a network error or 5xx is retried
  once on the same provider (500 ms delay) before failover — an hour-long
  upstream 503 no longer burns every fallback in the same window.
- **Streaming requests fail over too**: an upstream 5xx/429 answered before
  the first stream byte is treated like a non-streaming failure; after the
  first byte, a stream abort stops the request (no duplicated output).
- **Client-caused errors** (400/404/422, e.g. context-length) are surfaced,
  not retried.
- **Honest error identity**: `model_not_found` (503) only when no configured
  provider (and no fallback) can serve the model; when every provider that
  could serve it is cooling down, the gateway returns `providers_unavailable`
  (503) with a `Retry-After` header instead — the remedy is waiting, not
  editing the config.
- The gateway **holds the provider API keys** (from `api_key_env` /
  `routre-cli.env`) and injects them upstream — a client's `Authorization`
  header is a placeholder and is never forwarded.

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

- Exact-match LRU keyed by SHA-256 of the **processed** body (post-RTK).
  Defaults: 512 entries / 1 h TTL / 8 MiB max entry; the shipped
  `config.all.json` uses 4096 entries / 24 h TTL / 64 MiB budget.
- Non-streaming responses only; streamed responses are never cached.
- Optional `prefix_order` moves system messages first for stable keys and
  stable upstream prompt-cache prefixes.
- Cache hits record their token savings in the ledger — credited with the
  **upstream-reported** prompt token count stored on the cached response,
  so the ledger matches the provider's billing numbers instead of
  length-based estimates.

### Token & cost ledger

Per coding agent (by User-Agent): requests, tokens in/out, RTK savings,
cache savings, and estimated cost.

- Persisted to `~/.routre-cli/usage.json` — survives restarts, **autosaved
  every 60 s and on SIGHUP**, so a crash loses at most one minute of
  ledger. Works offline from the persisted file when the gateway is down.
- Costs come from provider-reported usage (OpenRouter reports real
  `usage.cost`) or from `price_in` / `price_out` in the config (USD per 1M
  tokens).
- Tail per-request detail with `routre-cli logs`, see the ledger with
  `routre-cli list`.

### Always-on daemon

- `deploy/routre-cli.service` + `deploy/routre-cli.socket` (systemd;
  socket activation → ~0 MB idle) and `deploy/dev.routrecli.daemon.plist`
  (launchd for macOS). `MemoryMax` guard included.
- **SIGHUP reloads config + env** without dropping connections (SIGINT /
  SIGTERM = graceful shutdown, ledger saved first).
- `routre-cli start [--autostart]`, `stop [--autostart]`, and `restart`
  manage the daemon through systemd (system or `--user` scope) or launchd;
  without an installed service they fall back to a detached background
  process logging to `~/.routre-cli/daemon.log`.

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

A full reference config with 501 models lives in `config.all.json`; a
minimal template is `config.example.json`.

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

---

## Benchmarks

Measured on this machine, 2026-08-15:

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

## Project layout

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

- Cross-kind **streaming** translation (OpenAI↔Anthropic) is implemented —
  text and tool-call frames are translated in-flight with the tool-call id
  preserved; non-streaming cross-kind stays lossy for cheap-tier fallback.
- Token estimates are an approximation (≈4 bytes/token) — a benchmark
  instrument, not billing-grade (tiktoken integration is planned).
- 90% is measured on tool-result tokens; output tokens are never
  compressed, so real-session savings depend on the tool-traffic mix
  (this is exactly what `routre-cli list` shows you).
- 401/403 token refresh is not implemented (cooldown + failover applies).

---

## License

MIT — see [LICENSE](LICENSE). (The RTK filter approach is a clean-room
reimplementation of the MIT-licensed 9router `open-sse/rtk` ideas.)

---

See [`SPEC.md`](SPEC.md) for the full decision record, metric definitions,
failover policy table, and validation roadmap.
