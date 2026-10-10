import { Interactive, useCurrentFrame } from "remotion";
import { bench, footprint, latency } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { rise } from "../timing";
import { LiveChip, StatCard } from "./Wheel";

/**
 * SHEET 06 — Stats. 4.5 seconds, three numbers, and each one carries the clause
 * that makes it true.
 *
 * This is the beat a sceptical viewer checks, so it does not show three bare
 * percentages. Every figure comes from a captured artifact in `promo/captures/`
 * and is qualified on screen:
 *
 *   91.2%   tool tokens removed   mean of 5 payloads · gate 90% · PASS
 *   12.6 MiB idle RAM             30s sample · zero growth
 *   ~26 ms  added, 1 MiB body     p99 ~28 ms
 *
 * The RAM figure is the captured 12.6 MiB, not the ~10 MiB that older docs
 * claimed — see the note in `theme.ts`.
 */
export const Stats: React.FC = () => {
  const frame = useCurrentFrame();

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 06" title="MEASURED" />
      <SceneBody align="center" gap={40}>
        <Interactive.Div
          name="Stats"
          style={{
            display: "flex",
            gap: 48,
            width: "100%",
            justifyContent: "center",
          }}
        >
          <StatCard
            name="ToolTokens"
            delay={0}
            value={`${bench.toolReduction}%`}
            label="tool tokens removed"
            qualifier={`mean of ${bench.perPayload.length} payloads · gate ${bench.gate}% · ${bench.result}`}
            accent
          />
          <StatCard
            name="IdleRam"
            delay={14}
            value={`${footprint.idleRssMiB} MiB`}
            label="idle RAM"
            qualifier={`${footprint.sampleSeconds}s sample · ${footprint.growthMiB} MiB growth`}
          />
          <StatCard
            name="Latency"
            delay={28}
            value={`~${latency.p50Ms} ms`}
            label={`added, ${latency.bodySize} body`}
            qualifier={`p99 ~${latency.p99Ms} ms`}
          />
        </Interactive.Div>

        {/* ---- where every number above came from ---- */}
        <Interactive.Div
          name="Provenance"
          style={{
            display: "flex",
            gap: 16,
            ...rise(frame, 66, 16, 22),
          }}
        >
          <LiveChip delay={66}>make bench</LiveChip>
          <LiveChip delay={70}>scripts/measure-ram.sh</LiveChip>
          <LiveChip delay={74}>bench=GatewayAddedLatency1MB</LiveChip>
        </Interactive.Div>
      </SceneBody>
      <Caption name="StatsCaption">
        <Accent>measured</Accent>, not promised
      </Caption>
    </SceneFrame>
  );
};


