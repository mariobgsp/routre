import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, fontDisplay, fontBody, palette, type, FPS } from "../theme";

/**
 * `FIG 08 · END CARD` — the site's small mono tag that labels each section.
 * Every scene carries one; it is the frame's only always-on chrome.
 */
export const FigLabel: React.FC<{
  readonly fig: string;
  readonly title: string;
}> = ({ fig, title }) => {
  return (
    <Interactive.Div
      name="FigLabel"
      style={{
        fontFamily: fontDisplay,
        fontSize: type.label,
        letterSpacing: 1.5,
        color: palette.steel,
        display: "flex",
        gap: 14,
        alignItems: "center",
      }}
    >
      <span style={{ color: palette.accent }}>{fig}</span>
      <span style={{ opacity: 0.5 }}>·</span>
      <span>{title}</span>
    </Interactive.Div>
  );
};

/**
 * Bottom caption bar. This project ships no voiceover, so the caption *is* the
 * narration — it has to stay legible at a glance on a phone.
 */
export const Caption: React.FC<{
  readonly children: React.ReactNode;
  readonly name?: string;
}> = ({ children, name = "Caption" }) => {
  return (
    <Interactive.Div
      name={name}
      style={{
        fontFamily: fontBody,
        fontSize: type.caption,
        fontWeight: 500,
        lineHeight: 1.35,
        color: palette.ink,
        textAlign: "center",
        textWrap: "balance",
      }}
    >
      {children}
    </Interactive.Div>
  );
};

/**
 * The one word in a caption that gets the accent colour. Used sparingly —
 * at most one per caption, per the copy rules in the brief.
 */
export const Accent: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return (
    <span style={{ color: palette.torch, fontWeight: 600 }}>{children}</span>
  );
};

/** A number that counts up to its value over `durationInFrames`, then stops. */
export const CountUp: React.FC<{
  readonly to: number;
  readonly decimals?: number;
  readonly startFrame?: number;
  readonly durationInFrames?: number;
  /** Thousands separators — `106150` reads worse than `106,150` at 76px. */
  readonly group?: boolean;
}> = ({ to, decimals = 0, startFrame = 0, durationInFrames = 30, group }) => {
  const frame = useCurrentFrame();

  const value = interpolate(frame, [startFrame, startFrame + durationInFrames], [0, to], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: EASE,
  });

  const fixed = value.toFixed(decimals);

  return <>{group ? Number(fixed).toLocaleString("en-US") : fixed}</>;
};

/**
 * Deterministic typewriter. Returns the visible slice of `text` for the
 * current frame — a pure function of the frame, so scrubbing is exact.
 */
export const typed = (
  frame: number,
  text: string,
  startFrame = 0,
  charsPerSecond = 28,
) => {
  const typedChars = Math.max(
    0,
    Math.floor(((frame - startFrame) * charsPerSecond) / FPS),
  );
  return text.slice(0, typedChars);
};

/** Block cursor that blinks on a 0.5s cycle — deterministic from the frame. */
export const cursorVisible = (frame: number) =>
  Math.floor(frame / 15) % 2 === 0;
