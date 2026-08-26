# Tech Stack — routre-cli

> Domain language for AI-navigability. See `PLAN.md` Deep Dive #2 for dialect seam.

## Core

- **Go 1.22+**, stdlib only, `CGO_ENABLED=0`, single static binary (~10 MiB).
- **Config**: `config.json` + `routre.env` (0600), SIGHUP reload via `config.Store`.
- **Gateway** (`internal/proxy`): localhost `127.0.0.1:20128`, OpenAI-compatible `/v1/*`, loopback-only `/ui`.

## Domain Terms

| Term | Meaning |
| ------ | --------- |
| **Provider** | Upstream LLM endpoint (`kind: openai | anthropic | gemini`,`base_url`,`api_key_env`,`models`,`price_in/out`). |
| **Tier** | Ordered fallback group (`subscription → cheap → free`); `router` tries tiers in order, per-provider cooldown (2s→30m). |
| **Dialect** | API wire format (`openai` chat.completions, `anthropic` messages, `gemini` generateContent, `responses`). Translation lives behind `internal/proxy/dialect` seam. |
| **RTK** | Real-Time Kompression: heuristic `tool_result` compression (12 filters, fail-open, never grows). `internal/rtk`. |
| **Cache** | Exact-match LRU keyed by SHA-256 of processed body (post-RTK, post-ordering), TTL, `internal/cache`. |
| **Ledger** | Per-agent token/cost usage (`internal/usage`, persisted to `~/.routre/usage.json`). |
| **Stream** | SSE event stream; cross-kind translation via state machines (`a2o`, `o2a`, `g2o`, `r2o`) inside dialect. Failover before first byte only. |
| **Cooldown** | Per-provider exponential backoff on `network/timeout/rateLimit/auth/server/credits` (see `router.ErrClass`). `ErrCredits` (402/401 CreditsError) fails over without cooldown. |
| **ForwardUnknown** | When true, unknown models are forwarded verbatim to all providers (failover), enabling zero-config new models. |
| **PromptCache** | Optional Anthropic `cache_control` injection (system prefix + last message) for cache-read billing. |

## Seams & Modules (deep)

- **`Dialect`** (`internal/proxy/dialect`): deep module, interface `Request(from,to,body)` + `Stream(from,to,upstream,w)` + `DetectFormat` + `IsStreaming`, `ErrUnsupported`. Adapters: one per pair (`openai↔anthropic`, `openai↔gemini`, `anthropic↔gemini`, `responses→openai`) + streaming state machines. In-process deps, no ports. Tests at `Dialect` interface (golden SSE/request fixtures), not per-adapter.
- **`Cache`** (`internal/cache`): deep, `Get/Put/Key/OrderPrompt`, LRU+TTL, already deep.
- **`RTK`** (`internal/rtk`): deep, `Apply([]byte)([]byte,bool)`, 12 filters, already deep.
- **`Router`** (`internal/router`): shallow today (score 2) — leaks `Candidate{IsWildcard,IsFree,Upstream}`; candidate for deepening (see PLAN #3).
- **`Pipeline`** (future): shallow god module (`chat.go` 1k LOC) — candidate #1.

## Invariants

- Gateway holds provider keys (`keystore.Store`), never forwards client `Authorization`.
- RTK: fail-open, never grows payload, 500B–10MiB window.
- Cache: non-streaming only, key over processed body, `prefix_order` stable.
- Translation: lossy (tools/images dropped, documented in `dialect` header), tool-call IDs round-trip unchanged, partial JSON forwarded verbatim, `[DONE]` guaranteed.
- Auth: optional `auth.secret_env` + per-process `~/.routre/auth.tok`.

## References

- `docs/SPEC.md` — decisions, metrics, failover table, known gaps.
- `PLAN.md` — deepening candidates, dialect grilling record.
