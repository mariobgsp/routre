# Plan: Deepening Round 2 — post #51/#52

> Scope: refactor-only. One candidate per PR, in priority order. Each must
> pass the **hard gate**: deep module solves a forcing function, not just
> "nice abstraction." Use `LANGUAGE.md` terms (module/seam/adapter/leverage/
> locality). Prior plan `specs/PLAN-dialect-pipeline.md` is **complete** —
> all 6 candidates landed in `345d346` (dialect, pipeline, router, extractor,
> gateway, reconfigurable) plus `8267cbc` (Anthropic↔Gemini pair).

## Context

Round 1 collapsed 5-file translation scatter, 1k-LOC chat god module, and 3
other shallow spots into deep modules behind small interfaces. Result:
`internal/proxy/dialect/` (1.8k LOC behind `Dialect.Request`/`Stream`/
`Supported`), `internal/proxy/pipeline.go` (`Pipeline.Process`/`Stream`),
`internal/router/router.go` (`Candidate.Payload`/`ShouldFailoverOnClientError`),
`internal/proxy/extractor.go`, `internal/proxy/gateway.go`,
`internal/config.Store.Register` for SIGHUP. `chat.go` dropped 945→661 LOC.

Since then: working tree carries the next feature batch (uncommitted):

- `internal/probe` + `routre doctor` + `health_check` config (320 LOC, 1 file)
- 503 enrichment (per-provider attempts[] in body) for non-streaming and
  streaming paths — `renderAllFailed503` + `attemptOutcome` helpers added to
  pipeline, ~120 LOC new tests
- `logs.go` `-errors` + `-provider` filters (filter closure + parseEntry +
  formatLogLine plumbing)
- `reqlog.Log` writes to stderr when `path==""` instead of silently dropping
- Router cooldown cap lowered 30m → 5m
- `Handlers.OnLoad` slimmed (cache/rtk/reqlog moved to `Register`; router
  stays in closure because of tier translation)

This round surfaces **5 new deepening candidates** that emerged *from*
round 1 and the new feature work. Same scoring (1–5, Ousterhout) and
forcing-function discipline.

## Approach

1. Pick the highest-priority candidate (`#1` is the most leveraged).
2. Grill — constraints, dependency category, seam placement, adapters.
3. Implement one PR per candidate. Delete waste tests after new interface
   tests exist. Update `specs/tech-architecture/tech-stack.md` if a new
   domain term is named.
4. After each PR, re-survey the codebase for the next layer.

No code in plan mode — markdown only. Plan submitted for review at the end.

## Candidates

### 1. Pipeline.Stream + processInternal — extract `CandidateRunner` to kill the twin-loop duplication

- **Files:** `internal/proxy/pipeline.go` (`Stream` lines 131–218, `processInternal`
  lines ~340–420), `internal/proxy/chat.go` (`relay`/`relayStream`/`streamRelay`)
- **Problem:** After round 1 the pipeline still owns **two near-identical
  halves**: `Stream(ctx, req, w)` for SSE and `processInternal(ctx, req)`
  for non-streaming. Both do: candidate iteration, per-candidate retry on
  transient classes, `tryLog []attemptOutcome` accumulation, last-error
  tracking, and 503 rendering via the *same* `renderAllFailed503(model, attempts)`
  helper. Two duplications, two `for _, cand := range cands` loops, two
  copies of the "if class==ErrAuth try refreshCredentials then retry"
  branch. Adding a 3rd request shape (e.g. embeddings) means copy-paste.
  `streamCandidate` is private to pipeline and *also* duplicates the
  auth-refresh-retry path that lives in processInternal's per-candidate
  call site. The two halves share no test surface — testing streaming 503
  enrichment doesn't exercise the non-streaming 503 path and vice versa.
  **Deletion test:** deleting either half would force the other to absorb
  the missing shape, scattering transport-specific concerns (SSE writer
  flush, content-type header, cross-kind dialect.Stream) across the
  shared loop. They earn their keep separately; the *seam between them*
  is what's missing.
- **Module Depth score:** 2 — Shallow. Pipeline exposes `Process` + `Stream`
  but callers (chat.go `route`) must know which one to call for which body
  shape; the per-candidate retry + auth-refresh + 503-render policy is
  repeated inline rather than shared.
- **Solution:** Extract a private `candidateRunner` (or a public
  `internal/proxy/pipeline/runner` package if tests need to drive it
  without HTTP). Interface: `Run(ctx, cands []router.Candidate, eval
  func(cand) Outcome) Outcome`, where `Outcome` carries the per-candidate
  lastErr/lastClass/tryLog and the runner handles the transient-retry
  loop + ErrAuth refresh + cooldown reporting. `Pipeline.Stream` becomes
  `runner.Run(..., eval=streamEval)` and `processInternal` becomes
  `runner.Run(..., eval=blockingEval)`. The two `eval` funcs own
  transport-specific writes (SSE flush vs buffered body).
- **Benefits:**
  - **Locality** — retry + auth-refresh + cooldown-reporting policy in one
    place; new request shapes (embeddings, audio) are one new `eval` func.
  - **Leverage** — tests at `candidateRunner.Run` cover both streaming and
    non-streaming retry paths with one fake `eval`. Currently the auth-
    refresh branch in `streamCandidate` is integration-tested only.
  - **Deletion test passes** — runner hides 3 separate policies (retry,
    refresh, report) that are today repeated in both halves. Removing it
    would scatter those across 2+ call sites.
- **Forcing function:** embeddings (queued in `docs/SPEC.md` roadmap) is
  the third request shape; without this split it would be the 3rd
  copy-paste of the candidate loop.
- **Dependency category:** In-process. Test with a fake `eval` and real
  router.

### 2. Failure Renderer — extract 503-shaping into a `failures` seam

