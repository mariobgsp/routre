import { Easing, interpolate, useCurrentFrame } from "remotion";
import { bench, fontBody, fontDisplay, palette } from "../theme";
import { SceneFrame } from "../primitives/SceneFrame";
import { Hamster } from "../primitives/Mascot";

/**
 * The Instagram Story cut — three beats, 1080×1920, 15s total.
 *
 * Two things separate this from `Bumper15`, and neither is cosmetic:
 *
 *   - **Safe zones.** Instagram covers the top of a Story with the account row
 *     and progress bars and the bottom with the caption/reply bar. Roughly the
 *     top and bottom 250px are behind chrome on most phones, so this cut uses
 *     300px of padding top and bottom. `Bumper15` uses 180px because it is a
 *     feed Reel, not a Story.
 *   - **One idea.** A Story viewer taps through. Three arguments in 15s is zero
 *     arguments, so this cut carries exactly one claim, the arithmetic behind
 *     it, and the ask.
 *
 * Type is larger throughout: at arm's length a 30px caption is not read.
 */

/** Kept clear of the account row (top) and the caption/reply bar (bottom). */
const SAFE_TOP = 300;
const SAFE_X = 80;

/** beat 1 — the claim. 3.5s */
export const StoryClaim: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <StoryShell label="SHEET 01" title="THE CLAIM">
      <div
        style={{
          fontFamily: fontBody,
          fontSize: 168,
          fontWeight: 600,
          letterSpacing: -6,
          color: palette.torch,
          lineHeight: 1,
          opacity: interpolate(frame, [8, 26], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
          scale: interpolate(frame, [8, 26], [0.9, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
            output: "perceptual-scale",
          }),
        }}
      >
        12.2×
      </div>
      <div
        style={{
          fontFamily: fontBody,
          fontSize: 52,
          fontWeight: 500,
          color: palette.ink,
          textAlign: "center",
          opacity: interpolate(frame, [22, 40], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        fewer tool tokens
      </div>
    </StoryShell>
  );
};

/** beat 2 — the arithmetic behind it. 6s */
export const StoryProof: React.FC = () => {
  const frame = useCurrentFrame();

  const rows = [
    { label: "sent", value: bench.toolTokensBefore, colour: palette.steelDim },
    { label: "billed", value: bench.toolTokensAfter, colour: palette.torch },
  ];

  return (
    <StoryShell label="SHEET 02" title="THE PROOF">
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          gap: 40,
          alignItems: "stretch",
          width: "100%",
        }}
      >
        {rows.map((row, i) => {
          const delay = 8 + i * 46;

          return (
            <div
              key={row.label}
              style={{
                display: "flex",
                flexDirection: "column",
                gap: 6,
                opacity: interpolate(frame, [delay, delay + 16], [0, 1], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                  easing: EASE_LOCAL,
                }),
                translate: `0px ${interpolate(frame, [delay, delay + 16], [26, 0], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                  easing: EASE_LOCAL,
                })}px`,
              }}
            >
              <div
                style={{
                  fontFamily: fontBody,
                  fontSize: 34,
                  color: palette.steelDim,
                  letterSpacing: 2,
                  textTransform: "uppercase",
                }}
              >
                {row.label}
              </div>
              <div
                style={{
                  fontFamily: fontBody,
                  fontSize: 108,
                  fontWeight: 600,
                  letterSpacing: -4,
                  color: row.colour,
                }}
              >
                <CountUpStory
                  to={row.value}
                  startFrame={delay}
                  durationInFrames={40}
                />
              </div>
              {i === 0 ? (
                <div
                  style={{
                    fontFamily: fontBody,
                    fontSize: 72,
                    color: palette.accent,
                    marginTop: 4,
                  }}
                >
                  ↓
                </div>
              ) : null}
            </div>
          );
        })}
      </div>

      <div
        style={{
          fontFamily: fontBody,
          fontSize: 28,
          color: palette.steelDim,
          textAlign: "center",
          marginTop: 48,
          opacity: interpolate(frame, [120, 140], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        est. tool tokens · `routre bench` · 5 tool-heavy payloads
      </div>
    </StoryShell>
  );
};

/** beat 3 — the ask. 5.5s */
export const StoryAsk: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <StoryShell label="SHEET 03" title="THE ASK">
      <div
        style={{
          fontFamily: fontDisplay,
          fontSize: 56,
          color: palette.ink,
          textAlign: "center",
          lineHeight: 1.5,
          opacity: interpolate(frame, [8, 26], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        one
        <br />
        address
      </div>
      <div
        style={{
          fontFamily: fontBody,
          fontSize: 40,
          fontWeight: 600,
          color: palette.accent,
          textAlign: "center",
          marginTop: 30,
          opacity: interpolate(frame, [24, 42], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        127.0.0.1:20128
      </div>
      <div
        style={{
          fontFamily: fontBody,
          fontSize: 30,
          color: palette.steel,
          textAlign: "center",
          marginTop: 44,
          opacity: interpolate(frame, [40, 58], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        github.com/mariobgsp/routre
      </div>
    </StoryShell>
  );
};

/* ------------------------------------------------------------------ helpers */

const EASE_LOCAL = Easing.bezier(0.22, 1, 0.36, 1);

const CountUpStory: React.FC<{
  readonly to: number;
  readonly startFrame: number;
  readonly durationInFrames: number;
}> = ({ to, startFrame, durationInFrames }) => {
  const frame = useCurrentFrame();

  const value = interpolate(
    frame,
    [startFrame, startFrame + durationInFrames],
    [0, to],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp", easing: EASE_LOCAL },
  );

  return <>{Number(value.toFixed(0)).toLocaleString("en-US")}</>;
};

/** The shared Story chrome: safe-zone padding, sheet label, mascot. */
const StoryShell: React.FC<{
  readonly children: React.ReactNode;
  readonly label: string;
  readonly title: string;
}> = ({ children, label, title }) => {
  const frame = useCurrentFrame();

  return (
    <SceneFrame padX={SAFE_X} padY={SAFE_TOP}>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          fontFamily: fontBody,
          fontSize: 24,
          color: palette.steelDim,
          letterSpacing: 2,
          opacity: interpolate(frame, [0, 14], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
            easing: EASE_LOCAL,
          }),
        }}
      >
        <span style={{ color: palette.accent }}>{label}</span>
        <span>{title}</span>
      </div>

      <div
        style={{
          flex: 1,
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          justifyContent: "center",
          gap: 26,
          /* The proof beat is the tallest of the three; without a cap the
             footer lifts into the caption bar on a 6.1" phone. */
          maxHeight: "100%",
          overflow: "hidden",
        }}
      >
        {children}
      </div>

      <div
        style={{
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          gap: 18,
          flexShrink: 0,
          paddingBottom: 40,
        }}
      >
        <Hamster
          name={`${title} hamster`}
          size={140}
          delay={14}
        />
        <div
          style={{
            fontFamily: fontBody,
            fontSize: 24,
            color: palette.steelDim,
            textAlign: "center",
            opacity: interpolate(frame, [26, 42], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE_LOCAL,
            }),
          }}
        >
          stop paying the token tax
        </div>
      </div>
    </SceneFrame>
  );
};
