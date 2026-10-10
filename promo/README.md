# routre promo video

Creative direction, copy, beat sheets and claims rules live in **[BRIEF.md](./BRIEF.md)**.
This file is only about running the build.

## What exists today

All beats are built. There are no placeholders left.

| Composition | Size | Length | Beats |
| --- | --- | --- | --- |
| `Hero30` | 1920×1080 | 30 s | 8/8 built, with audio |
| `Bumper15` | 1080×1920 | 15 s | 4/4 built, with audio |
| `BillHook` | 1920×1080 | 3.5 s | two counters, 12.2× ratio, sourced footnote |
| `Reveal` | 1920×1080 | 2.5 s | four agents, one thread, one address |
| `Compress` | 1920×1080 | 4.5 s | the captured bench table, five bars over the gate line |
| `Cache` | 1920×1080 | 4 s | one request twice; the header flip is the whole beat |
| `Failover` | 1920×1080 | 5 s | real 429, real cooldown, client still gets 200 |
| `Stats` | 1920×1080 | 4.5 s | three figures, each qualified, each naming its command |
| `Ledger` | 1920×1080 | 3 s | the real `routre list` rows |
| `EndCard` | 1920×1080 | 3 s | install command, repo URL, HUD row |

## Cuts

| Composition | Size | Length | What it is |
| --- | --- | --- | --- |
| `Master60` | 1920×1080 | 64 s | **The film.** 24s of the problem first, then the 36s cut in order |
| `Hero30` | 1920×1080 | 36 s | The same film with the problem cut — the ad |
| `Bumper15` | 1080×1920 | 15 s | Feed Reel: three beats |
| `Story15` | 1080×1920 | 15 s | Instagram Story: one beat, IG-safe zones |

`Master60` is the one to link from the README. It opens on the three things that
actually happen on a normal afternoon with an AI coding agent — the tool-output
flood, paying twice for the same context, and a 429 stopping the run — and only
then offers the gateway. `Hero30` opens on the answer, which is what makes it an
ad and what makes it useless to someone deciding whether to care.

`Story15` is not a reformat of `Bumper15`. Instagram covers the top of a Story
with the account row and the bottom with the caption bar, so it uses 300px safe
zones; and a Story viewer taps through, so it argues one thing instead of three.

## The mascot

The hamster is a **rig**, not a picture: `primitives/Mascot.tsx` draws it as SVG
parts on independent transforms — head, two ears, eye lids, cheeks, whiskers —
so it blinks, breathes, leans and rocks. `HamsterWheel` is the same rig plus a
wheel, and it is the only beat where the mascot is the *subject* rather than a
visitor: the wheel carries tokens and most of them never reach the gate, which is
the compression mechanism shown as one object instead of a table.

Two things it deliberately does not do: it never speaks, and it never carries a
number. Every figure on screen comes from `promo/captures/` (enforced by
`audit-claims.py`).

The layout note in `HamsterWheel` is worth keeping: the hamster and the wheel sit
**side by side**, not overlapping. Four attempts to overlap them all read as the
hamster wearing the wheel or standing inside it — a cartoon head and a wire
wheel share no depth cues, so any overlap is ambiguous. Separation plus the
paw-trail is what makes it legible.

## Published assets

In `assets/`, ready to embed. All silent and recompressed, because a
muted-autoplay embed cannot carry sound anyway:

| File | Size | Where |
| --- | --- | --- |
| `routre-promo-64s-master-embed.mp4` | 1.3 MB | README + landing page |
| `routre-promo-36s-hero-embed.mp4` | 852 KB | README, as a link |
| `routre-promo-15s-story-embed.mp4` | 198 KB | README, as a link |
| `routre-promo-15s-vertical-embed.mp4` | 345 KB | feed Reel |
| `*.{jpg,srt,vtt}` | ~100 KB ea | poster frames + caption sidecars |

The full-quality renders **with audio** stay in `promo/remotion/out/` and are
gitignored. Those are the ones to upload to YouTube, X or Instagram.

## Reproducing everything

```bash
python3 promo/audit-claims.py         # every on-screen number vs its capture
promo/capture.sh                     # real gateway evidence → promo/captures/
make bench                           # bench + RAM figures the captures quote
python3 promo/make-audio.py          # synthesise every sound, no assets
python3 promo/make-captions.py       # .srt/.vtt sidecars for all four cuts
cd promo/remotion && npm run dev     # scrub and edit
npm run render:all                   # all four cuts, with audio
```

Run `audit-claims.py` after any re-capture. It is the mechanical form of the
brief's golden rule — if a constant in `theme.ts` stops matching
`promo/captures/`, the script exits non-zero rather than letting a stale number
reach the screen.