- **Files:** `internal/proxy/pipeline.go` (`renderAllFailed503`, `attemptOutcome`,
  4 inline `w.Header().Set` + `w.WriteHeader(http.StatusServiceUnavailable)`
  - `w.Write(...)` blocks in `Stream` and `processInternal`),
  `internal/probe/probe.go` (will want the same per-provider reason format
  for `routre doctor` output)
- **Problem:** 503-with-per-provider-breakdown is rendered **twice** in
  pipeline (stream + non-stream) plus a near-twin for `providers_unavailable`
  and `model_not_found`. Each rendering picks `Content-Type`,
  `Retry-After`, and the JSON body shape. `attemptOutcome` is private to
  pipeline but the same data shape is what `routre doctor` needs to print
  to a terminal. Today the doctor output is hand-rolled log lines; the
  JSON wire format and the human format are not the same module, and a
  future `-json` flag for doctor would re-derive the wire shape.
- **Module Depth score:** 2 — Shallow. Each 503 rendering is a 10–15-line
  inline block; readers must verify the four renderings agree (same field
  names, same omission rules, same cooldown rounding).
- **Solution:** Extract a small `failures` package (or
  `internal/proxy/pipeline/failures`):

  ```go
  package failures

  type Outcome struct {
      Provider, Kind, Class, Err string
      Cooldown                   time.Duration
  }

  // Render writes the wire-format 503 body to w with the given model and
  // status. kind picks all_providers_failed | providers_unavailable | model_not_found.
  func Render(w http.ResponseWriter, kind string, model string, attempts []Outcome, retryAfter time.Duration)
  ```

  Pipeline calls `failures.Render(w, "all_providers_failed", model, outcomes, 5*time.Second)`
  once. Probe / `routre doctor` can use the same `Outcome` type to emit
  human or JSON output without re-defining fields.
- **Benefits:**
  - **Locality** — one place to add a new 503 class, one place to fix
    `cooldown_seconds` rounding, one place to surface a wire change.
  - **Leverage** — pipeline and doctor share the type; future `-json`
    doctor output reuses `failures.Outcome` and a tiny JSON encoder.
  - **Test surface** — `TestRender` covers all 4 kinds with golden JSON
    - header assertions; existing `proxy_test.go` 503 tests become thin
    assertions on body fields.
- **Forcing function:** `routre doctor` already prints the same per-provider
  info as the 503 body, in a different format. Without a shared module the
  format drift starts here.
- **Dependency category:** In-process. No I/O — pure formatting over
  `[]Outcome` + `http.ResponseWriter`.

### 3. Probe — kill the global config + collapse the two config sources

- **Files:** `internal/probe/probe.go` (`Runner` takes `store *config.Store`
  via `NewWithStore`, but also reads from a package-level `cfg`/`SetConfig`/
  `currentConfig()` global; `tick` calls `r.currentConfig()` which falls
  through to `currentConfig()` when `store==nil`), `main.go` (calls
  `NewWithStore(..., st)` so the global is **never** used by the daemon)
- **Problem:** The probe has **two parallel config sources** for the same
  purpose: a `Runner.store` (constructor-injected) and a package-level
  `cfg *config.Config` (set by `SetConfig`). `tick` checks store first
  then falls through to the global. The global is dead code in the
  current daemon (main.go uses `NewWithStore`) but is still public API
  and still tested via `currentConfig()`. Worse: `SetConfig` and the
  `cfgMu` mutex exist alongside the store, so a reader cannot tell which
  one the daemon actually uses without reading `main.go`. The `Runner`
  type also carries a `store` field that `Probe` doesn't, but
  `Runner.ProbeOne` just delegates to `Probe` — the "Runner" vs
  "Probe" split is two types for one thing.
- **Module Depth score:** 2 — Shallow. `Probe.Do` is clean (1 entry
  point, deep). `Runner` is 4 fields with mixed concerns (the loop +
  the store + the stop channel + the probe).
- **Solution:** Two cleanups:
  - **Delete the global.** `cfg`/`SetConfig`/`currentConfig` are
    unused by the daemon; tests can inject a `*config.Config` via
    `Runner.Config` field. If a 2nd caller ever needs the global,
    add it back then.
  - **Merge `Runner` into `Probe`.** Runner is `Probe + ticker +
    config source + stop channel`. Either:
    - (a) Make `Probe` a pure HTTP-call function, move the loop to
      `internal/probe/loop.go` with explicit `Start(interval, source
      func() []config.Provider)`. Two types, one job each.
    - (b) Keep `Probe` (one-shot) + add a `Loop(probe, interval,
      source, stop)` free function. No `Runner` struct.
  - (a) is the deeper choice: it makes the **config source** a
    first-class dep (instead of a hidden `Runner.store` field), so
    tests can drive the loop with a fake source. The current
    `Runner.store`/`SetConfig` double-sourcing is exactly the
    "hidden field" pattern that obscures a seam.
- **Benefits:**
  - **Locality** — one type per job (`Probe` = one HTTP call;
    `Loop` = one periodic loop). Today `Runner` is 4-field struct
    doing both.
  - **Leverage** — `Loop` tests can drive any source (config-store
    today, future SIGHUP-aware source, fake for tests) without
    monkey-patching the global.
  - **Deletion test** — `Runner` deleted, replaced by `Loop(probe,
    interval, source, stop)`. The hidden `Runner.store` field
    becomes an explicit `source func() []config.Provider` parameter.
