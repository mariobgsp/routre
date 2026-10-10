import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, bench, fontBody, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, CountUp, FigLabel } from "../primitives/Type";
import { Plate } from "../primitives/Plate";

/**
 * SHEET 01 — The Bill. 3.5 seconds, and the only loud moment in the whole cut.
 *
 * Direction B: one session, two counters, and the top one runs away. This is
 * the hook, so it has to land in under three seconds and it has to be *true* —
 * which rules out the invented "18,400 vs 1,190 tok" figures the brief first
 * sketched. Both numbers and the ratio between them come from the same captured
 * `make bench` run that Beat 3 shows the evidence for:
 *
 *   aggregate TOOL reduction : 91.2%  (106150 → 8677 est. tokens)
 *
 *   106150 / 8677 = 12.23  →  "12.2× fewer tool tokens"
 *
 * That ratio is arithmetic on the capture, not a marketing figure, and the
 * footnote names the workload so nobody has to guess what was measured.
 */
export const BillHook: React.FC = () => {
  const frame = useCurrentFrame();

  const ratio = bench.toolTokensBefore / bench.toolTokensAfter;

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 01" title="THE BILL" />
      <SceneBody align="center" gap={48}>
        <Interactive.Div
          name="Counters"
          style={{
            display: "flex",
            gap: 56,
            width: "100%",
            justifyContent: "center",
          }}
        >
          <Counter
            name="DirectCounter"
            label="without routre"
            value={bench.toolTokensBefore}
            delay={0}
            tone="waste"
            countStart={0}
          />
          <Counter
            name="RoutedCounter"
            label="through routre"
            value={bench.toolTokensAfter}
            delay={4}
            tone="kept"
            countStart={0}
          />
        </Interactive.Div>

        {/* ---- the ratio, which is the only thing the viewer has to remember ---- */}
        <Interactive.Div
          name="Ratio"
          style={{
            fontFamily: fontBody,
            fontSize: 76,
            fontWeight: 600,
            color: palette.torch,
            opacity: interpolate(frame, [64, 78], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
            translate: `0px ${interpolate(frame, [64, 78], [16, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            })}px`,
          }}
        >
          <CountUp
            to={ratio}
            decimals={1}
            startFrame={64}
            durationInFrames={20}
          />
          × fewer tool tokens
        </Interactive.Div>

        {/* ---- what was actually measured. No footnote, no claim, no video. ---- */}
        <Interactive.Div
          name="Source"
          style={{
            fontFamily: fontBody,
            fontSize: 26,
            color: palette.steelDim,
            opacity: interpolate(frame, [80, 94], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          `routre bench` · 5 tool-heavy payloads · est. tokens
        </Interactive.Div>
      </SceneBody>
      <Caption name="BillCaption">
        the same session, <Accent>sent twice</Accent>
      </Caption>
    </SceneFrame>
  );
};

const Counter: React.FC<{
  readonly label: string;
  readonly value: number;
  readonly delay: number;
  readonly countStart: number;
  readonly tone: "waste" | "kept";
  readonly name: string;
}> = ({ label, value, delay, countStart, tone, name }) => {
  const waste = tone === "waste";

  return (
    <Plate
      name={name}
      delay={delay}
      // The two panels are the opening frame of the whole film: they are on
      // screen from frame 0, before the numbers start running.
      fadeIn={0}
      style={{
        width: 780,
        borderColor: waste ? palette.hairline : palette.accent,
        backgroundColor: waste
          ? "rgba(14, 35, 71, 0.35)"
          : palette.accentSoft,
      }}
    >
      <div style={{ padding: "30px 36px 34px" }}>
        <Interactive.Div
          name={`${name}Label`}
          style={{
            fontFamily: fontBody,
            fontSize: 28,
            color: waste ? palette.steelDim : palette.steel,
          }}
        >
          {label}
        </Interactive.Div>

        <Interactive.Div
          name={`${name}Value`}
          style={{
            fontFamily: fontBody,
            fontSize: 150,
            fontWeight: 600,
            letterSpacing: -3,
            color: waste ? palette.steel : palette.ink,
            marginTop: 10,
          }}
        >
          <CountUp
            to={value}
            startFrame={countStart}
            durationInFrames={46}
            group
          />
        </Interactive.Div>

        <Interactive.Div
          name={`${name}Unit`}
          style={{
            fontFamily: fontBody,
            fontSize: 24,
            color: palette.steelDim,
            marginTop: 6,
          }}
        >
          est. tool tokens
        </Interactive.Div>
      </div>
    </Plate>
  );
};
