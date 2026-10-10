import { interpolate, useCurrentFrame } from "remotion";
import { blink, drawOn, oscillate } from "../timing";

/**
 * A *rigged* hamster — the mascot, rebuilt as SVG parts that move independently.
 *
 * Why not animate the JPEG: `docs/icon/` is a 256px flattened picture with a
 * baked-in teal backdrop. The best it can ever do is bob and fade, which is what
 * the first version did and it read as a sticker. This is a rig — head, ears,
 * eye lids, cheeks, and two front paws — each on its own transform, so the
 * character can blink, lean, and hold something.
 *
 * It is drawn, not traced: the proportions are read off the original (wide
 * cheeks, small ears set high, glossy eyes) and rebuilt as paths. Same
 * character, animatable.
 *
 * Every transform is `useCurrentFrame`-derived, so it renders identically on
 * every machine and scrubs exactly.
 */

const FUR = "#ffc19b";
const FUR_DARK = "#f5a97e";
const CREAM = "#fdf2e3";
const EYE = "#2b2118";

export const Hamster: React.FC<{
  readonly size?: number;
  readonly delay?: number;
  readonly name?: string;
  /** Blink faster the longer it has been on screen. */
  readonly blinkPeriod?: number;
  /** Turn the head toward the viewer (default) or profile it. */
  readonly look?: "front" | "side";
  style?: React.CSSProperties;
}> = ({ size = 200, delay = 0, name = "Hamster", blinkPeriod = 96, style }) => {
  const frame = useCurrentFrame();
  const t = frame - delay;

  // Breathing: the whole body rises and falls on a 3.4s cycle.
  const breathe = oscillate(t, 3, 102);
  // Lean: a slow weight-shift so it is never perfectly still.
  const lean = oscillate(t, 1.6, 152, 38);
  // Ear flop, offset from the head so the two lag slightly — that lag is what
  // makes a cartoon ear read as attached rather than painted on.
  const earL = oscillate(t, 7, 102, 10);
  const earR = oscillate(t, 6, 102, 58);
  // Cheeks squash a touch on the breath.
  const cheek = 1 + oscillate(t, 0.045, 102, 22);
  // Eye lids.
  const lid = blink(Math.max(0, t), blinkPeriod, 14);

  // Entrance: a short settle from below with a slight overshoot in scale.
  const enter = interpolate(t, [0, 16], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: (x) => 1 - Math.pow(1 - x, 3),
  });
  const scale = interpolate(enter, [0, 1], [0.86, 1]);

  return (
    <svg
      name={name}
      viewBox="0 0 200 200"
      width={size}
      height={size}
      style={{ overflow: "visible", ...style }}
      aria-hidden="true"
    >
      <defs>
        <radialGradient id="ham-body" cx="38%" cy="28%" r="82%">
          <stop offset="0%" stopColor="#ffd9c0" />
          <stop offset="100%" stopColor={FUR} />
        </radialGradient>
        <radialGradient id="ham-cheek" cx="34%" cy="30%" r="78%">
          <stop offset="0%" stopColor="#ffffff" />
          <stop offset="100%" stopColor={CREAM} />
        </radialGradient>
      </defs>

      <g
        transform={`translate(100 ${118 + breathe}) scale(${scale * (2 - 1 / cheek)}) rotate(${lean} 0 0)`}
        opacity={enter}
      >
        {/* ---- ears: behind the head, each on its own rotation ---- */}
        <g transform={`rotate(${earL} -46 -50)`}>
          <circle cx={-46} cy={-50} r={21} fill={FUR_DARK} />
          <circle cx={-46} cy={-50} r={12} fill={CREAM} opacity={0.75} />
        </g>
        <g transform={`rotate(${earR} 46 -50)`}>
          <circle cx={46} cy={-50} r={21} fill={FUR_DARK} />
          <circle cx={46} cy={-50} r={12} fill={CREAM} opacity={0.75} />
        </g>

        {/* ---- head ---- */}
        <circle cx={0} cy={0} r={62} fill="url(#ham-body)" />

        {/* ---- cheeks: the signature of the character ---- */}
        <ellipse
          cx={-42}
          cy={16}
          rx={34 * cheek}
          ry={28 * cheek}
          fill="url(#ham-cheek)"
        />
        <ellipse
          cx={42}
          cy={16}
          rx={34 * cheek}
          ry={28 * cheek}
          fill="url(#ham-cheek)"
        />

        {/* ---- eyes ---- */}
        <g>
          <ellipse cx={-24} cy={-12} rx={10.5} ry={12} fill={EYE} />
          <ellipse cx={24} cy={-12} rx={10.5} ry={12} fill={EYE} />
          {/* the lid is a fur-coloured cap that drops over the eye */}
          <g
            style={{
              transformBox: "fill-box",
              transformOrigin: "center top",
            }}
            transform={`translate(0 ${-24 + (1 - lid) * 26})`}
          >
            <circle cx={-24} cy={-12} r={12.6} fill={FUR} />
            <circle cx={24} cy={-12} r={12.6} fill={FUR} />
          </g>
          {/* highlights, drawn last so a blink never covers them */}
          <circle cx={-21} cy={-16} r={3.4} fill="#fff" opacity={lid} />
          <circle cx={27} cy={-16} r={3.4} fill="#fff" opacity={lid} />
          <circle cx={-27} cy={-7} r={1.7} fill="#fff" opacity={lid * 0.7} />
          <circle cx={21} cy={-7} r={1.7} fill="#fff" opacity={lid * 0.7} />
        </g>

        {/* ---- nose + mouth ---- */}
        <path
          d="M -7 12 Q 0 20 7 12 Q 0 15 -7 12 Z"
          fill="#e08a72"
        />
        <path
          d="M 0 18 Q 0 26 0 26"
          stroke="#c9785f"
          strokeWidth={2.2}
          fill="none"
          strokeLinecap="round"
        />
        <path
          d="M 0 26 Q -8 32 -14 25"
          stroke="#c9785f"
          strokeWidth={2.2}
          fill="none"
          strokeLinecap="round"
        />
        <path
          d="M 0 26 Q 8 32 14 25"
          stroke="#c9785f"
          strokeWidth={2.2}
          fill="none"
          strokeLinecap="round"
        />

        {/* ---- whiskers ---- */}
        <g stroke="#e9c3a8" strokeWidth={1.6} strokeLinecap="round" opacity={0.9}>
          <path d="M -56 22 L -78 16" />
          <path d="M -56 30 L -78 32" />
          <path d="M 56 22 L 78 16" />
          <path d="M 56 30 L 78 32" />
        </g>
      </g>
    </svg>
  );
};