- **Forcing function:** SIGHUP-aware source reload is on the roadmap
  (per candidate #4 in prior plan — budgets/forwardUnknown). The
  current `NewWithStore` poll-on-tick would re-derive that.
- **Dependency category:** In-process. Test with a fake source + a
  `httptest.Server` upstream.
- **Note:** Seams test as two adapters: `Loop` is a function so the
  *real* adapter is `func()` returning the live config, the *test*
  adapter is `func() []config.Provider{ return fixed }`. The current
  `Runner` blurs them.

### 4. Log classification — make `reqlog.Entry` know what a failure is

- **Files:** `internal/reqlog/reqlog.go` (`Entry.Class` is a free-form
  string written by callers), `logs.go` (`-errors` filter has 4 hard-coded
  class names: `all_failed`, `failover`, `error`, plus the implicit
  `status >= 400` rule), `internal/proxy/chat.go` (writes classes
  `ok`/`cache`/`all_failed`/`error`/`stream`), `internal/probe/probe.go`
  (writes `healthcheck`, `healthcheck_server`, `healthcheck_rateLimit`...)
- **Problem:** The meaning of "is this entry a failure?" is split across
  every writer and reader. `logs -errors` has to know that `all_failed`
  counts as an error even when `Status < 400` (the streaming 503 path).
  Any new failure mode (e.g. probe introducing `healthcheck_auth`) needs
  the filter to be updated or the entry silently disappears from
  `-errors`. The contract between writer and reader is implicit.
- **Module Depth score:** 2 — Shallow. `Entry` is a wire struct (correct
  for the JSONL format) but it doesn't carry the "is failure" decision
  the rest of the system needs.
- **Solution:** Add `func (e Entry) IsFailure() bool` to reqlog.
  Single source of truth: `status >= 400 || class is in {all_failed,
  failover, error, healthcheck_<non-ok>}`. Logs filter becomes
  `if errorsOnly && !e.IsFailure() { skip }`. New failure classes
  require updating `IsFailure`, not every reader.
- **Benefits:**
  - **Locality** — failure classification in one method.
  - **Leverage** — `logs -errors`, any future dashboard, any alerting
    hook uses the same predicate.
  - **Test surface** — `TestEntry_IsFailure` table over classes + statuses.
- **Forcing function:** the user already asked for `-errors`; the next
  ask is "show me only doctor failures" or "show me only streaming
  failures" — those will be class-string lookups that should not be
  freeform in each tool.
- **Dependency category:** In-process.

### 5. Handlers.relay — collapse `relay` + `relayStream` + `streamRelay` into one module

- **Files:** `internal/proxy/chat.go` (`relay` line 259, `relayStream` line
  303, `streamRelay` line 347, plus `buildUpstreamRequest` line 208 and
  the `relayOpenAIGuaranteeFinish`/`readRawFrame` SSE helpers)
- **Problem:** Three function names do the same conceptual job: take a
  payload, build an upstream request, send it, copy the response back
  (buffered or streamed). `relay` calls `relayStream` for the streaming
  case; `relayStream` calls `streamRelay` for the actual SSE copy. The
  branching is: non-streaming (buffered body + retryable 4xx) vs
  streaming (SSE copy + `[DONE]` guard + cross-kind dialect). The
  `buildUpstreamRequest` helper is already shared (good), but the
  per-path kind→URL mapping (`/v1/chat/completions` vs
  `/v1/messages` vs `/v1beta/models/...:generateContent`) is repeated
  in two places and the per-path body-size limit (`maxUpstreamError` vs
  `maxResponseRead`) is set inline. Adding a 4th path (e.g. `/v1/embeddings`)
  would be 3 edits.
- **Module Depth score:** 2 — Shallow. Handlers exposes 3 relay methods
  with overlapping concerns. Pipeline has to know which one to call.
- **Solution:** Extract a `relay` module (or `internal/proxy/relay`):

  ```go
  package relay

  type Kind int // OpenAI, Anthropic, Gemini
  type Shape int // Buffered, Streamed

  type Outcome struct {
      Status      int
      Body        []byte // empty on streamed
      ContentType string
      RetryAfter  time.Duration
      Usage       StreamUsage
  }

  type Relay struct {
      HTTPClient *http.Client
      KeyFn      func(envName string) (string, error) // injectable key lookup
      Logger     *log.Logger
  }

  func (r *Relay) Send(ctx context.Context, w http.ResponseWriter, provider Provider, body []byte, shape Shape) (Outcome, error)
  ```

  Internally: `Send` picks the per-kind URL, builds the request via
  `buildUpstreamRequest` (now private to relay), sends, and dispatches
  on `shape` for the response copy. Pipeline calls `relay.Send(..., Buffered)`
  for non-streaming and `relay.Send(..., Streamed)` for SSE — one
  call site, no per-shape branching in pipeline.
- **Benefits:**
  - **Locality** — kind→URL mapping, per-path body-size limits, and
    `Anthropic-Version` defaults in one place.
  - **Leverage** — pipeline drops 2 of 3 call sites; new shapes
    (embeddings, audio) are one `Shape` constant + one URL table row.
  - **Test surface** — `TestRelay_Send` table over (kind, shape, status,
    body-shape) with `httptest.Server`. Currently `relay`/`relayStream`
    are integration-tested only via the 1k-LOC `proxy_test.go`.
- **Forcing function:** the per-kind URL map will need to grow with
  Anthropic Responses (queued) and any new Gemini endpoint variant;
  the current 3-edit cost will compound.
- **Dependency category:** In-process (HTTP client) +
  Local-substitutable (key lookup is faked in tests; production
  delegates to `Handlers.providerKey`).
- **Note:** Two adapters for key lookup already exist (production
  via `keystore.Store`, test via injected func) — the seam is
  justified, not hypothetical.

## Reuse (verified in-repo)

- `internal/proxy/dialect.Dialect` — deep, reuse; candidate #5's
  `relay.Send` calls `dialect.Stream` for cross-kind SSE.
- `internal/router.Candidate.Payload` + `ShouldFailoverOnClientError` —
  deep, reuse; candidate #1's `candidateRunner.eval` takes the
  candidate and returns the response.
- `internal/cache.Cache` + `internal/rtk.RTK` — deep, reuse as-is.
- `internal/usage.Store.RecordFull` / `Record` — deep, reuse.
- `internal/probe.Probe` — already deep (one-shot, one HTTP call);
  candidate #3 makes the loop around it also deep.
- `internal/proxy/pipeline_test.go` (`renderAllFailed503` tests already
  in `proxy_test.go`) — reuse as the test surface for #2.

## Files to modify (per candidate)

| Candidate | Primary files | Secondary (tests/docs) |
| ----------- | --------------- | ------------------------ |
| 1 CandidateRunner | `internal/proxy/pipeline.go`, new `internal/proxy/pipeline/runner.go` (or internal/pipeline package) | `proxy_test.go` (consolidate 503 tests around runner) |
| 2 Failures | `internal/proxy/pipeline.go`, new `internal/proxy/pipeline/failures.go` | `proxy_test.go`, `probe_test.go` (if added) |
| 3 Probe cleanup | `internal/probe/probe.go` (delete global, merge Runner→Loop), `main.go` (update callsite) | `probe_test.go` (new) |
| 4 Log classification | `internal/reqlog/reqlog.go`, `logs.go` (filter uses `IsFailure`) | `reqlog_test.go` (new) |
| 5 Relay module | `internal/proxy/chat.go` (delete relay* + buildUpstreamRequest, keep captureWriter / usageSniffer), new `internal/proxy/relay/` | `proxy_test.go`, new `relay_test.go` |

## Steps (analysis concluded; execution is per-candidate)

- [x] Survey post-#51 codebase, identify friction
- [x] Score module depth (1–5) per candidate; apply deletion test
- [x] Write this plan with 5 ranked candidates, each with forcing function
- [ ] **User picks** 1–2 candidates to grill
- [ ] Grill picked candidate: constraints, deps category per
      `DEEPENING.md`, seam placement, adapters, interface sketches
- [ ] User confirms preferred interface sketch
- [ ] Implement PR: write new module, delete waste tests, add interface
      tests, update `specs/tech-architecture/tech-stack.md` if a new
      domain term is named
- [ ] Verify: `go test ./...` + import-boundary check
      (`bash scripts/check-import-boundaries.sh` if a new package
      edge is introduced)

## Verification (general, per-candidate)

- `go test ./...` stays green; new tests at the deepened interface
  replace the old integration-only ones.
- No new public-package imports without an entry in
  `specs/import-boundaries.json` (per skill §5).
- Manual smoke: `routre serve` + a streaming + non-streaming request
  per kind combination; `routre doctor` and `routre logs -errors`
  exercise the new module's downstream callers.
- If candidate adds a domain term (e.g. "Failure", "Relay" as modules
  rather than just files), record in
  `specs/tech-architecture/tech-stack.md` in the same PR.

## Risks

- **Over-deepening #2 (Failures).** It's a small module with one
  rendering responsibility. If round 2 lands #1 (CandidateRunner) the
  renderer naturally moves into the runner's 503-emit path and #2
  becomes trivial. If we land #2 first, #1's eval can return
  `failures.Outcome` for the runner to render. Order matters: do #1
  first.
- **Probe cleanup (#3) might over-collapse.** If a 2nd consumer of
  the global config appears (e.g. `routre check` decides to call
  `SetConfig`), the global regains its keep. Defer #3 if no 2nd
  consumer is on the horizon.
- **Test deletion discipline (per `DEEPENING.md`).** Old
  integration tests on `relay`/`relayStream`/`renderAllFailed503`
  become waste once interface tests exist. Delete, don't layer.
- **Domain-language drift.** #2 introduces a "Failure" concept that
  should be added to `specs/tech-architecture/tech-stack.md` (e.g.
  "Failure: a request outcome (per-provider attempt + class + cooldown)
  rendered as 503 wire or human output"). Lazy-create the term in the
  same PR.

## Prioritization (recommended order)

1. **#1 CandidateRunner** — kills the most-duplicated code path;
   unblocks embeddings/Responses/add-a-3rd-shape work.
2. **#2 Failures** — piggybacks on #1; clean way to share wire format
   with `routre doctor`.
3. **#5 Relay module** — pipeline stops branching on shape; new
   endpoints (Responses, embeddings) become URL table rows.
4. **#4 Log classification** — small, but pays back across `logs` and
   any future dashboard.
5. Defer **#3 Probe cleanup** until a 2nd consumer of the global
   config appears. Marked lowest priority.

---

## Deep Dive: #1 CandidateRunner + #2 Failures (paired)

> Picked by user: **#1 CandidateRunner + #2 Failures** (paired; do #1 first,
> then #2 piggybacks on it). This section is the grilling record. Working
> tree (probe, doctor, 503 enrichment, logs filters) is in scope; its
> `cmdDoctor` + `probe.logResult` human output is the forcing function
> for #2's `RenderHuman`.

### Constraints any new interface must satisfy (shared by #1 + #2)

1. **Pre-candidate prep is byte-identical** — `tryCandidate` and
   `streamCandidate` (pipeline.go ~lines 500–525 and ~228–254) execute
   the same sequence: `cand.Payload(processed, requested)` →
   Responses-kind rejection (clientFmt==Responses + kind in
   {anthropic, gemini}) → crossKind `dialect.Request` → `rewriteModel`
   if crossKind + upstream differs → `clampPayload` →
   `injectPromptCache` for anthropic. Differs only in the relay call.
   Any interface must hide this behind a `prepFn` so a 3rd shape
   doesn't re-derive it.
2. **Pre-first-byte failure contract preserved** — `streamCandidate`
   returning `(false, err, class)` before the SSE writer is flushed
   means "failover is safe, move to next candidate". After first byte
   = `(true, nil, router.ErrStream)` (no failover). The runner must
   respect this boundary; the `eval` abstraction cannot hide it.
   Surface: `runnerOutcome.Emitted bool`.
3. **Auth-refresh retry stays in scope** — when
   `ClassifyStatusBody == ErrAuth`, both `tryCandidate` and
   `streamCandidate` call `p.handlers.refreshCredentials(envName)` and
   recurse once. The runner must own this loop, not the `eval`.
4. **Cooldown reporting is identical** — `ReportFailureWithBackoff`
   (with parsed `retryAfter`) is called from both halves in the same
   place. Runner owns it; eval returns `(class, retryAfter)` only.
5. **Metric emission is identical** — `metrics.Failure(name,
   class.String())` + `metrics.CacheRead(cacheRead)` +
   `metrics.Request(client, name, model, "ok")`. Runner owns it; eval
   returns the `cacheRead` token count.
6. **503 wire format is shared by `routre doctor` semantic** — same
   per-provider shape (provider / kind / class / error / cooldown). The
   Failures module's `Outcome` type is the one struct pipeline and
   doctor both emit.
7. **Streaming replay cache is shape-specific** — only the streaming
   eval captures SSE bytes for the replay cache; the non-streaming
   eval does not. Runner must call back to eval for the capture, not
   own it. Surface: `runnerOutcome.Captured []byte`.
8. **stdlib only, no new deps** — same as round 1.

### Dependency category

Both candidates: **In-process** (pure orchestration, no I/O beyond what
the eval provides). No ports needed. Runner tests drive a fake `eval`;
Failures tests build `[]Outcome` directly. `failures` is consumed by
pipeline (in-process) and `cmdDoctor` (in-process); same In-process
category — both adapters are in-repo code.

### Why pair #1 + #2

If #1 lands first, the runner's `tryLog` is `[]failures.Outcome` by
construction (one fewer mapping in the 503 emit path). If #2 lands
first, #1's eval returns `[]failures.Outcome` to the runner for the
503 render — same shape. Either order works, but #1-first is cleaner
because the runner owns the type choice. Risks section above already
flags the order.

### Deep Dive: #1 CandidateRunner

#### Problem restated with friction log

Pipeline's `Stream` and `processInternal` are ~80 LOC each (after the
503 enrichment work in the working tree) doing near-identical work:

| Step | Stream | processInternal |
| --- | --- | --- |
| cache key + miss metric | yes | yes |
| `CandidatesWithFallbacks` | yes | yes |
| 503 when `len==0` (× 2 sub-cases) | inline render | inline render |
| per-candidate retry loop (max 2 attempts, transient classes only) | yes | yes |
| `tryLog []attemptOutcome` accumulation | yes | yes |
| `renderAllFailed503` final render | yes | yes |
| `tryCandidate` / `streamCandidate` prep block | byte-identical | byte-identical |
| relay call | `p.handlers.relay(..., w, true, ...)` | `p.handlers.relay(..., rec, false, ...)` |
| success path usage recording | `usage.RecordFull` (streamUsage) | `usage.RecordFull` (extractor) |
| success path cache write | SSE replay cache | regular cache |

Only 4 of ~12 steps differ (relay writer, usage capture, cache write,
success-vs-failure return). Reader must keep both halves in mind to
verify they stay in sync on the 4 dimensions that *do* differ.

#### Module Depth score: 2

Small interface (`Stream` + `processInternal`) but the per-candidate
policy is repeated inline. Callers must trust the two halves stay
in sync.

#### Where the seam lives

`internal/proxy` package, **private to pipeline** (test-internal). Same
package because:

- runner has no callers outside pipeline (no public surface)
- putting it in its own package adds an import for `router.Candidate`
  and `router.ErrClass` without buying isolation (no 2nd adapter
  justifies a port)
- if a 2nd caller appears (e.g. a background `prewarm` task that
  exercises candidates), promote to `internal/proxy/runner` then

```text
handlers.route (caller)
    │
    ├── pipeline.Stream(ctx, req, w)        ──┐
    │       │                                  │
    │       └── pipeline.runner.Run(eval=streamEval) ──┐
    │                                                  │
    └── pipeline.Process(ctx, req)         ──┐       │
            │                                  │       │
            └── pipeline.runner.Run(eval=blockingEval) ┘
                                                   │
                                              [retry + refresh + report policy]
                                              [503 emit via failures.Render]
```

#### Interface sketch — three radically different designs

##### Sketch A — Runner struct, eval callback (RECOMMENDED)

```go
package proxy  // private, test-internal

type runnerEval func(ctx context.Context, cand router.Candidate, payload []byte) runnerOutcome

type runnerOutcome struct {
    Response   Response     // non-streaming: body + status; zero on stream
    Susage     streamUsage  // streaming: tokens captured from SSE; zero on non-stream
    Captured   []byte       // streaming replay-cache bytes; empty on non-stream
    Status     int
    RetryAfter time.Duration
    Class      router.ErrClass
    Err        error
    CacheRead  int64
    Emitted    bool         // true once any byte was written to w (stream only)
}

type runnerSummary struct {
    OK      bool
    Outcome runnerOutcome
    TryLog  []failures.Outcome
}

type candidateRunner struct {
    router      *router.Router
    metrics     *metrics.Metrics
    d           *dialect.Dialect
    cfg         *config.Store
    refreshFn   func(envName string) bool
    prepFn      func(ctx context.Context, cand router.Candidate, api, clientFmt apiFormat,
                    requested string, body, processed []byte) ([]byte, error)
    maxAttempts int
    retryDelay  time.Duration
}

func (r *candidateRunner) Run(ctx context.Context, w http.ResponseWriter,
    cands []router.Candidate, eval runnerEval) runnerSummary {
    var tryLog []failures.Outcome
    var lastErr error
    var lastClass router.ErrClass
    for _, cand := range cands {
        payload, perr := r.prepFn(ctx, cand, api, clientFmt, requested, body, processed)
        if perr != nil { /* reject, append tryLog, continue */ }
        for attempt := 0; attempt < r.maxAttempts; attempt++ {
            if attempt > 0 {
                if lastClass == router.ErrClient { break }
                time.Sleep(r.retryDelay)
            }
            out := eval(ctx, cand, payload)
            r.record(cand, out)  // ReportSuccess / ReportFailureWithBackoff / metrics
            if out.Status >= 200 && out.Status < 300 { return runnerSummary{OK: true, Outcome: out} }
            if out.Class == router.ErrAuth && r.refreshFn(cand.Provider.Provider.APIKeyEnv) { continue }
            if out.Emitted { return runnerSummary{OK: true, Outcome: out} } // no failover after byte
            if !router.IsRetryableClass(out.Class) { break }
        }
        tryLog = append(tryLog, makeOutcome(cand, lastErr, lastClass, router.CooldownRemaining(cand.Provider)))
    }
    return runnerSummary{OK: false, TryLog: tryLog}
}
```

Pipeline usage:

```go
// pipeline.Stream:
summary := p.runner.Run(ctx, w, cands, p.streamEval)
if !summary.OK {
    failures.Render(w, failures.KindAllFailed, requested, summary.TryLog, 5*time.Second)
    return nil
}

// pipeline.processInternal:
summary := p.runner.Run(ctx, nil, cands, p.blockingEval)
if !summary.OK {
    body, hdr := failures.RenderBody(failures.KindAllFailed, requested, summary.TryLog, 5*time.Second)
    return Response{StatusCode: 503, Body: body, Header: hdr}, nil
}
```

Hides: retry policy, ErrAuth refresh, cooldown reporting, metric
emission, post-loop 503 emit (via failures). Trade-off: `eval` returns
a 9-field struct (slightly wide) but each field is shape-specific; the
runner cannot know which fields the eval populated, and `Emitted` is
the only post-hoc channel.

##### Sketch B — Pair of methods on Pipeline (no eval callback)

```go
func (p *Pipeline) tryOnce(ctx, ...) runnerOutcome { ... }  // 50 LOC, the eval
func (p *Pipeline) tryWithRetry(ctx, cands, eval evalFn) runnerSummary { ... }
```

Same as A but the eval callback is hand-rolled on Pipeline rather than
a private runner struct. Trade-off: Pipeline becomes the runner; the
test surface is the same but the *struct* is gone, replaced by 2
methods that close over `p`'s fields.

##### Sketch C — Typed `Attempt` that carries the relay writer

```go
type Attempt struct {
    Cand      router.Candidate
    Payload   []byte
    Writer    http.ResponseWriter  // nil for non-streaming
    Recorder  *responseRecorder     // nil for streaming
    Body      []byte // original body (for extractor)
    Processed []byte // rtk'd
    Requested string
    ClientFmt apiFormat
    Client    string
    RTKSaved  int
    API       apiFormat
}
func (p *Pipeline) runOne(att Attempt) runnerOutcome { ... }
```

Each call site builds the Attempt explicitly; runner takes
`[]Attempt` not `[]Candidate`. Trade-off: Attempt is a 12-field struct
that every caller must populate. High ceremony for callers; runner
gains nothing (eval still owns per-shape writing).

#### Recommendation (opinionated)

**Ship Sketch A, internal to pipeline.go.** Reasons:

- **Runner as struct** beats method on Pipeline because the runner
  holds its own deps (`router`, `metrics`, `d`, `cfg`) and is
  testable in isolation by injecting fakes.
- **`eval` callback** is the right abstraction because the only
  shape-specific work is "send the prepared payload, return what
  happened" — the prep is shared (`prepFn`), the post-mortem is
  shared (runner records), only the per-shape I/O is shape-specific.
- **`runnerOutcome.Emitted` bool** is the contract for the
  pre-first-byte-vs-after-first-byte failover rule. Without it, the
  eval's success-vs-failure decision has to be re-derived from
  `Status + Err` and the runner can misclassify.
- **Failures is folded in.** The runner's `tryLog` is
  `[]failures.Outcome` directly (not `[]attemptOutcome`). One type.
  Saves a mapping in the 503 emit path.

Steal from B: keep the runner as a *private struct inside pipeline.go*
for now. If a 2nd user (prewarm? health-check that exercises a real
request?) appears, promote to `internal/proxy/runner`. No port
needed at 1 user.

Steal from C (lightly): the `Attempt` shape can be a `runnerPrep` struct
return value for `prepFn` so the prep result (payload, errors) is
typed, not a 2-tuple. Skipped for now — YAGNI; the inline
`payload, err := r.prepFn(...)` is two lines.

Resulting diff: `pipeline.go` `Stream` shrinks 80→~25 LOC (just cache
- relay eval), `processInternal` shrinks 100→~30 LOC, `tryCandidate`
and `streamCandidate` become `blockingEval` and `streamEval` private
methods. New `candidateRunner` struct (~120 LOC). One private
`runnerOutcome` + `runnerSummary` type.

#### Tests at the new interface

Old `proxy_test.go` 503 tests
(`TestStreamingAllProvidersFailedIncludesReasons`,
`TestNonStreamingAllProvidersFailedIncludesReasons`,
`TestProvidersUnavailableIncludesModelAndCooldown`) **survive
unchanged** — they assert on observable wire bytes through the
Pipeline interface. The runner is private so they don't need to know
it exists.

New tests at the runner's interface (private — `_test.go` in
`package proxy`):

- `TestRunner_RetryOnTransientClass` — first attempt returns
  `Class=ErrServer`, second succeeds; runner does NOT log a final
  503. Asserts on `runnerSummary.OK` and a single
  `metrics.Failure`/no `ReportFailure` on the success path.
- `TestRunner_AuthRefreshRecursion` — first attempt
  `Class=ErrAuth`, `refreshFn` returns true, second attempt
  succeeds. Asserts: one refresh call, no 503, success recorded.
- `TestRunner_NoFailoverAfterEmitted` — streaming eval returns
  `Emitted=true, Err=nil`; runner stops iterating, returns
  `OK=true` even if subsequent cands exist. (The `Emitted`
  contract.)
- `TestRunner_NonRetryableStopsEarly` — `Class=ErrClient`, runner
  does NOT loop to attempt 2 for that cand; moves to next cand.
- `TestRunner_AllCandsFailReturnsTryLog` — every cand fails
  (different classes); `runnerSummary.TryLog` is one entry per
  unique provider with the right `Class` + `Cooldown`.

Assertion style: `runnerSummary` is private to the test, so the
test drives the runner with a fake `eval` that returns canned
`runnerOutcome`s. Tests survive runner-internal refactors as long
as the per-cand retry + refresh + report policy stays correct.

### Deep Dive: #2 Failures

#### Problem restated with friction log

503 rendering lives in **6 places** in the current working tree:

1. `pipeline.Stream` — `all_providers_failed` final render
   (post-loop)
2. `pipeline.Stream` — `providers_unavailable` (when cands==0 +
   cooldown)
3. `pipeline.Stream` — `model_not_found` (when cands==0 + no
   cooldown)
4. `pipeline.processInternal` — same 3, but returning a `Response`
   instead of writing to `w`
5. `cmdDoctor` (main.go) — *similar* per-provider log format, but
   as human log lines: `%-18s %-9s %s status=%d %v`
6. `internal/probe.Runner.logResult` — *similar* per-provider
   format as reqlog entries + human log

That's 3 JSON wire kinds × 2 transports (HTTP stream / non-stream
Response) + 2 human formats. Each is hand-rolled; a wire change
breaks 3–4 places, not 1.

