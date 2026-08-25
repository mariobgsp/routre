# Plan: Deepen Architecture — routre-cli

> Scope: refactor-oriented. No new user-facing features. Each candidate must
> pass the **hard gate**: deep module solves a forcing function, not just "nice
> abstraction." Use `LANGUAGE.md` terms (module/seam/adapter/leverage/locality).

## Context

routre-cli is a ~11k LOC Go binary (stdlib-only, 10 MiB RAM idle). Core flow:

```text
client → proxy/handlers → rtk.Apply → cache.Get/Put → router.Candidates → proxy/chat.route → relay/streamRelay → upstream
```

Current architecture is flat: `internal/proxy/chat.go` (1,069 LOC) owns the
entire request pipeline (RTK, cache, candidate loop, model rewrite, max_tokens
clamp, translation, prompt-cache injection, key injection, retry, usage,
metrics, logging). Dialect translation is split across 5 files
(`translate.go`, `translate_gemini.go`, `stream_translate.go`, `responses.go`,
`responses_stream.go`) with no single seam. `internal/router` owns model
routing + cooldowns but leaks `Candidate{IsWildcard,IsFree,Upstream}` so callers
must branch on provider-specific fields.

No `specs/tech-architecture/tech-stack.md` or `specs/adr/` exists — domain
vocabulary is implicit in `docs/SPEC.md` and package names. Churn heuristic
(90 days): `internal/proxy/chat.go` (16 commits), `internal/config/config.go` (13),
`internal/router/router.go` (8), `README.md` (24), `main.go` (19) are hot spots.
High-churn proxy + router confirm friction.

Goal: surface deepening moves that improve **locality** (one place to change)
and **leverage** (one interface, many callers/tests) and **testability** (interface
is the test surface). Present candidates; grilling picks one.

## Approach (recommended)

Do **analysis + one deepening** per PR. This plan is the analysis artifact
(candidates below). Execution order:

1. Present candidates (this file) → user picks one.
2. Grill the picked candidate (constraints, dependencies category per
   `DEEPENING.md`, seam placement, adapters, tests).
3. Implement the deepened module in one PR, deleting old shallow tests and
   adding tests at the new interface (interface is the test surface).

No code changes in plan mode — markdown only.

## Candidates

### 1. Pipeline Orchestrator — collapse `chat.go` god module

- **Files:** `internal/proxy/chat.go` (1,069 LOC), `internal/proxy/handlers.go`,
  `internal/proxy/build.go`, `internal/proxy/usage_sniff.go`,
  `internal/rtk/rtk.go`, `internal/cache/cache.go`, `internal/router/router.go`
- **Problem:** `route()` does 7 steps inline (read body → RTK → ordering →
  cache lookup → candidate loop with per-attempt retry → cross-kind translate
  → prompt-cache inject → relay/streamRelay → cache write → usage/metrics).
  Understanding one concept (e.g. "when does cache key include ordering?")
  requires bouncing between `chat.go`, `cache.OrderPrompt`, `rtk.Apply`, and
  `buildPayload`. Adding Gemini touched `chat.go` + `translate_gemini.go` +
  `stream_translate.go` + `handlers.go` separately. Tests are integration-only
  (`proxy_test.go` needs a mock upstream + real Handlers); RTK+cache+cooldown
  interactions cannot be unit-tested through a small interface.
  **Deletion test:** deleting `route` would scatter RTK, cache, routing,
  translation, and retry logic across every caller — complexity reappears.
  But `route` itself is pass-through spaghetti, not a deep module earning its keep.
- **Module Depth score:** 1 — Shallow. Interface is `route(w,r,api)` (tiny) but
  callers must know ordering, cache keying, IsWildcard failover, cross-kind
  before-first-byte rule, and prompt-cache semantics. Interface complexity ≈
  implementation complexity.
- **Solution:** Extract a `Pipeline` module whose interface is
  `Process(ctx, Request) (Response, error)` — one entry point hiding RTK,
  ordering, cache, candidate selection, translation, and retry. `Handlers.route`
  becomes `pipeline.Process` + HTTP adapt. Dependencies are In-process (RTK,
  cache) and Local-substitutable (router is in-memory); no port needed beyond
  the pipeline's own interface. Existing `Handlers.tryCandidate/relay` become
  private implementation behind the seam.
