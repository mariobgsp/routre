import { Series } from "remotion";
import { EndCard } from "./scenes/EndCard";
import { Reveal } from "./scenes/Reveal";
import { Compress } from "./scenes/Compress";
import { Cache } from "./scenes/Cache";
import { Failover } from "./scenes/Failover";
import { Stats } from "./scenes/Stats";
import { Ledger } from "./scenes/Ledger";
import { Problem, Problem02, Problem03, TheTurn } from "./scenes/Problem";
import { BillHook } from "./scenes/BillHook";
import { Wheel } from "./scenes/Wheel";
import { BumperBill, BumperFailover, BumperStats } from "./scenes/Bumper";
import { StoryClaim, StoryProof, StoryAsk } from "./scenes/Story";
import { BumperAudio, HeroAudio, MasterAudio, StoryAudio } from "./audio";

/**
 * The 30s hero cut — direction A+B hybrid.
 *
 * Opens on the answer, which is what makes it an ad. See `Master60` for the
 * cut that earns the answer first.
 *
 *   1.  0.0– 3.5s  BillHook        (B)
 *   2.  3.5– 6.0s  Reveal          (A)
 *   3.  6.0–10.5s  Compress        (A)
 *   4. 10.5–16.5s  Wheel           (B)   the mascot, doing the mechanism
 *   5. 16.5–20.5s  Cache           (A)
 *   6. 20.5–25.5s  Failover        (A)
 *   7. 25.5–30.0s  Stats           (A)
 *   8. 30.0–33.0s  Ledger          (A)
 *   9. 33.0–36.0s  EndCard         (A)
 *
 * 1080 frames at 30fps. Each beat is its own `<Series.Sequence>` with an inline
 * duration, so the Studio exposes one editable timeline entry per beat.
 */
export const Hero30: React.FC = () => {
  return (
    <>
      <Series>
        <Series.Sequence name="1 BillHook (B)" durationInFrames={105}>
          <BillHook />
        </Series.Sequence>
        <Series.Sequence name="2 Reveal (A)" durationInFrames={75}>
          <Reveal />
        </Series.Sequence>
        <Series.Sequence name="3 Compress (A)" durationInFrames={135}>
          <Compress />
        </Series.Sequence>
        <Series.Sequence name="4 Wheel (B)" durationInFrames={180}>
          <Wheel />
        </Series.Sequence>
        <Series.Sequence name="5 Cache (A)" durationInFrames={120}>
          <Cache />
        </Series.Sequence>
        <Series.Sequence name="6 Failover (A)" durationInFrames={150}>
          <Failover />
        </Series.Sequence>
        <Series.Sequence name="7 Stats (A)" durationInFrames={135}>
          <Stats />
        </Series.Sequence>
        <Series.Sequence name="8 Ledger (A)" durationInFrames={90}>
          <Ledger />
        </Series.Sequence>
        <Series.Sequence name="9 EndCard (A)" durationInFrames={90}>
          <EndCard />
        </Series.Sequence>
      </Series>
      <HeroAudio />
    </>
  );
};

/**
 * The 60s master — the same film with the problem stated first.
 *
 * The 30s cut opens on the answer, which works as an ad and fails anyone
 * deciding whether to care. This cut spends its first 24 seconds on the three
 * things that actually happen on a normal afternoon with an AI coding agent,
 * and only then offers the gateway. Everything after `TheTurn` is the 30s cut,
 * unchanged and in order.
 *
 *   0.  0.0–24.0s  Problem     three real pains, 8s each (SHEET 00)
 *   1. 24.0–28.0s  TheTurn     "three problems. one address." (SHEET 00b)
 *   2. 28.0–31.5s  BillHook    (B)
 *   3. 31.5–34.0s  Reveal      (A)
 *   4. 34.0–38.5s  Compress    (A)
 *   5. 38.5–44.5s  Wheel       (B)   the mascot, doing the mechanism
 *   6. 44.5–48.5s  Cache       (A)
 *   7. 48.5–53.5s  Failover    (A)
 *   8. 53.5–58.0s  Stats       (A)
 *   9. 58.0–61.0s  Ledger      (A)
 *  10. 61.0–64.0s  EndCard     (A)
 *
 * 1920 frames at 30fps (64s).
 */