#### Module Depth score: 2

`renderAllFailed503` is a 40-LOC function, but the *interface*
includes "how do I render a providers_unavailable 503?" — the
caller must know to inline-build a different `map[string]any` and
`json.Marshal` it. The 6 sites drift.

#### Where the seam lives

`internal/proxy/failures` package (top-level under proxy, not
nested). Reasons:

- `cmdDoctor` and probe consume it too — not just pipeline
- It owns one type (`Outcome`) and three render functions
  (`Render` for stream, `RenderBody` for buffered, `RenderHuman`
  for terminal/reqlog). Tiny public surface → safe to import from
  3 packages.
- No ports needed (pure formatter over `[]Outcome` +
  `http.ResponseWriter`).

```text
pipeline.Stream ──┐
                  ├──▶ failures.Render / RenderBody / RenderHuman
pipeline.process ─┤
cmdDoctor ────────┤
probe.Runner ─────┘
```

#### Interface sketch — three radically different designs

##### Sketch A — `Render(w, ...)` + `RenderBody(...)` + `RenderHuman(...)` (RECOMMENDED)

```go
package failures

type Outcome struct {
    Provider string
    Kind     string        // "openai" | "anthropic" | "gemini"
    Class    string        // router.ErrClass.String(); empty when ok
    Err      string
    Cooldown time.Duration // 0 = not in cooldown
}

type Kind int
const (
    KindAllFailed Kind = iota
    KindProvidersUnavailable
    KindModelNotFound
)

// Render writes a 503 to w with the per-provider breakdown (when
// outcomes is non-empty). Sets Content-Type and Retry-After.
func Render(w http.ResponseWriter, kind Kind, model string, outcomes []Outcome, retryAfter time.Duration)

// RenderBody returns the same body as []byte + headers, for code
// paths that build a Response (non-streaming) instead of writing
// directly.
func RenderBody(kind Kind, model string, outcomes []Outcome, retryAfter time.Duration) (body []byte, header http.Header)

// RenderHuman writes a one-line per-provider summary to w
// (for `routre doctor` log output and probe logResult). Same
// Outcome type; different format.
func RenderHuman(w io.Writer, outcomes []Outcome)
```

