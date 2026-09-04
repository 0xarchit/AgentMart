#!/bin/sh
# Starts the merchant on loopback, then the buyer on the one published port. The
# buyer is the reachable one because Telegram posts updates to it; the merchant is
# not reachable from outside this container at all.
set -eu

MARKET_PORT="${MARKET_PORT:-8081}"
HEALTH_TRIES="${MARKET_HEALTH_TRIES:-60}"
# A managed host hands out one port, in PORT. An explicit USER_AGENT_ADDR still
# wins, so a local run can pin its own.
USER_AGENT_ADDR="${USER_AGENT_ADDR:-:${PORT:-8082}}"
export USER_AGENT_ADDR

market=""
user=""

stop() {
  # Signals go to both children, and to whichever of them exists yet.
  for pid in "$market" "$user"; do
    [ -n "$pid" ] || continue
    kill -TERM "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap stop TERM INT

echo "[agents] starting merchant on :${MARKET_PORT}"
/usr/local/bin/market &
market=$!

# The buyer's first act is to read the merchant's card, so it waits rather than
# racing. A merchant that exits early is reported here instead of surfacing later as
# an unexplained buyer failure.
tries=0
until wget -q -O- "http://127.0.0.1:${MARKET_PORT}/health" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if ! kill -0 "$market" 2>/dev/null; then
    echo "[agents] merchant exited before it was healthy; check SUPABASE_URL and UPSTASH_REDIS_REST_URL" >&2
    exit 1
  fi
  if [ "$tries" -ge "$HEALTH_TRIES" ]; then
    echo "[agents] merchant did not become healthy in ${HEALTH_TRIES}s" >&2
    stop
    exit 1
  fi
  sleep 1
done
echo "[agents] merchant healthy"

echo "[agents] starting buyer on ${USER_AGENT_ADDR}"
/usr/local/bin/user &
user=$!

# ponytail: a five second poll rather than wait -n, which busybox ash does not
# reliably have. It means up to five seconds between a process dying and the
# container stopping. Either one leaving is a stop, not something to restart into: a
# restart loop in here would hide which of the two is broken.
while kill -0 "$market" 2>/dev/null && kill -0 "$user" 2>/dev/null; do
  sleep 5
done

echo "[agents] one of the two services exited, stopping the other" >&2
stop
exit 1
