import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, GATEWAY, fontBody, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel, typed } from "../primitives/Type";
import { Diagram, Node, Wire, WireLabel } from "../primitives/Schematic";

/**
 * SHEET 02 — Reveal. 2.5 seconds: four apps, one thread, one address.
 *
 * This is the first Blueprint beat after the Bill hook, and the hinge of the
 * whole cut: the top half of the split screen is gone and what remains redraws
 * as the schematic. Everything after this scene is technical and quiet, so the
 * only loud thing it may contain is the one orange thread.
 *
 * The clients are the real ones the README names — opencode, Claude Code,
 * Codex, Cursor. The address is `GATEWAY` from `theme.ts`, not a literal.
 */
export const Reveal: React.FC = () => {
  const frame = useCurrentFrame();

  const clients = [
    { label: "opencode", sub: "OPENAI_BASE_URL" },
    { label: "claude code", sub: "ANTHROPIC_BASE_URL" },
    { label: "codex", sub: "OPENAI_BASE_URL" },
    { label: "cursor", sub: "any OpenAI CLI" },
  ];

  const nodeTop = 44;
  const nodeHeight = 104;
  const nodeGap = 44;
  const busX = 430;
  const busBottom =
    nodeTop + clients.length * nodeHeight + (clients.length - 1) * nodeGap;
  const midY = nodeTop + nodeHeight / 2 + ((busBottom - nodeTop - nodeHeight) / 2);

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 02" title="REVEAL" />
      <SceneBody align="center">
        <Interactive.Div
          name="SectionLabel"
          style={{
            fontFamily: fontBody,
            fontSize: 28,
            letterSpacing: 2,
            color: palette.steelDim,
            marginBottom: 36,
            textTransform: "uppercase",
            opacity: interpolate(frame, [0, 10], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          your agents
        </Interactive.Div>

        <Diagram width={1300} height={660}>
          {/* the four apps, landing in sequence */}
          {clients.map((client, i) => (
            <Node
              key={client.label}
              name={`Client ${client.label}`}
              x={0}
              y={nodeTop + i * (nodeHeight + nodeGap)}
              w={400}
              label={client.label}
              sub={client.sub}
              subSize={19}
              delay={2 + i * 3}
            />
          ))}

          {/* stub wires from each app into the bus */}
          {clients.map((client, i) => (
            <Wire
              key={`stub-${client.label}`}
              name={`Stub ${client.label}`}
              x={400}
              y={nodeTop + i * (nodeHeight + nodeGap) + nodeHeight / 2 - 1}
              w={busX - 400}
              delay={10 + i * 3}
              durationInFrames={8}
              thickness={2}
              tone="dim"
            />
          ))}

          {/* the bus, then the one thread that matters */}
          <Wire
            name="Bus"
            x={busX - 1}
            y={nodeTop + nodeHeight / 2 - 1}
            h={busBottom - (nodeTop + nodeHeight / 2)}
            delay={24}
            durationInFrames={10}
            thickness={2}
            tone="dim"
          />
          <Wire
            name="MainThread"
            x={busX}
            y={midY - 2}
            w={440}
            delay={32}
            durationInFrames={16}
            packet
          />

          {/* the node the whole pitch is about */}
          <Node
            name="RoutreNode"
            x={870}
            y={midY - 56}
            w={360}
            label="routre"
            sub="loopback gateway"
            tone="accent"
            led="ok"
            delay={40}
            labelSize={40}
            subSize={20}
          />

          <WireLabel
            name="Address"
            x={870}
            y={midY + 78}
            delay={46}
            size={30}
            colour={palette.ink}
          >
            {typed(frame, GATEWAY, 46, 40)}
            <span
              style={{
                color: palette.accent,
                opacity: Math.floor(frame / 15) % 2 === 0 ? 1 : 0,
              }}
            >
              ▌
            </span>
          </WireLabel>
        </Diagram>
      </SceneBody>
      <Caption name="RevealCaption">
        point your agent at <Accent>one address</Accent>
      </Caption>
    </SceneFrame>
  );
};
