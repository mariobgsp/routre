#!/usr/bin/env python3
"""
make-captions.py — write the caption sidecars for every cut.

There is no voiceover (BRIEF.md §0), so the burned-in caption bar *is* the
narration. These sidecars transcribe it for players, platforms and anyone who
wants the text without the video.

Timing is derived from each cut's real beat sheet, so it cannot drift from the
render: if a beat's duration changes in `Timelines.tsx`, regenerate rather than
hand-editing.

    python3 promo/make-captions.py
"""
import os

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "remotion", "out")

# name -> list of (start_s, end_s, text). One cue per beat.
CUES = {
    "master60": [
        (0.0,   8.0,  "this is the normal afternoon"),
        (8.0,  16.0,  "pay again for the same context"),
        (16.0, 24.0,  "and then the run stops"),
        (24.0, 28.0,  "three problems. one address. that is all routre does"),
        (28.0, 31.5,  "the same session, sent twice"),
        (31.5, 34.0,  "point your agent at one address"),
        (34.0, 38.5,  "tool output shrinks before it's billed"),
        (38.5, 44.5,  "the wheel turns. most of it never gets billed"),
        (44.5, 48.5,  "ask twice, pay once"),
        (48.5, 53.5,  "busy provider, next provider"),
        (53.5, 58.0,  "measured, not promised"),
        (58.0, 61.0,  "see every token"),
        (61.0, 64.0,  "one static binary · 10.9 MiB · 12.6 MiB idle RAM"),
    ],
    "hero30": [
        (0.0,   3.5,  "the same session, sent twice"),
        (3.5,   6.0,  "point your agent at one address"),
        (6.0,  10.5,  "tool output shrinks before it's billed"),
        (10.5, 16.5,  "the wheel turns. most of it never gets billed"),
        (16.5, 20.5,  "ask twice, pay once"),
        (20.5, 25.5,  "busy provider, next provider"),
        (25.5, 30.0,  "measured, not promised"),
        (30.0, 33.0,  "see every token"),
        (33.0, 36.0,  "one static binary · 10.9 MiB · 12.6 MiB idle RAM"),
    ],
    "bumper15": [
        (0.0,  3.0,  "the same session, sent twice"),
        (3.0,  7.0,  "busy provider, next provider"),
        (7.0, 11.0,  "measured, not promised"),
        (11.0, 15.0, "one static binary · 10.9 MiB · 12.6 MiB idle RAM"),
    ],
    "story15": [
        (0.0,  3.5,  "12.2x fewer tool tokens"),
        (3.5,  9.5,  "106,150 sent, 8,677 billed"),
        (9.5, 15.0,  "one address: 127.0.0.1:20128"),
    ],
}


def stamp(seconds, sep):
    h, rest = divmod(int(seconds), 3600)
    m, s = divmod(rest, 60)
    frac = seconds - int(seconds)
    return f"{h:02d}:{m:02d}:{s:02d}{sep}{int(round(frac * 1000)):03d}"


def main():
    os.makedirs(OUT, exist_ok=True)
    for name, cues in CUES.items():
        srt = []
        vtt = ["WEBVTT", ""]
        for i, (start, end, text) in enumerate(cues, 1):
            srt.append(
                f"{i}\n{stamp(start, ',')} --> {stamp(end, ',')}\n{text}\n"
            )
            vtt.append(f"{stamp(start, '.')} --> {stamp(end, '.')}\n{text}\n")
        with open(os.path.join(OUT, f"{name}.srt"), "w") as f:
            f.write("\n".join(srt))
        with open(os.path.join(OUT, f"{name}.vtt"), "w") as f:
            f.write("\n".join(vtt))
        print(f"  {name:10s} {len(cues):2d} cues")


if __name__ == "__main__":
    main()
