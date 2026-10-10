import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, bench, fontBody, palette } from "../theme";
import {
  LAND,
  SETTLE,
  drawOn,
  oscillate,
  rise,
  stagger,
  wipe,
} from "../timing";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { HamsterWheel } from "../primitives/Mascot";
import { Plate } from "../primitives/Plate";

/**
 * SHEET 03b — The Wheel. 6 seconds.
 *
 * The mascot finally does the job the brief gave it in §6: it runs a wheel of
 * tokens into the gateway and most of them never make it through. The mechanism
 * that Beats 3, 4 and 5 explain with tables and terminals is here shown as one
 * object doing the thing — which is the only reason a mascot belongs in a
 * technical film at all.
 *
 * The numbers are the captured ones, and the split is arithmetic:
 *
 *   106,150 est. tool tokens in  →  8,677 billed      (captures/bench.txt)
 *
 * The wheel's rotation and the token flow are driven by one `progress` value, so
 * the visual is scrubbable to an exact state rather than merely looking right.
 *
 * In `Master60` this sits directly after `Compress`: the table, then the thing
 * the table is about.
 */
export const Wheel: React.FC = () => {
  const frame = useCurrentFrame();

  const WHEEL_START = 8;
  const WHEEL_END = 132;
  const wheelProgress = interpolate(
    frame,
    [WHEEL_START, WHEEL_END],
    [0, 1],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp", easing: EASE },
  );

  // The gate: a hard line the tokens have to pass. It draws on, then holds.
  const gate = drawOn(frame, 4, 16);

  // The ticker: counts down as tokens drop.
  const inTokens = bench.toolTokensBefore;
  const outTokens = bench.toolTokensAfter;
  const remaining = Math.round(
    inTokens + (outTokens - inTokens) * wheelProgress,
  );

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 03b" title="THE WHEEL" />
      <SceneBody align="center" gap={40}>

        {/* ---------- the machine ----------
            The wheel and its gate are one unit: the hamster is inside the
            wheel group, so the whole thing is laid out as a single row and the
            mascot can never land on top of the spokes. */}
        <div
          style={{
            position: "relative",
            display: "flex",
            alignItems: "flex-end",
            justifyContent: "center",
            gap: 72,
          }}
        >
          <HamsterWheel
            size={520}
            delay={0}
            progress={wheelProgress}
            tokens={10}
          />

          {/* the gate the tokens pass through */}
          <div
            style={{
              position: "relative",
              width: 7,
              height: 380,
              marginBottom: 20,
              backgroundColor: palette.hairline,
              overflow: "hidden",
            }}
          >
            <div
              style={{
                position: "absolute",
                inset: 0,
                backgroundColor: palette.accent,
                scale: `1 ${gate}`,
                boxShadow: `0 0 22px ${palette.accent}88`,
              }}
            />
          </div>

          {/* what survives the gate */}
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              justifyContent: "flex-end",
              gap: 12,
              paddingBottom: 30,
            }}
          >
            <div
              style={{
                fontFamily: fontBody,
                fontSize: 22,
                color: palette.steelDim,
                letterSpacing: 2,
              }}
            >
              BILLED
            </div>
            <div
              style={{
                fontFamily: fontBody,
                fontSize: 76,
                fontWeight: 600,
                letterSpacing: -3,
                color: palette.torch,
                ...rise(frame, 6, 18, 20),
              }}
            >
              {outTokens.toLocaleString("en-US")}
            </div>
          </div>
        </div>

        {/* ---------- the ratio, once the wheel has turned ----------
            The arrow is a drawn SVG line rather than the "→" glyph: IBM Plex
            Mono has no arrow in its latin subset, so the character silently
            fell back and landed on top of the figure. */}
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 26,
            ...rise(frame, 46, 24, 26),
          }}
        >
          <span
            style={{
              fontFamily: fontBody,
              fontSize: 100,
              fontWeight: 600,
              letterSpacing: -4,
              color: palette.ink,
            }}
          >
            {inTokens.toLocaleString("en-US")}
          </span>
          <svg width={64} height={20} viewBox="0 0 64 20" aria-hidden="true">
            <line
              x1={0}
              y1={10}
              x2={46}
              y2={10}
              stroke={palette.steelDim}
              strokeWidth={2}
            />
            <path
              d="M 46 3 L 62 10 L 46 17 Z"
              fill={palette.steelDim}
            />
          </svg>
          <span
            style={{
              fontFamily: fontBody,
              fontSize: 100,
              fontWeight: 600,
              letterSpacing: -4,
              color: palette.torch,
            }}
          >
            {outTokens.toLocaleString("en-US")}
          </span>
          <span style={{ fontSize: 34, color: palette.steelDim, marginLeft: 14 }}>
            est. tool tokens
          </span>
        </div>

        {/* ---------- the running ticker: the wheel is doing work ---------- */}
        <Plate
          name="Ticker"
          delay={10}
          style={{
            width: 620,
            overflow: "hidden",
          }}
        >
          <div
            style={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              padding: "14px 22px",
              fontFamily: fontBody,
              fontSize: 24,
              color: palette.steelDim,
            }}
          >
            <span>in flight</span>
            <span
              style={{
                color: palette.ink,
                fontSize: 28,
                fontWeight: 600,
                fontVariantNumeric: "tabular-nums",
              }}
            >
              {remaining.toLocaleString("en-US")}
            </span>
          </div>
          {/* the fill is the *drop*, so the bar empties as the count falls */}
          <div
            style={{
              height: 6,
              backgroundColor: palette.hairlineSoft,
            }}
          >
            <div
              style={{
                height: "100%",
                width: `${interpolate(
                  frame,
                  [WHEEL_START, WHEEL_END],
                  [100, 0],
                  { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
                )}%`,
                backgroundColor: palette.accent,
              }}
            />
          </div>
        </Plate>

      </SceneBody>
      <Caption name="WheelCaption">
        the wheel turns. <Accent>most</Accent> of it never gets billed
      </Caption>
    </SceneFrame>
  );
};

