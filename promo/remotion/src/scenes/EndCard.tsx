import { Interactive, interpolate, useCurrentFrame } from "remotion";
import {
  EASE,
  INSTALL,
  REPO,
  fontBody,
  fontDisplay,
  footprint,
  palette,
} from "../theme";
import { SceneFrame, SceneBody } from "../primitives/SceneFrame";
import { Led } from "../primitives/Plate";
import { TermPrompt, Terminal } from "../primitives/Terminal";
import { Caption, FigLabel, cursorVisible, typed } from "../primitives/Type";
import { ScanSweep } from "../primitives/Backdrop";

/** Frame count of the end card as scheduled in the 30s hero (27.0–30.0s). */
export const ENDCARD_FRAMES = 90;

/**
 * The end card shows the command over two lines so it can stay at 30px and stay
 * paste-accurate. `INSTALL` in `theme.ts` remains the single source of truth —
 * the split here derives from it, so the two can't drift apart.
 */
const INSTALL_HEAD = `${INSTALL.split(" | sh")[0]} \\`;
const INSTALL_TAIL = "  | sh";

/**
 * SHEET 08 — the end card. 3 seconds, hard CTA.
 *
 * Built first because it is the only scene with no dependency on captured
 * footage: no terminal recording, no diagram, no bench output. If the palette
 * or type scale is wrong, it is wrong here and nowhere else.
 *
 * Everything on screen is paste-accurate — this is a developer audience and a
 * wrong flag is the first comment.
 */
export const EndCard: React.FC<{
  readonly padX?: number;
  readonly padY?: number;
}> = ({ padX, padY }) => {
  const frame = useCurrentFrame();

  const head = typed(frame, INSTALL_HEAD, 28, 42);
  const tail = typed(frame, INSTALL_TAIL, 28, 42);
  const installShown = head.length >= INSTALL_HEAD.length ? `${head}\n${tail}` : head;

  return (
    <SceneFrame padX={padX} padY={padY}>
      <ScanSweep startFrame={4} durationInFrames={46} />
      <FigLabel fig="SHEET 08" title="END CARD" />
      <SceneBody align="center" gap={36}>
        {/* ---- wordmark: the dot hangs off the left of the text so the text
                itself stays optically centred in the frame ---- */}
        <Interactive.Div
          name="Wordmark"
          style={{
            position: "relative",
            fontFamily: fontDisplay,
            fontSize: 128,
            letterSpacing: -2,
            color: palette.ink,
            opacity: interpolate(frame, [4, 22], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
            translate: `0px ${interpolate(frame, [4, 22], [26, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            })}px`,
          }}
        >
          <span
            style={{
              position: "absolute",
              left: -52,
              top: "50%",
              marginTop: -11,
              width: 22,
              height: 22,
              borderRadius: "50%",
              backgroundColor: palette.accent,
              opacity: interpolate(frame, [10, 30], [0, 1], {
                extrapolateLeft: "clamp",
                extrapolateRight: "clamp",
                easing: EASE,
              }),
            }}
          />
          routre
        </Interactive.Div>

        {/* ---- tagline ---- */}
        <Interactive.Div
          name="Tagline"
          style={{
            fontFamily: fontBody,
            fontSize: 62,
            fontWeight: 600,
            letterSpacing: 0.5,
            color: palette.torch,
            textAlign: "center",
            opacity: interpolate(frame, [14, 34], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          stop paying the token tax
        </Interactive.Div>

        {/* ---- install command ---- */}
        <Terminal
          name="InstallTerminal"
          title="you@gateway: ~"
          tag="bash"
          delay={26}
          bodyFontSize={28}
          // Two lines at 28px × 1.65, so the panel does not grow when the
          // second line types in and shove the URL and HUD down the frame.
          bodyHeight={96}
          style={{ maxWidth: 1560 }}
        >
          <TermPrompt>
            {installShown}
            <span
              style={{
                color: palette.accent,
                opacity: cursorVisible(frame) ? 1 : 0,
              }}
            >
              ▌
            </span>
          </TermPrompt>
        </Terminal>

        {/* ---- repo URL ---- */}
        <Interactive.Div
          name="RepoUrl"
          style={{
            fontFamily: fontBody,
            fontSize: 40,
            fontWeight: 600,
            color: palette.steel,
            opacity: interpolate(frame, [70, 86], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          {REPO}
        </Interactive.Div>

        {/* ---- HUD row ---- */}
        <Interactive.Div
          name="Hud"
          style={{
            display: "flex",
            gap: 40,
            alignItems: "center",
            fontFamily: fontBody,
            fontSize: 26,
            color: palette.steelDim,
            letterSpacing: 0.5,
            opacity: interpolate(frame, [78, 92], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
              easing: EASE,
            }),
          }}
        >
          <span style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <Led delay={80} /> LIVE · 127.0.0.1:20128
          </span>
          <span>MIT</span>
          <span>6 PLATFORMS</span>
        </Interactive.Div>
      </SceneBody>
      <Caption name="EndCaption">
        one static binary · {footprint.binaryMiB} MiB · {footprint.idleRssMiB} MiB
        idle RAM
      </Caption>
    </SceneFrame>
  );
};
