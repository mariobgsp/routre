import { Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, fontBody, gateway, palette } from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Caption, Accent, FigLabel } from "../primitives/Type";
import { TermDim, TermOk, Terminal } from "../primitives/Terminal";

/**
 * SHEET 07 — Ledger. 3 seconds. The "prove it to yourself" beat, and the one
 * that gets cut first if the film runs long.
 *
 * The rows are the real `routre list` output from
 * `promo/captures/gateway-evidence.txt`, from the loopback harness — so the
 * agent name, request count and token split are genuine, and the *shape* is what
 * a real session shows. Two things are deliberately not on screen:
 *
 *   - the dollar figure. With mock providers the cost is computed from the
 *     config's price_in/price_out, so it is arithmetic, not a bill.
 *   - the footer line, which says "you would have paid" — true of the harness,
 *     misleading next to mock numbers.
 *
 * What survives is the thing a user actually wants: per-agent requests, tokens
 * consumed, and tokens saved, broken out.
 */
export const Ledger: React.FC = () => {
  const frame = useCurrentFrame();

  const rows = [
    { key: "requests", label: "requests", value: `${gateway.ledger.requests}` },
    {
      key: "consumed",
      label: "consumed",
      value: `${gateway.ledger.consumedIn + gateway.ledger.consumedOut} tokens (${gateway.ledger.consumedIn} in + ${gateway.ledger.consumedOut} out)`,
    },
    {
      key: "saved",
      label: "saved",
      value: `${gateway.ledger.savedCache} tokens (rtk ${gateway.ledger.savedRtk} + cache ${gateway.ledger.savedCache})`,
    },
  ];

  return (
    <SceneFrame>
      <FigLabel fig="SHEET 07" title="LEDGER" />
      <SceneBody gap={36}>
        <Terminal
          name="LedgerTerminal"
          title="routre list"
          tag="live"
          delay={0}
          bodyFontSize={28}
          bodyHeight={250}
          style={{ maxWidth: 1440 }}
        >
          <div style={{ color: palette.steelDim }}>== token & cost ledger ==</div>
          <div style={{ height: 10 }} />
          <div style={{ color: palette.ink }}>
            {gateway.ledger.agent}
          </div>
          <div style={{ height: 10 }} />

          {rows.map((row, i) => (
            <Interactive.Div
              key={row.key}
              name={`Ledger ${row.key}`}
              style={{
                display: "flex",
                gap: 18,
                opacity: interpolate(frame, [10 + i * 9, 24 + i * 9], [0, 1], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                  easing: EASE,
                }),
                translate: `0px ${interpolate(frame, [10 + i * 9, 24 + i * 9], [12, 0], {
                  extrapolateLeft: "clamp",
                  extrapolateRight: "clamp",
                  easing: EASE,
                })}px`,
              }}
            >
              <span
                style={{
                  width: 160,
                  textAlign: "right",
                  color: palette.steelDim,
                }}
              >
                {row.label}:
              </span>
              <span style={{ color: row.key === "saved" ? palette.ok : palette.ink }}>
                {row.key === "saved" ? <TermOk>{row.value}</TermOk> : row.value}
              </span>
            </Interactive.Div>
          ))}

          <div style={{ height: 12 }} />
          <div
            style={{
              opacity: interpolate(frame, [46, 60], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          >
            <TermDim>
              {"per agent, by User-Agent · survives restarts · works offline"}
            </TermDim>
          </div>
        </Terminal>

        <Interactive.Div
          name="LedgerChips"
          style={{
            display: "flex",
            gap: 16,
            opacity: interpolate(frame, [60, 74], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <Chip delay={60}>tokens in / out</Chip>
          <Chip delay={64}>rtk + cache savings</Chip>
          <Chip delay={68}>per provider / model</Chip>
        </Interactive.Div>
      </SceneBody>
      <Caption name="LedgerCaption">
        see <Accent>every</Accent> token
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
