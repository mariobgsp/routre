import { AbsoluteFill, Interactive } from "remotion";
import { safe } from "../theme";
import { BlueprintBackdrop, Scanlines } from "./Backdrop";

/**
 * The shared scene layout: backdrop + scanlines + the safe-area box that every
 * scene composes inside. Scenes only supply their middle content and (usually)
 * a caption, so nothing has to re-derive the margins.
 *
 * `padX`/`padY` override the safe area for the 1080×1920 vertical cut, where the
 * horizontal margin is proportionally much larger.
 */
export const SceneFrame: React.FC<{
  readonly children: React.ReactNode;
  readonly padX?: number;
  readonly padY?: number;
  readonly name?: string;
}> = ({ children, padX = safe.x, padY = safe.y, name = "SceneFrame" }) => {
  return (
    <AbsoluteFill name={name}>
      <BlueprintBackdrop />
      <AbsoluteFill
        name="SafeArea"
        style={{
          paddingLeft: padX,
          paddingRight: padX,
          paddingTop: padY,
          paddingBottom: padY,
          display: "flex",
          flexDirection: "column",
          // Without this the bottom of SceneBody lands exactly where the caption
          // starts, and the HUD row collides with it.
          rowGap: 28,
        }}
      >
        {children}
      </AbsoluteFill>
      <Scanlines />
    </AbsoluteFill>
  );
};

/**
 * The vertical slice of a scene between the header label and the caption.
 * `center` is what a viewer should notice first — it gets the space.
 */
export const SceneBody: React.FC<{
  readonly children: React.ReactNode;
  readonly gap?: number;
  readonly align?: React.CSSProperties["alignItems"];
  readonly justify?: React.CSSProperties["justifyContent"];
}> = ({ children, gap = 32, align = "flex-start", justify = "center" }) => {
  return (
    <Interactive.Div
      name="SceneBody"
      style={{
        flex: 1,
        display: "flex",
        flexDirection: "column",
        gap,
        alignItems: align,
        justifyContent: justify,
        minHeight: 0,
      }}
    >
      {children}
    </Interactive.Div>
  );
};
