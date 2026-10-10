import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, fontBody, gateway, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { TermDim, TermOk, TermPrompt, Terminal } from "../primitives/Terminal";

/**
 * SHEET 04 — Cache. 4 seconds, and the cheapest mechanism to explain.
 *
 * The whole beat is one request sent twice. What changes is a single response
 * header, and that is the honest picture: the win is not "faster", it is that
 * the second identical ask never reaches a provider at all.
 *
 * Both headers are verbatim from `promo/captures/gateway-evidence.txt`.
 *
 * Deliberately absent: any speed ratio. The capture's miss/hit timings
 * (0.70 ms vs 0.28 ms) were measured against a loopback mock, so a "Nx faster"
 * claim built on them would be an artefact of the harness, not of routre.
 */
export const Cache: React.FC = () => {
  const frame = useCurrentFrame();

  // Kept as plain strings rather than inline JSX text: the payload contains `}`
  // and `/*`, which would terminate a JSX expression early.
  const BODY_L1 = '{"model":"mock-model","messages":[{"role":"user",';
  const BODY_L2 = '"content":"promo cache probe"}]}';

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 04" title="CACHE" />
      <SceneBody gap={40}>
        <Terminal
          name="CacheTerminal"
          title="curl -sD - -X POST /v1/chat/completions"
          tag="twice"
          delay={0}
          bodyFontSize={27}
          bodyHeight={230}
          style={{ maxWidth: 1500 }}
        >
          <div>
            <TermPrompt>
              <TermDim>{BODY_L1}</TermDim>
            </TermPrompt>
          </div>
          <div style={{ paddingLeft: 30 }}>
            <TermDim>{`${BODY_L2}   /* identical, byte for byte */`}</TermDim>
          </div>
          <div style={{ height: 18 }} />

          {/* first ask — a miss, and it went upstream */}
          <div
            style={{
              opacity: interpolate(frame, [14, 30], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <TermDim>{"1st  "}</TermDim>
            <span style={{ color: palette.steel }}>
              {gateway.cache.firstHeader}
            </span>
            <span style={{ color: palette.steelDim }}>
              {"   → relayed to the provider"}
            </span>
          </div>

          {/* second ask — a hit, and nothing left the machine */}
          <div
            style={{
              opacity: interpolate(frame, [48, 64], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
              translate: `0px ${interpolate(frame, [48, 64], [10, 0], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              })}px`,
            }}
          >
            <TermDim>{"2nd  "}</TermDim>
            <TermOk>{gateway.cache.secondHeader}</TermOk>
            <span style={{ color: palette.steelDim }}>
              {"   → replayed from memory, no upstream call"}
            </span>
          </div>
        </Terminal>

        {/* ---- the mechanism, in three chips ---- */}
        <Interactive.Div
          name="CacheChips"
          style={{
            display: "flex",
            gap: 16,
            opacity: interpolate(frame, [76, 92], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <Chip delay={76}>sha256 of the canonical body</Chip>
          <Chip delay={81}>
            {gateway.cache.defaultEntries} entries ·{" "}
            {gateway.cache.defaultTtlHours}h
          </Chip>
          <Chip delay={86} tone="ok">
            streaming replay is byte-identical
          </Chip>
        </Interactive.Div>
      </SceneBody>
      <Caption name="CacheCaption">
        ask twice, <Accent>pay once</Accent>
      </Caption>
    </SceneFrame>
  );
};

const Chip: React.FC<{
  readonly children: React.ReactNode;
  readonly delay: number;
  readonly tone?: "ok";
}> = ({ children, delay, tone }) => {
  const frame = useCurrentFrame();

  return (
    <span
      style={{
        fontFamily: fontBody,
        fontSize: 24,
        color: tone === "ok" ? palette.ok : palette.steel,
        border: `1px solid ${tone === "ok" ? palette.ok : palette.hairline}`,
        borderRadius: 999,
        padding: "8px 20px",
        opacity: interpolate(frame, [delay, delay + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        }),
        translate: `0px ${interpolate(frame, [delay, delay + 12], [10, 0], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        })}px`,
      }}
    >
      {children}
    </span>
  );
};
