# tests

This folder holds integration / e2e tests for `routre`.

## Layout

- **Unit tests** stay colocated with their packages (`*_test.go` next to `*.go`) — this is idiomatic Go and lets tests access unexported helpers without exporting them.
  - `list_test.go`, `setup_test.go`, `start_test.go`, `stop_test.go` at the repo root test the `main` package's CLI helpers.
  - `internal/*/*_test.go` test each internal package.

- **Integration tests** (binary-level, cross-package) live here in `tests/`:
  - `tests/integration_test.go` — builds the `routre` binary and exercises `routre check`, `routre list`, `routre bench`, and the `/healthz` + `/ui` endpoints.

Run everything:

```bash
go test ./...
go test ./tests -v   # integration only
```

## Why not move all `_test.go` into `tests/`?

Moving `package main` tests (e.g. `start_test.go`) into `tests/` would put them in a different package directory, so they could no longer access unexported functions like `startManaged` or `runCommand` without exporting them. Keeping unit tests next to their source is the standard Go layout and keeps the diff small.