/**
 * The hamster pushing a wheel — direction C from the brief, finally earned.
 *
 * The wheel rotates; the paws turn with it at the rim; the hamster leans into
 * the push and the whole rig rocks on each half-turn. Tokens ride in the top of
 * the wheel and drop out of the bottom, which is the mechanism the cut is about
 * rendered as one object rather than as a diagram.
 */
export const HamsterWheel: React.FC<{
  readonly size?: number;
  readonly delay?: number;
  readonly name?: string;
  /** 0 → 1 across the scene. Drives the wheel and the token flow. */
  readonly progress?: number;
  readonly tokens?: number;
}> = ({
  size = 260,
  delay = 0,
  name = "HamsterWheel",
  progress = 1,
  tokens = 9,
}) => {
  const frame = useCurrentFrame();
  const t = Math.max(0, frame - delay);

  const enter = interpolate(t, [0, 18], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: (x) => 1 - Math.pow(1 - x, 3),
  });

  /*
   * Layout, stated once so it is not guessed at call sites:
   *
   *   hub   (250, 120) r=72   the wheel, on the right
   *   body  (78, 118)         the hamster, running up to it from the left
   *
   * The two objects sit side by side rather than overlapping. Every attempt to
   * overlap the hamster with the rim failed for the same reason: a cartoon head
   * and a wire wheel share no depth cues, so any overlap reads as the hamster
   * *wearing* the wheel or standing *inside* it. Separation is what makes it
   * legible, and the paw-trail between them carries the "pushing" idea.
   */
  const HUB_X = 250;
  const HUB_Y = 120;
  const RIM = 72;
  const BODY_X = 78;
  const BODY_Y = 118;

  // Wheel angle, driven by `progress` so it is scrubbable to an exact state.
  const spin = progress * 360 * 2;
  // The body rocks on its own axis, half a wheel-period out of phase with the spin.
  const rock = oscillate(t, 2.2, 51);

  return (
    <svg
      name={name}
      viewBox="0 0 330 250"
      width={size}
      height={(size * 250) / 330}
      style={{ overflow: "visible" }}
      aria-hidden="true"
    >
      <g opacity={enter} transform={`translate(0 ${(1 - enter) * 26})`}>
        {/* ---- ground shadow, so the rig is planted ---- */}
        <ellipse cx={HUB_X} cy={238} rx={92} ry={9} fill="#000" opacity={0.34} />

        {/* ---- the wheel ---- */}
        <g transform={`translate(${HUB_X} ${HUB_Y}) rotate(${spin})`}>
          <circle r={RIM} fill="none" stroke="#4a3f5c" strokeWidth={7} />
          <circle r={RIM - 12} fill="none" stroke="#ca8a04" strokeWidth={3} opacity={0.7} />
          {[0, 60, 120, 180, 240, 300].map((deg) => (
            <line
              key={deg}
              x1={0}
              y1={0}
              x2={0}
              y2={-(RIM - 3)}
              stroke="#7d7466"
              strokeWidth={3}
              transform={`rotate(${deg})`}
            />
          ))}
          {/* hub cap, drawn after the spokes so it reads as being on top */}
          <circle r={11} fill="#ca8a04" />
        </g>

        {/* ---- tokens riding in the top of the wheel ----
            Clamped to the upper arc and held clear of the hub: at a tight radius
            a token reads as a stray dot rather than as cargo. 150° across the
            top, centred, is a load. */}
        {Array.from({ length: tokens }).map((_, i) => {
          const dropAt = 0.08 + i * (0.84 / Math.max(1, tokens));
          if (progress > dropAt) return null;
          const angle = -168 + ((i + 0.5) / tokens) * 156;
          return (
            <circle
              key={`top-${i}`}
              r={9}
              fill="#f0e6d2"
              opacity={0.92}
              transform={`translate(${HUB_X} ${HUB_Y}) rotate(${angle}) translate(0 -52)`}
            />
          );
        })}

        {/* ---- the hamster, running up to the wheel ----
            Bobbing on a 51-frame cycle (1.7s), the same period as the wheel, so
            the two look mechanically coupled rather than independently looping. */}
        <g transform={`translate(${BODY_X} ${BODY_Y + rock}) rotate(${rock * 0.8})`}>
          <HamsterInline breath={oscillate(t, 3, 51, 12)} blinkPeriod={84} />
        </g>

        {/* ---- tokens dropping out of the bottom ----
            They fall from the bottom of the rim, past the hamster's shoulder,
            and fade at the floor. Kept on one x so they read as a stream fed by
            the wheel rather than as something the hamster is shedding. */}
        {Array.from({ length: tokens }).map((_, i) => {
          const dropAt = 0.08 + i * (0.84 / Math.max(1, tokens));
          if (progress < dropAt) return null;
          const local = (progress - dropAt) / 0.26;
          if (local > 1) return null;
          const fall = Math.sqrt(local) * 96;
          const fade = 1 - Math.max(0, (local - 0.4) / 0.6);
          return (
            <circle
              key={`drop-${i}`}
              r={9}
              fill="#f0e6d2"
              opacity={fade * 0.95}
              transform={`translate(${HUB_X + 6} ${HUB_Y + RIM - 4 + fall})`}
            />
          );
        })}
      </g>
    </svg>
  );
};