/**
 * A beat-transition: a hairline that wipes across the frame and a paw-trail
 * that lifts off. Placed between beats that would otherwise hard-cut, so the
 * film reads as continuous rather than as a slideshow.
 *
 * Not used at every boundary — a cut on a beat change is usually the stronger
 * edit. This is for the two places where the film changes subject (problem →
 * turn, mechanism → mascot).
 */
export const BeatBridge: React.FC<{
  readonly durationInFrames?: number;
}> = ({ durationInFrames = 26 }) => {
  const frame = useCurrentFrame();

  const progress = interpolate(frame, [0, durationInFrames], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: SETTLE,
  });

  const fade = interpolate(
    frame,
    [0, durationInFrames * 0.4, durationInFrames * 0.7, durationInFrames],
    [0, 1, 1, 0],
    { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
  );

  return (
    <div
      style={{
        position: "absolute",
        inset: 0,
        pointerEvents: "none",
        opacity: fade,
      }}
    >
      <div
        style={{
          position: "absolute",
          left: `${interpolate(progress, [0, 1], [8, 92])}%`,
          top: "50%",
          width: 340,
          height: 2,
          backgroundColor: palette.accent,
          boxShadow: `0 0 26px ${palette.accent}`,
        }}
      />
    </div>
  );
};

/**
 * The stat card, rebuilt with the timing helpers so a value *lands* rather than
 * appearing. Used by `Stats` and the vertical bumper.
 */
export const StatCard: React.FC<{
  readonly name: string;
  readonly delay: number;
  readonly value: string;
  readonly label: string;
  readonly qualifier: string;
  readonly accent?: boolean;
  readonly width?: number | string;
}> = ({ name, delay, value, label, qualifier, accent, width = 544 }) => {
  const frame = useCurrentFrame();

  return (
    <Plate
      name={name}
      delay={delay}
      style={{
        width,
        borderColor: accent ? palette.accent : palette.hairlineSoft,
        backgroundColor: accent ? palette.accentSoft : "rgba(14, 35, 71, 0.45)",
      }}
    >
      <div style={{ padding: "38px 36px 34px" }}>
        {/* the value lands with a small overshoot; the label settles after it */}
        <Interactive.Div
          name={`${name}Value`}
          style={{
            fontFamily: fontBody,
            fontSize: 86,
            fontWeight: 600,
            letterSpacing: -2,
            whiteSpace: "nowrap",
            color: accent ? palette.torch : palette.ink,
            scale: `${interpolate(
              frame,
              [delay + 4, delay + 22],
              [0.9, 1],
              { extrapolateLeft: "clamp", extrapolateRight: "clamp", easing: LAND },
            )}`,
            ...rise(frame, delay, 22, 22),
          }}
        >
          {value}
        </Interactive.Div>

        <Interactive.Div
          name={`${name}Label`}
          style={{
            fontFamily: fontBody,
            fontSize: 30,
            color: palette.steel,
            marginTop: 8,
            ...rise(frame, delay + stagger(1, 5), 12, 18),
          }}
        >
          {label}
        </Interactive.Div>

        {/* the qualifier wipes in rather than fading: it is the part that has to
            be read, and a wipe says "read this line". */}
        <div style={{ position: "relative", marginTop: 14, paddingTop: 12 }}>
          <div
            style={{
              position: "absolute",
              left: 0,
              right: 0,
              top: 0,
              height: 1,
              backgroundColor: palette.hairlineSoft,
              transformOrigin: "left center",
              scale: `${wipe(frame, delay + 10, 20)}% 100%`,
            }}
          />
          <Interactive.Div
            name={`${name}Qualifier`}
            style={{
              fontFamily: fontBody,
              fontSize: 21,
              lineHeight: 1.5,
              color: palette.steelDim,
              opacity: interpolate(
                frame,
                [delay + 12, delay + 26],
                [0, 1],
                { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
              ),
            }}
          >
            {qualifier}
          </Interactive.Div>
        </div>
      </div>
    </Plate>
  );
};

/** A chip that pops in and then breathes very slightly, so nothing is static. */
export const LiveChip: React.FC<{
  readonly children: React.ReactNode;
  readonly delay: number;
  readonly tone?: "ok";
  readonly breathe?: boolean;
}> = ({ children, delay, tone, breathe = true }) => {
  const frame = useCurrentFrame();
  const idle = breathe ? 1 + oscillate(frame, 0.012, 88, delay) : 1;

  return (
    <span
      style={{
        fontFamily: fontBody,
        fontSize: 24,
        color: tone === "ok" ? palette.ok : palette.steel,
        border: `1px solid ${tone === "ok" ? palette.ok : palette.hairline}`,
        borderRadius: 999,
        padding: "8px 20px",
        scale: `${idle}`,
        ...rise(frame, delay, 12, 18),
      }}
    >
      {children}
    </span>
  );
};
