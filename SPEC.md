# routre-cli — Specification & Decision Record

Status: **v0.1.0-dev (scaffold + first measurements, 2026-08-15)**

This document records the decisions behind routre-cli, the metrics it
promises, the measured evidence, known gaps, and the validation plan. It is
grounded in two research passes: external evidence
([`research.md`](../research.md)) and local codebase analysis of 9router
([`context.md`](../context.md)).

---

## 1. Decision record (why this exists)

| # | Decision | Evidence |
| --- | --- | --- |
| D1 | **Do not fork/port 9router.** It is a Next.js/Node gateway + dashboard with ~80 MB idle RAM, a documented unbounded leak to ~4.8 GB in ~3 days (issue #1245), 2–3 min tool-loop latency regressions (#1440), and unbenchmarked 20–40% savings claims (walked back in PR #2005/#1998). | research.md F1/F2/F9; context.md §5 |
| D2 | **Single static Go binary** (~6.7 MiB, no CGO, stdlib-only). Go sustains ~5K QPS at ~11 ms proxy overhead; Rust only if sub-1 ms P99 becomes a requirement. Python gateways (LiteLLM: 4 GiB/worker prod guidance) and Node are out for a 100–200 MB daemon. | research.md §4/§5, F4/F5 |
| D3 | **OpenAI-compatible HTTP on 127.0.0.1** (`/v1/chat/completions` + `/v1/messages`). CLI tools (Claude Code, Codex, Cursor…) only accept base-URL env vars (`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`), not unix sockets. Unix socket reserved for future control channel. | research.md F6; context.md §6 |
| D4 | **Failover = tiered providers + per-provider exponential cooldown** (2 s base → 30 min cap), retry only before the first stream byte; mid-stream SSE aborts never fail over (no duplicated output); client-caused 4xx (400/404/422) surface without failover. Mirrors 9router's accountFallback policy (MIT, reusable pattern) minus the dashboard. | research.md §5; context.md §3 |
| D5 | **Token reduction = heuristic tool_result compression (RTK-style) + exact-match response cache + optional stable-prefix ordering.** No local LM compressor (LLMLingua-class would add a model and break the RAM budget), no semantic cache (needs embeddings/DB), no vector store. | research.md §3, F3; context.md §4 |
| D6 | **Always-on via systemd unit (MemoryMax=200M) with optional socket activation (~0 MB idle)**; launchd plist for macOS. SIGHUP reload, no drop of connections. Lifecycle commands `start [--autostart]`, `stop [--autostart]`, `restart` (systemd `--user` scope supported). | research.md F7; deploy/ |
| D7 | **Honest metric definition.** The 90% guarantee applies to **tool-result tokens in tool-heavy payloads** — the same narrow sense in which the "90%" claim is defensible at all (Anthropic's 90% cache-read discount is a price discount, not token removal; RouteLLM's 85% is cost, not tokens; LLMLingua's 95% is input-only with a local LM). Whole-payload reduction is reported separately and depends on the workload mix. | research.md §3 |

## 2. Metrics (the promises)

### 2.1 Token reduction

| Metric | Definition | Gate |
| --- | --- | --- |
| Tool reduction | estimated tokens inside tool/tool_result content, before vs after RTK, averaged over `benchdata/*.json` (5 tool-heavy request bodies: git-diff, build-output, tree+ls, grep -rn, git-log) | **≥ 90%** (default `-target 90`) |
| Payload reduction | estimated tokens of the whole request body, before vs after | reported, workload-dependent |

Token estimation is `internal/tokenize` (≈ 4 bytes/token + CJK ≈ 1 token/rune);
it is a measurement instrument for before/after deltas of the same payload,
not a billing-grade tokenizer. Exact numbers require tiktoken /
claude-tokenizer integration (open item).

**Measured 2026-08-15 (this machine):**

| payload | payload% | tool% |
| --- | --- | --- |
| build-output.json | 90.0 | 90.6 |
| git-diff.json | 87.3 | 87.9 |
| git-log.json | 91.9 | 92.8 |
| grep-results.json | 91.9 | 92.1 |
| tree-ls.json | 91.8 | 90.3 |
| **aggregate** | **90.6** | **90.7** → **PASS** |

### 2.2 RAM

| Metric | Definition | Gate |
| --- | --- | --- |
| Idle RSS | `scripts/measure-ram.sh` samples VmRSS for N seconds with zero traffic | ≤ 100 MiB |
| Peak RSS | VmHWM over the window (extend with load in P1) | ≤ 200 MiB |
| Binary size | `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w"` | report |

**Measured:** idle avg 9 MiB, peak 9 MiB, growth 0 MiB over 15 s, binary
6.7 MiB → **PASS**. Under load (P1 harness): three consecutive opencode
1.18.15 sessions through the gateway, RSS 12 MiB avg / 12.9 MiB peak
(VmHWM) → **PASS** vs 200 MiB cap (mock upstream, `cmd/mock-upstream`).

### 2.3 Failover correctness

Integration tests (`internal/proxy/proxy_test.go`) assert, against mock
upstreams:

- tier order and exactly-one-request-per-provider failover (a,b fail → c serves);
- no failover on client-caused 400; failover on 401 and on 5xx;
- 503 + `Retry-After: 5` when all providers fail;
- SSE streaming pass-through with `[DONE]`;
- mid-stream abort does **not** fail over;
- cache hit returns identical body with exactly one upstream call;
- RTK-compressed body is what the upstream receives (never grows).

## 3. Architecture

```text
client CLI (opencode / Claude Code / Codex / ...)
   │  ANTHROPIC_BASE_URL / OPENAI_BASE_URL → http://127.0.0.1:20128
   ▼
proxy (internal/proxy)
   ├─ RTK compression (internal/rtk)   — tool_result heuristics, fail-open
   ├─ prefix ordering (optional)       — system messages first, stable key
   ├─ exact-match cache (internal/cache) — sha256(body), LRU+TTL, non-streaming
   ├─ usage ledger (internal/usage)    — tokens + cost per client (User-Agent)
   └─ failover loop (internal/router)  — tiers → providers → cooldowns
        │  kind match?  relay as-is (gateway-held API key injected)
        │  kind mismatch?  translate (non-streaming only; streaming → 501)
        ▼
upstream providers (anthropic / openai / openrouter ...) in tier order
```

API keys live in `routre-cli.env` (0600) next to `config.json` and are
loaded automatically; the gateway injects them upstream, so clients never
need real keys (a client `Authorization` header is treated as a
placeholder and never forwarded). Distribution: `npm install -g
routre-cli` ships one static Go binary per platform; `routre-cli setup`
writes config + key file interactively; `routre-cli list` shows the
per-client ledger.

Signal handling: SIGHUP → reload config (router/cache/rtk updated in place);
SIGINT/SIGTERM → graceful shutdown (5 s bound). Daemon lifecycle:
`routre-cli start [--autostart]` / `stop [--autostart]` / `restart`
drive systemd (system or `--user` scope) or launchd; without an
installed service they fall back to a detached `serve` process
(`~/.routre-cli/daemon.log`) and a port scan.

## 4. Failover policy table

| Upstream outcome | Class | Action |
| --- | --- | --- |
| network error / timeout | network/timeout | cooldown++, fail over |
| 401 / 403 | auth | cooldown++, fail over (token refresh is P2) |
| 429 | rate-limit | cooldown++, fail over |
| 5xx | server | cooldown++, fail over |
| 400 / 404 / 422 | client | surface to client, no failover |
| stream error before first byte | network | cooldown++, fail over |
| stream error after first byte | stream-abort | stop; never retry (duplication) |
| upstream non-2xx on a streaming request | same as non-streaming | fail over before the first byte (nothing streamed yet); only 2xx streams |

Cooldown: `failures` count → `base·2^(failures-1)`, capped at 30 min;
success resets. Stream aborts never escalate.

Error identity: `model_not_found` (503) is reserved for models no
configured provider lists (and no fallback matches). When every provider
that could serve the model is in cooldown, the gateway returns
`providers_unavailable` (503) with a `Retry-After` header instead — the
remedy is waiting, not editing the config.

## 5. Token reduction design

RTK filters (autodetect on first 1 KiB, best score wins, fallback
smart-truncate): git-diff (25 changed lines/hunk cap + head/tail trim),
git-status (dedup), git-log (dedup + 50/15 trim), grep (dedup + 80/40 trim),
find/ls (dedup), tree (dedup + 80/40 trim), read-numbered/search-list
(dedup), dedup-log (repeated-run score — a constant score was a real bug the
bench gate caught), build-output (dedup + 50/25 trim), smart-truncate
(120/60 default). Safety: 500 B–10 MiB window, never-grow, fail-open on
malformed JSON, `X-9Router-Token-Saver`-style per-request bypass is
future work (header passthrough list in `chat.go`).

Cache: exact-match LRU (sha256 of processed body), default 512 entries /
1 h TTL / 8 MiB max entry; `prefix_order` option reorders system messages
first for stable keys and stable upstream cache prefixes. Streamed
responses are never cached.

## 6. Known gaps & assumptions (honest)

0. **End-to-end validation used a mock upstream, not a paid provider.** The
   `opencode-go` credential is session-scoped: it authorizes OpenRouter
   `/models` but OpenRouter chat rejects it with 401 (reproduced with a
   direct request, so the router's failover+cooldown handled it correctly).
   OpenCode's own hosted models (`opencode/*`) only serve through the
   opencode binary, so they cannot be routed. A real provider key
   (`OPENROUTER_API_KEY` etc.) is required for paid-provider e2e; the mock
   path (opencode → routre-cli → mock) validates the full pipeline:
   streaming, auth passthrough, RTK, cache, failover.

1. **Cross-kind streaming translation is not implemented** — returns 501.
   Same-kind streaming works; cross-kind requires the 9router-class
   translator matrix (OpenAI↔Anthropic↔Gemini) as a follow-up.
2. **Non-streaming cross-kind translation is lossy**: tool definitions are
   dropped; `tool_use` blocks flatten to text; `tool_result` loses the
   `tool_call_id` link; images become placeholders. Documented in
   `translate.go`. Fine for cheap-tier fallback, not for tool loops.
3. **Token estimates, not billing tokens** (see §2.1). 90.7% is on the
   defined metric; a real-session measurement (P1: Claude Code/Codex
   through the gateway, before/after) is the next evidence step.
4. **`stream:true` detection** matches two literal spacings; exotic
   whitespace would be misdetected (client then receives a buffered
   response — safe, just not streaming). Mitigation: JSON decode when
   false-positive risk matters (P2).
5. **Re-marshaling normalizes whitespace/key order** when compression
   fires; cache keys are over the normalized body, so client-side cache
   churn is possible. Acceptable; token-level splice is a P2 option.
6. **`prefix_order` is conservative** — it never reorders when the first
   message is already a system message; tool-message-first prompts (common
   in agents) are not reordered behind the system prefix (TODO).
7. **Under-load RAM** (many concurrent SSE streams) unmeasured; bounded
   by 64 MiB request cap, 128 MiB response cap, 8 MiB cache entry cap,
   and systemd MemoryMax=200M.
8. **API keys** come from the environment (`api_key_env`), never from the
   config file; `check` verifies presence. 401/403 token refresh is P2.
9. 9router's 20–40% claims are marketing-grade (its own diagnostics
   walked them back); our numbers are reproducible via `make bench`.

## 7. Validation plan (roadmap)

- **P0 (done)**: scaffold + unit/integration tests + bench gate + RAM
  script. All green. OpenCode e2e (mock upstream): answer delivered,
  exit 0; RTK compressed a 23.4 KB tool_result body to 5.4 KB upstream;
  identical repeat request served from cache; RSS 12 MiB avg under load.
- **P1**: (a) paid-provider e2e with a real key (OpenRouter/Anthropic);
  (b) real-session token deltas (opencode → gateway → provider, before/after
  with RTK on/off); (c) tiktoken/claude-tokenizer integration for
  billing-grade numbers; (d) systemd socket-activation end-to-end check
  (~0 MB idle claim).
- **P2**: 401/403 refresh-with-retry; per-request RTK bypass header;
  periodic provider health pings (proactive failover); `Retry-After`
  honoring; prompt-cache header passthrough verification (cache_control).
- **P3 (optional)**: full dialect translator matrix (streaming-capable);
  TUI status (or keep `/v1/status` JSON); Rust port only if P99 targets
  force it.

## 8. Reproducing

```bash
export PATH=$HOME/go-sdk/go/bin:$PATH   # or system Go ≥ 1.22
make build test bench                   # build + tests + 90% gate
./scripts/measure-ram.sh ./routre-cli ./config.example.json 30
./routre-cli check -config config.example.json
```
