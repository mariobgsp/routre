import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, fontBody, gateway, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { Diagram, Node, Wire, WireLabel } from "../primitives/Schematic";
import { Led } from "../primitives/Plate";

/**
 * SHEET 05 — Failover. 5 seconds, the longest beat, because it is the one people
 * arrive for: free tiers return 429 constantly and a coding agent cannot stop.
 *
 * The captured facts, all from `promo/captures/gateway-evidence.txt`:
 *   - provider A answers a real HTTP 429
 *   - the client still receives 200, served by the next provider
 *   - A enters cooldown — `mock-a cooldown 2s (fails=1)`, visible in both
 *     /v1/status and `routre list`
 *   - the failure is counted at `routre_upstream_failures_total{…,class="rate-limit"}`
 *
 * The cooldown shown is the 2s base. The 5m cap is a constant in
 * `internal/router/classify.go` and is stated as a policy, not shown growing —
 * reaching the cap takes nine stacked failures, which no 5-second beat can show
 * without lying about the clock.
 */
export const Failover: React.FC = () => {
  const frame = useCurrentFrame();

  const nodeW = 360;
  const aX = 40;
  const bX = 700;
  const nodeY = 90;

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 05" title="FAILOVER" />
      <SceneBody align="center">
        <Diagram width={1400} height={420}>
          <WireLabel name="Tier1Label" x={aX} y={nodeY - 44} delay={0}>
            tier 1 · primary
          </WireLabel>
          <WireLabel name="Tier2Label" x={bX} y={nodeY - 44} delay={30}>
            tier 2 · backup
          </WireLabel>

          <Node
            name="ProviderA"
            x={aX}
            y={nodeY}
            w={nodeW}
            label={gateway.failover.failedProvider}
            sub="primary"
            delay={0}
          />

          {/* the 429 lands: node dims, and states the class rather than only
              colouring it red (colour alone is never the whole message) */}
          <Interactive.Div
            name="RateLimited"
            style={{
              position: "absolute",
              left: aX,
              top: nodeY + 118,
              fontFamily: fontBody,
              fontSize: 24,
              color: palette.alarm,
              display: "flex",
              alignItems: "center",
              gap: 10,
              opacity: interpolate(frame, [14, 28], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <Led tone="alarm" delay={14} size={10} />
            429 rate-limit
          </Interactive.Div>

          {/* the cooldown the router actually applied, as captured */}
          <Interactive.Div
            name="Cooldown"
            style={{
              position: "absolute",
              left: aX,
              top: nodeY + 156,
              fontFamily: fontBody,
              fontSize: 24,
              color: palette.steel,
              opacity: interpolate(frame, [26, 40], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            cooldown {gateway.failover.cooldown} · fails=
            {gateway.failover.failures}
          </Interactive.Div>

          {/* The wire is the failover: it does not exist while A is being tried,
              and draws from A to B once A is cooling down. */}
          <Wire
            name="FailoverWire"
            x={aX + nodeW}
            y={nodeY + 48}
            w={bX - (aX + nodeW)}
            delay={32}
            durationInFrames={20}
            packet
          />

          <Node
            name="ProviderB"
            x={bX}
            y={nodeY}
            w={nodeW}
            label={gateway.failover.servedBy}
            sub="backup"
            tone="accent"
            led="ok"
            delay={38}
          />

          {/* what the client actually got */}
          <Interactive.Div
            name="ClientResult"
            style={{
              position: "absolute",
              left: bX + nodeW + 40,
              top: nodeY,
              width: 260,
              fontFamily: fontBody,
              opacity: interpolate(frame, [58, 74], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <div style={{ fontSize: 84, fontWeight: 600, color: palette.ok }}>
              {gateway.failover.clientStatus}
            </div>
            <div
              style={{
                fontSize: 24,
                color: palette.steelDim,
                marginTop: 6,
                lineHeight: 1.4,
              }}
            >
              the client never saw the 429
            </div>
          </Interactive.Div>
        </Diagram>

        <Interactive.Div
          name="FailoverChips"
          style={{
            display: "flex",
            gap: 16,
            marginTop: 48,
            opacity: interpolate(frame, [96, 112], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <Chip delay={96}>tiered order · subscription → cheap → free</Chip>
          <Chip delay={101}>cooldown {gateway.failover.policy} per provider</Chip>
          <Chip delay={106}>Retry-After honoured</Chip>
        </Interactive.Div>
      </SceneBody>
      <Caption name="FailoverCaption">
        busy provider, <Accent>next provider</Accent>
      </Caption>
    </SceneFrame>
  );
};

const Chip: React.FC<{
  readonly children: React.ReactNode;
  readonly delay: number;
}> = ({ children, delay }) => {
  const frame = useCurrentFrame();

  return (
    <span
      style={{
        fontFamily: fontBody,
        fontSize: 24,
        color: palette.steel,
        border: `1px solid ${palette.hairline}`,
        borderRadius: 999,
        padding: "8px 20px",
        opacity: interpolate(frame, [delay, delay + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        }),
      }}
    >
      {children}
    </span>
  );
};
