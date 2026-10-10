import { Easing, Interactive, interpolate, useCurrentFrame } from "remotion";
import { EASE, fontBody, palette } from "../theme";
import { Led, Plate } from "./Plate";

/**
 * The schematic layer: absolutely positioned nodes and wires on a fixed
 * coordinate grid, so a scene can be authored in plain numbers and stay put
 * frame to frame. Beats 2, 3, 5 and 6 are all drawn with these.
 */
export const Diagram: React.FC<{
  readonly width: number;
  readonly height: number;
  readonly children: React.ReactNode;
  readonly name?: string;
}> = ({ width, height, children, name = "Diagram" }) => {
  return (
    <Interactive.Div
      name={name}
      style={{ position: "relative", width, height, flexShrink: 0 }}
    >
      {children}
    </Interactive.Div>
  );
};

/**
 * A connector. Horizontal when `w` is given, vertical when `h` is.
 * `packet` rides a torch dot along the wire as it draws — travel is linear,
 * because a packet on a wire should not ease.
 */
export const Wire: React.FC<{
  readonly x: number;
  readonly y: number;
  readonly w?: number;
  readonly h?: number;
  readonly delay: number;
  readonly durationInFrames: number;
  readonly thickness?: number;
  readonly tone?: "accent" | "dim" | "alarm" | "ok";
  readonly packet?: boolean;
  readonly name?: string;
}> = ({
  x,
  y,
  w,
  h,
  delay,
  durationInFrames,
  thickness = 3,
  tone = "accent",
  packet = false,
  name = "Wire",
}) => {
  const frame = useCurrentFrame();
  const horizontal = w !== undefined;

  const colour =
    tone === "accent"
      ? palette.accent
      : tone === "alarm"
        ? palette.alarm
        : tone === "ok"
          ? palette.ok
          : palette.hairline;

  const progress = interpolate(
    frame,
    [delay, delay + durationInFrames],
    [0, 100],
    {
      extrapolateLeft: "clamp",
      extrapolateRight: "clamp",
      easing: Easing.linear,
    },
  );

  const dot = thickness + 8;

  return (
    <Interactive.Div
      name={name}
      style={{
        position: "absolute",
        left: x,
        top: y,
        width: horizontal ? w : thickness,
        height: horizontal ? thickness : h,
      }}
    >
      {/* the undrawn track, so the route is legible before it carries traffic */}
      <div
        style={{
          position: "absolute",
          inset: 0,
          backgroundColor: tone === "dim" ? palette.steel : palette.hairline,
          opacity: tone === "dim" ? 0.45 : 0.7,
        }}
      />
      <div
        style={{
          position: "absolute",
          left: 0,
          top: 0,
          bottom: horizontal ? undefined : 0,
          width: horizontal ? `${progress}%` : thickness,
          height: horizontal ? thickness : `${progress}%`,
          backgroundColor: colour,
          boxShadow: `0 0 14px ${colour}66`,
        }}
      />
      {packet ? (
        <div
          style={{
            position: "absolute",
            left: horizontal ? `${progress}%` : -dot / 2 + thickness / 2,
            top: horizontal ? -dot / 2 + thickness / 2 : `${progress}%`,
            width: dot,
            height: dot,
            marginLeft: horizontal ? -dot / 2 : 0,
            marginTop: horizontal ? 0 : -dot / 2,
            borderRadius: "50%",
            backgroundColor: palette.torch,
            boxShadow: `0 0 16px ${palette.torch}`,
            // Arrived means delivered: the dot fades rather than sitting on the
            // wire looking like a stuck pixel.
            opacity: interpolate(
              frame,
              [delay + durationInFrames, delay + durationInFrames + 6],
              [1, 0],
              { extrapolateLeft: "clamp", extrapolateRight: "clamp" },
            ),
          }}
        />
      ) : null}
    </Interactive.Div>
  );
};

/**
 * A node on the schematic. `accent` swaps the hairline border for the accent
 * one — reserved for the node the whole cut is about.
 */
export const Node: React.FC<{
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly label: string;
  readonly sub?: string;
  readonly tone?: "plain" | "accent";
  readonly led?: "ok" | "alarm" | "dim";
  readonly delay?: number;
  readonly labelSize?: number;
  readonly subSize?: number;
  readonly name?: string;
}> = ({
  x,
  y,
  w,
  label,
  sub,
  tone = "plain",
  led,
  delay = 0,
  labelSize = 30,
  subSize = 22,
  name = "Node",
}) => {
  return (
    <div style={{ position: "absolute", left: x, top: y, width: w }}>
      <Plate
        name={name}
        delay={delay}
        style={
          tone === "accent"
            ? { borderColor: palette.accent, backgroundColor: palette.accentSoft }
            : undefined
        }
      >
        <div
          style={{
            padding: "16px 22px",
            display: "flex",
            alignItems: "center",
            gap: 14,
          }}
        >
          {led ? <Led tone={led} delay={delay + 10} size={10} /> : null}
          <div style={{ minWidth: 0 }}>
            <div
              style={{
                fontFamily: fontBody,
                fontSize: labelSize,
                fontWeight: 500,
                color: tone === "accent" ? palette.torch : palette.ink,
                whiteSpace: "nowrap",
              }}
            >
              {label}
            </div>
            {sub ? (
              <div
                style={{
                  fontFamily: fontBody,
                  fontSize: subSize,
                  color: palette.steelDim,
                  whiteSpace: "nowrap",
                  marginTop: 4,
                }}
              >
                {sub}
              </div>
            ) : null}
          </div>
        </div>
      </Plate>
    </div>
  );
};

/** A label attached to a wire, e.g. the address a node is reachable at. */
export const WireLabel: React.FC<{
  readonly x: number;
  readonly y: number;
  readonly children: React.ReactNode;
  readonly delay?: number;
  readonly align?: "left" | "center";
  readonly size?: number;
  readonly colour?: string;
  readonly name?: string;
}> = ({
  x,
  y,
  children,
  delay = 0,
  align = "left",
  size = 26,
  colour = palette.steelDim,
  name = "WireLabel",
}) => {
  const frame = useCurrentFrame();

  return (
    <Interactive.Div
      name={name}
      style={{
        position: "absolute",
        left: x,
        top: y,
        width: 640,
        textAlign: align,
        fontFamily: fontBody,
        fontSize: size,
        color: colour,
        opacity: interpolate(frame, [delay, delay + 12], [0, 1], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: EASE,
        }),
      }}
    >
      {children}
    </Interactive.Div>
  );
};
