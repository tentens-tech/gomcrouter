#!/usr/bin/env bash
# End-to-end test for the gomcrouter docker-compose example.
#
# Prerequisites:
#   - `docker compose up -d` already running this directory's stack
#   - host tools: nc (BSD or GNU), curl, docker, awk, grep, printf
#
# What it checks:
#   1. SET via gomcrouter fans out to BOTH memcaches (AllFastestRoute)
#   2. Stopping memcache1 → router still serves writes/reads via memcache2
#   3. Restarting memcache1 → writes fan out to both again
#   4. Same as 2 for memcache2
#   5. Same as 3 for memcache2
#
# Run with:
#   ./test.sh
set -u -o pipefail

cd "$(dirname "$0")"

# --- Configuration --------------------------------------------------------

ROUTER="${ROUTER:-localhost:8080}"
MC1="${MC1:-localhost:11211}"
MC2="${MC2:-localhost:11212}"
METRICS_URL="${METRICS_URL:-http://localhost:9090/metrics}"

# Worst-case time for gomcrouter to update healthy_hosts gauge:
#   healthcheck interval (10s) + scraper interval (5s) + slack.
HEALTHCHECK_GRACE="${HEALTHCHECK_GRACE:-25}"

NC_TIMEOUT="${NC_TIMEOUT:-2}"

# --- Output helpers -------------------------------------------------------

if [ -t 1 ]; then
  RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; BOLD=""; RESET=""
fi

PASS=0
FAIL=0
FAIL_NAMES=()

step()  { printf '\n%s--- %s ---%s\n' "$YELLOW$BOLD" "$1" "$RESET"; }
pass()  { printf '%sPASS%s %s\n' "$GREEN" "$RESET" "$1"; PASS=$((PASS + 1)); }
fail()  { printf '%sFAIL%s %s\n' "$RED" "$RESET" "$1"; FAIL=$((FAIL + 1)); FAIL_NAMES+=("$1"); }
fatal() { printf '%sFATAL%s %s\n' "$RED" "$RESET" "$1"; exit 1; }

# --- Memcache protocol helpers --------------------------------------------
#
# We hold stdin open for SETTLE_S seconds after writing the payload because
# BSD `nc` on macOS closes the socket immediately on stdin EOF, regardless of
# `-w`. We can't use a trailing `quit\r\n` to make the peer close because
# gomcrouter's LocalRoute for `quit` does not close the connection (and we
# need the same helper to work against direct memcaches and the router).
#
# Each helper writes the protocol bytes via `printf` directly in the pipe.
# Building the payload via command substitution `$(printf ...)` and passing
# it as a string is unsafe — bash strips trailing newlines from $() output,
# which leaves memcache waiting for the rest of the command and timing us
# out before any response is read.

SETTLE_S="${SETTLE_S:-0.3}"

mc_set() {
  local hp=$1 key=$2 val=$3 ttl=${4:-60}
  local host=${hp%:*} port=${hp#*:}
  { printf 'set %s 0 %d %d\r\n%s\r\n' "$key" "$ttl" "${#val}" "$val"
    sleep "$SETTLE_S"; } \
    | nc -w "$NC_TIMEOUT" "$host" "$port" 2>/dev/null
}

mc_get() {
  local hp=$1 key=$2
  local host=${hp%:*} port=${hp#*:}
  { printf 'get %s\r\n' "$key"
    sleep "$SETTLE_S"; } \
    | nc -w "$NC_TIMEOUT" "$host" "$port" 2>/dev/null
}

mc_flush_all() {
  local hp=$1
  local host=${hp%:*} port=${hp#*:}
  { printf 'flush_all\r\n'
    sleep "$SETTLE_S"; } \
    | nc -w "$NC_TIMEOUT" "$host" "$port" 2>/dev/null >/dev/null
}

# --- Assertions -----------------------------------------------------------

assert_set_ok() {
  local hp=$1 key=$2 val=$3 desc=$4 resp clean
  resp=$(mc_set "$hp" "$key" "$val" || true)
  clean=$(printf '%s' "$resp" | tr -d '\r')
  case "$clean" in
    *"STORED"*) pass "$desc" ;;
    *)          fail "$desc — expected STORED, got: $(printf '%s' "$resp" | tr '\r\n' '  ')" ;;
  esac
}

# Verify the key exists with the expected value via a memcache GET.
assert_has() {
  local hp=$1 key=$2 want=$3 desc=$4 resp clean body header
  resp=$(mc_get "$hp" "$key" || true)
  clean=$(printf '%s' "$resp" | tr -d '\r')
  header=$(printf '%s\n' "$clean" | sed -n '1p')
  body=$(printf   '%s\n' "$clean" | sed -n '2p')
  case "$header" in
    "VALUE $key 0 "*) ;;
    *)
      fail "$desc — expected '$want', got: $(printf '%s' "$resp" | tr '\r\n' '  ')"
      return ;;
  esac
  if [ "$body" = "$want" ]; then
    pass "$desc"
  else
    fail "$desc — expected '$want', got: $(printf '%s' "$resp" | tr '\r\n' '  ')"
  fi
}

