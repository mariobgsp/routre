#!/usr/bin/env bash
# measure-ram.sh — measures routre-cli RAM usage (idle/peak/growth) plus
# binary size, against the research targets: idle <= 100MB, hard cap 200MB.
#
# Usage:
#   ./scripts/measure-ram.sh [binary] [config] [seconds]
# Defaults: ./routre-cli, ./config.json, 30s idle sampling.
#
# Requires a config whose tiers point at a reachable endpoint (or a mock);
# with no providers configured the daemon still idles, which is what we
# measure. Samples VmRSS every second and reports VmHWM at the end.

set -euo pipefail

BIN="${1:-./routre-cli}"
CFG="${2:-./config.json}"
SECONDS="${3:-30}"

if [ ! -x "$BIN" ]; then
  echo "binary not found: $BIN (run 'make build' first)" >&2
  exit 1
fi

echo "== routre-cli RAM measurement =="
echo "binary: $BIN  config: $CFG  idle window: ${SECONDS}s"
echo

# Build a temp config with a localhost-only listener and no providers, so
# the daemon is guaranteed idle.
TMPCFG=$(mktemp)
cat >"$TMPCFG" <<JSON
{
  "listen": "127.0.0.1:20129",
  "tiers": [],
  "rtk": {"enabled": true, "min_bytes": 500, "max_bytes": 10485760},
  "cache": {"enabled": true, "max_entries": 512, "ttl_seconds": 3600, "prefix_order": false}
}
JSON

"$BIN" serve -config "$TMPCFG" &
PID=$!
trap 'kill $PID 2>/dev/null || true; rm -f "$TMPCFG"' EXIT

# Wait for the listener.
for i in $(seq 1 50); do
  if curl -sf http://127.0.0.1:20129/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

SIZE=$(stat -c%s "$BIN" 2>/dev/null || stat -f%z "$BIN")
echo "binary size      : $((SIZE / 1024)) KiB ($(echo "scale=1; $SIZE/1048576" | bc) MiB)"

read_rss() {
  awk '/VmRSS:/ {print $2}' "/proc/$PID/status" 2>/dev/null || echo 0
}
read_hwm() {
  awk '/VmHWM:/ {print $2}' "/proc/$PID/status" 2>/dev/null || echo 0
}

PEAK=0
SUM=0
N=0
echo "sampling VmRSS every 1s for ${SECONDS}s ..."
for i in $(seq 1 "$SECONDS"); do
  RSS=$(read_rss)
  SUM=$((SUM + RSS))
  N=$((N + 1))
  if [ "$RSS" -gt "$PEAK" ]; then PEAK=$RSS; fi
  printf "\r  t=%3ds  rss=%6d KiB  peak=%6d KiB" "$i" "$RSS" "$PEAK"
  sleep 1
done
echo

AVG=$((SUM / N))
HWM=$(read_hwm)
echo
echo "== results =="
echo "idle avg RSS     : $((AVG / 1024)) MiB ($AVG KiB)"
echo "peak RSS (HWM)   : $((HWM / 1024)) MiB ($HWM KiB)"
echo "growth over run  : $(((HWM - AVG) / 1024)) MiB (sampled-window estimate)"

if [ "$AVG" -le 102400 ] && [ "$HWM" -le 204800 ]; then
  echo "TARGET: PASS (idle <= 100 MiB, hard cap <= 200 MiB)"
else
  echo "TARGET: FAIL — exceeds the 100-200 MiB research budget"
  exit 1
fi
