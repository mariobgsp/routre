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

One static binary gives every OpenAI/Anthropic-compatible CLI — opencode,
Claude Code, Codex, Cursor, … — automatic provider failover, RTK token
compression, response caching, and a per-agent token/cost ledger. A localhost
dashboard at `http://127.0.0.1:20128/ui` lets non-programmers configure it
without editing JSON.

Current release: **v0.7.2** — see [CHANGELOG.md](CHANGELOG.md). Footprint,
benchmark, and model-catalog figures live in one place:
[Claims & measurements](#claims--measurements).

**Where to go next:** [Install](#install) ·
[Install with an AI prompt](#install-with-an-ai-prompt) ·
[Quick start](#quick-start) · [Point your agent at it](#point-your-agent-at-it) ·
[Dashboard](#local-dashboard-no-terminal-needed) ·
[Commands](#commands) · [HTTP endpoints](#http-endpoints) ·
[Configuration](#configuration) · [How it works](#how-it-works) ·
[Claims & measurements](#claims--measurements)

## Install

<p align="center">
  <video src="assets/routre-promo-64s-master-embed.mp4" poster="assets/routre-promo-64s-master.jpg"
         width="720" autoplay muted loop playsinline controls></video>
</p>

<p align="center"><sub>
  the 64s version, problem first ·
  <a href="assets/routre-promo-36s-hero-embed.mp4">36s cut</a> ·
  <a href="assets/routre-promo-15s-story-embed.mp4">15s story</a> ·
  <a href="promo/">how it was made</a>
</sub></p>

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

### Install with an AI prompt

Paste this into Claude Code, Codex, Cursor, or opencode — the agent runs the same
commands you would:

```text
Install routre (github.com/mariobgsp/routre) on this machine:
1. Detect OS/CPU, fetch the latest release asset for this platform
   (curl install.sh on macOS/Linux, zip from Releases on Windows), verify sha256,
   and put the binary on PATH as routre.
2. Run: routre setup   (wizard; non-interactive if keys already in env)
   If the provider API keys are missing, ask me once and write them to
   routre.env (0600).
3. Start the gateway: routre serve (or routre start --autostart for boot).
4. Point this agent at it:
   export OPENAI_BASE_URL=http://127.0.0.1:20128
   export ANTHROPIC_BASE_URL=http://127.0.0.1:20128
5. Verify: curl http://127.0.0.1:20128/healthz  and  routre list
6. Open the dashboard: http://127.0.0.1:20128/ui
Keep it stdlib-only. Do not install Python, Postgres, or Node.
```

Non-interactive one-shot (macOS/Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre/main/install.sh | sh && routre setup && routre serve
```

## Quick start

```bash
routre setup              # interactive wizard: providers, URLs, API keys, prices
routre serve              # gateway on 127.0.0.1:20128
```

`setup` writes two files next to `config.json`:

- `config.json` — providers, tiers, base URLs, models (no secrets)
- `routre.env` — API keys, **0600**, auto-loaded by `serve` / `check` / `list`

A ready-made config ships in [`config.all.json`](config.all.json) (see the
[claims table](#claims--measurements) for its model count).

## Point your agent at it

Failover, compression, and caching come for free as soon as a client points at the
gateway:

| Client | Configure with |
| --- | --- |
| Claude Code | `export ANTHROPIC_BASE_URL=http://127.0.0.1:20128` |
| Codex / opencode / Cursor | `export OPENAI_BASE_URL=http://127.0.0.1:20128` |
| Any OpenAI-compatible client | base URL `http://127.0.0.1:20128/v1` |
| Any Anthropic-compatible client | base URL `http://127.0.0.1:20128` |

routre holds the provider keys and injects them upstream; a client's
`Authorization` header is a placeholder and is never forwarded.

### Local dashboard (no terminal needed)

Open `http://127.0.0.1:20128/ui` — live status, provider tiers, key presence, a
form to set API keys, and a validated JSON editor for the full config. Loopback
only; adds no measurable memory at idle.

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
  "rtk":   { "enabled": true, "level": "routre", "min_bytes": 500, "max_bytes": 10485760,
             "scan_bytes": 524288, "budget_ms": 150 },
  "cache": { "enabled": true, "max_entries": 512, "ttl_seconds": 3600 },
  "timeouts": { "candidate_seconds": 45, "request_seconds": 180,
                "header_seconds": 120, "extension_seconds": 45,
                "generation_seconds": 300 },
  "persist":  { "enabled": true, "interval_seconds": 60 },
  "tiers": [
    { "name": "subscription", "providers": [
      { "name": "openrouter", "kind": "openai",
        "base_url": "https://openrouter.ai/api/v1",
        "api_key_env": "OPENROUTER_API_KEY",
        "models": ["tencent/hy3"],
        "price_in": 0, "price_out": 0 }   // USD per 1M tokens; 0 = unknown
    ] }
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
| `rtk.level` | `routre` (default, most aggressive) or `standard` |
| `rtk.scan_bytes` / `rtk.budget_ms` | bound compression work on very large tool output (defaults 512 KiB / 150 ms) |
| `timeouts.*` | where routre gives up on a slow upstream (see [How it works](docs/HOW-IT-WORKS.md)) |
| `persist.*` | keep the cache and lifetime counters warm across restarts (default on) |

A missing field takes its default. A full reference config is
[`config.example.json`](config.example.json); the large ready-made catalog is
[`config.all.json`](config.all.json).

## How it works

Every request runs a 7-step pipeline: **detect** the API dialect → **compress**
tool output (RTK) → **cache** lookup → **route** across tiers →
**retry/failover** → **translate** dialect → **relay**. The gateway holds your
provider keys and injects them upstream; failover, compression, and caching are
automatic.

📖 **[How it works →](docs/HOW-IT-WORKS.md)** — the full pipeline, request
lifecycle, failover policy, RTK filters, cache internals, cross-dialect
translation, observability, security, and diagrams.

## Claims & measurements

The single place these numbers live. Each row states how to reproduce it.

| Metric | Result | Reproduce | Target |
| --- | --- | --- | --- |
| RTK tool-token reduction | **94.8%** aggregate, **93.3%** worst payload (at the shipped `routre` level; `level: standard` measures ≈ 91%) | `make bench` | ≥ 90% (bench-gated) |
| Idle RSS | **12.6 MiB** | `./scripts/measure-ram.sh` | ≤ 100 MiB |
| Binary size | **10.9 MiB** | `CGO_ENABLED=0` build with `-s -w` | small |
| Gateway-added latency (1 MiB body) | **~26 ms p50** | `go test ./internal/proxy -bench=GatewayAddedLatency` | small |
| Model catalog | **509 models** | count the `models` arrays in [`config.all.json`](config.all.json) | — |

> Bench and footprint figures are toolchain- and machine-dependent. The above
> were re-measured on go1.27.1 linux/amd64; an earlier toolchain measured
> ~10 MiB idle / 10.6 MiB binary. `./scripts/measure-ram.sh` reproduces the
> footprint numbers (it needs `bc` on PATH — an `awk` one-liner would do).

## Project layout

```text
main.go, bench.go, setup.go, start.go, stop.go, list.go, logs.go, models.go, update.go
internal/proxy/          HTTP gateway, SSE relay, /ui dashboard, failover runner
internal/router/         tiers, failover, cooldowns
internal/rtk/            token compression (11 filters)
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