/**
 * The head-and-paws rig, inline for `HamsterWheel` (no nesting one animated
 * SVG inside another, which is how transforms get lost).
 */
const HamsterInline: React.FC<{
  readonly breath: number;
  readonly blinkPeriod: number;
}> = ({ breath, blinkPeriod }) => {
  const frame = useCurrentFrame();
  const lid = blink(frame, blinkPeriod, 8);
  const earL = oscillate(frame, 6, 102, 10);
  const earR = oscillate(frame, 5, 102, 58);

  return (
    <g transform={`translate(0 ${breath})`}>
      <g transform={`rotate(${earL} -30 -34)`}>
        <circle cx={-30} cy={-34} r={15} fill={FUR_DARK} />
      </g>
      <g transform={`rotate(${earR} 30 -34)`}>
        <circle cx={30} cy={-34} r={15} fill={FUR_DARK} />
      </g>

      <circle cx={0} cy={0} r={44} fill="url(#ham-body)" />
      <ellipse cx={-30} cy={12} rx={24} ry={20} fill="url(#ham-cheek)" />
      <ellipse cx={30} cy={12} rx={24} ry={20} fill="url(#ham-cheek)" />

      <ellipse cx={-17} cy={-8} rx={7.5} ry={8.5} fill={EYE} />
      <ellipse cx={17} cy={-8} rx={7.5} ry={8.5} fill={EYE} />
      <g transform={`translate(0 ${-17 + (1 - lid) * 18})`}>
        <circle cx={-17} cy={-8} r={9} fill={FUR} />
        <circle cx={17} cy={-8} r={9} fill={FUR} />
      </g>
      <circle cx={-15} cy={-11} r={2.4} fill="#fff" opacity={lid} />
      <circle cx={19} cy={-11} r={2.4} fill="#fff" opacity={lid} />

      <path d="M -5 9 Q 0 14 5 9 Q 0 12 -5 9 Z" fill="#e08a72" />
      <path
        d="M 0 13 Q 0 19 0 19 M 0 19 Q -6 23 -10 18 M 0 19 Q 6 23 10 18"
        stroke="#c9785f"
        strokeWidth={1.8}
        fill="none"
        strokeLinecap="round"
      />
    </g>
  );
};