Pipeline usage:

```go
// pipeline.Stream (replace inline 503 emit):
if !summary.OK {
    failures.Render(w, failures.KindAllFailed, requested, summary.TryLog, 5*time.Second)
    return nil
}

// pipeline.processInternal (replace inline 503 return):
if !summary.OK {
    body, hdr := failures.RenderBody(failures.KindAllFailed, requested, summary.TryLog, 5*time.Second)
    return Response{StatusCode: 503, Body: body, Header: hdr}, nil
}
```

Doctor usage:

```go
// cmdDoctor (replace inline logger.Printf loop):
failures.RenderHuman(logger.Writer(), outcomes)
```

Hides: JSON shape, field omission rules (omit zero class, omit zero
cooldown), cooldown rounding (`Round(time.Second).Seconds()`),
`Retry-After` header parsing, `providers_unavailable` vs
`model_not_found` branch.

##### Sketch B — Builder pattern

```go
type Response struct {
    Kind       Kind
    Model      string
    Outcomes   []Outcome
    RetryAfter time.Duration
}
func (r *Response) WriteTo(w http.ResponseWriter)
func (r *Response) Body() ([]byte, http.Header)
func (r *Response) Human() string
```

Single struct, three methods. Trade-off: more API surface, builder
pattern adds boilerplate (`r := &failures.Response{Kind: ..., Outcomes:
...}; r.WriteTo(w)`).

