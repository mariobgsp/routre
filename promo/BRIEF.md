# routre promo video — brainstorm brief

Status: **direction locked, build started.** Decisions below (2026-09-30), rest is
the brainstorm record they were made from.

## 0. Locked decisions

| Decision | Choice |
| --- | --- |
| **Creative direction** | **A+B hybrid** — Blueprint visual language (§6 A), The Bill as the opening hook (§6 B) |
| **Audio** | **No VO.** Burned-in captions + SFX + music bed. Silent-first |
| **Build order** | Remotion skeleton + end card first, then scenes |
| Open (§18) | Direction ✅ · Audio ✅ · First deliverable ✅ · Cuts: 30 s hero + 15 s vertical · EN only · Money claim: bench-gated % only, no invented bills · Mascot: not in the hero cut |

### What A+B hybrid means concretely

- **0–5 s is The Bill**: one screen, one agent session, a token counter climbing.
  Two copies of the counter, two bills. Cut on the reveal. This is the ad hook.
- **5 s onward is Blueprint**: the surviving half of the split is re-drawn as a
  navy schematic — the orange thread through `routre` into a provider tier list.
  Everything after this is technical, quiet, and on-brand.
- The Bill is **never** restated later. Its numbers are re-earned in the Blueprint
  stats beat (19.5–24 s) so the ad hook and the proof land on the same figures.
- No dropped shadows, no gradients-with-perspective, no camera moves. The only
  "loud" moment in the whole cut is the counter.

---

## 1. TL;DR

The thing we are selling is not "a Go proxy". It is:

> **You point your AI tools at one address, and the bill stops leaking.**

Everything in the video should serve that sentence. Three proof beats back it:
**90% fewer tokens** · **never gets blocked** · **10 MiB of RAM you never notice**.

