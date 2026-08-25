# routre-cli

A low-RAM LLM gateway for coding agents.

One static binary (~6.7 MiB, ~9 MiB RAM idle) that gives every
OpenAI/Anthropic-compatible CLI — opencode, Claude Code, Codex, Cursor, … —
automatic provider failover, RTK token compression (≥90% on tool-heavy
traffic), response caching, and a per-agent token/cost ledger.

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

## Quick start

```bash
npm install -g routre-cli

# one-time setup (provider base URLs + API keys)
routre-cli setup

# start the daemon
routre-cli start
```

Then point your coding agent at the gateway. For example, for Claude Code:

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:20128
```

All your provider keys stay on your machine (in `~/.routre-cli/`), and the
gateway handles routing, failover, token compression, caching, and cost
tracking for you.

## What you get for free

- **Automatic provider failover** — one provider down or rate-limited? The
  gateway transparently routes to the next healthy tier.
- **RTK token compression** — compress tool results by ≥90% before they hit
  the model, so prompt caching stays cheap and context stays small.
- **Response caching** — identical repeat requests are served from a bounded
  LRU cache (with a stable-prefix ordering to maximize upstream
  prompt-cache hits).
- **Per-agent token & cost ledger** — `routre-cli list` and a live
  `/v1/status` endpoint show what each agent spent.
- **Zero-config model discovery** — providers served through `GET /v1/models`
  have their model lists discovered automatically; no need to hand-maintain
  the list in config.

## Requirements

- Node.js ≥ 18 (for the npm launcher)
- macOS, Windows, or Linux (x64 / arm64)

## GitHub

Source, config reference, benchmarks, and changelog:
<https://github.com/mariobgsp/routre-cli>

## License

MIT