- **Benefits:** Locality — cache-key construction, RTK window, and prompt-cache
  injection live in one place. Leverage — one `Process` call backs every
  HTTP handler (`/v1/chat`, `/v1/messages`, `/v1/responses`); tests exercise
  the full policy via the pipeline interface with an in-memory cache/router
  (no HTTP). Old integration tests that poke `Handlers` become redundant.
- **Forcing function:** adding the 4th dialect (Anthropic↔Gemini) without this
  requires touching `chat.go` again; with pipeline, dialect matrix is data,
  not branches.
- **Dependency category:** In-process. Test with real RTK + real LRU + fake router.

### 2. Dialect — unify 5-file translation scatter

- **Files:** `internal/proxy/translate.go` (250), `translate_gemini.go` (297),
  `stream_translate.go` (588), `responses.go` (347), `responses_stream.go` (371),
  `chat.go` cross-kind branches (lines 200–280 in relay/streamRelay)
- **Problem:** No module owns "dialect." Request translation (`translateBody`),
  response translation (`geminiToOpenAI`, `openAIToResponses`), and streaming
  state machines (`a2oState`, `o2aState`, `g2oState`, `r2oState`) are separate
  files with separate error contracts. Adding Gemini required adding `g2oState`
  - `openAIToGemini` + `geminiToOpenAI` + `kind==gemini` branches in `chat.go`
  in 4 places. `kindOf()` string switch is the seam, but callers branch on it
  everywhere instead of dispatching. Byte-level `isStreaming` detection lives
  in `chat.go` but belongs to dialect.
- **Module Depth score:** 2 — Shallow. Each file's interface is 1–2 funcs;
  together they force callers to know which file to call for which pair
  (`fmtOpenAI→fmtGemini` vs `fmtGemini→fmtOpenAI`) and that Responses only maps
  to OpenAI upstreams (rejected in `tryCandidate`). Interface complexity ≈ sum
  of implementations.
- **Solution:** One `Dialect` module: interface
  `TranslateRequest(from,to,body)(body,error)` + `TranslateStream(from,to,upstream,w)` +
  `DetectFormat(path,body) Format`. Behind the seam: the 5 current files become
  private adapters (one per pair). `chat.go` stops branching on `kind`; it
  calls `dialect.Translate*`. Rejecting Anthropic↔Gemini becomes one registry
  miss, not an `if kind==…` branch.
- **Benefits:** Locality — all lossy-mapping docs, SSE state, and finish-guard
  live together. Leverage — `chat.go` drops ~150 LOC of branches; new dialect
  is one adapter, not 4-file edit. Tests at `Dialect` interface use golden
  SSE fixtures (already exist in `stream_translate_test.go`) without HTTP.
- **Forcing function:** Anthropic↔Gemini is explicitly a known gap (#2 in
  README). Implementing it without this deepening doubles the scatter.
- **Dependency category:** In-process (pure JSON/SSE transforms).

### 3. Model Routing — unify strip/prefix/free-variant/wildcard logic

- **Files:** `internal/router/router.go` (640 LOC — `Candidates`, `CandidatesWithFallbacks`,
  `stripProviderPrefix`, `listedName`, `freeVariantOf`, `providerServes`,
  `MinCooldownForModel`), `internal/proxy/chat.go` (`buildPayload`, `rewriteModel`,
  `clampMaxTokens`, wildcard failover branch in `tryCandidate`)
- **Problem:** Model name handling is split: router knows how to *find* providers
  for a model, but `chat.go` knows how to *rewrite* the model field and *clamp*
  max_tokens and *decide* wildcard failover. `Candidate{Upstream,IsFree,IsWildcard}`
  leaks provider internals so `chat.go` must branch on `IsWildcard` to decide
  "fail over vs surface 400." `freeVariantOf` heuristics (suffix `:free` vs `-free`,
  `LastIndex("/")` tail logic) are tested via router but consumed via proxy,
  so a change to free-variant naming requires fixing both packages.
- **Module Depth score:** 2 — Shallow. Router's interface is `Candidates(model)`
  but callers must also call `buildPayload` + `rewriteModel` + check `IsWildcard`
  correctly. Implementation is richer than its interface hides.
