import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { EASE, palette } from "../theme";

/**
 * The blueprint backdrop every scene sits on: navy void, 44px hairline grid,
 * a soft top-weighted mask so the grid fades out toward the bottom, and the
 * CRT scanlines from the landing page.
 *
 * Static by design — the brief's motion rules say the camera never moves.
 */
export const BlueprintBackdrop: React.FC = () => {
  return (
    <AbsoluteFill name="Backdrop" style={{ backgroundColor: palette.paper }}>
      <AbsoluteFill
        name="Grid"
        style={{
          backgroundImage: `linear-gradient(${palette.grid} 1px, transparent 1px), linear-gradient(90deg, ${palette.grid} 1px, transparent 1px)`,
          backgroundSize: "64px 64px",
          maskImage:
            "radial-gradient(120% 100% at 50% 0%, #000 30%, transparent 92%)",
          WebkitMaskImage:
            "radial-gradient(120% 100% at 50% 0%, #000 30%, transparent 92%)",
        }}
      />
    </AbsoluteFill>
  );
};

/**
 * The one moving layer allowed over the backdrop: a CRT scanline pattern.
 * Rendered last so it sits above every plate, exactly like `--z-fx` on the site.
 */
export const Scanlines: React.FC = () => {
  return (
    <AbsoluteFill
      name="Scanlines"
      style={{
        pointerEvents: "none",
        backgroundImage:
          "repeating-linear-gradient(0deg, rgba(255,255,255,0.014) 0 1px, transparent 1px 3px)",
      }}
    />
  );
};

/**
 * A single accent-tinted band that sweeps down the frame once, then rests off
 * screen. `EASE` in, linear out — a sweep should not feel springy.
 */
export const ScanSweep: React.FC<{
  readonly startFrame: number;
  readonly durationInFrames: number;
}> = ({ startFrame, durationInFrames }) => {
  const frame = useCurrentFrame();

  return (
    <AbsoluteFill
      name="ScanSweep"
      style={{
        pointerEvents: "none",
        backgroundImage:
          "linear-gradient(180deg, transparent, rgba(202,138,4,0.10) 45%, rgba(255,122,61,0.14) 50%, transparent)",
        height: "22%",
        // interpolate() returns a unitless number, so `top: 125` would mean
        // 125px. The percentage sign has to be re-attached by hand.
        top: `${interpolate(
          frame,
          [startFrame, startFrame + durationInFrames],
          [-25, 125],
          {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE,
          },
        )}%`,
      }}
    />
  );
};
