# routre

> **In plain English:** routre is a tiny helper on your computer that sits between
> your AI apps and AI providers.
>
> - **Saves money** — shrinks repetitive tool output before it is billed (≥90% on
>   tool-heavy traffic).
> - **Stays online** — if one provider is busy or down, it automatically tries the
>   next one.
> - **Easy to use** — set it up once with `routre setup`, or open the settings page
>   with no terminal needed.

```text
Your app → routre (one address) → cheapest healthy provider → answer back
```

A single static binary (~10 MiB, ~10 MiB RAM idle) that gives every
OpenAI/Anthropic-compatible CLI — opencode, Claude Code, Codex, Cursor, … —
automatic provider failover, RTK token compression (≥90% on tool-heavy traffic),
response caching, and a per-agent token/cost ledger. A localhost dashboard at
`http://127.0.0.1:20128/ui` lets non-programmers configure it without editing JSON.

Current release: **v0.7.2** — see [CHANGELOG.md](CHANGELOG.md).

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre/main/install.sh | sh
routre version
```

Downloads the latest GitHub release, verifies its sha256 checksum, and installs a
static binary to `~/.local/bin` (no sudo). Env overrides: `ROUTRE_INSTALL_DIR`,
`ROUTRE_VERSION`. Upgrade with `routre update`. Windows: download the
`routre_windows_*.zip` asset from [Releases](https://github.com/mariobgsp/routre/releases/latest).

From source (developers, needs Go ≥ 1.22): `make build`. Full release instructions
are in the [Releasing](https://github.com/mariobgsp/routre/blob/main/Makefile)
target comments and `.github/workflows/release.yml`.

## Quick start

```bash
routre setup              # interactive wizard: providers, URLs, API keys, prices
routre serve              # gateway on 127.0.0.1:20128
```

`setup` writes two files next to `config.json`:

- `config.json` — providers, tiers, base URLs, models (no secrets)
- `routre.env` — API keys, **0600**, auto-loaded by `serve` / `check` / `list`

Then point a coding agent at it:

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:20128   # Claude Code
export OPENAI_BASE_URL=http://127.0.0.1:20128      # Codex / opencode / etc.
```

Failover, compression, and caching come for free from here. A ready-made config
exposing **506 models** through one endpoint ships in [`config.all.json`](config.all.json).

### Local dashboard (no terminal needed)

Open `http://127.0.0.1:20128/ui` — live status, provider tiers, key presence, a
form to set API keys, and a validated JSON editor for the full config. Loopback
only; adds ~0 MiB at idle.

## Commands

| Command | Purpose |
| --- | --- |
| `routre setup` | interactive wizard (providers, URLs, API keys) |
| `routre serve [--debug]` | run the gateway in the foreground |
| `routre start [--autostart]` | start the daemon (systemd/launchd, or detached) |
| `routre stop [--autostart]` | stop the daemon (+ disable auto-start) |
| `routre restart` | restart the daemon (keeps auto-start state) |
| `routre check` | validate config + API keys |
| `routre doctor` | probe every provider (per-provider `ok`/`overloaded`/`auth`) |
| `routre list` | connected providers + token/cost ledger |
| `routre models sync \| diff` | fetch `GET /v1/models` per provider, persist new IDs |
| `routre logs [-n] [-f] [-errors]` | tail the per-request log |
| `routre bench [-target 90]` | RTK token-reduction benchmark (gated) |
| `routre update [-check]` | self-update: download + verify + replace this binary |
| `routre version` | print version |

## HTTP endpoints

`POST /v1/chat/completions`, `POST /v1/responses`, `POST /v1/messages`,
`GET /v1/models`, `GET /v1/status`, `GET /v1/usage`, `GET /healthz`,
`GET /metrics` (Prometheus), `GET /ui` (dashboard). `/v1/responses` speaks the
OpenAI Responses API and works with `OPENAI_BASE_URL` out of the box.

## Configuration

```jsonc
{
  "listen": "127.0.0.1:20128",
  "forward_unknown": true,
  "rtk":   { "enabled": true, "min_bytes": 500, "max_bytes": 10485760 },
  "cache": { "enabled": true, "max_entries": 512, "ttl_seconds": 3600 },
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
| `kind` | `openai`, `anthropic`, or `gemini` (dialect translation for cross-kind fallback) |
| `api_key_env` | env var holding the key — loaded from `routre.env` or shell |
| `price_in` / `price_out` | USD per 1M tokens for cost reporting (optional) |
| `forward_unknown` | forward a model no provider lists (default true) |
| `tiers` order | fallback order; keep subscription/cheap/free |

A full reference config is [`config.example.json`](config.example.json); the
506-model one is [`config.all.json`](config.all.json).

## How it works

Every request runs a 7-step pipeline: **detect** the API dialect → **compress**
tool output (RTK) → **cache** lookup → **route** across tiers → **retry/failover**
→ **translate** dialect → **relay**. The gateway holds your provider keys and
injects them upstream; failover, compression, and caching are automatic.

📖 **[How it works →](docs/HOW-IT-WORKS.md)** — the full pipeline, request
lifecycle, failover policy, RTK filters, cache internals, cross-dialect
translation, observability, security, and diagrams.

## Benchmarks

| Metric | Result | Target |
| --- | --- | --- |
| RTK tool-token reduction | **91.5%** (worst payload 90.3%) | ≥ 90% (bench-gated) |
| Idle RSS | **~10 MiB** | ≤ 100 MiB |
| Binary size | **~10.6 MiB** | small |
| Gateway-added latency (1 MiB body) | ~26 ms p50 | small |

`routre bench` gates the ≥90% RTK claim — it fails the build on a regression.
Reproduce with `make build test bench`.

## Project layout

```text
main.go, bench.go, setup.go, start.go, stop.go, list.go, logs.go, models.go, update.go
internal/proxy/          HTTP gateway, SSE relay, /ui dashboard, failover runner
internal/router/         tiers, failover, cooldowns
internal/rtk/            token compression (12 filters)
internal/cache/          exact-match LRU
internal/proxy/dialect/  cross-dialect SSE translation
tests/                   binary-level e2e suite (drives the real `routre serve`)
```

See [docs/HOW-IT-WORKS.md](docs/HOW-IT-WORKS.md#project-layout) for the full map.

## Development

```bash
make build test bench    # build, run all tests, gate the 90% RTK claim
go test ./tests -v       # binary-level e2e suite only
```

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for the release history.

## License

MIT — see [LICENSE](LICENSE). (The RTK filter approach is a clean-room
reimplementation of the MIT-licensed 9router `open-sse/rtk` ideas.)
