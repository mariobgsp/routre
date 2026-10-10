import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, bench, fontBody, fontDisplay, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, CountUp, FigLabel } from "../primitives/Type";
import { Hamster } from "../primitives/Mascot";

/**
 * SHEET 00 — The Problem. 24 seconds, and the reason this whole thing exists.
 *
 * The 30s cut opens on the answer, which is fine as an ad and useless to anyone
 * deciding whether to care. This section is the one that was asked for: three
 * things that actually happen on a normal afternoon with an AI coding agent,
 * stated before anything is offered as a fix.
 *
 * Each panel is one complaint, 8 seconds each, built from a captured figure:
 *
 *   01  the tool output is the bill, and nobody chose it
 *       106,150 est. tool tokens across five tool-heavy payloads
 *   02  and you pay again for the same context on the next turn
 *       the same payloads come to 8,677 before billing
 *   03  then the provider is busy, and the run stops
 *       a real HTTP 429, recorded in captures/gateway-evidence.txt
 *
 * The hamster sits through all three. It carries nothing and explains nothing;
 * it is the thing that makes three panels read as one person's afternoon rather
 * than as three slides of a product.
 *
 * Implementation note: each panel is a `<Sequence>` in `Timelines.tsx` with its
 * own local clock. That is why every panel animates from 0 and never collides
 * with its neighbour — three absolutely-positioned panels sharing one clock
 * would need every timing offset to be re-derived by hand, and they would
 * overlap instead.
 */
export const Problem: React.FC = () => (
  <SceneFrame>
    <FigLabel fig="SHEET 00" title="THE PROBLEM" />
    <SceneBody align="center">
      <P01Flood />
    </SceneBody>
    <Caption name="Problem 01 caption">
      <Accent>this</Accent> is the normal afternoon
    </Caption>
  </SceneFrame>
);

/**
 * Panel 01, under the name the registry and the timeline both use.
 *
 * The first panel *is* `Problem`: it was written first, and renaming it would
 * churn the timeline for no gain. The other two are `Problem02` / `Problem03`.
 */
export { Problem as Problem01 };

/** Panel 01 — the flood. 8s */
const P01Flood: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <PanelShell
      index="01"
      title="the tool output is the bill"
      body="grep, git diff, build logs — the model never chose to send all of it, and you pay for all of it"
      figure={bench.toolTokensBefore}
      unit="est. tool tokens"
      foot="5 tool-heavy payloads · routre bench"
      delay={4}
    >
      {/* the flood itself, so the number is something you have seen before */}
      <FloodBars frame={frame} delay={22} />
    </PanelShell>
  );
};

/** Panel 02 — the repeat. 8s */
export const Problem02: React.FC = () => (
  <SceneFrame>
    <FigLabel fig="SHEET 00" title="THE PROBLEM" />
    <SceneBody align="center">
      <PanelShell
        index="02"
        title="and you pay again for the same context"
        body="the identical ask, sent again on the next turn, is billed again as if it were new"
        figure={bench.toolTokensAfter}
        unit="what it could have been"
        foot="same payloads, compressed before billing"
        delay={4}
      >
        <RepeatRows frame={useCurrentFrame()} delay={22} />
      </PanelShell>
    </SceneBody>
    <Caption name="Problem 02 caption">
      <Accent>pay again</Accent> for the same context
    </Caption>
  </SceneFrame>
);

/** Panel 03 — the stall. 8s */
export const Problem03: React.FC = () => (
  <SceneFrame>
    <FigLabel fig="SHEET 00" title="THE PROBLEM" />
    <SceneBody align="center">
      <PanelShell
        index="03"
        title="then the provider is busy"
        body="a 429 stops the run. The agent waits, retries, or gives up — and the work stops with it"
        figure={null}
        unit="HTTP 429 rate-limit"
        foot="a real one, captured · see promo/captures/"
        delay={4}
      >
        <StallRows frame={useCurrentFrame()} delay={22} />
      </PanelShell>
    </SceneBody>
    <Caption name="Problem 03 caption">
      and then the <Accent>run stops</Accent>
    </Caption>
  </SceneFrame>
);

/**
 * SHEET 00b — the turn. 4 seconds.
 *
 * The whole 60s cut is built to earn these four seconds. Deliberately short, and
 * the only place in the film where it stops complaining and starts proposing.
 */
