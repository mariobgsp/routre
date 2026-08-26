# Changelog

All notable changes to routre-cli are documented here, newest first.
Releases are version-tagged (`v*`); the release workflow attaches
per-platform binaries to the GitHub Release. CI
(`.github/workflows/ci.yml`) runs tests on every push and PR.

The format loosely follows [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [0.4.0] — 2026-08-26

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

## [0.3.2] — 2026-08-22

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

## [0.3.1] — 2026-08-21

### Fixed

- **`/v1/models` now returns the OpenAI-compatible format.** Each entry is an
  object with an `id` field (`{"data": [{"id": "provider/model"}, …]}`)
  instead of a bare string, as expected by Hermes and other OpenAI-compatible
  clients. Fixes model-verification warnings when adding models.

### Added

- **`stealth/ox-alpha` and `stealth/ox-alpha:free`** added to the OpenRouter
  provider in `config.all.json`, matching the gateway's served models list.

## [0.3.0] — 2026-08-20

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

## [0.2.0] — 2026-08-20

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

## [0.1.9] — 2026-08-20

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

## [0.1.8] — 2026-08-19

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

## [0.1.7] — 2026-08-18

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

## [0.1.6] — 2026-08-18

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

## [0.1.5] — 2026-08-18

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

## [0.1.4] — 2026-08-17

### Changed

- Corrected the binary-embedded version string so `routre-cli version`
  matches the installed npm version (0.1.3's published binary reported
  0.1.2). No functional change.

## [0.1.3] — 2026-08-17

### Fixed

- Cross-kind streaming translation (OpenAI ↔ Anthropic) supported for
  streaming requests (previously 501).
- Usage ledger autosave; transient 5xx retried once before failover; cache
  hits report upstream token usage.

## [0.1.2] — 2026-08-17

### Changed

- Scoped Windows packages to `@mariobgsp/*` (npm rejects unscoped `win32`
  names); publish rules updated.

## [0.1.1] — 2026-08-17

### Fixed

- Daemon start/stop/restart lifecycle; gateway 503 fixes.

## [0.1.0] — 2026-08-17

Initial release: single Go binary (no runtime deps) providing an
OpenAI/Anthropic-compatible localhost gateway with provider failover, RTK
token compression, response caching, and a per-agent token/cost ledger.
