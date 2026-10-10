#!/usr/bin/env python3
"""
audit-claims.py — check that every number the video shows exists in a capture.

The rule the whole promo runs on (BRIEF.md §13): *a number only reaches the
screen if a captured artifact produced it.* This script enforces it mechanically,
so a stale constant in `theme.ts` cannot survive a re-capture.

    python3 promo/audit-claims.py     # exit 1 on any mismatch

Covers the figures that appear as numbers on screen. Strings that are claims in
prose (the policy ceiling, a metric name) are checked by the same script but
tagged separately, because they come from source rather than from a capture.
"""

import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
CAPTURES = os.path.join(HERE, "captures")
THEME = os.path.join(HERE, "remotion", "src", "theme.ts")
GO_SRC = os.path.join(HERE, "..", "internal", "router", "classify.go")


def read(path):
    with open(path, errors="ignore") as f:
        return f.read()


def grab(pattern, text):
    m = re.search(pattern, text)
    return m.group(1) if m else None


def main():
    bench = read(os.path.join(CAPTURES, "bench.txt"))
    foot = read(os.path.join(CAPTURES, "footprint.txt"))
    gw = read(os.path.join(CAPTURES, "gateway-evidence.txt"))
    theme = read(THEME)
    policy = read(GO_SRC)

    # (label, value shown in the video, pattern proving it in a capture)
    checks = [
        ("toolReduction", grab(r"toolReduction: ([\d.]+)", theme),
         grab(r"aggregate TOOL reduction\s*:\s*([\d.]+)%", bench)),
        ("payloadReduction", grab(r"payloadReduction: ([\d.]+)", theme),
         grab(r"aggregate payload reduction\s*:\s*([\d.]+)%", bench)),
        ("worstPayload", grab(r"worstPayload: ([\d.]+)", theme),
         grab(r"per-payload worst\s*:\s*([\d.]+)%", bench)),
        ("toolTokensBefore", grab(r"toolTokensBefore: (\d+)", theme),
         grab(r"TOOL reduction\s*:\s*[\d.]+%\s+\((\d+)", bench)),
        ("toolTokensAfter", grab(r"toolTokensAfter: (\d+)", theme),
         grab(r"TOOL reduction\s*:\s*[\d.]+%\s+\(\d+ \u2192 (\d+)", bench)),
        ("gate", grab(r"gate: ([\d.]+)", theme),
         grab(r"target gate\s*:\s*([\d.]+)%", bench)),
        ("idleRssMiB", grab(r"idleRssMiB: ([\d.]+)", theme),
         grab(r"idle RSS\s+: \d+ KiB = ([\d.]+) MiB", foot)),
        ("binaryMiB", grab(r"binaryMiB: ([\d.]+)", theme),
         grab(r"binary size\s+: \d+ B = ([\d.]+) MiB", foot)),
        ("cacheHeader", grab(r'firstHeader: "([^"]+)"', theme),
         grab(r"(X-Llrouter-Cache: miss)", gw)),
        ("cacheHitHeader", grab(r'secondHeader: "([^"]+)"', theme),
         grab(r"(X-Llrouter-Cache: hit)", gw)),
        ("cooldown", grab(r'cooldown: "([^"]+)"', theme),
         grab(r'cooldown_remaining"\s*:\s*"([^"]+)"', gw)),
        ("failoverStatus", grab(r"clientStatus: (\d+)", theme),
         grab(r"HTTP=(\d+)", gw)),
    ]

    # Policy ceilings are source constants, not measurements.
    source_checks = [
        ("cooldownMax 5m", "Max: 5 * time.Minute" in policy),
    ]

    failures = []
    print("== numbers shown on screen vs their capture ==")
    for label, shown, captured in checks:
        if shown is None or captured is None:
            failures.append(f"{label}: could not read (shown={shown}, capture={captured})")
            print(f"  MISSING {label:18s} shown={shown} capture={captured}")
            continue
        if shown == captured:
            print(f"  exact   {label:18s} {shown}")
        elif abs(float(shown) - float(captured)) < 0.051:
            direction = "up" if float(shown) > float(captured) else "down"
            print(f"  round   {label:18s} {shown}  (capture {captured}, rounds {direction})")
        else:
            failures.append(f"{label}: video shows {shown}, capture says {captured}")
            print(f"  MISMATCH {label:17s} shown={shown} capture={captured}")

    print("\n== policy constants (source, not measurement) ==")
    for label, ok in source_checks:
        print(f"  {'OK     ' if ok else 'MISSING'} {label}")
        if not ok:
            failures.append(f"{label} not found in {GO_SRC}")

    print()
    if failures:
        print(f"FAILED — {len(failures)} problem(s):")
        for f in failures:
            print(f"  - {f}")
        return 1
    print(f"PASS — {len(checks)} on-screen numbers trace to a capture, "
          f"{len(source_checks)} policy constant(s) verified in source")
    return 0


if __name__ == "__main__":
    sys.exit(main())