##### Sketch C — One generic-ish `Format(Format, ...)` function

```go
type Format int
const (
    FormatWireJSON Format = iota
    FormatHumanText
    FormatReqlogEntry // emits a reqlog.Entry
)

func Format(f Format, kind Kind, model string, outcomes []Outcome, retryAfter time.Duration) []byte
```

Single function, format picked by enum. Trade-off: the function
returns `[]byte`; the caller has to set headers itself. Loses the
typed return for the body+header pair. `FormatReqlogEntry` is
premature — only 1 caller (probe) needs a single-result format, not
the batch format that `Format` describes.

#### Recommendation (opinionated)

**Ship Sketch A, with `Render` and `RenderBody` as two free
functions, plus `RenderHuman` for the doctor/probe path.** Reasons:

- **`Render` vs `RenderBody` split is the right call** because
  `Render` writes headers + body to an `http.ResponseWriter` (for
  streaming) and `RenderBody` returns `(body, header)` (for the
  `Response` struct that non-streaming returns). The caller picks
  the variant that matches its transport. Two free functions with
  one shared private `buildBody()` helper. No builder ceremony.
- **`RenderHuman` is the doctor+probe entry point.** Same
  `Outcome` type, different format. The 3-format problem
  (wire + human + log) is *3 functions*, not 1 generic switch.
