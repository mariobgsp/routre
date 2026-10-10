import { Interactive } from "remotion";
import { fontBody, fontMono, palette } from "../theme";
import { Plate } from "./Plate";

/**
 * The terminal window from the landing page: three dots, a title, a tag, and a
 * body. Used for the install command, and later for `routre list` and the
 * bench output — so those beats read as one system.
 *
 * `body` is a list of already-styled rows rather than a string, because most
 * rows carry per-token colour (prompts, values, dimmed comments).
 */
export const Terminal: React.FC<{
  readonly title: string;
  readonly tag: string;
  readonly children: React.ReactNode;
  readonly name?: string;
  readonly delay?: number;
  readonly bodyFontSize?: number;
  /** Pins the body's height so a typing command can't reflow the frame. */
  readonly bodyHeight?: number;
  readonly style?: React.CSSProperties;
}> = ({
  title,
  tag,
  children,
  name = "Terminal",
  delay = 0,
  bodyFontSize = 30,
  bodyHeight,
  style,
}) => {
  return (
    <Plate name={name} delay={delay} style={{ width: "100%", ...style }}>
      <Interactive.Div
        name="TermHead"
        style={{
          display: "flex",
          alignItems: "center",
          gap: 16,
          padding: "14px 24px",
          borderBottom: `1px solid ${palette.hairlineSoft}`,
        }}
      >
        <span style={{ display: "flex", gap: 8 }}>
          <Dot colour="#e2554c" />
          <Dot colour="#ca8a04" />
          <Dot colour="#7bc96f" />
        </span>
        <span
          style={{
            fontFamily: fontMono,
            fontSize: 26,
            color: palette.steel,
          }}
        >
          {title}
        </span>
        <span
          style={{
            fontFamily: fontBody,
            fontSize: 20,
            color: palette.steelDim,
            border: `1px solid ${palette.hairline}`,
            padding: "2px 10px",
            marginLeft: "auto",
          }}
        >
          {tag}
        </span>
      </Interactive.Div>
      <Interactive.Div
        name="TermBody"
        style={{
          fontFamily: fontBody,
          fontSize: bodyFontSize,
          lineHeight: 1.65,
          color: palette.ink,
          padding: "26px 28px",
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
          minHeight: bodyHeight,
        }}
      >
        {children}
      </Interactive.Div>
    </Plate>
  );
};

const Dot: React.FC<{ readonly colour: string }> = ({ colour }) => {
  return (
    <span
      style={{
        display: "inline-block",
        width: 12,
        height: 12,
        borderRadius: "50%",
        backgroundColor: colour,
      }}
    />
  );
};

/** A `$`-prefixed command row. */
export const TermPrompt: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return (
    <div>
      <span style={{ color: palette.steelDim, marginRight: 14 }}>$</span>
      <span>{children}</span>
    </div>
  );
};

/** A dimmed comment or unit label. */
export const TermDim: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return <span style={{ color: palette.steelDim }}>{children}</span>;
};

/** An accent-coloured value. */
export const TermAccent: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return <span style={{ color: palette.torch }}>{children}</span>;
};

/** A healthy value. */
export const TermOk: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return <span style={{ color: palette.ok }}>{children}</span>;
};

/** A blocked or failed value. */
export const TermAlarm: React.FC<{ readonly children: React.ReactNode }> = ({
  children,
}) => {
  return <span style={{ color: palette.alarm }}>{children}</span>;
};
