import { Interactive, interpolate, useCurrentFrame } from "remotion";
import {
  EASE,
  bench,
  footprint,
  fontBody,
  gateway,
  latency,
  palette,
} from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, CountUp, FigLabel } from "../primitives/Type";
import { Led, Plate } from "../primitives/Plate";
import { Diagram, Node, Wire } from "../primitives/Schematic";

/**
 * The three vertical beats of the 15s bumper, in 1080×1920.
 *
 * Same captured numbers as the hero cut, re-laid-out for a phone. They live in
 * one file because they share the vertical geometry (936px content column, 72px
 * margins) and there are only three.
 *
 * Beats, per §9 of the brief:
 *   1.  0– 3s  the same two counters, stacked instead of side by side
 *   2.  3– 7s  failover, stacked
 *   3.  7–11s  the three measured figures, stacked
 */

const PAD_X = 72;
const PAD_Y = 180;

/* ------------------------------------------------------------------ beat 1 */

export const BumperBill: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <SceneFrame padX={PAD_X} padY={PAD_Y}>
      <FigLabel fig="SHEET 01" title="THE BILL" />
      <SceneBody align="center" gap={44}>
        <Counter
          name="BumperDirect"
          label="without routre"
          value={bench.toolTokensBefore}
          waste
          delay={0}
        />
        <Counter
          name="BumperRouted"
          label="through routre"
          value={bench.toolTokensAfter}
          delay={8}
        />
        <Interactive.Div
          name="BumperRatio"
          style={{
            fontFamily: fontBody,
            fontSize: 84,
            fontWeight: 600,
            color: palette.torch,
            opacity: interpolate(frame, [40, 56], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <CountUp
            to={bench.toolTokensBefore / bench.toolTokensAfter}
            decimals={1}
            startFrame={40}
            durationInFrames={20}
          />
          × fewer tool tokens
        </Interactive.Div>
        <Interactive.Div
          name="BumperSource"
          style={{
            fontFamily: fontBody,
            fontSize: 24,
            color: palette.steelDim,
            opacity: interpolate(frame, [58, 72], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          `routre bench` · 5 tool-heavy payloads
        </Interactive.Div>
      </SceneBody>
      <Caption name="BumperBillCaption">
        the same session, <Accent>sent twice</Accent>
      </Caption>
    </SceneFrame>
  );
};

const Counter: React.FC<{
  readonly name: string;
  readonly label: string;
  readonly value: number;
  readonly delay: number;
  readonly waste?: boolean;
}> = ({ name, label, value, delay, waste }) => {
  return (
    <Plate
      name={name}
      delay={delay}
      fadeIn={0}
      style={{
        width: "100%",
        borderColor: waste ? palette.hairline : palette.accent,
        backgroundColor: waste
          ? "rgba(14, 35, 71, 0.35)"
          : palette.accentSoft,
      }}
    >
      <div style={{ padding: "28px 32px 30px" }}>
        <div
          style={{
            fontFamily: fontBody,
            fontSize: 26,
            color: waste ? palette.steelDim : palette.steel,
          }}
        >
          {label}
        </div>
        <div
          style={{
            fontFamily: fontBody,
            fontSize: 96,
            fontWeight: 600,
            letterSpacing: -2,
            color: waste ? palette.steel : palette.ink,
            marginTop: 6,
          }}
        >
          <CountUp to={value} startFrame={delay} durationInFrames={34} group />
        </div>
        <div
          style={{
            fontFamily: fontBody,
            fontSize: 22,
            color: palette.steelDim,
            marginTop: 4,
          }}
        >
          est. tool tokens
        </div>
      </div>
    </Plate>
  );
};

/* ------------------------------------------------------------------ beat 2 */

export const BumperFailover: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <SceneFrame padX={PAD_X} padY={PAD_Y}>
      <FigLabel fig="SHEET 05" title="FAILOVER" />
      <SceneBody align="center">
        <Diagram width={936} height={1180}>
          <WireLabelTop text="tier 1 · primary" y={0} delay={0} />
          <Node
            name="BumperProviderA"
            x={68}
            y={48}
            w={800}
            label={gateway.failover.failedProvider}
            sub="primary"
            delay={0}
          />
          <Interactive.Div
            name="BumperRateLimited"
            style={{
              position: "absolute",
              left: 68,
              top: 168,
              fontFamily: fontBody,
              fontSize: 28,
              color: palette.alarm,
              display: "flex",
              alignItems: "center",
              gap: 12,
              opacity: interpolate(frame, [16, 30], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <Led tone="alarm" delay={16} size={12} />
            429 rate-limit
          </Interactive.Div>
          <Interactive.Div
            name="BumperCooldown"
            style={{
              position: "absolute",
              left: 68,
              top: 214,
              fontFamily: fontBody,
              fontSize: 28,
              color: palette.steel,
              opacity: interpolate(frame, [28, 42], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            cooldown {gateway.failover.cooldown} · fails=
            {gateway.failover.failures}
          </Interactive.Div>

          {/* the wire is the failover: drawn only once tier 1 is cooling */}
          <Wire
            name="BumperFailoverWire"
            x={468}
            y={262}
            h={330}
            delay={44}
            durationInFrames={24}
            packet
          />

          <WireLabelTop text="tier 2 · backup" y={610} delay={44} />
          <Node
            name="BumperProviderB"
            x={68}
            y={658}
            w={800}
            label={gateway.failover.servedBy}
            sub="backup"
            tone="accent"
            led="ok"
            delay={50}
          />
          <Interactive.Div
            name="BumperClientResult"
            style={{
              position: "absolute",
              left: 68,
              top: 792,
              fontFamily: fontBody,
              opacity: interpolate(frame, [74, 90], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <div
              style={{
                display: "flex",
                alignItems: "baseline",
                gap: 24,
              }}
            >
              <div style={{ fontSize: 104, fontWeight: 600, color: palette.ok }}>
                {gateway.failover.clientStatus}
              </div>
              <div style={{ fontSize: 30, color: palette.steelDim, maxWidth: 460 }}>
                — the client never saw the 429
              </div>
            </div>
          </Interactive.Div>
          <Interactive.Div
            name="BumperFailoverPolicy"
            style={{
              position: "absolute",
              left: 68,
              top: 966,
              fontFamily: fontBody,
              fontSize: 26,
              color: palette.steelDim,
              opacity: interpolate(frame, [90, 106], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            cooldown {gateway.failover.policy} · Retry-After honoured
          </Interactive.Div>
        </Diagram>
      </SceneBody>
      <Caption name="BumperFailoverCaption">
        busy provider, <Accent>next provider</Accent>
      </Caption>
    </SceneFrame>
  );
};

/** A small centred section label for the vertical layout. */
const WireLabelTop: React.FC<{
  readonly text: string;
  readonly y: number;
  readonly delay: number;
}> = ({ text, y, delay }) => {
  const frame = useCurrentFrame();

  return (
    <Interactive.Div
      name={`Label ${text}`}
      style={{
        position: "absolute",
        left: 0,
        top: y,
        width: 936,
        textAlign: "center",
        fontFamily: fontBody,
        fontSize: 26,
        color: palette.steelDim,
        letterSpacing: 1,
        opacity: interpolate(frame, [delay, delay + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        }),
      }}
    >
      {text}
    </Interactive.Div>
  );
};

/* ------------------------------------------------------------------ beat 3 */

export const BumperStats: React.FC = () => {
  const frame = useCurrentFrame();

  const stats = [
    {
      name: "BumperToolTokens",
      value: `${bench.toolReduction}%`,
      label: "tool tokens removed",
      qualifier: `mean of ${bench.perPayload.length} payloads · ${bench.result}`,
      accent: true,
    },
    {
      name: "BumperIdleRam",
      value: `${footprint.idleRssMiB} MiB`,
      label: "idle RAM",
      qualifier: `${footprint.sampleSeconds}s sample · ${footprint.growthMiB} MiB growth`,
      accent: false,
    },
    {
      name: "BumperLatency",
      value: `~${latency.p50Ms} ms`,
      label: `added, ${latency.bodySize} body`,
      qualifier: `p99 ~${latency.p99Ms} ms`,
      accent: false,
    },
  ];

  return (
    <SceneFrame padX={PAD_X} padY={PAD_Y}>
      <FigLabel fig="SHEET 06" title="MEASURED" />
      <SceneBody align="center" gap={36}>
        {stats.map((stat, i) => {
          const delay = i * 16;

          return (
            <Plate
              key={stat.name}
              name={stat.name}
              delay={delay}
              style={{
                width: "100%",
                borderColor: stat.accent ? palette.accent : palette.hairlineSoft,
                backgroundColor: stat.accent
                  ? palette.accentSoft
                  : "rgba(14, 35, 71, 0.45)",
              }}
            >
              <div style={{ padding: "26px 32px 24px" }}>
                <div
                  style={{
                    fontFamily: fontBody,
                    fontSize: 80,
                    fontWeight: 600,
                    letterSpacing: -2,
                    color: stat.accent ? palette.torch : palette.ink,
                  }}
                >
                  {stat.value}
                </div>
                <div
                  style={{
                    fontFamily: fontBody,
                    fontSize: 27,
                    color: palette.steel,
                    marginTop: 4,
                  }}
                >
                  {stat.label}
                </div>
                <div
                  style={{
                    fontFamily: fontBody,
                    fontSize: 21,
                    color: palette.steelDim,
                    marginTop: 12,
                    paddingTop: 12,
                    borderTop: `1px solid ${palette.hairlineSoft}`,
                    lineHeight: 1.45,
                  }}
                >
                  {stat.qualifier}
                </div>
              </div>
            </Plate>
          );
        })}

        <Interactive.Div
          name="BumperProvenance"
          style={{
            fontFamily: fontBody,
            fontSize: 22,
            color: palette.steelDim,
            opacity: interpolate(frame, [64, 80], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          make bench · measure-ram.sh · GatewayAddedLatency1MB
        </Interactive.Div>
      </SceneBody>
      <Caption name="BumperStatsCaption">
        <Accent>measured</Accent>, not promised
      </Caption>
    </SceneFrame>
  );
};