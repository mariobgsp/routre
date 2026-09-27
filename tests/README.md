# tests — binary-level e2e suite

This folder holds `routre`'s **end-to-end** tests. Unlike the unit tests under
`internal/`, these drive the real `routre` binary as a child process: they build
it, write a config, boot `routre serve` against mock upstreams, and talk to it
over HTTP exactly like a coding agent would. Flag parsing, config loading, the
bind address, the HTTP surface, and process shutdown are all real here — not
simulated.

## Layout

- `harness_test.go` — builds the binary once, boots `routre serve` on a random
  port against `internal/mock` upstreams, isolates `ROUTRE_CLI_DATA_DIR` into a
  temp dir, and provides HTTP/CLI helpers.
- `e2e_test.go` — the scenarios: health, models, chat, streaming, `/v1/messages`,
  `/v1/responses`, failover, cache, auth, `/ui`, `/metrics`, `notfound`, the
  usage ledger, and the `list`/`check`/`version` CLI subcommands.

Run:

```bash
go test ./tests -v   # e2e only
go test ./...        # everything (unit + e2e)
```

## What is intentionally NOT here

Unit tests stay colocated with their packages (`*_test.go` next to `*.go`).
These test things with no meaningful e2e equivalent — an e2e test of a BPE token
count or a keystore crypto round-trip is slower *and* less precise:

- `internal/tokenize/` — exact BPE token counts
- `internal/rtk/` — compression ratio (also a CI gate via `routre bench`)
- `internal/keystore/` — AES-GCM round-trip / tamper handling
- `internal/proxy/dialect/` — JSON translation shapes
- the **fuzz** targets (`FuzzSSEFrame`, `FuzzRTKApply`) and **benchmarks** —
  both are CI gates and cannot be expressed as e2e cases

`internal/proxy/` and `internal/router/` keep unit tests for internal
invariants the e2e surface cannot reach (failover budget slices, first-byte
watchdog races, envelope key derivation). Request-lifecycle cases that the e2e
now covers (health, models, status, metrics, cache hit) live in `tests/`.