/**
 * A running paw-trail: three fading paw prints that appear and lift off. Used as
 * a transition texture, so a cut reads as "something moved here" rather than
 * cutting on a hard edge.
 */
export const PawTrail: React.FC<{
  readonly count?: number;
  readonly delay?: number;
  readonly step?: number;
  readonly size?: number;
  readonly colour?: string;
}> = ({ count = 3, delay = 0, step = 14, size = 22, colour = "#ca8a04" }) => {
  const frame = useCurrentFrame();

  return (
    <div style={{ display: "flex", gap: 18, alignItems: "center" }}>
      {Array.from({ length: count }).map((_, i) => {
        const d = delay + i * step;
        const life = interpolate(frame, [d, d + 18, d + 34], [0, 1, 0], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
        });
        const riseY = interpolate(frame, [d, d + 34], [0, -14], {
          extrapolateLeft: "clamp",
          extrapolateRight: "clamp",
          easing: (x) => 1 - Math.pow(1 - x, 2),
        });

        return (
          <svg
            key={i}
            viewBox="0 0 24 24"
            width={size}
            height={size}
            style={{
              opacity: life * 0.75,
              translate: `0px ${riseY}px`,
              scale: interpolate(life, [0, 0.4, 1], [0.6, 1, 0.9]),
            }}
          >
            <ellipse cx={12} cy={15} rx={6.5} ry={5.5} fill={colour} />
            <circle cx={5.5} cy={7} r={2.4} fill={colour} />
            <circle cx={11} cy={4.5} r={2.5} fill={colour} />
            <circle cx={17} cy={6} r={2.3} fill={colour} />
          </svg>
        );
      })}
    </div>
  );
};

/**
 * A draw-on hairline that sweeps across the frame — the blueprint's own motion,
 * used to separate two beats without a hard cut. Progress is 0 → 1.
 */
export const SweepLine: React.FC<{
  readonly progress: number;
  readonly width?: number;
  readonly colour?: string;
}> = ({ progress, width = 1728, colour = "#ca8a04" }) => {
  return (
    <div
      style={{
        position: "absolute",
        left: "50%",
        top: "50%",
        width: `${progress * width}px`,
        height: 2,
        backgroundColor: colour,
        boxShadow: `0 0 18px ${colour}`,
        translate: "-50% -50%",
        opacity: Math.min(1, progress * 3),
      }}
    />
  );
};

export { drawOn };
