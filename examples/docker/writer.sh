#!/bin/sh
set -u

ADDR="${GOMCROUTER_ADDR:-gomcrouter}"
PORT="${GOMCROUTER_PORT:-8080}"
GOOD_KEY="${GOOD_KEY:-mykey}"
BAD_KEY="${BAD_KEY:-missingkey}"
TTL="${TTL:-3600}"
VALUE="hello-gomcrouter"

echo "writer: waiting for ${ADDR}:${PORT}..."
until nc -z "$ADDR" "$PORT" 2>/dev/null; do
  sleep 1
done
echo "writer: connected"

echo "writer: SET ${GOOD_KEY} ttl=${TTL}s value='${VALUE}'"
printf "set %s 0 %s %d\r\n%s\r\n" "$GOOD_KEY" "$TTL" "${#VALUE}" "$VALUE" \
  | nc -w 2 "$ADDR" "$PORT"

while true; do
  i=1
  while [ "$i" -le 10 ]; do
    printf "get %s\r\n" "$GOOD_KEY" | nc -w 2 "$ADDR" "$PORT" >/dev/null
    i=$((i + 1))
  done
  printf "get %s\r\n" "$BAD_KEY" | nc -w 2 "$ADDR" "$PORT" >/dev/null
  echo "$(date +%H:%M:%S) writer: 10 hits on '${GOOD_KEY}' + 1 miss on '${BAD_KEY}'"
  sleep 1
done