# Read healthy host count from /metrics.
healthy_count() {
  curl -fsS "$METRICS_URL" 2>/dev/null \
    | awk '/^gomcrouter_upstream_healthy_hosts_total[ {]/ {print int($NF); exit}'
}

# Wait until the healthy host count gauge equals $1, up to $2 seconds.
wait_healthy_count() {
  local want=$1 timeout=$2 elapsed=0 cnt
  while [ "$elapsed" -lt "$timeout" ]; do
    cnt=$(healthy_count || echo "")
    if [ "$cnt" = "$want" ]; then
      return 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  return 1
}

# --- Compose helpers ------------------------------------------------------

COMPOSE=(docker compose -f docker-compose.yml)

stop_mc()  { "${COMPOSE[@]}" stop "$1" >/dev/null; }
start_mc() { "${COMPOSE[@]}" start "$1" >/dev/null; }

# Best-effort restoration if the script is interrupted mid-scenario.
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ] || [ "$FAIL" -gt 0 ]; then
    printf '\n%sRestoring memcache1/memcache2 to running state…%s\n' "$YELLOW" "$RESET" >&2
    "${COMPOSE[@]}" start memcache1 memcache2 >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

# --- Pre-flight -----------------------------------------------------------

command -v nc      >/dev/null || fatal "nc is required"
command -v curl    >/dev/null || fatal "curl is required"
command -v docker  >/dev/null || fatal "docker is required"

# Note: read into a variable rather than piping directly into grep -q.
# `set -o pipefail` would otherwise propagate SIGPIPE from `docker compose ps`
# (grep -q closes stdin on first match) and make this branch fire spuriously.
running_ps=$("${COMPOSE[@]}" ps --status running 2>/dev/null || true)
if ! printf '%s\n' "$running_ps" | grep -Fq gomcrouter; then
  fatal "docker compose stack is not running. Start it first: docker compose up -d"
fi

RUN_ID="$(date +%s)-$$"
printf '%sgomcrouter docker example: integration tests%s\n' "$BOLD" "$RESET"
printf 'run_id=%s\n' "$RUN_ID"
printf 'router=%s mc1=%s mc2=%s\n' "$ROUTER" "$MC1" "$MC2"

# Make sure we don't read stale data from a previous run.
step "Setup: flush_all on both memcaches and wait for 2 healthy hosts"
mc_flush_all "$MC1"
mc_flush_all "$MC2"
if wait_healthy_count 2 30; then
  pass "router reports 2 healthy hosts"
else
  fail "router did not report 2 healthy hosts within 30s (got: $(healthy_count))"
  exit 1
fi

# --- Scenario 1: fan-out --------------------------------------------------

step "Scenario 1: SET via router lands in both memcaches"
KEY="key-fanout-$RUN_ID"
VAL="alpha-$RUN_ID"
assert_set_ok "$ROUTER" "$KEY" "$VAL" "router accepted SET"
sleep 0.3
assert_has   "$MC1"    "$KEY" "$VAL" "memcache1 has key"
assert_has   "$MC2"    "$KEY" "$VAL" "memcache2 has key"

# --- Scenario 2: memcache1 down -------------------------------------------

