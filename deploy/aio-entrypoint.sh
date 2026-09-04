#!/bin/sh
# Starts the three services in the order they need each other and keeps the
# container's life tied to all of them. Only the dashboard is published; the buyer
# reaches the merchant over localhost, which is the whole point of this image.
set -eu

MARKET_PORT="${MARKET_PORT:-8081}"
HEALTH_TRIES="${MARKET_HEALTH_TRIES:-60}"

stop() {
  # Signals go to every child, not just the one in front.
  kill -TERM "$market" "$user" "$web" 2>/dev/null || true
  wait "$market" "$user" "$web" 2>/dev/null || true
}
trap stop TERM INT

echo "[aio] starting merchant on :${MARKET_PORT}"
/usr/local/bin/market &
market=$!

# The buyer's first act is to read the merchant's card, so it waits rather than
# racing. A merchant that exits early is reported here instead of surfacing later as
# an unexplained buyer failure.
tries=0
until wget -q -O- "http://127.0.0.1:${MARKET_PORT}/health" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if ! kill -0 "$market" 2>/dev/null; then
    echo "[aio] merchant exited before it was healthy; check SUPABASE_URL and UPSTASH_REDIS_REST_URL" >&2
    exit 1
  fi
  if [ "$tries" -ge "$HEALTH_TRIES" ]; then
    echo "[aio] merchant did not become healthy in ${HEALTH_TRIES}s" >&2
    stop
    exit 1
  fi
  sleep 1
done
echo "[aio] merchant healthy"

echo "[aio] starting buyer on ${USER_AGENT_ADDR:-:8082}"
/usr/local/bin/user &
user=$!

echo "[aio] starting dashboard on :${PORT:-3000}"
node /app/web/server.js &
web=$!

# ponytail: a five second poll rather than wait -n, which busybox ash does not
# reliably have. It means up to five seconds between a process dying and the
# container stopping. Any process leaving is a stop, not something to restart into:
# a restart loop inside one container hides which of the three is broken.
while kill -0 "$market" 2>/dev/null && kill -0 "$user" 2>/dev/null && kill -0 "$web" 2>/dev/null; do
  sleep 5
done

echo "[aio] one of the three services exited, stopping the rest" >&2
stop
exit 1
