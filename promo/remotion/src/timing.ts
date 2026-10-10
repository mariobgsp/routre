import { Easing, interpolate, useVideoConfig } from "remotion";

/**
 * Timing helpers.
 *
 * Everything here exists so motion is written as *intent* — "land", "settle",
 * "breathe", "blink" — rather than as a hand-tuned pair of frame numbers at
 * every call site. When a beat's length changes, these keep their character
 * instead of silently going stiff.
 */

/** The site's easing: `0.45s cubic-bezier(0.22, 1, 0.36, 1)`. */
export const EASE = Easing.bezier(0.22, 1, 0.36, 1);

/** Overshoot for anything that "lands". */
export const LAND = Easing.bezier(0.18, 1.42, 0.4, 1);

/** Symmetric, for things that swing or settle. */
export const SETTLE = Easing.bezier(0.34, 1.26, 0.32, 1);

/**
 * A spring from a spring config. Use for scale/translate where a bounce reads
 * as physical. Avoid on text — a springing headline is a tic, not a motion.
 */
export const spring = (config?: Parameters<typeof Easing.spring>[0]) =>
  Easing.spring({ damping: 200, ...config });

/**
 * Stagger helper: given an index, returns that item's delay.
 *
 * Used wherever a scene reveals a list — the bench rows, the stat cards, the
 * problem panels. A stagger is what makes a list read as a sequence of thoughts
 * rather than as one object appearing.
 */
export const stagger = (
  index: number,
  step = 6,
  base = 0,
): number => base + index * step;

/**
 * A continuous oscillation. `period` is in frames; `phase` shifts it.
 *
 * The mascot's breathing, the LED ring, the grid's slow drift — anything that
 * should never quite be still. Returns 0 at `phase` and swings symmetrically
 * about it, so it is frame-deterministic and scrub-safe.
 */
export const oscillate = (
  frame: number,
  amplitude: number,
  period: number,
  phase = 0,
): number => {
  const t = (((frame + phase) % period) / period) * Math.PI * 2;
  return Math.sin(t) * amplitude;
};

/** A single blink cycle over `period` frames: closed for a few, then open. */
export const blink = (frame: number, period = 96, phase = 0): number => {
  const local = (frame + phase) % period;
  if (local > 7) return 1; // open
  // 0 -> 1 -> 0 over 7 frames
  return interpolate(local, [0, 3.5, 7], [1, 0.08, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
};

/**
 * A rising, settling entrance: the thing most elements use.
 *
 * Returns the transform + opacity for an element that should come *up* into
 * place and settle, rather than just fade. `lift` is how far it travels in px.
 */
export const rise = (
  frame: number,
  delay: number,
  lift = 26,
  duration = 22,
): { translate: string; opacity: number } => {
  const t = interpolate(frame, [delay, delay + duration], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: SETTLE,
  });
  return {
    translate: `0px ${interpolate(t, [0, 1], [lift, 0], { easing: EASE })}px`,
    opacity: t,
  };
};

/**
 * A sliding entrance along an axis. `axis` is "x" or "y"; `from` is the signed
 * distance it travels.
 */
export const slideIn = (
  frame: number,
  delay: number,
  axis: "x" | "y" = "x",
  from = 40,
  duration = 24,
): { translate: string; opacity: number } => {
  const t = interpolate(frame, [delay, delay + duration], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });
  const d = interpolate(t, [0, 1], [from, 0]);
  return {
    translate: axis === "x" ? `${d}px 0px` : `0px ${d}px`,
    opacity: t,
  };
};

/**
 * A draw-on for lines: 0 → full width/height, used by the schematic wires so a
 * connector arrives the way a cable is paid out rather than by popping in.
 */
export const drawOn = (
  frame: number,
  delay: number,
  duration = 18,
): number =>
  interpolate(frame, [delay, delay + duration], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });

/** A wipe for a plate or bar: 0 → 1 as a percentage width. */
export const wipe = (
  frame: number,
  delay: number,
  duration = 22,
): number =>
  interpolate(frame, [delay, delay + duration], [0, 100], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });

/**
 * A one-shot "pop": scale 0 → 1 on the overshooting `LAND` curve, then hold.
 * For things that arrive as a single event — a stat landing, the mascot
 * appearing. `LAND` already overshoots; that overshoot *is* the pop, so there is
 * no separate amplitude to tune here.
 */
export const pop = (
  frame: number,
  delay: number,
  duration = 20,
): { scale: number; opacity: number } => {
  const t = interpolate(frame, [delay, delay + duration], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: LAND,
  });
  return { scale: t, opacity: interpolate(t, [0, 0.3, 1], [0, 1, 1]) };
};

/**
 * Frames-per-second-aware wrapper, for callers that think in seconds.
 * Every scene in this project is 30fps, but deriving it means a future 24fps
 * render does not silently halve every animation.
 */
export const useSeconds = () => {
  const { fps } = useVideoConfig();
  return {
    fps,
    /** seconds → frames */
    s: (seconds: number) => Math.round(seconds * fps),
  };
};