A "placeholder" beat still renders — it shows the beat's slot, timecode and intent
so the timeline is a real 30 s document you can scrub today. Replacing one is a
one-line change at its `<Series.Sequence>` call site in `src/Timelines.tsx`.

## Run it

```bash
cd promo/remotion
npm i

npm run dev            # Remotion Studio — scrub, edit props, live preview
npm run lint           # eslint + tsc
npm run render:hero    # out/hero30.mp4 (with audio)
npm run render:bumper  # out/bumper15.mp4 (with audio)
npm run still EndCard out/endcard.png --frame=89
```

Node ≥ 20. First render downloads a Headless Shell (~86 MB) into
`node_modules/.cache`; after that renders are offline.

## Layout

```
capture.sh              produces captures/gateway-evidence.txt from a real gateway
make-audio.py           synthesises every sound; nothing is licensed
make-captions.py        .srt/.vtt sidecars for all four cuts
audit-claims.py         fails if any on-screen number drifts from its capture
captures/
  bench.txt             make bench  → the 91.2 % and the 106,150 → 8,677
  footprint.txt         measure-ram.sh → 12.6 MiB idle, 10.9 MiB binary
  gateway-evidence.txt  real 429, real cooldown, real cache hit, real ledger

remotion/src/
├── Root.tsx          composition registry
├── Timelines.tsx     Master60 + Hero30 + Bumper15 + Story15 timelines
├── audio.tsx         bed + UI cues, frame-accurate to each cut's beats
├── timing.ts         motion vocabulary: rise, stagger, blink, drawOn, wipe…
├── theme.ts          every colour, size, number and copy constant
├── fonts.ts          Press Start 2P · VT323 · IBM Plex Mono, loaded once
├── scenes/
│   ├── Problem.tsx     SHEET 00/00b — the three pains, then the turn
│   ├── BillHook.tsx    SHEET 01 — two counters, one ratio, one honest footnote
│   ├── Reveal.tsx      SHEET 02 — apps → one thread → routre → the address
│   ├── Compress.tsx    SHEET 03 — the captured bench table
│   ├── Wheel.tsx       SHEET 03b — the mascot doing the mechanism, + shared
│   │                    StatCard / LiveChip / BeatBridge
│   ├── Cache.tsx       SHEET 04 — one request twice
│   ├── Failover.tsx    SHEET 05 — real 429, real cooldown
│   ├── Stats.tsx       SHEET 06 — three figures, each qualified
│   ├── Ledger.tsx      SHEET 07 — the real routre list rows
│   ├── EndCard.tsx     SHEET 08 — install command, repo URL, HUD row
│   ├── Bumper.tsx      the three vertical bumper beats
│   └── Story.tsx       the three Instagram Story beats
└── primitives/
    ├── SceneFrame.tsx  backdrop + safe area + caption slot
    ├── Backdrop.tsx    navy void, blueprint grid, scanlines, sweep
    ├── Plate.tsx       bordered panel + corner brackets, LED
    ├── Terminal.tsx    window chrome + typed row helpers
    ├── Schematic.tsx   nodes, wires, travelling packet
    ├── Mascot.tsx      the rigged mascot: blink, breathe, lean, wheel
    └── Type.tsx        FigLabel, Caption, CountUp, typewriter, cursor
```

## House rules for anyone editing this

1. **Animate with `useCurrentFrame()` only.** No CSS transitions or animations —
   they do not render deterministically.
2. **Numbers come from `theme.ts`, never retyped.** `INSTALL`, `REPO`, `GATEWAY`
   exist so the video and the README cannot drift apart.
3. **`interpolate()` returns a unitless number.** `top: interpolate(...)` is
   *pixels*. Re-attach `%` or `px` yourself.
4. **Every command on screen must be paste-accurate.** It is a dev audience; a
   wrong flag is the first comment.
5. **A number only reaches the screen if a captured artifact produced it.**
   See §13 of the brief for what has to be recorded first.
6. **Keep copy inside the brief's limits:** ≤ 6 words on screen at once, at most
   one accent word per caption, no feature list longer than three.
7. **After touching a number, run `python3 promo/audit-claims.py`.** It diffs
   every figure in `theme.ts` against `promo/captures/` and fails loudly.
8. **Stat wording is load-bearing.** `91.2 %` is the *mean of five per-payload
   percentages*, not a pooled token ratio — see §4 of the brief. Never let it
   become "91.2 % off your bill".
9. **Frame 0 of a cut must be composed.** A `Plate` fades in from opacity 0, so
   a scene whose first plate lands at frame 0 opens on an empty navy field. Pass
   `fadeIn={0}` for the opening panel of a film (as `BillHook` does) or start the
   beat's content earlier.
