#!/bin/sh
set -u

URL="${METRICS_URL:-http://gomcrouter:9090/metrics}"
INTERVAL="${INTERVAL:-2}"
PREFIX="${PREFIX:-gomcrouter_}"

PREV=/tmp/prev
CUR=/tmp/cur
: > "$PREV"

echo "metrics-watcher: polling ${URL} every ${INTERVAL}s"
echo "  legend:  '*' changed value,  '+' new metric,  '  ' unchanged"
sleep 2

while true; do
  if ! wget -qO- "$URL" 2>/dev/null \
      | grep "^${PREFIX}" \
      | grep -v '_bucket{' > "$CUR"; then
    echo "metrics-watcher: fetch failed"
    sleep "$INTERVAL"
    continue
  fi

  printf '\033[2J\033[H'
  echo "=== gomcrouter metrics @ $(date +%H:%M:%S) ==="
  echo ""

  while IFS= read -r line; do
    id="${line% *}"
    old=$(grep -F "${id} " "$PREV" 2>/dev/null | head -n 1)
    if [ -z "$old" ]; then
      printf '\033[32m+ %s\033[0m\n' "$line"
    elif [ "$old" != "$line" ]; then
      printf '\033[33m* %s\033[0m\n' "$line"
    else
      printf '  %s\n' "$line"
    fi
  done < "$CUR"

  cp "$CUR" "$PREV"
  sleep "$INTERVAL"
done
