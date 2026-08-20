#!/usr/bin/env bash
# gen-vocab.sh — fetches and embeds the BPE vocab tables for internal/tokenize.
#
# The tables are gzip-compressed into internal/tokenize/data/ and shipped in
# the binary via go:embed. Sources are pinned by URL; this script writes a
# sources.txt with SHA-256 checksums so the embedded data is auditable and
# reproducible.
#
# Usage: bash scripts/gen-vocab.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA="$ROOT/internal/tokenize/data"
mkdir -p "$DATA"

# OpenAI cl100k_base and o200k_base byte-pair-encoding merge ranks.
# Format: "<base64-bytes> <rank>" per line.
CL100K="https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"
O200K="https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken"

fetch() {
  local url="$1" out="$2"
  echo "==> fetching $out"
  curl -sSL -o "$DATA/$out" "$url"
}

fetch "$CL100K" "cl100k_base.bpe.txt"
fetch "$O200K" "o200k_base.bpe.txt"

echo "==> compressing"
gzip -9 -f "$DATA/cl100k_base.bpe.txt"
gzip -9 -f "$DATA/o200k_base.bpe.txt"

echo "==> writing sources.txt"
{
  echo "# BPE vocab sources (fetched $(date -u +%Y-%m-%d))"
  echo "cl100k_base.bpe.txt.gz $CL100K $(sha256sum "$DATA/cl100k_base.bpe.txt.gz" | cut -d' ' -f1)"
  echo "o200k_base.bpe.txt.gz $O200K $(sha256sum "$DATA/o200k_base.bpe.txt.gz" | cut -d' ' -f1)"
} > "$DATA/sources.txt"

echo "done. files:"
ls -la "$DATA"
