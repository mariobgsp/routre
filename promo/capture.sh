#!/usr/bin/env bash
# capture.sh — produce the real command output the promo quotes as evidence.
#
# Everything here runs offline against the repo's own `cmd/mock-upstream`. No API
# keys, no network egress, no touching the real gateway on 127.0.0.1:20128 — this
# script uses 21096/21097 for the upstreams and 21128 for the gateway.
#
# What it captures:
#   cache       — a real cache MISS followed by a real HIT (X-Llrouter-Cache)
#   failover    — provider A returns a real 429; B answers; A enters cooldown
#   ledger      — `routre list` from the live gateway and from disk with it down
#   status      — /v1/status, including failures + cooldown_remaining
#   metrics     — /metrics, the real Prometheus series names
#   requestlog  — the per-request JSONL line
#
# Usage: promo/capture.sh    (writes into promo/captures/)
# Requires: go, curl, jq-free (plain shell + grep only)

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${ROUTRE_CAPTURE_WORK:-/tmp/routre-capture}"
OUT="$REPO/promo/captures"

PORT_FAIL=21097   # mock-a, always 429
PORT_OK=21096     # mock-b, healthy
PORT_GW=21128     # the gateway under test

cleanup() {
  set +e
  for p in "$GW_PID" "$FAIL_PID" "$OK_PID"; do
    [ -n "${p:-}" ] && kill "$p" 2>/dev/null
  done
  wait 2>/dev/null
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/cfg" "$WORK/data" "$OUT"

# --------------------------------------------------------------------------
# 1. build the mock upstream twice
#
# `cmd/mock-upstream` hardcodes its admin listener on 127.0.0.1:19998, so two
# instances cannot both bind. Rather than patch the repo we build the second copy
# with `go build -overlay`, which leaves `git status` clean.
# --------------------------------------------------------------------------
echo "==> building mock upstreams"
CGO_ENABLED=0 go build -o "$WORK/bin/mock-fail" ./cmd/mock-upstream || exit 1

mkdir -p "$WORK/alt"
sed 's#ListenAndServe("127.0.0.1:19998"#ListenAndServe("127.0.0.1:19995"#' \
  cmd/mock-upstream/main.go > "$WORK/alt/main.go"
printf '{"Replace":{"%s/cmd/mock-upstream/main.go":"%s/alt/main.go"}}\n' \
  "$REPO" "$WORK" > "$WORK/alt/overlay.json"
CGO_ENABLED=0 go build -overlay "$WORK/alt/overlay.json" \
  -o "$WORK/bin/mock-ok" ./cmd/mock-upstream || exit 1

# --------------------------------------------------------------------------
# 2. config: two tiers, A always 429, B healthy
# --------------------------------------------------------------------------
cat > "$WORK/cfg/config.json" <<JSON
{
  "listen": "127.0.0.1:$PORT_GW",
  "log_level": "info",
  "forward_unknown": true,
  "request_log": "$WORK/requests.jsonl",
  "rtk":  { "enabled": true, "min_bytes": 500, "max_bytes": 10485760 },
  "cache": { "enabled": true, "max_entries": 4096, "ttl_seconds": 86400,
             "prefix_order": true, "max_bytes": 67108864 },
  "tiers": [
    { "name": "primary", "providers": [
      { "name": "mock-a", "kind": "openai", "base_url": "http://127.0.0.1:$PORT_FAIL",
        "api_key_env": "MOCK_KEY", "models": ["mock-model"],
        "price_in": 3.0, "price_out": 15.0 } ] },
    { "name": "backup", "providers": [
      { "name": "mock-b", "kind": "openai", "base_url": "http://127.0.0.1:$PORT_OK",
        "api_key_env": "MOCK_KEY", "models": ["mock-model"],
        "price_in": 3.0, "price_out": 15.0 } ] }
  ]
}
JSON
echo 'MOCK_KEY=not-a-real-key' > "$WORK/cfg/routre.env"
chmod 600 "$WORK/cfg/routre.env"

# --------------------------------------------------------------------------
# 3. start everything
# --------------------------------------------------------------------------
echo "==> starting upstreams + gateway"
"$WORK/bin/mock-fail" -port "$PORT_FAIL" -fail 429 > "$WORK/mock-fail.log" 2>&1 &
FAIL_PID=$!
"$WORK/bin/mock-ok" -port "$PORT_OK" > "$WORK/mock-ok.log" 2>&1 &
OK_PID=$!

ROUTRE_CLI_DATA_DIR="$WORK/data" ./routre serve -config "$WORK/cfg/config.json" \
  > "$WORK/gateway.log" 2>&1 &
GW_PID=$!

for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT_GW/healthz" >/dev/null 2>&1 && break
  sleep 0.2
done
curl -sf "http://127.0.0.1:$PORT_GW/healthz" >/dev/null 2>&1 || {
  echo "gateway did not come up; see $WORK/gateway.log" >&2; exit 1
}

# --------------------------------------------------------------------------
# 4. SAFETY GUARD — prove the port we are about to read is OUR gateway
#
# `routre list` takes a global --url that does NOT come from the config's
# `listen`. Pointing it at a config alone is not enough: the first version of
# this script passed -config and silently queried whatever gateway was on the
# default port, which wrote a real usage ledger into the repo. So before any
# capture, verify the gateway on $PORT_GW serves ONLY the two mocks.
# --------------------------------------------------------------------------
STATUS_JSON=$(curl -s "http://127.0.0.1:$PORT_GW/v1/status")
PROVIDERS=$(printf '%s' "$STATUS_JSON" | tr ',' '\n' \
  | sed -n 's/.*"name":"\([^"]*\)".*/\1/p' | sort -u)
UNEXPECTED=$(printf '%s\n' "$PROVIDERS" | grep -vxE 'mock-a|mock-b' || true)
if [ -n "$UNEXPECTED" ]; then
  echo "ABORT: gateway on $PORT_GW is not the capture harness. It serves:" >&2
  printf '  %s\n' $UNEXPECTED >&2
  echo "Refusing to capture — this would leak real usage data." >&2
  exit 1
fi
echo "==> guard ok: gateway on $PORT_GW serves only: $(printf '%s' "$PROVIDERS" | tr '\n' ' ')"

# --------------------------------------------------------------------------
# 5. the evidence
# --------------------------------------------------------------------------
LOG="$OUT/gateway-evidence.txt"
{
  echo "# routre gateway evidence — captured artifact"
  echo "# command : promo/capture.sh"
  echo "# date    : $(date -Iseconds)"
  echo "# branch  : $(git -C "$REPO" rev-parse --abbrev-ref HEAD) @ $(git -C "$REPO" rev-parse --short HEAD)"
  echo "# go      : $(go version)"
  echo "#"
  echo "# Fully offline. Two instances of the repo's own cmd/mock-upstream on"
  echo "# $PORT_FAIL (always 429) and $PORT_OK (healthy); gateway on $PORT_GW."
  echo "# No real provider, no API key, no network egress. The 429 is a real HTTP"
  echo "# 429 from the mock and the cooldown is the router's own exponential"
  echo "# backoff — but the providers are mocks, so the dollar figures below are"
  echo "# computed from the config's price_in/price_out, not billed by anyone."
  echo
  echo "## FAILOVER — provider A answers 429, the client still gets 200 from B"
  echo
} > "$LOG"

BODY='{"model":"mock-model","messages":[{"role":"user","content":"promo failover probe"}]}'
{
  echo '$ curl -sD - -o /dev/null -X POST /v1/chat/completions -d @body'
  curl -s -D - -o /dev/null -X POST "http://127.0.0.1:$PORT_GW/v1/chat/completions" \
    -H 'Content-Type: application/json' -H 'User-Agent: promo-demo/1.0' \
    -d "$BODY" -w 'HTTP=%{http_code}  total=%{time_total}s\n' \
    | grep -iE 'x-llrouter|^HTTP='
  echo
  echo "Note: the 429 never reaches the client. The gateway tried A, saw the"
  echo "rate limit, cooled A down and relayed B's answer. This is the point."
  echo
  echo "## COOLDOWN — the same window, read from /v1/status"
  echo
} >> "$LOG"

# Request and status poll must share one command: the first cooldown is only 2s.
{
  curl -s -o /dev/null -X POST "http://127.0.0.1:$PORT_GW/v1/chat/completions" \
    -H 'Content-Type: application/json' -d "$BODY"
  echo '$ curl /v1/status | jq .providers'
  curl -s "http://127.0.0.1:$PORT_GW/v1/status" \
    | tr ',' '\n' | grep -iE 'name|failures|cooldown_remaining|tier' \
    | sed 's/^[[:space:]]*//'
  echo
  echo "Cooldown policy is 2s base, doubling, capped at 5m"
  echo "(internal/router/classify.go DefaultCooldownPolicy)."
  echo
  echo "## CACHE — identical request twice"
  echo
} >> "$LOG"

CACHE_BODY='{"model":"mock-model","messages":[{"role":"user","content":"promo cache probe"}]}'
{
  echo '$ ... first request'
  curl -s -D - -o "$WORK/miss.json" -X POST "http://127.0.0.1:$PORT_GW/v1/chat/completions" \
    -H 'Content-Type: application/json' -H 'User-Agent: promo-demo/1.0' \
    -d "$CACHE_BODY" -w 'HTTP=%{http_code}  total=%{time_total}s\n' \
    | grep -iE 'x-llrouter|^HTTP='
  echo '$ ... identical request again'
  curl -s -D - -o "$WORK/hit.json" -X POST "http://127.0.0.1:$PORT_GW/v1/chat/completions" \
    -H 'Content-Type: application/json' -H 'User-Agent: promo-demo/1.0' \
    -d "$CACHE_BODY" -w 'HTTP=%{http_code}  total=%{time_total}s\n' \
    | grep -iE 'x-llrouter|^HTTP='
  echo
  if cmp -s "$WORK/miss.json" "$WORK/hit.json"; then
    echo "Bodies byte-identical (cmp: no differences). The hit replayed from memory."
  else
    echo "WARNING: bodies differ — do not claim byte-identical replay."
  fi
  echo
  echo "## LEDGER — routre list, live gateway"
  echo
} >> "$LOG"

# --url is explicit and must stay explicit: it is the only thing keeping this
# away from whatever real gateway is on the default port.
ROUTRE_CLI_DATA_DIR="$WORK/data" ./routre list -config "$WORK/cfg/config.json" \
  --url "http://127.0.0.1:$PORT_GW" > "$WORK/list-live.txt" 2>&1

# Second guard, on the content itself. At indent 2 `routre list` prints agent
# names, live provider state and a summary footer. Only *this script's* rows are
# allowlisted; anything else means the capture is contaminated, so drop the file.
OFFENDERS=$(awk '
  /^  [^ ]/ {
    if ($0 ~ /^  (mock-a|mock-b|promo-demo|curl\/)/) next
    if ($0 ~ /^  (==|source:|requests:|consumed:|saved:|cost:|prompt cache:|by provider\/model:|no traffic|TOTAL)/) next
    if ($0 ~ /^  \[(primary|backup)\]/) next
    if ($0 ~ /^  [0-9]+-MB gateway/) next
    print
  }
' "$WORK/list-live.txt")
if [ -n "$OFFENDERS" ]; then
  echo "ABORT: routre list returned rows this script did not create:" >&2
  printf '%s\n' "$OFFENDERS" | head -5 >&2
  rm -f "$WORK/list-live.txt"
  echo "Refusing to write the ledger capture." >&2
  exit 1
fi
cat "$WORK/list-live.txt" >> "$LOG"

# The ledger autosaves every 60s; SIGHUP flushes it now, so the offline capture
# below reads a real file instead of "no traffic yet".
kill -HUP "$GW_PID" 2>/dev/null
sleep 1

{
  echo
  echo "## LEDGER — routre list with the gateway DOWN (reads the persisted file)"
  echo
} >> "$LOG"
ROUTRE_CLI_DATA_DIR="$WORK/data" ./routre list -config "$WORK/cfg/config.json" \
  --url http://127.0.0.1:1 >> "$LOG" 2>&1

{
  echo
  echo "## /metrics — the real series names"
  echo
  echo '$ curl /metrics'
  curl -s "http://127.0.0.1:$PORT_GW/metrics"
  echo
  echo "## REQUEST LOG — one real JSONL line"
  echo
  echo "Note: dial_ms / headers_ms / ttfb_ms are documented in"
  echo "docs/HOW-IT-WORKS.md but are never assigned anywhere in the codebase"
  echo "(Phases is built in two places and both set TotalMS only), and the"
  echo "fields are omitempty so they are silently absent. Only latency_ms and"
  echo "total_ms are real."
  echo
} >> "$LOG"

sleep 1
curl -s "http://127.0.0.1:$PORT_GW/v1/chat/completions" -o /dev/null \
  -X POST -H 'Content-Type: application/json' -d "$BODY" 2>/dev/null
{
  echo '$ tail -1 requests.jsonl'
  tail -1 "$WORK/requests.jsonl" 2>/dev/null || echo "(request_log empty)"
  echo
  echo "## /v1/status — cache + rtk counters"
  echo
  curl -s "http://127.0.0.1:$PORT_GW/v1/status" | tr ',' '\n' \
    | grep -iE 'cache_|rtk_|uptime|discovery' | sed 's/^[[:space:]]*//'
} >> "$LOG"

echo
echo "==> wrote $LOG"