export const Master60: React.FC = () => {
  return (
    <>
      <Series>
        {/* The three panels are separate Sequences on purpose: each gets its own
            local clock, so nothing overlaps and no offset is hand-derived. */}
        <Series.Sequence name="0a Problem 01 flood" durationInFrames={240}>
          <Problem />
        </Series.Sequence>
        <Series.Sequence name="0b Problem 02 repeat" durationInFrames={240}>
          <Problem02 />
        </Series.Sequence>
        <Series.Sequence name="0c Problem 03 stall" durationInFrames={240}>
          <Problem03 />
        </Series.Sequence>
        <Series.Sequence name="1 TheTurn (A)" durationInFrames={120}>
          <TheTurn />
        </Series.Sequence>
        <Series.Sequence name="2 BillHook (B)" durationInFrames={105}>
          <BillHook />
        </Series.Sequence>
        <Series.Sequence name="3 Reveal (A)" durationInFrames={75}>
          <Reveal />
        </Series.Sequence>
        <Series.Sequence name="4 Compress (A)" durationInFrames={135}>
          <Compress />
        </Series.Sequence>
        <Series.Sequence name="5 Wheel (B)" durationInFrames={180}>
          <Wheel />
        </Series.Sequence>
        <Series.Sequence name="6 Cache (A)" durationInFrames={120}>
          <Cache />
        </Series.Sequence>
        <Series.Sequence name="7 Failover (A)" durationInFrames={150}>
          <Failover />
        </Series.Sequence>
        <Series.Sequence name="8 Stats (A)" durationInFrames={135}>
          <Stats />
        </Series.Sequence>
        <Series.Sequence name="9 Ledger (A)" durationInFrames={90}>
          <Ledger />
        </Series.Sequence>
        <Series.Sequence name="10 EndCard (A)" durationInFrames={90}>
          <EndCard />
        </Series.Sequence>
      </Series>
      <MasterAudio />
    </>
  );
};

/**
 * The 15s vertical bumper — direction B, per §9 of the brief. Same components
 * and the same captured numbers as the hero, re-laid-out for a phone.
 *
 * 450 frames at 30fps in a 1080×1920 frame, so the horizontal safe area is
 * roughly double the landscape one.
 */
export const Bumper15: React.FC = () => {
  return (
    <>
      <Series>
        <Series.Sequence name="1 Bill split (B)" durationInFrames={90}>
          <BumperBill />
        </Series.Sequence>
        <Series.Sequence name="2 Failover (A)" durationInFrames={120}>
          <BumperFailover />
        </Series.Sequence>
        <Series.Sequence name="3 Stats (A)" durationInFrames={120}>
          <BumperStats />
        </Series.Sequence>
        <Series.Sequence name="4 EndCard (A)" durationInFrames={120}>
          <EndCard padX={72} padY={180} />
        </Series.Sequence>
      </Series>
      <BumperAudio />
    </>
  );
};

/**
 * Instagram Story — 1080×1920, 15s. One idea, not three.
 *
 * A Story viewer taps through; a 15s Story arguing three things argues none, so
 * this is the single strongest claim (the 12.2× ratio), its arithmetic, and the
 * address. The safe zones are Story-specific — see `scenes/Story.tsx`.
 */
export const Story15: React.FC = () => {
  return (
    <>
      <Series>
        <Series.Sequence name="1 The claim" durationInFrames={105}>
          <StoryClaim />
        </Series.Sequence>
        <Series.Sequence name="2 The proof" durationInFrames={180}>
          <StoryProof />
        </Series.Sequence>
        <Series.Sequence name="3 The ask" durationInFrames={165}>
          <StoryAsk />
        </Series.Sequence>
      </Series>
      <StoryAudio />
    </>
  );
};