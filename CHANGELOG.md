# Changelog

All notable changes to routre-cli are documented here, newest first.
Releases are published to npm via a `v*` tag push (see
[`.github/workflows/publish.yml`](.github/workflows/publish.yml)).

The format loosely follows [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