- **Solution:** Deepen `router` so its interface returns a ready-to-send payload
  view: `Candidates(model)` returns `[]Candidate{UpstreamPayload() []byte, ShouldFailoverOnClientError() bool}` —
  model rewrite + max_tokens clamp move behind the seam. `chat.go` stops doing
  JSON decode/marshal for model names; it passes the raw `processed` body and
  gets the per-candidate bytes back. Optionally fold `forwardUnknown` wildcard
  routing into the same decision so `chat.go` wildcard branch disappears.
- **Benefits:** Locality — all model-name magic in one module. Leverage —
  `chat.go` drops `buildPayload`/`rewriteModel` (~60 LOC) and the wildcard
  branch; router tests alone guard the contract. Deletion test passes:
  deleting router would scatter cooldown + model routing across every caller.
- **Forcing function:** adding budget-aware routing / per-provider model aliasing
  (mentioned in `Config.Budgets` / `SPEC.md` roadmap) needs this seam.
- **Dependency category:** In-process.

### 4. Reconfigurable Subsystem — coalesce SIGHUP reload wiring

- **Files:** `internal/config/config.go` (`Store.Load/Reload/Save`, `SetOnLoad`),
  `internal/config/envfile.go`, `internal/keystore/keystore.go`,
  `internal/proxy/handlers.go` (`NewHandlers` + `OnLoad` callback — 25 lines
  rebuilding router/cache/rtk/logger), `main.go` SIGHUP path
- **Problem:** Reload touches 5 subsystems via a closure that captures
  `rtr`, `cch`, `tk`, `reqlog`, `logger` and resets each. Each subsystem has
  its own `Update/Reset` method but no shared contract. Adding a new reloadable
  (e.g. budgets, prompt-cache flag) requires editing the closure. Testing
  reload requires a real `Store` + temp file + `Load()`; no interface to
  drive reload with in-memory config.
- **Module Depth score:** 2 — Shallow. Interface is `Store.Load()` but the
  real interface includes "call order of Reset then SetForwardUnknown then
  Update then SetPath" — ordering that callers must know.
- **Solution:** Introduce a `Reconfigurable` seam: any subsystem implements
  `Reconfigure(Config)` (1 method). `Store` keeps a registry and calls each
  on reload in a fixed order. `Handlers.OnLoad` disappears; each subsystem
  registers itself. SIGHUP path becomes `store.Reload()` only.
- **Benefits:** Locality — reload ordering in one place (`config.Store`).
  Leverage — new reloadable is one `Register` call, no closure edit. Tests
  drive reload via an in-memory `Config` without temp files.
- **Forcing function:** budgets + any per-tier reloadable config are on the
  roadmap; otherwise defer (hypothetical seam — currently only 1 adapter
  per subsystem).
- **Dependency category:** In-process / Local-substitutable (file-backed store
  could be faked).
- **Note:** One adapter per subsystem today — hypothetical seam. Defer unless
  a 2nd reloadable (budgets) lands. Marked lowest priority.

### 5. Usage Ledger — unify non-streaming/streaming token capture

- **Files:** `internal/proxy/usage_sniff.go` (sniffer + regex),
  `internal/proxy/handlers.go` `usageFromBody` / `pricesOf` / `cacheEntry`,
  `internal/usage/usage.go` (`Store.RecordFull`, `Prices.CostOf`),
  `internal/proxy/chat.go` streaming success path vs non-streaming success path
  (two separate `RecordFull`/`Record` call sites)
- **Problem:** Token extraction is duplicated: `usageFromBody` parses non-streaming
  JSON for `prompt_tokens`/`completion_tokens`/`cached_tokens`; `usageSniffer`
  regex-scans SSE lines for the same fields; Gemini path wraps via
  `geminiToOpenAI` before `usageFromBody` so Gemini tokens are read through an
  OpenAI shape. Two `Record*` call sites in `chat.go` must be kept in sync.
  Adding a provider that reports usage differently (e.g. Gemini's
  `cachedContentTokenCount`) required adding a 3rd regex alternative.