step "Scenario 2: stop memcache1, router still serves writes/reads via memcache2"
stop_mc memcache1
if wait_healthy_count 1 "$HEALTHCHECK_GRACE"; then
  pass "router reports 1 healthy host after memcache1 stopped"
else
  fail "router did not detect memcache1 unhealthy within ${HEALTHCHECK_GRACE}s"
fi

KEY2="key-mc1-down-$RUN_ID"
VAL2="beta-$RUN_ID"
assert_set_ok "$ROUTER" "$KEY2" "$VAL2" "router accepted SET while mc1 down"
sleep 0.3
assert_has    "$MC2"    "$KEY2" "$VAL2" "memcache2 has new key"

resp=$(mc_get "$ROUTER" "$KEY2" || true)
body=$(printf '%s' "$resp" | tr -d '\r' | sed -n '2p')
if [ "$body" = "$VAL2" ]; then
  pass "router GET returns value via memcache2"
else
  fail "router GET — expected '$VAL2', got: $(printf '%s' "$resp" | tr '\r\n' '  ')"
fi

# --- Scenario 3: memcache1 back up ----------------------------------------

step "Scenario 3: restart memcache1, writes fan out to both again"
start_mc memcache1
if wait_healthy_count 2 "$HEALTHCHECK_GRACE"; then
  pass "router reports 2 healthy hosts after memcache1 restart"
else
  fail "router did not detect memcache1 healthy again within ${HEALTHCHECK_GRACE}s"
fi

KEY3="key-mc1-back-$RUN_ID"
VAL3="gamma-$RUN_ID"
assert_set_ok "$ROUTER" "$KEY3" "$VAL3" "router accepted SET after mc1 came back"
sleep 0.3
assert_has    "$MC1"    "$KEY3" "$VAL3" "memcache1 has the new key"
assert_has    "$MC2"    "$KEY3" "$VAL3" "memcache2 has the new key"

# --- Scenario 4: memcache2 down -------------------------------------------

step "Scenario 4: stop memcache2, router still serves writes/reads via memcache1"
stop_mc memcache2
if wait_healthy_count 1 "$HEALTHCHECK_GRACE"; then
  pass "router reports 1 healthy host after memcache2 stopped"
else
  fail "router did not detect memcache2 unhealthy within ${HEALTHCHECK_GRACE}s"
fi

KEY4="key-mc2-down-$RUN_ID"
VAL4="delta-$RUN_ID"
assert_set_ok "$ROUTER" "$KEY4" "$VAL4" "router accepted SET while mc2 down"
sleep 0.3
assert_has    "$MC1"    "$KEY4" "$VAL4" "memcache1 has new key"

resp=$(mc_get "$ROUTER" "$KEY4" || true)
body=$(printf '%s' "$resp" | tr -d '\r' | sed -n '2p')
if [ "$body" = "$VAL4" ]; then
  pass "router GET returns value via memcache1"
else
  fail "router GET — expected '$VAL4', got: $(printf '%s' "$resp" | tr '\r\n' '  ')"
fi

# --- Scenario 5: memcache2 back up ----------------------------------------

step "Scenario 5: restart memcache2, writes fan out to both again"
start_mc memcache2
if wait_healthy_count 2 "$HEALTHCHECK_GRACE"; then
  pass "router reports 2 healthy hosts after memcache2 restart"
else
  fail "router did not detect memcache2 healthy again within ${HEALTHCHECK_GRACE}s"
fi

KEY5="key-mc2-back-$RUN_ID"
VAL5="epsilon-$RUN_ID"
assert_set_ok "$ROUTER" "$KEY5" "$VAL5" "router accepted SET after mc2 came back"
sleep 0.3
assert_has    "$MC1"    "$KEY5" "$VAL5" "memcache1 has the new key"
assert_has    "$MC2"    "$KEY5" "$VAL5" "memcache2 has the new key"

# --- Summary --------------------------------------------------------------

printf '\n%s==========================================%s\n' "$BOLD" "$RESET"
printf '%s passed, %s failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  printf '\nFailed:\n'
  for n in "${FAIL_NAMES[@]}"; do
    printf '  - %s\n' "$n"
  done
  exit 1
fi
printf '%sAll tests passed.%s\n' "$GREEN" "$RESET"
