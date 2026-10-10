import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, bench, fontBody, fontMono, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { CountUp } from "../primitives/Type";
import { Plate } from "../primitives/Plate";

/**
 * SHEET 03 — Compress. 4.5 seconds, and the first beat that has to be *true*.
 *
 * Every number here is transcribed from `promo/captures/bench.txt`, a verbatim
 * `make bench` capture. Nothing is invented and nothing is rounded in a
 * flattering direction:
 *
 *   aggregate TOOL reduction : 91.2%  (106150 → 8677 est. tokens)  [gate 90%]
 *   per-payload worst        : 90.3% (git-diff.json)
 *   result: PASS
 *
 * The bench's printed aggregate is the arithmetic *mean* of the per-payload
 * percentages, so this scene shows both — the five rows that produce the mean,
 * and the pooled token counts — rather than asking the viewer to trust a lone
 * percentage. See BRIEF.md §4.
 */
export const Compress: React.FC = () => {
  const frame = useCurrentFrame();

  const rows = bench.perPayload;
  const rowHeight = 74;
  const barLeft = 520;
  const barWidth = 760;

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 03" title="COMPRESS" />
      <SceneBody>
        <Plate name="BenchPlate" delay={0} style={{ width: "100%" }}>
          {/* ---- table header ---- */}
          <Interactive.Div
            name="BenchHead"
            style={{
              display: "flex",
              alignItems: "center",
              padding: "18px 32px",
              borderBottom: `1px solid ${palette.hairlineSoft}`,
              fontFamily: fontBody,
              fontSize: 22,
              color: palette.steelDim,
            }}
          >
            <span style={{ width: barLeft }}>payload</span>
            <span style={{ width: barWidth }}>tool-token reduction</span>
          </Interactive.Div>

          {/* ---- one row per captured payload ---- */}
          <div style={{ padding: "8px 32px 18px" }}>
            {rows.map((row, i) => {
              const delay = 10 + i * 5;

              return (
                <Interactive.Div
                  key={row.file}
                  name={`Row ${row.file}`}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    height: rowHeight,
                    opacity: interpolate(frame, [delay, delay + 10], [0, 1], {
                      extrapolateLeft: "clamp",
                      extrapolateRight: "clamp",
                      easing: EASE,
                    }),
                  }}
                >
                  <span
                    style={{
                      width: barLeft,
                      fontFamily: fontBody,
                      fontSize: 26,
                      color: palette.ink,
                    }}
                  >
                    {row.file}
                  </span>

                  {/* The bar is 0–100 %, not rescaled: the point of the beat is
                      that *every* payload clears 90 %, so the differences
                      between payloads are not the story and the gate line is. */}
                  <span
                    style={{
                      width: barWidth,
                      display: "flex",
                      alignItems: "center",
                      gap: 18,
                    }}
                  >
                    <span
                      style={{
                        width: barWidth - 120,
                        height: 12,
                        backgroundColor: palette.hairline,
                        opacity: 0.5,
                        position: "relative",
                      }}
                    >
                      <span
                        style={{
                          position: "absolute",
                          inset: 0,
                          width: `${interpolate(
                            frame,
                            [delay + 4, delay + 24],
                            [0, row.toolPct],
                            {
                              extrapolateLeft: "clamp",
                              extrapolateRight: "clamp",
                              easing: EASE,
                            },
                          )}%`,
                          backgroundColor: palette.accent,
                          boxShadow: `0 0 12px ${palette.accent}55`,
                        }}
                      />
                      {/* the gate, at 90 % of a 0–100 track */}
                      <span
                        style={{
                          position: "absolute",
                          left: `${bench.gate}%`,
                          top: -7,
                          bottom: -7,
                          width: 2,
                          backgroundColor: palette.torch,
                        }}
                      />
                      {i === 0 ? (
                        <span
                          style={{
                            position: "absolute",
                            left: `${bench.gate}%`,
                            top: -32,
                            transform: "translateX(-50%)",
                            fontFamily: fontBody,
                            fontSize: 20,
                            color: palette.torch,
                          }}
                        >
                          gate
                        </span>
                      ) : null}
                    </span>
                    <span
                      style={{
                        fontFamily: fontMono,
                        fontSize: 34,
                        color: palette.ink,
                        width: 100,
                      }}
                    >
                      <CountUp
                        to={row.toolPct}
                        decimals={1}
                        startFrame={delay + 4}
                        durationInFrames={20}
                      />
                      %
                    </span>
                  </span>
                </Interactive.Div>
              );
            })}
          </div>
        </Plate>

        {/* ---- the pooled token counts: a real ratio, no footnote needed ---- */}
        <Interactive.Div
          name="TokenDrop"
          style={{
            display: "flex",
            alignItems: "baseline",
            gap: 26,
            fontFamily: fontBody,
            opacity: interpolate(frame, [40, 54], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <span style={{ fontSize: 44, color: palette.steelDim }}>
            <CountUp
              to={bench.toolTokensBefore}
              startFrame={40}
              durationInFrames={22}
              group
            />
          </span>
          <span style={{ fontSize: 44, color: palette.accent }}>→</span>
          <span style={{ fontSize: 76, fontWeight: 600, color: palette.ink }}>
            <CountUp
              to={bench.toolTokensAfter}
              startFrame={50}
              durationInFrames={26}
              group
            />
          </span>
          <span style={{ fontSize: 32, color: palette.steelDim }}>
            est. tool tokens
          </span>
        </Interactive.Div>

        {/* ---- the mechanism and the guarantee, as chips so neither wraps ---- */}
        <Interactive.Div
          name="Chips"
          style={{
            display: "flex",
            gap: 16,
            opacity: interpolate(frame, [66, 80], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <Chip delay={66} tone="ok">{`${bench.result} · gate ${bench.gate}%`}</Chip>
          <Chip delay={70}>11 filters · no local model</Chip>
          <Chip delay={74}>fails open</Chip>
          <Chip delay={78}>aggregate + per payload</Chip>
        </Interactive.Div>
      </SceneBody>
      <Caption name="CompressCaption">
        tool output shrinks <Accent>before</Accent> it's billed
      </Caption>
    </SceneFrame>
  );
};

/** A short pill — the site's `.pill` idiom, kept to a few words per the copy rules. */
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
