/**
 * routre promo — design tokens.
 *
 * Every value here is copied from the landing page (`docs/index.html` `:root`).
 * If the site palette changes, change it here too — the video must not drift
 * away from the brand.
 */

import { Easing } from "remotion";

export const palette = {
  /** Navy void — page background. */
  paper: "#12101f",
  /** Slightly raised surface. */
  paper2: "#1b1830",
  /** Deepest well, used for insets and the vignette. */
  paper3: "#0c0a18",
  /** Primary text. */
  ink: "#f0e6d2",
  /** Secondary text, mono labels. */
  steel: "#c9b896",
  /** Tertiary text, units and footnotes. */
  steelDim: "#7d7466",
  /**
   * Blueprint grid lines. Slightly stronger than the site's 0.16: a video is
   * watched at phone size and after compression, where a hairline at 16% is
   * simply gone.
   */
  grid: "rgba(42, 74, 127, 0.26)",
  /** Panel border, at full strength. */
  hairline: "#4a3f5c",
  /** Panel border as used by `.plate` on the site (accent, 30%). */
  hairlineSoft: "rgba(202, 138, 4, 0.3)",
  /** Amber accent — the one colour that is allowed to shout. */
  accent: "#ca8a04",
  accentSoft: "rgba(202, 138, 4, 0.12)",
  /** Hotter amber, for the single hottest element in a frame. */
  torch: "#ff7a3d",
  /** Healthy / online. */
  ok: "#7bc96f",
  /** Downstream failure (429, 5xx, cooldown). */
  alarm: "#e2554c",
} as const;

export const fontDisplay = "PressStart2P, monospace";
export const fontMono = "VT323, ui-monospace, monospace";
export const fontBody = "IBM Plex Mono, ui-monospace, monospace";

/**
 * The site's `--ease`: `0.45s cubic-bezier(0.22, 1, 0.36, 1)`.
 * Used for every entrance in the video — nothing eases any other way.
 */
export const EASE = Easing.bezier(0.22, 1, 0.36, 1);

/**
 * Safe area for a 1920×1080 frame (see video-layout rules): key text stays at
 * least 80px from the sides and 100px from the top and bottom.
 * Vertical cuts scale the x values down; see `scaleFor`.
 */
export const safe = {
  x: 96,
  y: 100,
} as const;

/** Font size minimums for a 1920px-wide composition. */
export const type = {
  headline: 88,
  display: 120,
  stat: 180,
  body: 44,
  caption: 56,
  label: 26,
} as const;

/** Frame rate for every composition in the project. */
export const FPS = 30;

/** The address the whole pitch is built on. */
export const GATEWAY = "http://127.0.0.1:20128";
export const REPO = "github.com/mariobgsp/routre";
export const INSTALL = "curl -fsSL https://raw.githubusercontent.com/mariobgsp/routre/main/install.sh | sh";

/**
 * The RTK bench figures, transcribed from `promo/captures/bench.txt`
 * (`make bench`, develop@87a775e, 2026-09-30, deterministic across runs).
 *
 * Do not hand-edit these. If a number here disagrees with a fresh `make bench`,
 * the capture is stale and the video is lying — re-capture first.
 *
 * `toolReduction` is the bench's printed aggregate, which is the arithmetic
 * **mean** of the per-payload percentages, not a pooled token ratio. That is why
 * every scene that shows it also shows `perPayload` or the token counts.
 */
export const bench = {
  /** (90.8 + 90.3 + 92.8 + 92.0 + 90.3) / 5 = 91.24 — the printed aggregate. */
  toolReduction: 91.2,
  /** (90.2 + 88.8 + 91.9 + 91.8 + 94.0) / 5 = 91.34 */
  payloadReduction: 91.3,
  /** Worst single payload — the gate is enforced per payload, not just overall. */
  worstPayload: 90.3,
  /** The pooled token counts from the same line — a real ratio, no footnote needed. */
  toolTokensBefore: 106150,
  toolTokensAfter: 8677,
  gate: 90.0,
  result: "PASS",
  perPayload: [
    { file: "build-output.json", toolPct: 90.8, touched: true },
    { file: "git-diff.json", toolPct: 90.3, touched: true },
    { file: "git-log.json", toolPct: 92.8, touched: true },
    { file: "grep-results.json", toolPct: 92.0, touched: true },
    { file: "tree-ls.json", toolPct: 90.3, touched: true },
  ],
} as const;

/**
 * The footprint figures, transcribed from `promo/captures/footprint.txt`
 * (`./scripts/measure-ram.sh ./routre ./config.example.json 30`, go1.27.1).
 *
 * These are deliberately *not* the ~10 MiB / 10.6 MiB figures that used to be in
 * the README: those were measured with an older Go toolchain and are not
 * reproducible here. A promo that quotes a number nobody can reproduce is worse
 * than one that quotes a slightly larger real number.
 *
 * Rounding: the captures hold 12.55 and 10.87. Both round *up* to the values
 * below, so no figure on screen is ever flattering.
 */
export const footprint = {
  /** 12852 KiB, sampled flat for the whole 30s window. */
  idleRssMiB: 12.6,
  peakRssMiB: 12.6,
  /** 0 MiB growth across the sample window. */
  growthMiB: 0,
  /** 11395232 bytes: CGO_ENABLED=0 -trimpath -ldflags "-s -w". */
  binaryMiB: 10.9,
  sampleSeconds: 30,
} as const;

/**
 * Gateway-added latency, from the measurement quoted in the comment above
 * `internal/proxy/bench_test.go`'s budget constant: p50 ~17.7 ms base, RTK
 * +6.5 ms, cache +1.6 ms. Raw wall clock against a ~0.1 ms mock; nothing is
 * subtracted. Reproduce with
 * `ROUTRE_ASSERT_LATENCY=1 go test ./internal/proxy -bench=GatewayAddedLatency1MB`.
 */
export const latency = {
  p50Ms: 26,
  p99Ms: 28,
  bodySize: "1 MiB",
} as const;

/**
 * Gateway evidence, transcribed from `promo/captures/gateway-evidence.txt`
 * (`promo/capture.sh` — two `cmd/mock-upstream` instances on loopback, gateway on
 * 21128, no keys, no egress).
 *
 * Two things these numbers are NOT:
 *   - the 429 is real, but the providers are mocks, so the dollar figures come
 *     from config prices, not a bill;
 *   - the cache timings are against a local mock, so the miss/hit *ratio* is a
 *     harness artefact. The video shows the header flip and the byte-identical
 *     replay, which is the actual mechanism — never a speed claim.
 */
export const gateway = {
  cache: {
    firstHeader: "X-Llrouter-Cache: miss",
    secondHeader: "X-Llrouter-Cache: hit",
    /** Verified with `cmp`: the replayed body is byte-identical. */
    byteIdentical: true,
    defaultEntries: 512,
    defaultTtlHours: 1,
  },
  failover: {
    /** The 429 never reaches the client: it got 200 from the next provider. */
    clientStatus: 200,
    servedBy: "mock-b",
    failedProvider: "mock-a",
    failureClass: "rate-limit",
    /** Real /v1/status + `routre list` output. */
    cooldown: "2s",
    failures: 1,
    policy: "2s → 5m",
    metric: 'routre_upstream_failures_total{provider="mock-a",class="rate-limit"}',
  },
  ledger: {
    agent: "promo-demo/1.0",
    requests: 3,
    consumedIn: 20,
    consumedOut: 10,
    savedCache: 10,
    savedRtk: 0,
  },
} as const;