- **Type-strict `Kind` enum** (not a string) prevents the
  "all_providers_failed" vs "providers_unavailable" typo class
  of bug that the prior inline code invited.
- **No port** — pure formatting, stdlib only. Failure module is
  the *easiest* one in the round to deepen.

Skipped: builder (B) — too much ceremony for 3 callers. Generic
format (C) — loses the typed return for the buffered path;
`FormatReqlogEntry` is premature (probe formats one result, not
a batch — that path stays as-is for now).

Resulting diff: new `internal/proxy/failures/failures.go` ~120 LOC
(3 functions, 1 type, 1 enum). Pipeline 4 inline 503 blocks become
4 `failures.Render` / `RenderBody` calls. `cmdDoctor` 12-line
result-print loop becomes `failures.RenderHuman`. `probe.logResult`
stays as-is (it formats the single-result case, not the batch
case; unification with `RenderHuman` is a separate pass).

#### Tests at the new interface

- `TestRender_AllFailed_GoldenJSON` — feed 3 `Outcome`s with
  different classes + cooldowns; assert on byte-exact JSON body
  - `Content-Type: application/json` + `Retry-After: 5` headers.
- `TestRender_ProvidersUnavailable_OmitsOutcomes` — when
  `outcomes==[]` (providers_unavailable / model_not_found kinds),
  body has no `attempts[]` field, only `model` +
  `cooldown_seconds` for unavailable.
