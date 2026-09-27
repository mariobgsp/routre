#!/usr/bin/env bash
# Render every docs/*.puml to docs/*.png.
#
# kroki.io's PlantUML endpoint is the renderer (no Java, no Graphviz needed).
# Its POST body limit is ~2 KB, so the source goes out deflate-encoded in the
# URL instead - that is why this is a script and not a plain curl one-liner.
# The SVG is a throwaway: rsvg-convert scales it into the committed PNG.
set -euo pipefail
cd "$(dirname "$0")/../docs"
ZOOM="${ZOOM:-1.17}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

for src in *.puml; do
  out="${src%.puml}"
  enc=$(python3 - "$src" <<'PY'
import sys, zlib
ALPHA = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"
c = zlib.compressobj(9, zlib.DEFLATED, -zlib.MAX_WBITS)
data = c.compress(open(sys.argv[1], "rb").read()) + c.flush()
out = []
for i in range(0, len(data), 3):
    b = data[i:i + 3].ljust(3, b"\0")
    n = b[0] << 16 | b[1] << 8 | b[2]
    out.append("".join(ALPHA[(n >> s) & 0x3F] for s in (18, 12, 6, 0)))
sys.stdout.write("".join(out))
PY
)
  curl -fsS "https://kroki.io/plantuml/svg/$enc" -o "$tmp/$out.svg"
  rsvg-convert -z "$ZOOM" -b white "$tmp/$out.svg" -o "$out.png"
  echo "$src -> $out.png"
done
