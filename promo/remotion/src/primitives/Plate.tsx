import { Easing, Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, palette } from "../theme";

/**
 * `.plate` from the landing page: a hairline-bordered panel with four accent
 * corner brackets that grow when the panel lands.
 *
 * `delay` is in frames so callers can stagger panels inside one scene.
 */
export const Plate: React.FC<{
  readonly children: React.ReactNode;
  readonly name?: string;
  readonly delay?: number;
  /** Fade length in frames. `0` means visible from frame 0 — use it for the
   *  opening panel of a cut, so frame 0 is composed rather than empty. */
  readonly fadeIn?: number;
  readonly style?: React.CSSProperties;
}> = ({ children, name = "Plate", delay = 0, fadeIn = 12, style }) => {
  const frame = useCurrentFrame();

  return (
    <Interactive.Div
      name={name}
      style={{
        position: "relative",
        border: `1px solid ${palette.hairlineSoft}`,
        backgroundColor: "rgba(14, 35, 71, 0.45)",
        opacity:
          fadeIn === 0
            ? 1
            : interpolate(frame, [delay, delay + fadeIn], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
        translate:
          fadeIn === 0
            ? undefined
            : `0px ${interpolate(frame, [delay, delay + fadeIn], [18, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              })}px`,
        ...style,
      }}
    >
      <Bracket delay={delay} placement="tl" />
      <Bracket delay={delay} placement="tr" />
      <Bracket delay={delay} placement="bl" />
      <Bracket delay={delay} placement="br" />
      {children}
    </Interactive.Div>
  );
};

type CornerSide = "top" | "bottom" | "left" | "right";

type BracketSpec = {
  /** Which edges are pinned; the bracket hugs that corner. */
  readonly inset: Readonly<Partial<Record<CornerSide, number>>>;
  /** Which edges carry the accent border. */
  readonly borders: ReadonlyArray<CornerSide>;
};

const BRACKET_CORNERS: Readonly<
  Record<"tl" | "tr" | "bl" | "br", BracketSpec>
> = {
  tl: { inset: { top: -1, left: -1 }, borders: ["top", "left"] },
  tr: { inset: { top: -1, right: -1 }, borders: ["top", "right"] },
  bl: { inset: { bottom: -1, left: -1 }, borders: ["bottom", "left"] },
  br: { inset: { bottom: -1, right: -1 }, borders: ["bottom", "right"] },
};

const BRACKET_ARM = 24;

const Bracket: React.FC<{
  readonly delay: number;
  readonly placement: "tl" | "tr" | "bl" | "br";
}> = ({ delay, placement }) => {
  const frame = useCurrentFrame();
  const { inset, borders } = BRACKET_CORNERS[placement];

  const arm = interpolate(frame, [delay + 8, delay + 28], [0, BRACKET_ARM], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });

  return (
    <span
      style={{
        position: "absolute",
        top: inset.top,
        left: inset.left,
        right: inset.right,
        bottom: inset.bottom,
        width: arm,
        height: arm,
        borderTop: borders.includes("top") ? `2px solid ${palette.accent}` : "none",
        borderBottom: borders.includes("bottom")
          ? `2px solid ${palette.accent}`
          : "none",
        borderLeft: borders.includes("left") ? `2px solid ${palette.accent}` : "none",
        borderRight: borders.includes("right")
          ? `2px solid ${palette.accent}`
          : "none",
        pointerEvents: "none",
      }}
    />
  );
};

const LED_TONES = {
  ok: { dot: palette.ok, rgba: "123, 201, 111" },
  alarm: { dot: palette.alarm, rgba: "226, 85, 76" },
  dim: { dot: palette.steelDim, rgba: "125, 116, 102" },
} as const;

/**
 * Status LED — the site's pulsing dot, driven by `useCurrentFrame` instead of a
 * CSS animation so the render is deterministic.
 */
export const Led: React.FC<{
  readonly tone?: keyof typeof LED_TONES;
  readonly delay?: number;
  readonly size?: number;
}> = ({ tone = "ok", delay = 0, size = 12 }) => {
  const frame = useCurrentFrame();
  const { dot, rgba } = LED_TONES[tone];

  // One 2.2s pulse cycle, matching the site's `led-pulse 2.2s`.
  const ring = (((frame - delay) % 66) + 66) % 66 / 66;
  const spread = ring * 14;

  return (
    <span
      style={{
        display: "inline-block",
        width: size,
        height: size,
        borderRadius: "50%",
        backgroundColor: dot,
        boxShadow: `0 0 0 ${spread}px rgba(${rgba}, ${0.5 * (1 - ring)})`,
        flexShrink: 0,
      }}
    />
  );
};

/** Easing helper for anything that needs to start and stop on its own time. */
export const fade = (frame: number, from: number, to: number) =>
  interpolate(frame, [from, to], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });

/** Linear-in-linear-out fade, for motion that should read as mechanical. */
export const fadeLinear = (frame: number, from: number, to: number) =>
  interpolate(frame, [from, to], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: Easing.linear,
  });
