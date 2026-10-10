#!/usr/bin/env python3
"""
make-audio.py — generates every sound in the promo mix.

There is no voiceover (that is a decision, see BRIEF.md §0), so the audio only
carries mechanics: one low bed plus four short UI sounds. Everything here is
synthesised from scratch — nothing is licensed, sampled or downloaded, so the
whole mix is reproducible from this file alone.

    python3 promo/make-audio.py

Writes into promo/remotion/public/audio/.
"""

import math
import os
import struct
import wave

SR = 44100
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "remotion", "public", "audio")


def write_wav(name, samples, vol=0.5):
    os.makedirs(OUT, exist_ok=True)
    path = os.path.join(OUT, name)
    with wave.open(path, "w") as f:
        f.setnchannels(1)
        f.setsampwidth(2)
        f.setframerate(SR)
        frames = (
            struct.pack("<h", int(max(-1.0, min(1.0, s * vol)) * 32767))
            for s in samples
        )
        f.writeframes(b"".join(frames))
    return path


def decay_env(i, n, attack=0.004, decay=0.25):
    """Fast attack, exponential tail. attack/decay are both fractions of total."""
    t = i / SR
    total = n / SR
    at = max(1e-4, attack)
    if t < at:
        return t / at
    rel = (t - at) / max(1e-4, total - at)
    return math.exp(-rel / max(1e-4, decay))


def tone(freq, dur, harmonics=(1.0, 0.35, 0.12), decay=0.25):
    """A short tone with a few harmonics — reads as 'device', not 'music'."""
    n = int(SR * dur)
    out = []
    for i in range(n):
        t = i / SR
        s = sum(
            amp * math.sin(2 * math.pi * freq * k * t)
            for k, amp in enumerate(harmonics, start=1)
        )
        out.append(s * decay_env(i, n, 0.004, decay))
    return out


def bed(seconds=30, pulse=False):
    """
    A low drone in A minor (A1 / C2 / E2) with a slow 8s swell.

    `pulse=True` adds a quiet tick every 0.5s. The hero uses it so the bed has
    motion under a 30s cut; the 15s bumper does not, because there is less room.
    """
    n = int(SR * seconds)
    root, third, fifth = 55.0, 65.41, 82.41
    out = []
    for i in range(n):
        t = i / SR
        s = 0.0
        for freq, amp in ((root, 1.0), (third, 0.5), (fifth, 0.35)):
            s += amp * math.sin(2 * math.pi * freq * t)
            if pulse:
                s += amp * 0.4 * math.sin(2 * math.pi * freq * 2 * t)
        s *= 0.5 + 0.5 * math.sin(2 * math.pi * t / 8.0)
        if pulse:
            ph = t % 0.5
            if ph < 0.01:
                s += 0.5 * math.exp(-ph / 0.02) * math.sin(2 * math.pi * 1400 * ph)
        out.append(s / 2.6)
    return out


def main():
    # pip — the install command landing
    write_wav("pip.wav", tone(880, 0.07, (1.0, 0.2), 0.05), 0.5)

    # tick — a cache hit (two of these, spaced 3 frames apart, read as a pair)
    write_wav("tick.wav", tone(1760, 0.045, (1.0, 0.15), 0.03), 0.5)

    # thunk — traffic moving to the next provider
    write_wav("thunk.wav", tone(196, 0.22, (1.0, 0.5, 0.25), 0.09), 0.5)

    # alarm — the 429 arriving. Square-ish and quieter: an error, not an event.
    al = tone(330, 0.16, (1.0, 0.7, 0.4), 0.06)
    for i in range(0, len(al), 2):
        al[i] *= 0.55
    write_wav("alarm.wav", al, 0.5)

    write_wav("bed30.wav", bed(), 0.10)
    write_wav("bed30-pulse.wav", bed(pulse=True), 0.11)

    for name in sorted(os.listdir(OUT)):
        size = os.path.getsize(os.path.join(OUT, name))
        print(f"  {name:20s} {size / 1024:8.1f} KiB")


if __name__ == "__main__":
    main()