- **Module Depth score:** 2 — Shallow. Interface is `usage.Store.RecordFull`
  but callers must know which extraction path to run (streaming vs not, Gemini
  vs OpenAI) and which fields are discounted (`cacheRead`).
- **Solution:** One `UsageExtractor` behind the usage seam: interface
  `Extract(body []byte, streaming bool, kind string) (prompt, completion, cacheRead, cost)`.
  Non-streaming JSON parse, SSE regex scan, and Gemini unwrapping become
  private adapters. `chat.go` does one `extractor.Extract()` call per request;
  `usage.Store` stays as the persistence seam.
- **Benefits:** Locality — all token-field knowledge in one module; `reCached`
  regex extended once. Leverage — `chat.go` drops dual paths to one call.
  Tests at extractor interface cover Gemini + OpenAI + Anthropic without HTTP.
- **Forcing function:** Gemini cached tokens already forced a 3rd alternative;
  any billing-grade tiktoken integration will need this seam.
- **Dependency category:** In-process. Two adapters justified: non-streaming
  JSON vs streaming SSE (mirrors the existing split).

### 6. CLI Surface — unify config/key/gateway plumbing across `cmd*` [lite]

- **Files:** `main.go` (flag parsing + `run` switch), `list.go` (config load +
  `fetchJSON` for /status + /usage), `setup.go` (interactive wizard +
  `config.Save`), `start.go`/`stop.go` (daemon + service detection),
  `logs.go`, `update.go` (release discovery)
- **Problem:** Each subcommand reimplements `config.NewStore(path).Load()` +
  `lookupKey` / `fetchJSON` / error mapping separately. `list.go` 365 LOC mixes
  table rendering, live gateway probing, and offline fallback. `setup.go` 296
  LOC mixes prompting, validation, and file writes. No shared `GatewayClient`
  or `ConfigContext`; testing any subcommand requires real files + real HTTP.
- **Module Depth score:** 2 — Shallow. Each `cmd*` file's interface is
  `cmdX(cfgPath, ...)` but implementation is mostly glue around config + HTTP.
- **Solution:** Extract a `Gateway` client module (interface:
  `Status()`, `Usage()`, `Models()` — small, hides `fetchJSON` + auth token
  handling) and a `ConfigContext` (loads `Store` + `Keystore` once). Subcommands
  take those as deps; CLI wiring stays in `main.go`. Not a priority — defer
  unless CLI gains more commands.
- **Benefits:** Locality — probe + auth logic in one place. Leverage —
  subcommands test with a fake `Gateway`. Deletion test is weak today (few
  shared callers), so defer.
- **Forcing function:** `routre update` + `routre check` + future `routre config`
  commands would make this earn its keep. Today it is hypothetical.

## Reuse (verified in-repo)

- `internal/rtk/rtk.go` `Apply([]byte) ([]byte,bool)` — already deep (score 5);
  keep as is, reuse via pipeline candidate #1.
- `internal/cache/cache.go` `Get/Put/Key/OrderPrompt` — deep (score 4–5);
  keep, reuse via pipeline.
- `internal/router/router.go` — `CooldownPolicy`, `ClassifyStatusBody`,
  `ReportFailureWithBackoff` (Retry-After floor) — reuse; deepen per #3.
- `internal/keystore.Store` `Get/Set/Refresh` — correctly separates key
  rotation from `os.Getenv`; reuse as pipeline dependency, don't duplicate
  key logic in candidates.
- `internal/config.Store` `Load/Reload/Save` + atomic temp+rename write — reuse;
  #4 builds on it.
- `internal/proxy/stream_translate_test.go` golden SSE fixtures — reuse for #2 tests.
- `internal/proxy/finish_guard_test.go` + `payload_test.go` — reuse for pipeline
  finish-guarantee coverage.

## Files to Modify (when a candidate is picked)

