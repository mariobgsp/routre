# Changelog

All notable changes to routre-cli are documented here, newest first.
Releases are version-tagged (`v*`); the release workflow attaches
per-platform binaries to the GitHub Release. CI
(`.github/workflows/ci.yml`) runs tests on every push and PR.

The format loosely follows [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

> Versioning restarted at v0.1.0 for the **routre** rebrand
> (2026-08-25). Sections marked `legacy` use the pre-rebrand
> routre-cli numbering and are kept for history only.

## [0.4.6] — 2026-09-03

### Added

- **CommandCode AI provider** — new `commandcode` tier (`https://api.commandcode.ai/provider/v1`, `kind: openai`, `COMMANDCODE_API_KEY=user_...`) with 19 models (`deepseek/deepseek-v4-flash`, `gpt-5.6-luna`, `claude-sonnet-5`, etc.). Tier is first so bare `deepseek/*` prefers `commandcode` over `opencode` free variants. Pi side: `~/.pi/agent/models.json` now documents custom-provider pattern (`api: openai-completions`, `apiKey: commandcode`, `models: [{id}]`) and `pi --list-models | grep commandcode` verification. Guide (`docs/AGENT_ROUTRE_GUIDE.md` §2-§3) updated with `commandcode` examples and `COMMANDCODE_API_KEY` in `routre.env`.

### Fixed

- **Provider-qualified routing hijack** — `commandcode/deepseek/deepseek-v4-flash` was still matched by `opencode-zen` via `freeVariantOf` tail (`deepseek-v4-flash` → `deepseek-v4-flash-free`) and sent to `opencode` with `400 Model is unavailable`. `Router.Candidates` now detects `qualifiedFor` (first segment equals a provider name) and skips all non-matching providers, so qualified `commandcode/<model>` isolates to `commandcode` only. Bare `deepseek/*` now correctly routes via tier order.

## [0.4.5] — 2026-09-03

### Added

- **Model-discovery polish (lean refresh)** — periodic `GET {base}/models` now runs on a jittered `6h ± 5m` timer (single timer, no per-provider goroutine, zero extra RAM — defeats fleet thundering herd), logs `model discovery: refreshed N providers, +M models` at info, and stamps freshness to `routre_discovery_last_success_timestamp_seconds` (`/metrics`) + `discovery_last_success` (`/v1/status`). `Router.DiscoverModelsWithStats` returns `(refreshed, added)`; `DiscoverModels` signature unchanged. README gains a cron one-liner for set-and-forget `models sync`.

## [0.4.4] — 2026-09-03

### Fixed

- **Opencode `x-opencode-session` header** — requests from `Go HTTP client` / `curl` user-agents were missing `x-opencode-session` and will error from `09/06` (opencode Go notification). `routre` now forwards an incoming `x-opencode-session` when present, otherwise injects a stable gateway-generated ID for every `opencode.ai` upstream request (relay streaming + non-streaming) and for `probe`/`doctor`. Minimal stdlib-only change (`crypto/rand` once per gateway instance, `ponytail: per-gateway stable, upgrade to per-client/per-conversation map if opencode optimizes on it`).

## [0.4.3] — 2026-09-02

### Fixed

- **Native Responses passthrough for opencode** — `muse-spark-1.2-contributor-free` (and other `opencode` `responses`-only models) returned `500` via `routre` because `/v1/responses` was always translated to `/v1/chat/completions` upstream, but `opencode.ai/zen` only serves that model on `/v1/responses`. `routre` now detects native upstreams (`base_url` contains `opencode.ai` → `isNativeResponsesBase`) and proxies `responses` verbatim to `/v1/responses` (model rewrite only). Non-native providers (e.g. `openrouter`) still translate `responses → chat` for failover. Streaming (`relayStream` `to=responses`) and non-streaming (`tryEval` wrap/cache) both respect the native path. Fixes `all_providers_failed` with `cooldown_remaining_seconds: 64→256` and restores `pi → routre → opencode.ai` for `muse-spark` (verified `200` streaming + non-streaming via `127.0.0.1:20128`).

- **Pi routing via routre** — `~/.pi/agent/models.json` now routes `opencode` + `openrouter` (and template for `anthropic`/`openai`/ChatGPT/Codex) through `http://127.0.0.1:20128/v1` instead of direct `https://opencode.ai/zen/v1`. `forward_unknown: true` and `ling-3.0-flash-fin-free`/`muse-spark-1.2` added to `opencode-zen` whitelist.

### Added

- **Agent Routre Guide** — `docs/AGENT_ROUTRE_GUIDE.md` (copied to `~/.pi/agent/skills/routre-guide/SKILL.md` + `~/.pi/agent/ROUTRE_GUIDE.md`) — mandatory `127.0.0.1:20128` rule for every provider (`opencode`, `openrouter`, `anthropic` ↔ Claude Code, `openai` ↔ ChatGPT/Codex, `google` ↔ Gemini), `models.json` + `config.json` templates, `forward_unknown:true`, `isNativeResponses` extension point, and copy-paste `curl` + `requests.jsonl` + `routre list` verification & failure signatures + pre-merge checklist. Prevents the `responses`→`chat` 500 recurrence.

- **Upstream 500 debug log** — `relayStream` non-2xx now logs `baseURL+path status body payload` (500 chars) when `Logger` is set, so the next `500` is diagnosable without a mock upstream.

## [0.4.2] — 2026-09-02

### Fixed

- **UI dashboard Host/Origin hardening** — loopback `Host` 403, `Origin` 403, `1MiB/64KB` overflow 400, empty key 400, bad JSON 400, invalid config `Validate` 400 — 15/15 UI tests, full suite + race green; smoke 7/7 on 20199.

## [0.4.1] — 2026-08-30

### Added

- **Per-provider cache labels** — `routre_cache_creation_tokens_total{provider="…"}` and `routre_cache_savings_tokens_total{provider="…"}` are now exposed in `/metrics` alongside the existing global sums, so the per-provider cost vs benefit of prompt caching can be compared at a glance. Backward-compatible: the unlabelled `_total` series are kept and equal the sum of the per-provider values. `CacheRead` / `CacheCreation` on the metrics layer now take a `provider` argument; the call sites in `internal/proxy/pipeline.go` pass `cand.Provider.Provider.Name` directly. The Anthropic vs OpenAI prompt-cache effect can now be measured separately on a single `/metrics` scrape.

- **Anthropic `cache_control` injection (inactive)** — when the OpenAI→Anthropic cross-kind translation fires, the system block and the last two conversation messages now carry `cache_control: {type: "ephemeral"}` breakpoints so Anthropic's upstream 5-minute prompt cache can cache the prefix across consecutive calls. Three unit tests cover the breakpoint shape (`internal/proxy/dialect/translate_test.go`). **Inactive on the current shipped configuration**: no live provider has `kind: "anthropic"` in `config.json`, so the OpenAI→Anthropic translation path does not fire for current traffic. The code is in the binary so that the moment an Anthropic-kind provider is added, the breakpoints activate without a code change. The per-provider label above will surface the cache_creation_input_tokens Anthropic returns on first use.

## [0.4.0] — 2026-08-30

### Added

- **Prompt-cache creation ledger** — provider-reported `cache_creation_input_tokens` (OpenAI's 1.25x write on a new cacheable prefix; Anthropic's equivalent) is now captured alongside cache reads. New `/metrics` counters: `routre_cache_creation_tokens_total` (counter) and `routre_cache_savings_tokens_total` (gauge, `read*0.9 - creation*0.25`). New per-row fields in `routre list`: `cache_creation` token count and `CacheSavingsUSD` (net prompt-cache savings at the provider's configured input price). Replaces the previous "read tokens only" blind spot — the prior live data showed 17.6M read tokens with creation tokens dropped entirely, so the *cost* of cache writes was invisible. The non-streaming extractor, streaming sniffer, and Anthropic→OpenAI dialect now all surface both fields. Pure observation change: no config knob, no cache logic touched.
- **Cache miss attribution** — misses are now counted by reason (`absent`, `expired`, `shape_mismatch`, `disabled`) via `routre_cache_misses_by_reason_total{reason=…}` in `/metrics` and `cache_miss_reasons` in `/v1/status`. The legacy `routre_cache_misses_total` counter is kept for compatibility. This answers “why do I miss?” before tuning anything.
- **Canonical cache keys** (`cache.canonical_keys`, default on) — the cache key is computed over a deterministic JSON round-trip (sorted keys, no whitespace, `json.Number` preserves large ints), so requests that differ only in key order or whitespace share a key and hit. Strictly output-inert: sampling parameters stay in the key, so there is zero wrong-output risk. Applies to stream and non-stream, get and put, via a single `Pipeline.keyFor`.
- **Sliding TTL** (`cache.sliding_ttl`, default on) — a hit refreshes the entry's expiry (`now + TTL`), so actively used entries no longer expire mid-use; only `max_bytes` bounds RAM.

### Changed

- **Cache defaults raised** — `max_entries` 4096→16384, `ttl_seconds` 86400→604800 (7 days), `max_bytes` 64→128 MiB. All still overridable in `config.json` and reloadable via SIGHUP.

## [0.3.5] — 2026-08-29

### Changed

- **Cache hit efficiency (A+B unconditional)** — `cacheKey` now strips `stream` flag so `stream:true` vs `stream:false` on the same prompt collides (exact-match hit). Prompt-prefix reuse unchanged via `cache.prompt_cache` (Anthropic `cache_control` injection) — no new config, no vector DB.
- **RTK token-cost accuracy + aggressive level** — `rtkSaved` now uses BPE `tokenize.Count` (cl100k) not heuristic `Estimate`, so ledger matches billing. `rtk.level` default promoted from `standard` → `routre` (blank-strip + dedup + head/tail) still fail-open; override with `"level":"standard"` in config.
- **Latency (seamless gateway)** — zero-copy when RTK/prefix unchanged (no re-marshal churn) + BPE cache (4096 LRU) keeps TTFB add <20ms stream / p50 <10ms non-stream. Existing `dial_ms`/`headers_ms`/`ttfb_ms`/`total_ms` in reqlog verify overhead.
- **Overloaded brief for `-free` models** — 503 wire for `-free` overloaded now reads `model "…-free" overloaded — free-tier capacity (not routre), retry in 1s` (atoms `attempts[]` kept). CLI `RenderHuman` (doctor/probe) collapses all-`overloaded` to one line: `overloaded — free-tier capacity (not routre), retrying in 1s`.
- **Docs landing for non-IT (single HTML)** — hero now reads "Your AI apps, cheaper & always online" with power-strip analogy; added `What is routre, in plain words?` (remembers answers / shrinks data / switches providers) + `Our goals` band (save money / never block / stay light). Three plain cards (💰 Pay less / 🛡️ Stay online / 📊 See spend) link to tech spec; engineers' detail stays behind existing sections. Single `docs/index.html` (979→1004 lines), no new deps, mobile-responsive.

## [0.3.4] — 2026-08-28

### Added

- **`routre models sync` / `diff`** — durable model updates so a provider's new model never leaves the user behind. Fetches each provider's `GET {base_url}/models` via the shared discovery path (`router.DiscoverModels`) and persists new IDs into `config.json` (additive by default, `--prune` to drop retired models). `--dry-run` / `--json` for scripting; `diff` is a dry-run alias. Unreachable providers are skipped with a warning and kept as-is. After a successful write the gateway is `SIGHUP`'d best-effort so the new list is live immediately. Discovery still runs every 6h + at startup + on `SIGHUP`; sync just makes it durable across restarts.
- **`forward_unknown` docs** — README adds "Keeping models current" section explaining the three-layer strategy: `forward_unknown` (instant forward), in-memory discovery (every 6h), and durable `models sync`.

### Changed

- **README refactored** — `Latest` collapsed to `v0.3.4` + collapsible `v0.3.2` detail, added `Keeping models current` subsection, added `models sync/diff` to Quick start and Commands table, updated Project layout (`models.go`), collapsed `doctor`/`bench` descriptions. Architecture caption kept version-agnostic.

## [0.3.3] — 2026-08-28

### Changed

- **README rewritten** — new "Latest (v0.3.2)" callout block summarises enriched 503, doctor, per-phase observability, latency hardening, candidateRunner, double-retry, and debug trace. Architecture section updated to describe the full pipeline (format detect → RTK → cache → router → runner → dialect → relay) and the new observability surface. Project layout adds `internal/proxy/dialect`, `internal/proxy/failures`, and the `candidateRunner` deep module. Observability section documents the new `dial_ms` / `headers_ms` / `ttfb_ms` / `total_ms` JSONL fields.
- **Architecture diagram** — `docs/architecture.puml` rewritten with white background, larger landscape layout, and comprehensive detail (full request pipeline, observability, persistence, providers, and per-component notes). Re-rendered PNG at 4022×1551.

## [0.3.2] — 2026-08-28

### Changed

- **Plans moved to vault** — `docs/PLAN.md`, `docs/SPEC.md`, `specs/PLAN-*.md`, `specs/tech-architecture/tech-stack.md` moved to `~/Projects/brain/plans/routre/`. `.gitignore` updated to keep plans out of the repo. `specs/tech-architecture/` and `specs/PLAN-*.md` are no longer tracked.
- **Unused imports removed** — `io`, `strings`, `reqlog` dropped from `internal/proxy/pipeline.go`; leftover `var _ =` placeholder lines removed.

## [0.3.1] — 2026-08-27

### Fixed

- **Streaming `overloaded` not stacking** — `streamCandidate` passed `nil` body to `ClassifyStatusBody`, so `529` with `overloaded_error` body was missed for streaming. Now captures `errBody` from `relay` and `529` with empty body is `ErrOverloaded`. Fixes the `4× in 30s` spam for `minimax-m3-free` via `commandcode` streaming.
- **`overloaded` double retry** — single-provider `overloaded` (e.g. `minimax-m3-free` only on `commandcode`) now does `2× 1s` retries before `503`, hiding a 2-sec upstream blip entirely. `DEBUG` logs `all overloaded → retry after 1s`.

### Added

- **`--debug` / `ROUTRE_DEBUG=1`** — verbose trace (`[DEBUG proxy] try/result`, `process/stream request`) to `stderr` for live `overloaded` tracking. Enable with `routre serve --debug` or `env ROUTRE_DEBUG=1`.

## [0.3.0] — 2026-08-27

### Added

- **`routre doctor`** — one-shot per-provider probe, prints ok/auth/server/network with cooldown. Shares the `failures.Outcome` shape with the 503 wire body so the per-provider reason format is identical on the terminal and over the wire.
- **`health_check` periodic probe.** Config: `health_check.enabled`, `interval_seconds`, `probe_model`. Internal `probe.Probe` (one HTTP call) + `probe.Runner` (ticker Loop). Probes are strictly observation-only — they never touch router cooldown, cache, usage, or metrics. Off by default.
- **Enriched 503 body** (`all_providers_failed`, `providers_unavailable`, `model_not_found`). Per-provider `attempts[]` on the wire with `{provider, kind, class, error, cooldown_remaining_seconds}`. Both streaming and non-streaming paths. New `internal/proxy/failures` package owns the wire + human render (`Render` / `RenderBody` / `RenderHuman`).
- **`routre logs -errors -provider <name>`** filters. `-errors` keeps `status>=400` or class `all_failed`/`failover`/`error`. `-provider` matches the upstream that served (or tried to serve) the request. `reqlog.Entry.Provider` now populated for every non-streaming entry.

### Fixed

- **`overloaded_error` 503 spam** — Anthropic `529 overloaded_error` (and any body matching `overloaded` / `temporarily unavailable` / `capacity` / `try again later` / `rate_limit` / `too many requests`) no longer stacks `2s→4s→8s→…→5m` exponential backoff. New `ErrOverloaded` class caps the cooldown at `30s` (or upstream `Retry-After`, whichever is shorter) and resets the failure counter. Plain `ErrServer` still escalates. Symptom that this fixes: `Retry failed after 3 attempts: 503: {"message":"Upstream model provider is temporarily unavailable…"}` cascade.
- **Cooldown cap `30m → 5m`.** A 60s upstream blip was turning into a half-hour outage; 5min still absorbs a sustained outage (exponential saturates by hit 9: 2·2⁸=512s ≈ 8.5m → clamped to 5m) and recovers faster.
- **Uniform-failure 503 reshaping.** When every candidate returns 4xx, the gateway now returns `404 model_not_found` (was `503 all_providers_failed`). When every candidate returns 401/403, returns `502 all_providers_unauthorized`. Mixed classes still surface as `503` with the per-provider breakdown.
- **Streaming 503 reqlog gap** — `chat.go route` previously logged nothing on a streaming all-failed response; now writes a `class: "all_failed"` line.
- **`reqlog.Log`** writes the entry to stderr when `request_log` is empty instead of silently dropping observability.

### Changed

- `keystore.Store` is now `sync.RWMutex` and exposes `Keys()` (snapshot of stored env names) for diagnostics.
- Cooldown `Max` lowered from `30m` to `5m`; `MaxHits` stays `30`.

## [0.2.3] — 2026-08-26

### Fixed

- **Slow `all_failed` failover (`28-73s` → `~3s`).** Transport `ResponseHeaderTimeout` `60s→20s`, `Dial`/`TLS` `10s→5s`, per-attempt `120s→30s` (streaming exempt). 5-provider cascade now completes quickly instead of stacking timeouts. Root cause of the reported “slow through routre”.

## [0.2.2] — 2026-08-26

### Fixed

- **Same-kind Anthropic/Gemini streaming was buffered at 32 KiB.**
  The generic same-kind streaming loop only flushed after `32 << 10` bytes
  accumulated, so Claude Code / Gemini streams smaller than one buffer burst
  at the end instead of trickling per-token. Now flushes per chunk (same
  as cross-kind and OpenAI paths). No change for OpenAI-dialect clients.

## [0.2.1] — 2026-08-26

### Added

- **Anthropic↔Gemini dialect pair.** A `gemini`-kind provider can now serve
  Anthropic-dialect clients (`/v1/messages`, e.g. Claude Code pointed at
  the gateway): `anthropicToGemini` request translation (system →
  `systemInstruction`, `tool_use`/`tool_result` blocks →
  `functionCall`/`functionResponse` with id→name re-linking, tools →
  `functionDeclarations`), `geminiToAnthropic` non-streaming response
  translation (content blocks, `stop_reason`, `usageMetadata`), and an
  in-flight `g2a` SSE state machine emitting the full Anthropic event
  sequence (`message_start` … `message_stop`) with deterministic
  `toolu_<name>` ids and guaranteed termination — including a new EOF tail
  that closes any stream whose upstream vanished without a terminal frame
  (also hardens the OpenAI→Anthropic path against missing `[DONE]`).
  Covered by golden tests at the Dialect interface and non-streaming/
  streaming e2e relay tests via the Gemini mock.
- **Streaming replay cache.** The response cache no longer skips streams:
  successful streaming responses are captured as exact client-dialect SSE
  bytes (bounded by the existing 8 MiB per-entry limit) and identical later
  requests replay byte-for-byte with zero upstream calls
  (`X-Llrouter-Cache: hit`, saved tokens credited to the ledger).
  Hardening that came with it: an upstream dying mid-stream after the first
  byte now surfaces as a stream abort end-to-end (`dialect.ErrAborted` →
  gateway abort contract) instead of looking like a clean end — so truncated
  streams can never be cached or mistaken for success on any path (the
  OpenAI guarantee-finish relay included). Streaming and JSON entries share
  keys but never cross shapes.

## [0.2.0] — 2026-08-26

### Added

- **curl installer** — `curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre-cli/main/install.sh | sh`
  downloads the latest GitHub release, verifies its sha256 checksum, and
  installs the static binary to `~/.local/bin` (no sudo). Env overrides:
  `ROUTRE_INSTALL_DIR`, `ROUTRE_VERSION`.
- **`routre-cli update`** — self-update: resolves the latest tag via the
  releases redirect (no api.github.com calls), verifies checksums, and
  atomically replaces the running binary with rollback on failure.
  `-check` reports without applying. Windows prints a re-install hint
  (rename-swap support deferred); npm-managed installs are refused with
  switch instructions.
- **Tag-driven release pipeline** (`.github/workflows/release.yml`): pushes of
  `v*` tags now attach per-platform assets (`routre-cli_{GOOS}_{GOARCH}`) and
  `checksums.txt` to GitHub Releases. Local twin: `make dist-release`.

### Changed

- **npm distribution deprecated.** The launcher shim now prints the curl
  command and exits non-zero; all published npm versions carry an
  `npm deprecate` notice. Registry packages remain installed for pinned
  dependents.
- **Architecture deepening (#51).** Translation scatter moved behind the
  `internal/proxy/dialect` seam (`Request`/`Stream`/`Supported`/
  `DetectFormat`, `ErrUnsupported`); the 7-step request orchestration
  (RTK → cache → routing → translation → retry) collapsed into
  `pipeline.Process/Stream`; router returns ready-to-send candidate
  payloads with failover decisions; token extraction unified behind one
  extractor; SIGHUP reload wiring replaced by a `Reconfigurable`
  registry; config/key/gateway plumbing behind a gateway client.
  No behavior change except the fixes below.

### Fixed

- Cached Responses-API replies were re-served wrapped/unwrapped
  incorrectly (raw vs envelope mismatch) — cache now stores and returns
  the client-dialect shape.
- `max_tokens` is re-clamped after cross-kind translation, so an
  upstream limit can no longer be exceeded by a translated body.
- Anthropic non-streaming responses carrying `tool_use` blocks are
  translated correctly instead of dropping the tool call.

## [legacy 0.3.2] — 2026-08-22

### Added

- **Zero-config model handling (`forward_unknown`, default `true`).** A model
  absent from every provider's `models` whitelist is forwarded verbatim to
  available providers in tier order. A provider rejecting the model (400/404)
  fails over to the next provider (no same-provider retry for deterministic
  rejections); the last rejection is surfaced if all providers refuse.
  Set `forward_unknown: false` to restore strict whitelist behavior.
- **Provider prompt-cache read tokens are now captured on streaming requests**
  (`cached_tokens` / `cache_read_input_tokens` / `cachedContentTokenCount`)
  and surfaced in the token & cost ledger (`routre-cli list` →
  "cache read: N tokens (provider-reported)") and in `/metrics`.
- Sniffer fast path: usage regexes are skipped for stream frames that cannot
  contain token fields.

### Fixed

- SIGHUP config reload now applies edits to `forward_unknown` (previously the
  startup value was kept until restart).
- Streaming responses previously recorded zero cache-read tokens; they now
  feed the same ledger as non-streaming responses.

## [legacy 0.3.1] — 2026-08-21

### Fixed

- **`/v1/models` now returns the OpenAI-compatible format.** Each entry is an
  object with an `id` field (`{"data": [{"id": "provider/model"}, …]}`)
  instead of a bare string, as expected by Hermes and other OpenAI-compatible
  clients. Fixes model-verification warnings when adding models.

### Added

- **`stealth/ox-alpha` and `stealth/ox-alpha:free`** added to the OpenRouter
  provider in `config.all.json`, matching the gateway's served models list.

## [legacy 0.3.0] — 2026-08-20

### Added

- **Pure-Go BPE tokenizer.** `internal/tokenize` now counts tokens with the
  real byte-pair-encoding algorithm using an embedded (gzip-compressed,
  `go:embed`) `cl100k_base` vocab — replacing the old ≈4-bytes/token
  heuristic. The ledger fallback, the `max_tokens` clamp, and the bench gate
  now measure real BPE token counts. Still stdlib-only and offline. Falls
  back to the heuristic on a missing/corrupt vocab (fail-open). The heap-
  based merge runs in O(n log n), fast enough for the live clamp and the
  multi-hundred-KB bench payloads.
- **`scripts/gen-vocab.sh`** — fetches the vocab tables from the pinned
  source URLs, gzip-compresses them into `internal/tokenize/data/`, and
  records SHA-256 checksums for auditability.
- **Bench gate re-baselined to the real tokenizer.** Re-measured:
  aggregate tool reduction **91.2%**, per-payload worst **90.3%** (git-diff)
  — still ≥ the 90% gate, so the target is unchanged.
- **Gemini as a streaming dialect (OpenAI↔Gemini).** A `gemini`-kind
  provider is now served to OpenAI-dialect clients: `openAIToGemini`
  request translation, `geminiToOpenAI` non-streaming response translation
  (carrying Gemini's `usageMetadata`), and an in-flight `g2o` SSE state
  machine that turns `streamGenerateContent` into OpenAI
  `chat.completion.chunk` with `[DONE]` termination. The relay builds the
  per-model `/v1beta/models/:generateContent` path (`?alt=sse` for
  streaming). The Anthropic↔Gemini pair is not yet implemented and is
  rejected rather than mis-answered. `config.all.json` gains a `gemini`
  provider (506 models). Covered by unit tests + a Gemini mock upstream and
  non-streaming/streaming e2e relay tests.
- **Optional gateway auth (`auth.secret_env`).** Off by default (zero-config
  unchanged). When enabled, every `/v1/*` request must carry the shared
  secret in the configured header (default `X-Routre-Key`) or
  `Authorization: Bearer`; mismatches get a `401 invalid_api_key` with no
  upstream call, and `/healthz` + `/metrics` stay open. `serve` mints a
  per-process token (`~/.routre-cli/auth.tok`, 0600, regenerated each start)
  so `list`/`check`/`logs` authenticate without pasting the secret. The
  `setup` wizard offers to enable it and generates the secret into
  `routre-cli.env`.

## [legacy 0.2.0] — 2026-08-20

### Added

- **CI test + bench gate (`.github/workflows/ci.yml`).** fmt, vet, `go test`,
  `go test -race`, the 90% RTK bench gate, and fuzz smoke now run on every
  push and PR — the bench gate previously ran only locally.
- **`routre-cli list --json`.** Emits the providers/ledger/totals data as one
  JSON document for scripting; the table output is unchanged.
- **Fuzz targets** for the SSE frame parser and the RTK pipeline (no-panic /
  never-grow invariants).
- **Tests** for `internal/metrics`, `internal/reqlog`, `internal/mock`, and
  the `list`/`setup` commands.
- **Version single-sourcing.** `main.version` is injected via `-ldflags`
  (Makefile + npm build), read from the launcher `package.json` by the npm
  build — prevents the stale-version bug from recurring.
- **Documented `/metrics`** Prometheus endpoint in the README/SPEC.

### Changed

- Provider API keys now live in an in-memory keystore
  (`internal/keystore`) instead of being mutated into the process
  environment on 401/403 refresh — no torn-key state under concurrent
  auth failures.
- `config.Load`/`Reload` and the `relay`/`relayStream` upstream request
  construction were deduplicated behind shared helpers (no behavior change).

## [legacy 0.1.9] — 2026-08-20

### Added

- **OpenAI Responses API bridge (`/v1/responses`).** opencode's built-in
  `openai` provider speaks the Responses API, not `chat.completions`, so it
  could not use the gateway with plain `OPENAI_BASE_URL`. Inbound Responses
  requests are translated to `chat.completions` for relay, then wrapped back
  into the Responses envelope for non-streaming clients or re-emitted as the
  named Responses SSE events (`response.created`, `output_text.delta`,
  `response.completed`, …) for streaming. Only openai-kind upstreams can serve
  a Responses request; an anthropic upstream is rejected rather than silently
  mis-answered. Covered by `responses_test.go` / `responses_stream_test.go`.

## [legacy 0.1.8] — 2026-08-19

### Added

- **Anthropic prompt caching (`cache.prompt_cache`).** Opt-in injection of
  `cache_control {type:"ephemeral"}` breakpoints (system prefix + last
  message) into outbound `/v1/messages` bodies, so repeat agentic prefixes
  are billed at the cache-read rate (0.1x on Anthropic hits). Threaded
  through the whole relay (same-kind and cross-kind). Off by default
  because it rewrites the request body; strictly additive — an existing
  `cache_control` is never overwritten or stripped.
- **401/403 refresh-with-retry.** On an upstream auth failure the gateway
  re-reads the `routre-cli.env` key file; if the API key rotated, it retries
  the same provider once with the fresh key before failing over. Serialized
  so concurrent 401s from one rotation don't race the reload.
- **`Retry-After` honoring.** The gateway now parses an upstream
  `Retry-After` header (seconds or HTTP-date) from 429/5xx responses and
  uses it as a *floor* on the provider's cooldown — it never shortens the
  default exponential backoff. Previously only emitted to the client, never
  acted on upstream.

### Fixed

- **Published binary version string lacked the final version segment.**
  `npm/build.mjs` builds the binary with no version ldflag, so the embedded
  version came from `main.go` (0.1.6) even when the npm package was 0.1.7 —
  `routre-cli version` reported a stale version after install. All version
  strings now live in one place each and are aligned on the tag. (This also
  re-fixes the 0.1.4 regression.)

## [legacy 0.1.7] — 2026-08-18

### Added

- **Model auto-discovery.** Each provider's own `GET {base_url}/models` list
  is fetched at startup (and refreshed every 6h / on SIGHUP) and merged
  additively into its candidate set, so the hand-maintained `models` list in
  config is no longer required — leave it empty and the gateway discovers
  the provider's models itself. Explicit `models` are kept as the seed and
  never shadowed; unreachable providers fall back to their config (possibly
  empty) with a warning, and startup never fails over discovery.
- **`setup` wizard no longer forces a models list** — it now reads
  "leave empty to auto-discover".

### Changed

- **Faster relay: merged the JSON pipeline.** On the same-kind relay path,
  the model rewrite and `max_tokens` clamp now mutate a single already-
  decoded document and marshal once, instead of two separate
  decode→re-encode passes. When no rewrite/clamp applies, the body is
  passed through with zero re-encoding (previous fast path preserved).
  Byte-for-byte behavior, cache keys, and fail-open semantics unchanged.

## [legacy 0.1.6] — 2026-08-18

### Fixed

- **Streams could end without a `finish_reason` for some providers.**
  Upstreams like opencode.ai's `gpt-5.6-luna` sometimes closed a streaming
  response after content without ever sending a chunk carrying a real
  `finish_reason`; strict OpenAI clients then aborted with
  `Stream ended without finish_reason`. The same-kind OpenAI relay now
  guarantees a terminal `finish_reason` chunk is emitted before
  `[DONE]`/EOF, synthesizing one when the upstream omits it. Byte-for-byte
  passthrough when the upstream behaves correctly; the Anthropic path is
  unchanged.

## [legacy 0.1.5] — 2026-08-18

### Fixed

- **Provider-qualified model names (`<provider>/<model>`) were sent upstream
  verbatim.** When a client used a model like `opencode-go/gpt-5.6-luna`, the
  router matched the bare tail against the provider's model list but
  forwarded the *full prefixed string* upstream. opencode.ai rejected it
  (`401 Model opencode-go/gpt-5.6-luna is not supported`), which made the
  gateway fail over through every tier and return a misleading
  `all_providers_failed` 503 — even though the provider served the model
  fine. The prefix is now recognized as a client-side routing label and
  stripped before forwarding, so the upstream always receives the bare
  listed name.
  - Multi-segment upstream IDs are preserved: `openrouter/openai/gpt-5.6-luna`
    → `openai/gpt-5.6-luna` (strip only the first segment, never
    tail-after-last-slash). This matches how opencode itself resolves
    `provider/model`.
  - Cross-kind translation (OpenAI ↔ Anthropic) no longer re-sends the
    original prefixed model string; the candidate's upstream model is applied
    after translation.

## [legacy 0.1.4] — 2026-08-17

### Changed

- Corrected the binary-embedded version string so `routre-cli version`
  matches the installed npm version (0.1.3's published binary reported
  0.1.2). No functional change.

## [legacy 0.1.3] — 2026-08-17

### Fixed

- Cross-kind streaming translation (OpenAI ↔ Anthropic) supported for
  streaming requests (previously 501).
- Usage ledger autosave; transient 5xx retried once before failover; cache
  hits report upstream token usage.

## [legacy 0.1.2] — 2026-08-17

### Changed

- Scoped Windows packages to `@mariobgsp/*` (npm rejects unscoped `win32`
  names); publish rules updated.

## [legacy 0.1.1] — 2026-08-17

### Fixed

- Daemon start/stop/restart lifecycle; gateway 503 fixes.

## [legacy 0.1.0] — 2026-08-17

Initial release: single Go binary (no runtime deps) providing an
OpenAI/Anthropic-compatible localhost gateway with provider failover, RTK
token compression, response caching, and a per-agent token/cost ledger.