Recommended first build: **a 30 s hero cut (16:9) + a 15 s vertical bumper**,
produced in [Remotion](https://remotion.dev) so text/numbers stay editable and every
platform cut is a re-render, not a re-edit.

---

## 2. What we are actually selling

**One line:** routre is a tiny helper on your computer that sits between your AI apps
and AI providers.

**Full 30-word pitch:** It remembers answers, shrinks tool output before it's billed,
and switches providers when one is busy — so your agent never fails and never overpays.

**Who it's for** (ranked by how well the video converts them):

| Audience | Their pain | The beat that lands |
| --- | --- | --- |
| Coding-agent users (opencode, Claude Code, Codex, Cursor) | Free-tier 429s, tabs open, model configs rot | **Stays online** — failover on overload |
| Anyone watching their token bill climb | Repeated tool output, re-reads | **Saves money** — RTK + cache, measured |
| Self-hosters / infra people | LiteLLM = Python + Postgres + Redis | **10 MiB, one static binary** |
| Non-terminal users | "another YAML file" | `routre setup` + dashboard at `/ui` |

**Anti-audience (do not speak to):** enterprise LLM gateway buyers, teams wanting
central routing/observability, model fine-tuners. The video should feel like a
single-engineer power strip, not a platform.

---

## 3. Message hierarchy

Every cut uses the same spine; only the length changes.

1. **Hook (0–4 s)** — the bill or the error. "Again?" Not "Introducing…".
2. **Reveal (4–9 s)** — one address: `http://127.0.0.1:20128`.
3. **Mechanism (9–22 s)** — three powers, one per beat: compress → cache → failover.
4. **Proof (22–27 s)** — the numbers, on screen, big, bench-backed.
5. **CTA (27–30 s)** — the one-line install + repo URL. Say it twice, show it once.

Copy rules:
- Never more than 6 words on screen at once.
- No feature list longer than 3 items. No "and also…".
- No "revolutionary", "seamless", "powerful", "enterprise-grade".
- Show the command, don't describe it.

---

## 4. Proof bank (the numbers we're allowed to put on screen)

All from `README.md` / `docs/HOW-IT-WORKS.md`. Sourced, repeatable with
`make build test bench`.

| Claim | Value | How it's earned |
| --- | --- | --- |
| Tool-output tokens removed | **91.2 %** aggregate, 90.3 % worst payload | `routre bench`, gates the build at ≥90 % |
| Whole-request body tokens removed | **91.3 %** | same bench |
| Idle RAM | **10 MiB** (12.9 MiB peak under 3 live opencode sessions) | `scripts/measure-ram.sh` |
| Binary | **10.9 MiB**, static, CGO off | `make build`; captured |
| Added latency | **~26 ms p50 / ~28 ms p99** on a 1 MiB body | `GatewayAddedLatency1MB` bench |
| Models behind one endpoint | **506** | `config.all.json` |
| RTK filters | **11**, heuristic, no LM, fail-open | `internal/rtk/` |
| Cache | exact-match LRU, 512 entries / 1 h (4096 / 24 h in `config.all.json`) | `internal/cache/` |
| Platforms | 6 (linux/darwin/windows × arch) | release workflow |
| Licence | MIT | `LICENSE` |
| Test surface | 23 binary-level e2e scenarios driving real `routre serve` | `tests/` |

### ⚠ Number conflicts — **resolved 2026-09-30** (verified against source)

Both conflicts were checked against the code. In both cases the site was wrong
and the doc was right, so the video follows the doc.

| Claim | `docs/HOW-IT-WORKS.md` | `docs/index.html` | Verdict | Use in video |
| --- | --- | --- | --- | --- |
| Cooldown backoff cap | `2s → 5m` | `2s → 30m` | **doc is correct** | `2s → 5m` |
| Proxy overhead | ~26 ms p50 (1 MiB) | "~11 ms per request" | **doc is correct** | `~26 ms p50` |

**Cooldown evidence.** `internal/router/classify.go:91-93` is the single source of
truth: `Base: 2 * time.Second, Max: 5 * time.Minute`. Growth is exponential ×2
(`router.go:146-156`), saturating at the 9th failure. The `30min` figure has no
constant behind it — it comes from a stale package comment at
`internal/router/router.go:6-9` that was never updated when the cap changed, and
the author explicitly rejected a 30 m cap in `classify.go:89` ("a 30min cap turned
a 60-second blip into a half-hour outage"). Likely origin of the site's number:
the separate 30 s `Retry-After` clamp at `router.go:122-140`, same "cap at 30"
shape, wrong unit.

**Latency evidence.** `internal/proxy/bench_test.go:22-34` is where the doc's
figures come from, and they are a comment quoting a real measurement:
`p50 ~17.7 ms` with RTK+cache off, `RTK +6.5 ms`, `cache +1.6 ms` →
**p50 ~25.6 ms / p99 ~28.3 ms** in the shipping configuration. It is a raw
wall-clock around `client.Do` + body drain against a mock whose own service time
is ~0.1 ms — nothing is subtracted, and both numbers measure the same thing.
There is no second, smaller benchmark anywhere. The only `11`-ish claim in the
repo is `CHANGELOG.md:354`, a v0.3.5 *aspiration* for typical (non-1 MiB)
requests. The site's `~11 ms` was introduced by commit `20109fc7` with no bench,
test, script or changelog entry behind it.

### ⚠ Two doc bugs found while verifying — worth fixing in the repo

1. **`docs/index.html:2504`** says "cooldown 2s→30m"; should be `2s→5m`.
2. **`internal/router/router.go:8`** — the stale "capped at 30min" package comment
   is what keeps regenerating the wrong number. Fix it at the source.
3. **`docs/HOW-IT-WORKS.md:44-47`** implies `/metrics` exposes latency. It does
   not — the Prometheus surface is counters and gauges only
   (`internal/metrics/metrics.go:190-267`). Per-phase latency lives in the JSONL
   request log only: `latency_ms`, `dial_ms`, `headers_ms`, `ttfb_ms`, `total_ms`
   (`internal/reqlog/reqlog.go:37-41`, written in `internal/proxy/chat.go:62-69`).
   **Consequence for the video:** the ledger beat cannot show latency on a
   `/metrics` panel. If latency goes on screen it has to come from a real JSONL
   line — and note `request_log` defaults to `""`, which writes to stderr instead
   of a file (`internal/config/config.go:104-105`).

### ⚠ The headline stat — `91.5 %` is stale, and there are three different ratios

**`README.md:140` and `docs/HOW-IT-WORKS.md:329` both say `91.5 %`. The bench does
not print that.** Captured 2026-09-30 at `develop@87a775e`
([`captures/bench.txt`](captures/bench.txt), deterministic across repeated runs):

```
aggregate payload reduction :  91.3%  (118574 → 9547 est. tokens)
aggregate TOOL reduction   :  91.2%  (106150 → 8677 est. tokens)  [RTK guarantee]
per-payload worst           :  90.3% (git-diff.json)
target gate                :  90.0% on tool reduction (aggregate AND per-payload)
result: PASS
```

So there are **three** numbers in play and only two of them are real:

| Figure | What it is | Use it? |
| --- | --- | --- |
| `91.5 %` | what the README and HOW-IT-WORKS claim; matches nothing the bench prints | **no** — it is stale |
| `91.2 %` | the bench's printed aggregate: the **arithmetic mean of the 5 per-payload percentages** (`bench.go:110-111`), verified by hand (90.8+90.3+92.8+92.0+90.3)/5 = 91.24 | yes, **labelled as a mean** |
| `91.8 %` | the **pooled token ratio** the same line prints in parentheses: 106150 → 8677 est. tokens | yes — this is the most honest "fewer tokens" framing, because it is a real token count |

Note also that the docs' "worst per-payload 90.3 % (tree-ls.json)" now belongs to
**git-diff.json**; tree-ls is also 90.3 %, but it is not the file the tool names.

**Wording rules for the stats beat** — all supported verbatim by the capture:

- `91.2 % tool tokens` + footnote `mean of 5 tool-heavy payloads`
- or the raw token evidence, which needs no footnote at all:
  `106,150 → 8,677 tool tokens`
- `90.3 % worst payload` is safe on its own, and `gate: 90 % · result: PASS` too

Never: "91.5 %", "91.5 % off your bill", "91.5 % of tokens saved", or any single
number presented as a whole-session ratio. Output tokens are never compressed, so
real-session savings depend on the tool-traffic mix — the project's own `Known
gaps` section says so, and the brief's audience #1 will check.

**Follow-up for the repo:** `README.md:140` and `docs/HOW-IT-WORKS.md:329-330`
should be corrected to the captured values.

---

## 5. Formats & deliverables

| Cut | Length | Aspect | Where it runs | Priority |
| --- | --- | --- | --- | --- |
| Hero | 30 s | 16:9 1920×1080 | README, landing page, YouTube (Shorts link) | **P0** |
| Bumper | 15 s | 9:16 1080×1920 | X, LinkedIn, Instagram, Discord | **P0** |
| Extended | 90–120 s | 16:9 | YouTube, HN comment, launch post | P1 |
| Silent loop | 8 s | 16:9 | landing page hero, muted autoplay | P1 |
| Captions | `.srt` + `.vtt` for all of the above | — | accessibility | P0 |

Same source composition renders all cuts (Remotion `<Sequence durationInFrames>`
per cut, shared components). Audio: one music bed, ducked under VO, plus 3 UI sounds
(`pip` on install, `thunk` on failover, `tick` on cache hit).

---

## 6. Three creative directions

### A — "Blueprint" *(recommended: matches the existing site)*

Terminal-as-blueprint. Navy void `#12101f`, hairline grids, monospace labels, one
orange accent, VT323/IBM-Plex-Mono typography, a slow scanline sweep. A request is a
packet drawing a line through a schematic; providers are nodes that light up or dim.

- **Feel:** engineered, quiet, precise. Zero stock-footage energy.
- **Why it wins:** `docs/index.html` already looks like this — the video is an
  extension of the brand, not a new one. Assets are code (SVG/Remotion), not footage.
- **Risk:** could feel static. Fix: animate the pipeline steps for real
  (7 steps, ~1 s each is watchable if they're literal labels).
- **Music:** low drone + tick percussion, no drop.

### B — "The bill"

Split screen. Left: an agent session burning tokens, a counter climbing. Right: the
same session through routre, counter flat. Numbers are the star; the gateway is the
quiet hero.

- **Feel:** punchy, ad-like, immediately legible to a non-technical viewer.
- **Why it wins:** best 15 s cut of the three. Perfect for X/LinkedIn.
- **Risk:** needs believable money numbers; must be labelled honestly
  ("measured on a tool-heavy session, not a guarantee").
- **Music:** percussive, two tempos that collide at the reveal.

### C — "Hamster in the machine"

The mascot (`docs/icon/routre-hamster.jpeg`) is the router. It runs on a wheel,
carries payloads, drops 90 % of them before the toll booth (the provider), and
sleeps in a 10 MiB hamster ball.

- **Feel:** friendly, meme-able, shareable. Instantly ownable.
- **Why it wins:** most likely to be clipped/reposted by other people; a mascot is
  the only asset here that ages well in an algorithm feed.
- **Risk:** can read unserious for infra-minded viewers → keep the mascot *silent*,
  as a motion-design element, never as a speaking character.
- **Music:** chiptune-ish, playful.

**Recommendation (locked):** **A+B hybrid.** A is the body of the hero cut, B is
the first 5 seconds and the whole 15 s vertical. C stays parked for a mascot loop on
the landing page.

---

## 7. Beat sheet — 30 s hero cut (direction A)

Times in seconds @ 30 fps.

| # | Time | Visual | On-screen text | VO / audio |
| --- | --- | --- | --- | --- |
| 1 | 0.0–3.5 | Terminal, a tool result dumps 40 000 chars of `git diff`/`ls`; a red `429 overloaded` flashes and the cursor dies | `your agent is burning tokens` | *"Same loop you ran yesterday."* |
| 2 | 3.5–6.0 | Screen splits; a single orange line threads from the app to a small node labelled `routre` | `one address: 127.0.0.1:20128` | *"Point it at routre. Once."* |
| 3 | 6.0–10.5 | **Compress** — the captured `routre bench` table rows in one by one, each tool% bar filling; then `106 150 → 8 677 est. tokens` | `1. compress · 11 filters, no LM`<br/>footnote `routre bench · gate 90% · PASS` | *"Tool output gets shrunk before it's billed."* |
| 4 | 10.5–14.5 | **Cache** — identical request returns instantly from an LRU ring, `X-Llrouter-Cache: hit` | `2. cache · sha256 LRU` | *"Ask twice, pay once."* |
| 5 | 14.5–19.5 | **Failover** — the red 429 node dims into cooldown, traffic slides to the next node; output keeps streaming | `3. failover · tiers + cooldown 2s→5m` | *"One provider is busy? The next one answers."* |
| 6 | 19.5–24.0 | Three cards: `91.2 %` · `12.6 MiB` · `~26 ms`, each with its qualifier underneath and a provenance pill naming the command that produced it | `91.2% tool tokens · 12.6 MiB · ~26 ms`<br/>qualifiers `mean of 5 payloads · PASS` / `30s sample · 0 MiB growth` / `p99 ~28 ms` | *"Measured, not promised."* |
| 7 | 24.0–27.0 | `routre list` output: tokens, cache hits, estimated cost, green LEDs | `see it in routre list` | *"See every token, every provider."* |
| 8 | 27.0–30.0 | Logo/hard end-card: install command types itself, URL | `curl -fsSL …/install.sh \| sh` + `github.com/mariobgsp/routre` | *"One binary. Ten megabytes. Go."* |

Total beats: 8. If the cut runs long, **drop beat 7** before touching 3/4/5.

---

## 8. Beat sheet — 90 s extended (adds depth, same spine)

| # | Time | Adds |
| --- | --- | --- |
| 0 | 0:00–0:10 | Cold open: a real failed session, red terminal, `providers_unavailable` |
| 1 | 0:10–0:18 | Install: `curl … \| sh` → `routre setup` → two env vars. ~11 ms overlay |
| 2 | 0:18–0:30 | The 7-step pipeline animates literally: detect → RTK → cache → route → retry → translate → relay |
| 3 | 0:30–0:45 | Cross-dialect: an Anthropic client streaming from an OpenAI provider, SSE frames translating live |
| 4 | 0:45–0:58 | Ledger: `~/.routre/usage.json` bars per agent; `/metrics` Prometheus panel |
| 5 | 0:58–1:10 | Honest engineering beat: "the 90 % is a **build gate**. Regress and the build fails." |
| 6 | 1:10–1:24 | One binary vs the alternatives table: 10 MiB vs 80 MiB vs GBs |
| 7 | 1:24–1:30 | End-card + CTA |

---

## 9. 15 s vertical bumper (direction B — split screen)

| Time | Picture | Text |
| --- | --- | --- |
| 0–3 | Top: agent session, counter `+18 400 tok`. Bottom: same session via routre, counter `+1 190 tok` | `same session. two bills.` |
| 3–7 | The orange line re-routes through `routre`; red `429` node dims, next node lights | `auto failover` |
| 7–11 | Stat stack slams in, qualifier under each | `91.2% · 12.6 MiB · ~26 ms` |
| 11–15 | End-card, install command + URL, hamster blink | `stop paying the token tax` |

---

## 10. Voiceover script (draft, ~150 wpm)

**30 s** (~75 words):

> Same loop. Same tools. Same bill.
> Point your agent at routre — one local address.
> Tool output shrinks before it's billed. Identical answers come from cache, free.
> And when a provider is overloaded, the next one answers before you notice.
> Ninety-one point five percent fewer tokens. Ten megabytes of RAM. One static binary.
> Stop paying the token tax.

**15 s** (~38 words):

> Same session. Two different bills.
> Routre compresses what you send, caches what you repeat, and switches providers
> when one gets busy. Ten megabytes. One install command.

**90 s** — same voice, expanded from the beat sheet; ~215 words. Write it only after
the 30 s is locked.

**Delivery notes:** calm, engineer-to-engineer, no hype inflection. No upward
ending — the CTA should feel like an instruction, not a sales close. If we can't get
good VO, ship it caption-first with sound design only (the terminal SFX carry it).

---

## 11. On-screen text & accessibility

- Burned-in captions for every VO line (styled like the site's mono labels), plus
  real `.srt`/`.vtt` sidecars.
- Minimum 44 px body text at 1080p; numbers ≥ 120 px.
- Contrast ≥ 4.5:1 on the navy `#12101f` background.
- Never convey meaning by colour alone (cooldown node = red **and** a `cool 2s` label).
- Every command on screen must be paste-accurate (this is a dev audience; a wrong
  flag is the first comment).

---

## 12. Visual language

| Token | Value | Source |
| --- | --- | --- |
| Background | `#12101f` / `#1b1830` / `#0c0a18` | `docs/index.html` |
| Ink | `#f0e6d2` | site |
| Steel | `#c9b896` | site |
| Accent | orange (site `--accent`) | site |
| Display font | VT323 or Press Start 2P (sparingly) | site |
| UI mono | IBM Plex Mono / JetBrains Mono | — |
| Motifs | hairline grid, corner brackets, LED dots, scanline sweep, sheet labels (`LVL 02`) | site |

Motion rules: 30 fps, ease-out entrances, nothing bounces, cuts on the beat, camera
never moves more than 4 % of frame. Text animates by typing or by counting up —
never by fading between two random fonts.

Sound: 3 UI sounds, one low drone bed, ducking under VO, no stingers after the CTA.

---

## 13. Asset inventory (what we must capture before animating)

| Asset | How | Notes |
| --- | --- | --- |
| Terminal sessions | `vhs` / `asciinema` recordings of real `routre` commands | `setup`, `serve`, `list`, `doctor`, `logs -errors`, `bench` |
| Bench output | `make bench` → `promo/captures/bench.txt` | source of the 91.2 % callout |
| RAM measurement | `./scripts/measure-ram.sh` | source of the 10 MiB callout |
| Dashboard | headless Chrome on `http://127.0.0.1:20128/ui` | for the ledger beat |
| `/metrics` | curl against a running gateway | Prometheus panel |
| Architecture art | `docs/architecture.png`, `request-lifecycle.png` | already exist — animate over them |
| Logo / mascot | `docs/icon/routre-hamster-256.jpeg` (+ full-res) | needs a clean vector for motion |
| Fonts | Google Fonts (VT323, Press Start 2P, IBM Plex Mono) | download locally for deterministic renders |

**Golden rule:** every number that appears on screen is copied from a captured
artifact, not typed by hand. If a number can't be reproduced today, it doesn't go
in the cut.

---

## 14. Toolchain plan

**Remotion** (React, deterministic frame rendering) is the default: it makes the
numbers editable props, the cuts reusable sequences, and the output reproducible
from `npm run render` without a GUI or a video editor.

```
promo/
├── BRIEF.md              ← this file
├── remotion/
│   ├── src/
│   │   ├── Root.tsx          compositions: Hero30, Bumper15, Extended90, Loop8
│   │   ├── theme.ts          palette + type tokens from §12
│   │   ├── scenes/           Terminal, Reveal, Compress, Cache, Failover, Stats, EndCard
│   │   ├── assets/           captures, logo, fonts (licensed/checked)
│   │   └── audio/            VO + SFX + music bed
│   └── package.json
└── out/                  rendered mp4s (gitignored)
```

Fallbacks if Remotion is unwanted: FFmpeg/ffmpeg-only "one HTML → frame" pipeline,
or plain SVG/CSS keyframes captured with headless Chromium. Don't hand-edit in a
NLE — text changes must be a one-line diff.

---

## 15. Claims guardrails

Safe to say: everything in §4, plus "loopback only", "keys stay local in a `0600`
env file", "the gateway holds your keys and injects them upstream", "MIT", "one
binary, six platforms", "`routre bench` fails the build under 90 %", and the
corrected `2s → 5m` cooldown.

Do **not** say / show:

- Any money saved figure that isn't reproducible from `routre list` on a real run.
- "Never fails" / "100 % uptime" — the honest failure modes are documented in
  `docs/HOW-IT-WORKS.md#known-gaps` (`providers_unavailable`, `all_providers_failed`).
- Enterprise routing/SSO/team features — routre is loopback, single-user.
- The non-streaming `openai→anthropic` translation gap is currently **fixed in the
  code** per CHANGELOG 0.7.2 notes but tracked separately — don't show that direction
  at all.
- Windows self-update (deferred); Windows installs from the zip.

---

## 16. Distribution

| Surface | Asset | Note |
| --- | --- | --- |
| GitHub README | Hero 30 s, muted-autoplay `<video>` loop poster | first impression for `curl` visitors |
| `docs/index.html` | Silent 8 s loop in the hero frame | matches existing HUD row |
| X / LinkedIn / Instagram | Bumper 15 s vertical + captions | best hook for cold audiences |
| YouTube | Extended 90 s, chapters | the durable home |
| Hacker News / r/LocalLLaMA | Extended + a comment linking the bench gate | the credibility play: show the gate |
| Launch post | 30 s + before/after token numbers | |

One caption template to reuse per surface: *hook → one proof → one command → URL.*

---

## 17. Work plan

- [x] **1. Lock the direction** (this file, §6) and the cut list (§5).
- [x] **4. Remotion skeleton** — `promo/remotion` scaffolded, theme tokens, primitives, and the end card rendered end to end.
- [ ] **2. Capture assets** (§13) — nothing gets animated before real footage exists.
- [ ] **3. Copy deck** — final on-screen text for 30 s and 15 s (no VO script needed).
- [ ] **5. Scene-by-scene** — beats 1–7 of `Hero30`, 1–3 of `Bumper15`; heaviest last (stats overlay).
- [ ] **6. Render all cuts**, mux SFX + music bed, sidecar captions.
- [ ] **7. Claims review** against §15 before publishing.
- [ ] **8. Publish + embed** in README and `docs/index.html`.

Effort estimate: ~1 day for the 30 s + 15 s if captures go smoothly, ~1 more day
for the 90 s extended cut.

### Build status — **complete** (2026-10-01)

All 8 hero beats and all 4 vertical beats are built. Both cuts render with
audio. Every number on screen traces to a capture in `promo/captures/`.

| Cut | Frames | Duration | Size | Audio |
| --- | --- | --- | --- | --- |
| `Hero30` | 900 | 30.000 s | 1920×1080@30, 3.6 MB | bed + 4 cues |
| `Bumper15` | 450 | 15.000 s | 1080×1920@30, 1.7 MB | bed + 2 cues |

`tsc` + `eslint` clean. Published silent recompressions in `assets/` are embedded
in the README and the landing page; caption sidecars (`.srt` + `.vtt`) ship
alongside both cuts.

**Captures** — all three regenerated 2026-10-01 and committed:
[`bench.txt`](captures/bench.txt), [`footprint.txt`](captures/footprint.txt),
[`gateway-evidence.txt`](captures/gateway-evidence.txt). The gateway capture runs
entirely offline against two `cmd/mock-upstream` instances; `capture.sh` refuses
to write the ledger unless it has first proved the gateway on its port serves
only `mock-a` and `mock-b`.

**Repo doc bugs found while verifying, all fixed:** 8 stale `91.5 %` instances,
`docs/index.html`'s `2s→30m` cooldown, the stale `router.go` package comment that
kept regenerating it, and the footprint figures (`~10 MiB` / `10.6 MiB` →
`12.6 MiB` / `10.9 MiB`, with a note that they track the toolchain).

**Still open, none blocking:**
- `docs/HOW-IT-WORKS.md:279-282` documents `dial_ms` / `headers_ms` / `ttfb_ms`
  in the JSONL log, but **nothing in the codebase ever assigns them** — `Phases`
  is constructed in two places and both set `TotalMS` only, and the fields are
  `omitempty` so they vanish silently. Either populate them or drop them from the
  docs. The video avoided the issue by not showing latency from the log at all.
- `docs/HOW-IT-WORKS.md:44-47` implies `/metrics` exposes latency; it does not.
- `scripts/measure-ram.sh` shells out to `bc`, which is not a documented
  dependency and is absent on several distros. `awk` would do.
- The 15 s bumper uses the *plain* bed rather than the pulsing one; the 30 s hero
  has the tick. Worth a listen before publishing.

**Known cosmetic item, needs eyes on motion:** a hard cut lands on a beat whose
content fades in over ~12 frames, so mid-film boundaries are carried for ~0.3 s by
the sheet label and caption alone. Reads as a deliberate breath in a contact
sheet; if it flickers in playback, lower `Plate`'s `fadeIn` or start each beat's
content earlier.

---

## 18. Open questions (answer these before I build anything)

1. **Direction** — A (Blueprint), B (The bill), C (Hamster), or A+B hybrid?
2. **Voice** — recorded human, TTS, or no VO (captions + SFX only)?
3. **Length first** — 30 s hero only, or 30 s + 15 s vertical?
4. **Language** — English only, or EN + subtitles?
5. **Number conflicts** (§4) — which is authoritative for cooldown and overhead?
6. **Money claim** — do we have a real `routre list` session to show a before/after
   bill, or do we stay on the bench-gated percentages?
7. **Placement** — README embed, landing page loop, socials — which first?
8. **Mascot** — is the hamster allowed in the hero cut, or art-only-until-a-vector-
   exists?

---

## 19. Copy bank (optional lines to reuse)

| Use | Line |
| --- | --- |
| Hero | Stop paying the token tax |
| Sub | The 10-MB gateway you forget is running |
| Mechanism | One address. Cheapest healthy provider. Answer back. |
| Compression | Shrunk before it's billed. |
| Cache | Ask twice, pay once. |
| Failover | Busy provider, next provider. You didn't notice. |
| Proof | Measured, not promised. |
| Install | One command. No Python. No Postgres. |
| Outro | A smart power strip for your AI tools. |
