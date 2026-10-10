# Roadmap

## Next release (after v0.7.0) — zero-edit, honest gateway

v0.7.0 shipped on 2026-09-24 (serve owns its model catalog). Everything below
is the release after it.

**Done when:** after `curl install.sh`, `routre list` is green — every configured pi model routed and healthy — with zero manual edits to `config.json` or `models.json`. The e2e check reproduces this against `cmd/mock-upstream` and leaves a repeatable artifact.

### AX — pi contract

- routre stays a plain OpenAI/Anthropic surface; no pi-specific code, no pi extension tool.
- Failures pass through with the real upstream status and message, plus routre headers: `cooldown_seconds`, `Retry-After`, named model. No new error envelope.
- Kills the 500-hijack class at the root: routre config owns the model list, so pi's `models.json` can no longer disagree with it.

### Config truth and auto-wire

- routre config is the source of truth for the model list; pi's `models.json` is generated from it.
- Installer discovers keys (opencode `auth.json`, provider env), writes config, wires `models.json`, starts the daemon, prints the green list.
- Auto-wire safety: backup, atomic write, printed diff, one-line restore.

### UX

- Dashboard is a read-only status page. CLI (`list`, `logs`) is the primary human and agent surface.
- No TUI installer; curl-pipe install stays non-interactive.

### DX

- `make check`: fmt, vet, `-race` tests, and the mock-upstream e2e proving install → green list.
- 10-line CONTRIBUTING for outside PRs.

### Open

- Whether unmerged branch work lands in v0.7.0 or it starts clean after `59de974`.
- README: a proper roadmap section, or another "Latest" block.

### Skipped (add when measured)

- TUI installer, pi extension tool, dashboard editing, bench gate in `make check`.