| Candidate | Primary files | Secondary (tests/docs) |
| ----------- | --------------- | ------------------------ |
| 1 Pipeline | `internal/proxy/chat.go`, `internal/proxy/handlers.go`, new `internal/proxy/pipeline.go` | `internal/proxy/proxy_test.go` (move to pipeline tests) |
| 2 Dialect | `internal/proxy/translate.go`, `translate_gemini.go`, `stream_translate.go`, `responses.go`, new `internal/proxy/dialect/` | `stream_translate_test.go`, `gemini_test.go`, `responses_test.go` |
| 3 Model Routing | `internal/router/router.go`, `internal/proxy/chat.go` (`buildPayload` removal) | `internal/router/router_test.go`, `internal/proxy/payload_test.go` |
| 4 Reconfigurable | `internal/config/config.go`, `internal/proxy/handlers.go`, `internal/cache/cache.go`, `internal/rtk/rtk.go`, `internal/router/router.go` | `internal/config/config_test.go` |
| 5 Usage Extractor | `internal/proxy/usage_sniff.go`, `internal/proxy/handlers.go`, `internal/usage/usage.go`, new `internal/proxy/usage_extractor.go` | `internal/proxy/usage_sniff_test.go` |
| 6 CLI | `main.go`, `list.go`, `setup.go`, new `internal/cli/gateway.go` | `list_test.go`, `setup_test.go` |

## Steps (analysis concluded; execution is per-candidate)

- [x] Explore codebase, churn, `chat.go`/`router`/`translate` friction, `SPEC.md` gaps
- [x] Score module depth (1–5, Ousterhout) per candidate; apply deletion test + seam discipline
- [x] Write this plan with 6 ranked candidates, each with forcing function
- [x] **User picks** 1–2 candidates to grill — **picked: #2 Dialect**
- [x] Grill #2: design tree below — constraints, deps, seam, adapters, interface sketches
- [x] User confirms preferred interface sketch — **confirmed hybrid A+B unified, internal/proxy/dialect, warnings deferred**
- [ ] For picked candidate: create `specs/tech-architecture/tech-stack.md` lazily if new domain term is named ("Dialect" as domain term for API format + translation); record rejected alternatives as ADRs on load-bearing rejections
- [ ] Implement PR #1 — `internal/proxy/dialect` package: move 5 files behind seam, add registry + DetectFormat, wire chat.go to Dialect interface
- [ ] Tests: delete old `openAItoAnthropic`-level unit tests after golden tests at Dialect interface exist; `go test ./...` + manual cross-kind streaming check
- [ ] Next candidates: #1 Pipeline (after dialect), then #3 Model Routing — defer #4/#6

## Verification

- `go test ./...` (existing proxy/router/config/unit suites stay green)
- `go test -run Test.*Translate` / `go test -run TestCandidates` for deepened interface
- `make bench` not required (no RTK change) but run to confirm no payload regression
- Manual: `routre serve` + `curl /v1/models` + `curl /v1/status` + one cross-kind
  streaming request (OpenAI client → Anthropic upstream) still translates
- Import-boundary hygiene: if a new `internal/proxy/dialect` or `internal/proxy/pipeline`
  package is split out, update `specs/import-boundaries.json` + run
  `bash scripts/check-import-boundaries.sh` (see skill §5). Until then, no new
  `source` edges are added — convention docs alone do not authorize imports.

## Risks

- Over-deepening: candidates 4 and 6 are currently hypothetical seams (one
  adapter). Marked lowest priority; defer until budgets / new CLI commands land.
- Translation regression: dialect deepening must preserve lossy-mapping contracts
  documented in `translate.go` header; golden fixtures mitigate.
- Cache-key stability: pipeline must not re-marshal when unchanged (existing
  `Apply` + `OrderPrompt` contract — return original bytes when unchanged).

## Prioritization (recommended order)

1. **#2 Dialect** — smallest, highest leverage, unblocks Anthropic↔Gemini gap.
2. **#1 Pipeline** — biggest win but larger diff; do after dialect so pipeline
   can take a `Dialect` dep.
3. **#3 Model Routing** — natural follow to pipeline (pipeline already needs router).
4. **#5 Usage Extractor** — piggybacks on pipeline.
5. Defer #4, #6 until forcing function arrives.

---

## Deep Dive: #2 Dialect — Grilling

> Picked 2026-08-25. This section is the grilling record for candidate #2.
> Resolve open decisions before implementation.

### Problem restated with friction log

