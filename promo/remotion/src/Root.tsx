import "./fonts";
import { Composition, Folder, Still } from "remotion";
import { Bumper15, Hero30, Master60, Story15 } from "./Timelines";
import { EndCard } from "./scenes/EndCard";
import { Reveal } from "./scenes/Reveal";
import { Compress } from "./scenes/Compress";
import { BillHook } from "./scenes/BillHook";
import { Wheel } from "./scenes/Wheel";
import { Cache } from "./scenes/Cache";
import { Failover } from "./scenes/Failover";
import { Stats } from "./scenes/Stats";
import { Ledger } from "./scenes/Ledger";
import { Problem, Problem02, Problem03, TheTurn } from "./scenes/Problem";
import { StoryClaim, StoryProof, StoryAsk } from "./scenes/Story";

export const RemotionRoot: React.FC = () => {
  return (
    <>
      <Composition
        id="Hero30"
        component={Hero30}
        width={1920}
        height={1080}
        fps={30}
        durationInFrames={1080}
      />
      <Composition
        id="Bumper15"
        component={Bumper15}
        width={1080}
        height={1920}
        fps={30}
        durationInFrames={450}
      />
      <Composition
        id="Master60"
        component={Master60}
        width={1920}
        height={1080}
        fps={30}
        durationInFrames={1920}
      />
      <Composition
        id="Story15"
        component={Story15}
        width={1080}
        height={1920}
        fps={30}
        durationInFrames={450}
      />

      <Folder name="Scenes">
        <Composition
          id="BillHook"
          component={BillHook}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={105}
        />
        <Composition
          id="Reveal"
          component={Reveal}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={75}
        />
        <Composition
          id="Compress"
          component={Compress}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={135}
        />
        <Composition
          id="Wheel"
          component={Wheel}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={180}
        />
        <Composition
          id="Cache"
          component={Cache}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={120}
        />
        <Composition
          id="Failover"
          component={Failover}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={150}
        />
        <Composition
          id="Stats"
          component={Stats}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={135}
        />
        <Composition
          id="Ledger"
          component={Ledger}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={90}
        />
        <Composition
          id="EndCard"
          component={EndCard}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={90}
        />

        <Composition
          id="Problem-01-flood"
          component={Problem}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={240}
        />
        <Composition
          id="Problem-02-repeat"
          component={Problem02}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={240}
        />
        <Composition
          id="Problem-03-stall"
          component={Problem03}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={240}
        />
        <Composition
          id="TheTurn"
          component={TheTurn}
          width={1920}
          height={1080}
          fps={30}
          durationInFrames={120}
        />

        <Composition
          id="Story-Claim"
          component={StoryClaim}
          width={1080}
          height={1920}
          fps={30}
          durationInFrames={105}
        />
        <Composition
          id="Story-Proof"
          component={StoryProof}
          width={1080}
          height={1920}
          fps={30}
          durationInFrames={180}
        />
        <Composition
          id="Story-Ask"
          component={StoryAsk}
          width={1080}
          height={1920}
          fps={30}
          durationInFrames={165}
        />

        <Still
          id="EndCard-Still"
          component={EndCard}
          width={1920}
          height={1080}
        />
        <Still
          id="Story-Poster"
          component={StoryAsk}
          width={1080}
          height={1920}
        />
      </Folder>
    </>
  );
};
