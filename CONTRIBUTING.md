# Build

Run `make build` to build the local binary.

# Format

Run `gofmt -w .` before committing.

# Tests

Run `go test ./...` for the fast test suite.

# Race tests

Run `go test -race ./...` before release.

# E2E

Run `make e2e` for the mock installer and gateway flow.

# Checks

`make check` is the required pre-merge gate.

# Pull requests

Keep changes focused and include tests for behavior changes.