No module owns `dialect`. Adding Gemini in 2026-08 touched 5 files + 4
branch sites in `chat.go` (`tryCandidate` lines ~190–350 for request rewrite,
`relay`/`streamRelay` for streaming dispatch, `kindOf` switch, finish-guard
exclusion). Reader must track `apiFormat` enum (`fmtOpenAI/FmtAnthropic/FmtGemini/FmtResponses`)
across files to answer "which pairs are supported?" — no registry answers it.
Cross-kind request vs streaming translation have separate error contracts
(request error = retryable before first byte; streaming translate error =
half-written SSE → stream abort). `isStreaming` body sniff lives in `chat.go`
but belongs to dialect (format detection). Responses→chat mapping drops fields
(`store`, `previous_response_id`) silently — documented only in a file header
comment, not in an interface invariant.

### Constraints any new interface must satisfy

1. **Lossy mappings are invariants, not bugs** — `translate.go` header lists
   them: tools dropped OpenAI→Anthropic, tool_use flattened, images replaced.
   Any interface must surface them as error modes / typed losses, not hide them.
2. **Never buffer whole response** — streaming translation is frame-by-frame
   via `bufio.Reader` + `sseEvent.read`; `flush` after each frame keeps tail
   latency flat. Interface must not force `[]byte` whole-body for streams.
3. **Failover contract preserved** — translate error *before* first byte is
   retryable (fail over to next provider); after first byte is `StreamAborted`.
   Interface must let caller know whether any byte was emitted (`emitted` bool
   in `streamTranslator`).
4. **Tool-call id round-trips unchanged** — `tool_use_id` ↔ `tool_call_id`
   never re-minted; partial JSON (`input_json_delta` / `arguments` chunks)
   forwarded verbatim. Interface must not accumulate them.
5. **Responses envelope is client-only** — no upstream serves `/v1/responses`;
   request is `responsesToOpenAI` + `openAIToResponses` re-wrap. `Dialect`
   must reject Responses→Anthropic/Gemini (today `tryCandidate` does it; seam
   should own it).
6. **Gemini is upstream-only** — `openAIToGemini` / `geminiToOpenAI` /
   `g2oState` only; Anthropic↔Gemini must remain rejected (not mis-answered).
7. **Format detection is part of the seam** — `detectFormat(path,body)` +
   `isStreaming(body)` are dialect concerns; callers should not sniff bytes
   themselves. `kindOf(string)` string switch must disappear behind registry.
8. **stdlib-only, no new deps** — project is stdlib-only by design (see
   `go.mod` — only stdlib). New module stays stdlib.

### Dependencies

Category **1 — In-process** (pure computation, no I/O). Always deepenable.
Adapters are pure functions over `[]byte` / `io.Reader`→`io.Writer`; tests use
in-memory bytes, no ports needed. No Local-substitutable or Remote adapters.

### Where the seam lives

**Seam = `internal/proxy/dialect` package boundary.** Callers (`chat.go`
`route`/`relay`/`streamRelay`) depend only on the exported interface types;
all 5 current files plus `detectFormat`/`isStreaming`/`kindOf` move behind it
as unexported adapters. No other package imports dialect's internals.

```text
chat.go (caller)  ──seam──▶  dialect.Package
                                ├─ openai↔anthropic adapter (existing translate.go)
                                ├─ openai↔gemini adapter (translate_gemini.go)
                                ├─ openai↔responses adapter (responses.go)
                                ├─ stream adapters: a2o/o2a/g2o/r2o (stream_translate.go + responses_stream.go)
                                └─ format detector (detectFormat + isStreaming)
```