- `TestRender_CooldownRounding` —
  `Outcome{Cooldown: 1.7*time.Second}` →
  `cooldown_remaining_seconds: 2` (round to nearest second).
- `TestRender_OmitsZeroFields` — `Outcome{Class: ""}` → no
  `"class":""` in JSON (omitempty on every field).
- `TestRenderBody_BufferPath` — same fixtures as `TestRender_*`
  but check the returned `[]byte` + `http.Header`.
- `TestRenderHuman_PerLine` — feed 4 outcomes; assert one
  human-readable line per outcome, status field present, no
  JSON braces.

Tests delete from proxy_test.go: nothing — the existing
`Test*AllProvidersFailedIncludesReasons` tests still pass because
they assert on the *bytes* through the Pipeline interface, not on
the helper's internals. That's the deletion-test discipline paying
back: the wire format is now the *module's contract*, and the old
tests assert the contract.

### Decisions — confirmed by user

1. **Pair #1 + #2, do #1 first.** #1 is the deeper seam (kills the
   duplicated candidate loop); #2 piggybacks because the runner's
   `tryLog` is `[]failures.Outcome` by construction.
2. **Runner is a private struct in `pipeline.go`**, not a new
   package. Single user (Pipeline.Stream + processInternal).
   Promote to `internal/proxy/runner` if a 2nd user appears.
3. **`prepFn` is in scope** as a runner field, not a free function
   the caller passes in. The byte-identical pre-relay block is
   constant across the two evals; making it a runner-owned
   callback keeps the seam clean.
4. **`Emitted bool` is the streaming contract.** The eval sets it
   to true once any byte reaches `w`; the runner respects it (no
   failover after Emitted). This is a hard invariant of the
   streaming translation, already documented in
   `stream_translate.go`.
5. **Failures module is `internal/proxy/failures`.** Public
   surface because 3 packages consume it (pipeline, cmdDoctor,
   probe).
6. **No new terms for tech-stack.md.** "CandidateRunner" and
   "Failures" are *module* names, not *domain* terms. The existing
   vocabulary (Candidate, Outcome, Cooldown, Provider) covers
   them. (A future deepening could elevate `Outcome` to a domain
   term — defer.)

### Hybrid interface locked (for the PRs)

```go
// internal/proxy/pipeline.go (private)
type candidateRunner struct {
    router      *router.Router
    metrics     *metrics.Metrics
    d           *dialect.Dialect
    cfg         *config.Store
    refreshFn   func(envName string) bool
    prepFn      func(ctx context.Context, cand router.Candidate, api, clientFmt apiFormat,
                    requested string, body, processed []byte) ([]byte, error)
    maxAttempts int
    retryDelay  time.Duration
}
type runnerOutcome struct {
    Response   Response
    Susage     streamUsage
    Captured   []byte
    Status     int
    RetryAfter time.Duration
    Class      router.ErrClass
    Err        error
    CacheRead  int64
    Emitted    bool
}
type runnerSummary struct {
    OK      bool
    Outcome runnerOutcome
    TryLog  []failures.Outcome
}
func (r *candidateRunner) Run(ctx context.Context, w http.ResponseWriter,
    cands []router.Candidate, eval runnerEval) runnerSummary

// internal/proxy/failures/failures.go (public)
type Outcome struct {
    Provider string
    Kind     string
    Class    string
    Err      string
    Cooldown time.Duration
}
type Kind int  // KindAllFailed | KindProvidersUnavailable | KindModelNotFound
func Render(w http.ResponseWriter, kind Kind, model string, outcomes []Outcome, retryAfter time.Duration)
func RenderBody(kind Kind, model string, outcomes []Outcome, retryAfter time.Duration) (body []byte, header http.Header)
func RenderHuman(w io.Writer, outcomes []Outcome)
```

---

*Grilled: #1 CandidateRunner + #2 Failures, paired. Ready for
implementation in two PRs (#1 first, #2 second). #3/#4/#5 deferred
— see Prioritization.*
