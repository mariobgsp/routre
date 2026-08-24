# PLAN: Replace npm distribution with curl installer + `routre-cli update`

> Status: RESEARCH COMPLETE — pending owner decisions (see bottom).

## Context

routre-cli ships today as 7 npm packages (launcher shim + 6 platform
binaries). Pain points observed during the 0.3.2 release:

- Launcher `optionalDependencies` had to be manually synced; a stale pin
  shipped old binaries until fixed (#28). Even now, published 0.3.1 launchers
  pin 0.3.0 binaries for end users.
- Release = bump `npm/routre-cli/package.json` → tag → build locally → run
  publish script in strict order (platforms first, launcher last).
- Non-node users must install npm just to get a static Go binary.

Owner wants: **curl one-liner install**, built-in **`routre-cli update`**, and
an effort estimate for the migration.

## Effort estimate (research-converged)

**Total: 12–28 h ≈ 2–4 dev-days** (external researcher triangulated 3–5 days
incl. cross-platform QA). Both researchers independently landed the same shape:

| Phase | Work | Hours |
| --- | --- | --- |
| 0 — Prep | Release workflow (build 6 targets on tag push → GitHub Release assets + `checksums.txt`); asset naming `{cmd}_{GOOS}_{GOARCH}.tar.gz` (+`.zip` win); `install.sh` (~150 lines POSIX sh: uname detect → download `releases/latest/download/…` redirect URL → sha256 verify → install prefix → PATH hint); README section | 4–8 h |
| 1 — Cutover | Final npm version whose postinstall/shim prints "moved to: `curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre-cli/main/install.sh \| sh`"; `npm deprecate` old versions; keep packages installed (never unpublish) | 2–4 h |
| 2 — Self-update | `update` subcommand: fetch latest release, semver-compare vs ldflags-stamped version, download asset, verify sha256, atomic replace (rename-swap; Windows `.old` dance), rollback on failure; refuse when npm-managed (deno PR #19910 pattern) | 6–16 h |

## Approach

1. New tag-gated workflow `.github/workflows/release.yml` reusing the
   cross-compile matrix conceptually (GoReleaser **or** port of `build.mjs`
   logic into Actions with `softprops/action-gh-release@v2`). Emits per-platform
   tarballs + `checksums.txt`.
2. `install.sh` modeled on golangci-lint/deno/uv templates. Never calls
   `api.github.com` (60 req/h/IP limit) — uses the
   `releases/latest/download/<asset>` 302-redirect URLs instead.
3. `routre-cli update` implementation — **DECIDED: zero-dep stdlib updater**
   (~150–200 lines; keeps the 27-byte go.mod). Fetches the
   `releases/latest/download` redirect URL, verifies sha256 against
   `checksums.txt`, atomic rename-swap with rollback on failure.
4. npm becomes a deprecation stub: shim prints the curl command and exits
   non-zero; `npm deprecate` set; registry packages stay installed (hard
   removal breaks pinned dependents with E404 — cited precedent: esbuild
   #1647, Atlas CLI FAQ). Revisit unpublishing after ~3 months of stats.
5. **DECIDED: Windows self-update deferred** — `routre-cli update` on Windows
   prints "re-run install.ps1" and exits; win32 binaries still published for
   manual install. Rename-swap support can be added later.

## Files to modify

- NEW `install.sh`, NEW `.github/workflows/release.yml`,
  NEW `internal/update/update.go` (+ `_test.go`)
- `main.go` — add `update` case to `run()` switch (~line 100)
- `Makefile` — add `dist-release` target alongside `dist-npm`
- `README.md`, `CHANGELOG.md`, `config.example.json` untouched
- Deprecate-only (files stay, behavior changes): `npm/routre-cli/bin/routre-cli.mjs`
- Cleanup at implementation time: delete stray `research.md` written by researchers

## Reuse (verified in-repo)

- Cross-compile matrix + ldflags stamping: `npm/build.mjs:26-33,103-110`
  (`-X main.version=${version}`, 6 GOOS/GOARCH targets)
- HTTP client pattern w/ sane timeouts: `internal/proxy/handlers.go`
  `newHTTPClient()` (dial/TLS/header timeouts worth copying into updater)
- Subcommand wiring + test patterns: `main.go run()` switch;
  `setup_test.go` / `start_test.go` style for `update_test.go`
- Version plumbing already exists (`var version` + `version` subcommand);
  updater compares against it
- Windows process quirks already handled somewhere in-repo:
  `detach_windows.go`

## Steps

- [ ] Phase 0: `release.yml` + asset naming + `checksums.txt`; test with a
      pre-release tag on a scratch repo or draft release
- [ ] Phase 0: `install.sh` + shellcheck + manual Linux/macOS runs; PATH hint
      when prefix not on PATH; never sudo
- [ ] Phase 1: npm deprecation shim + `npm deprecate` + README rewrite
- [ ] Phase 2: zero-dep `internal/update` — latest-release redirect fetch,
      semver compare vs ldflags version, sha256 verify, rename-swap replace;
      `--check` dry-run flag; Windows → print re-install hint + exit;
      refuse-on-npm-managed detection (`which` prefix check)
- [ ] Phase 2: unit tests mirroring `*_test.go` patterns (fake server serving
      release JSON + asset, version compare table, checksum-mismatch case,
      atomic-replace on unix)
- [ ] Docs + CHANGELOG; delete stray `research.md`

## Verification

- Fresh-machine install: `curl … | sh` on Linux x64/arm64 (WSL ok), macOS
- `routre-cli update` happy path: fake older version → updates → new version prints
- Failure paths: checksum mismatch aborts; network error leaves binary intact;
  npm-managed install refuses with instructions
- CI stays green (`gofmt/vet/test/race/bench/fuzz` gates unchanged)
- One full dress rehearsal: cut v0.3.3 through the new pipeline end-to-end

## Risks (top 5, from research)

1. Asset-name drift breaks installer/updater → CI smoke-test downloads per release
2. api.github.com rate limits → use redirect URLs only (already in approach)
3. Windows running-exe replace → **deferred by decision**; win32 users get a
   clear re-install message from `update`
4. Stranded npm users → stub prints instructions, exits nonzero, never wraps stale binary
5. PATH/prefix shadowing (exactly what bit us locally: `~/.local/bin` vs
   `~/.npm-global/bin`) → installer warns; `update` warns on unexpected prefix

## Gaps / assumptions

- No stopwatch writeup exists for this exact migration; hours are triangulated
- go-selfupdate binary-size cost unmeasured (relevant to Q1 below)
- Scout subagent crashed before reporting; local-context facts above are
  first-hand from this session's work on the same files

## Owner decisions (ANSWERED)

1. Updater implementation → **zero-dep stdlib** (no new deps in go.mod)
2. Windows self-update → **deferred** (prints re-install hint; binaries still shipped)
3. Install prefix default → **`~/.local/bin`**, no sudo ever, PATH warning printed