One adapter = hypothetical seam. Here 4+ pairs exist → real seam. Keeping
`internal/proxy/dialect` inside `proxy` (not `internal/dialect` top-level)
keeps import boundaries trivial; promote later if pipeline (candidate #1) needs
it.

### What the implementation hides

- Per-pair JSON field maps (which OpenAI fields map to Anthropic `system`,
  `messages`, `max_tokens` clamping, Gemini `generationConfig`, etc.)
- SSE state machines (`a2oState`, `o2aState`, `g2oState`, `r2oState`) + `sseEvent`
  framing, `finishGuard` synthesis, `[DONE]` handling
- Lossy-omission decisions (tool definitions, image_url placeholders)
- `provider/model` prefix stripping that belongs to routing vs dialect (dialect
  should *not* do prefix stripping — it receives bare model already; callers
  strip before calling dialect)

### Interface sketches — three radically different designs

#### Sketch A — Minimal (1–3 entry points, max leverage per entry)

```go
// Package dialect — single Dialect type, pair registry inside.
package dialect

type Format int // OpenAI, Anthropic, Gemini, Responses

type Dialect struct{} // stateless, pair registry is a map[pair]adapter

// Request translates a full request body between formats. Returns error if
// the pair is unsupported (e.g. Anthropic→Gemini, Responses→Anthropic).
func (d *Dialect) Request(from, to Format, body []byte) ([]byte, error)

// Stream translates an upstream SSE stream into the client's dialect,
// writing SSE frames to w and flushing after each. Never buffers whole body.
// Returns error before first byte = retryable; after first byte caller
// treats as StreamAborted.
func (d *Dialect) Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) error

func DetectFormat(path string, body []byte) Format
func IsStreaming(body []byte) bool
```

Usage:

```go
if d.IsSupported(clientFmt, providerKind) { // or Request returns unsupported error
    payload, _ = d.Request(clientFmt, providerKind, processed)
    // streaming:
    err := d.Stream(clientFmt, providerKind, resp.Body, w, flusher.Flush)
}
```

Hides: all adapters behind two registry lookups. Trade-off: minimal surface,
but caller cannot introspect *why* a pair is lossy; lossy behavior is just docs.
Tests at `Request`/`Stream` — golden files per pair.

#### Sketch B — Flexible (pair-aware, extension-friendly)

```go
package dialect

type Pair struct{ From, To Format }
type Result struct {
    Body     []byte
    Warnings []string // e.g. "tools dropped", "image_url replaced"
}
type StreamResult struct {
    Emitted bool // whether any byte reached w before error
    Err     error
}

type Registry struct{} // holds map[Pair]RequestAdapter + map[Pair]StreamAdapter

func NewRegistry() *Registry // registers built-ins; callers can Register(Pair, adapt)
func (r *Registry) Request(from, to Format, body []byte) (Result, error)
func (r *Registry) Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) StreamResult
func (r *Registry) Supported() []Pair
func (r *Registry) Lossy(pair Pair) []string // invariants: what the pair drops
```

Usage shows loss introspection; adding Anthropic↔Gemini is `r.Register(pair, adapter)`
without editing registry. Trade-off: larger interface, more leverage for
tooling (e.g. `/ui` could show "this route is lossy"), but callers that just
want bytes now handle `Result.Warnings`.

#### Sketch C — Common-case trivial (optimize for OpenAI client → any upstream)

Observation: 95% of traffic is OpenAI-dialect clients (openCode, Codex, Cursor)
via `OPENAI_BASE_URL`; Anthropic client is niche (Claude Code via `ANTHROPIC_BASE_URL`).
Optimize default to one call, make rare pairs explicit.

```go
package dialect

// FromOpenAI is the fast path — what most callers need.
func FromOpenAI(to Format, body []byte) ([]byte, error)
func StreamFromOpenAI(to Format, upstream io.Reader, w io.Writer, flush func()) error

// Generic escape hatch for the rare reverse direction.
func Request(from, to Format, body []byte) ([]byte, error)
func Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) error

// Non-streaming response re-wrap (Gemini→OpenAI, chat→Responses) lives here too,
// not in chat.go.
func ResponseToClient(clientFmt Format, upstreamKind Format, upstreamBody []byte, model string) ([]byte, error)
```

Usage (common case):

```go
payload, err := dialect.FromOpenAI(kindOf(provider.Kind), processed)
// rare:
// payload, err := dialect.Request(fmtAnthropic, fmtOpenAI, body)
```

Hides: that OpenAI↔Responses re-wrap is just another dialect pair via
`ResponseToClient`. Trade-off: trivial default call (`FromOpenAI`), but two
ways to do the same thing (FromOpenAI vs Request) — slight API redundancy.

### Recommendation (opinionated)

**Ship Sketch A as the PR, steal one idea from B.**

A is the deepest: 2 methods + 2 funcs. Callers drop all `if kind==` branches
to one `Request`/`Stream` call; unsupported pair is a plain error (today's
`tryCandidate` rejection becomes `dialect.ErrUnsupported`). Leverage is max —
every handler + future pipeline uses the same two methods. Locality is max —
all 5 files + detection behind one package.

Steal from B: expose `Supported() []Pair` (or `ErrUnsupported` sentinel with
`Pair` in it) so `chat.go` can log "unsupported dialect pair X→Y" without
parsing an error string, and so tests can enumerate pairs without hardcoding
them. Do **not** steal `Warnings` — YAGNI until `/ui` wants it; today warnings
are docs in the package header (same as `translate.go` does now). `ResponseToClient`
from C is folded into `Request`/`Stream` — responses are just another `Format`,
not a special method; keeping them separate is a premature seam (only one
adapter: chat→responses).

Resulting diff: `chat.go` −~130 LOC branches + `kindOf`/`isStreaming` moves;
new `internal/proxy/dialect` ~1.8k LOC (moved, not rewritten) + ~80 lines of
registry + thin `DetectFormat`.

Hybrid interface to implement:

```go
package dialect

type Format int // OpenAI, Anthropic, Gemini, Responses
var ErrUnsupported = errors.New("unsupported dialect pair")

type Dialect struct{}
func (d *Dialect) Request(from, to Format, body []byte) ([]byte, error)
func (d *Dialect) Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) error
func (d *Dialect) Supported() []Pair // optional, for tests/logging
func DetectFormat(path string, body []byte) Format
func IsStreaming(body []byte) bool
```

### Tests at the new interface

Old shallow tests on `openAItoAnthropic`/`anthropicToOpenAI` directly become
waste — delete them after `Dialect.Request` golden tests exist. New tests at
`Dialect` interface:

- `TestRequest/<pair>` — table over `Supported()` pairs with request golden
  JSON (existing `translate_test.go` + `gemini_test.go` + `responses_test.go`
  inputs become the goldens; add one for unsupported pair → `ErrUnsupported`).
- `TestStream/<pair>` — SSE golden fixtures (reuse `stream_translate_test.go`
  fixtures: feed `upstream` bytes via `strings.Reader`, capture `w`, assert
  frames + `[DONE]` + unchanged `tool_use_id`).
- `TestStream_FailoverContract` — translate error before first byte is
  retryable (no `Emitted`), after first byte is abort. Assert via a broken
  `upstream` reader that fails mid-stream.
- `TestDetectFormat` + `TestIsStreaming` — path vs body sniff (edge: content
  containing `"stream":true` literal must not trigger).
- Negative: Responses→Anthropic and Anthropic→Gemini must return
  `ErrUnsupported`, never a body.

Assertion style: observable bytes through the interface, not internal state
(`a2oState.curToolIdx` etc. is private). Tests survive internal refactors
(state-machine rewrite) as long as the pair's bytes stay correct.

### Decisions — confirmed 2026-08-25

1. **Package path — `internal/proxy/dialect`** (nested). Keeps proxy boundary
   small; no top-level import-boundaries.json entry needed now. Promote to
   `internal/dialect` only if Pipeline (candidate #1) needs it.
2. **Responses unified** — `Responses` is a `Format` variant inside dialect.
   Single registry for all pairs (`OpenAI↔Anthropic`, `OpenAI↔Gemini`,
   `OpenAI↔Responses`). Chat re-wrap is just another adapter, not a separate package.
3. **Warnings deferred** — losses stay documented in package header (as today).
   No `Result.Warnings` in interface. Add only if `/ui` or `check --explain` needs it.

Hybrid interface locked:

```go
package dialect

type Format int // FormatOpenAI, FormatAnthropic, FormatGemini, FormatResponses
var ErrUnsupported = errors.New("unsupported dialect pair")
type Pair struct{ From, To Format }

type Dialect struct{}
func (d *Dialect) Request(from, to Format, body []byte) ([]byte, error)
func (d *Dialect) Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) error
func (d *Dialect) Supported() []Pair
func DetectFormat(path string, body []byte) Format
func IsStreaming(body []byte) bool
```

---

*Grilled: #2 Dialect — confirmed hybrid A+B unified, internal/proxy/dialect, warnings deferred. Ready for implementation.*