export const TheTurn: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 00b" title="THE TURN" />
      <SceneBody align="center" gap={34}>
        <Hamster name="Hamster turn" size={200} delay={2} />

        <Interactive.Div
          name="TurnLine"
          style={{
            fontFamily: fontDisplay,
            fontSize: 56,
            lineHeight: 1.4,
            color: palette.ink,
            textAlign: "center",
            opacity: interpolate(frame, [10, 26], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          three problems.
          <br />
          <span style={{ color: palette.torch }}>one address.</span>
        </Interactive.Div>
      </SceneBody>
      <Caption name="TurnCaption">
        that is <Accent>all</Accent> routre does
      </Caption>
    </SceneFrame>
  );
};

/* ------------------------------------------------------------------ shell */

const PanelShell: React.FC<{
  readonly index: string;
  readonly title: string;
  readonly body: string;
  readonly figure: number | null;
  readonly unit: string;
  readonly foot: string;
  readonly delay: number;
  readonly children?: React.ReactNode;
}> = ({ index, title, body, figure, unit, foot, delay, children }) => {
  const frame = useCurrentFrame();

  return (
    <div
      style={{
        position: "relative",
        display: "flex",
        flexDirection: "row",
        alignItems: "center",
        justifyContent: "center",
        gap: 80,
        width: "100%",
      }}
    >
      <Hamster
        name={`Hamster ${index}`}
        size={250}
        delay={delay}
        style={{ flexShrink: 0 }}
      />

      <div style={{ width: 900, flexShrink: 0 }}>
        <div
          style={{
            fontFamily: fontBody,
            fontSize: 26,
            color: palette.steelDim,
            letterSpacing: 2,
            marginBottom: 12,
            opacity: interpolate(frame, [delay, delay + 14], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          {index}
        </div>

        <div
          style={{
            fontFamily: fontBody,
            fontSize: 50,
            fontWeight: 600,
            lineHeight: 1.22,
            color: palette.ink,
            marginBottom: 16,
            opacity: interpolate(frame, [delay + 6, delay + 22], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
            translate: `0px ${interpolate(frame, [delay + 6, delay + 22], [20, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            })}px`,
          }}
        >
          {title}
        </div>

        <div
          style={{
            fontFamily: fontBody,
            fontSize: 29,
            lineHeight: 1.5,
            color: palette.steel,
            marginBottom: 26,
            opacity: interpolate(frame, [delay + 12, delay + 28], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          {body}
        </div>

        {children}

        <div
          style={{
            display: "flex",
            alignItems: "baseline",
            gap: 18,
            marginTop: 22,
            opacity: interpolate(frame, [delay + 30, delay + 46], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          {figure !== null ? (
            <span
              style={{
                fontFamily: fontBody,
                fontSize: 86,
                fontWeight: 600,
                letterSpacing: -3,
                color: palette.torch,
              }}
            >
              <CountUp
                to={figure}
                startFrame={delay + 30}
                durationInFrames={40}
                group
              />
            </span>
          ) : (
            <span
              style={{
                fontFamily: fontBody,
                fontSize: 86,
                fontWeight: 600,
                letterSpacing: -2,
                color: palette.alarm,
              }}
            >
              429
            </span>
          )}
          <span
            style={{
              fontFamily: fontBody,
              fontSize: 28,
              color: palette.steelDim,
            }}
          >
            {unit}
          </span>
        </div>

        <div
          style={{
            fontFamily: fontBody,
            fontSize: 22,
            color: palette.steelDim,
            marginTop: 12,
            opacity: interpolate(frame, [delay + 44, delay + 58], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          {foot}
        </div>
      </div>
    </div>
  );
};

/* ------------------------------------------------------- panel details */

/** Panel 01's illustration: raw tool output, big. */
const FloodBars: React.FC<{ frame: number; delay: number }> = ({
  frame,
  delay,
}) => {
  const rows = [
    { label: "grep", width: 92 },
    { label: "git diff", width: 100 },
    { label: "build log", width: 74 },
  ];

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 9 }}>
      {rows.map((row, i) => {
        const d = delay + i * 7;

        return (
          <div
            key={row.label}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 14,
              opacity: interpolate(frame, [d, d + 12], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <span
              style={{
                fontFamily: fontBody,
                fontSize: 22,
                color: palette.steelDim,
                width: 108,
              }}
            >
              {row.label}
            </span>
            <span
              style={{
                height: 16,
                width: `${interpolate(frame, [d, d + 26], [0, row.width * 6.4], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                  easing: EASE,
                })}px`,
                backgroundColor: palette.steel,
                opacity: 0.42,
              }}
            />
          </div>
        );
      })}
    </div>
  );
};

/** Panel 02's illustration: the same ask, twice, both billed. */
const RepeatRows: React.FC<{ frame: number; delay: number }> = ({
  frame,
  delay,
}) => {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      {[0, 1].map((i) => {
        const d = delay + i * 10;

        return (
          <div
            key={i}
            style={{
              display: "flex",
              alignItems: "center",
              gap: 16,
              fontFamily: fontBody,
              fontSize: 24,
              color: palette.steel,
              opacity: interpolate(frame, [d, d + 12], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <span style={{ color: palette.steelDim, width: 22 }}>
              {i === 0 ? "1st" : "2nd"}
            </span>
            <span style={{ flex: 1, color: palette.hairline }}>──────────</span>
            <span style={{ color: palette.alarm }}>billed in full</span>
          </div>
        );
      })}
    </div>
  );
};

/** Panel 03's illustration: the request hanging. */
const StallRows: React.FC<{ frame: number; delay: number }> = ({
  frame,
  delay,
}) => {
  return (
    <div
      style={{
        fontFamily: fontBody,
        fontSize: 26,
        color: palette.alarm,
        opacity: interpolate(frame, [delay, delay + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        }),
      }}
    >
      <span style={{ color: palette.steelDim }}>$</span> curl -X POST /v1/chat/completions
      <div style={{ marginTop: 12 }}>HTTP 429 · rate-limit</div>
      <div style={{ marginTop: 12, color: palette.steelDim }}>
        <span
          style={{
            opacity: interpolate(
              frame,
              [delay + 20, delay + 26, delay + 32, delay + 38],
              [1, 0.2, 1, 0.2],
              { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
            ),
          }}
        >
          retrying…
        </span>{" "}
        retrying… retrying…
      </div>
    </div>
  );
};
