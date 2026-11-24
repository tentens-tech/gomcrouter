#!/usr/bin/env bash
# End-to-end tests of gomcrouter in docker.
#
#   test/e2e/run.sh                   # all suites
#   test/e2e/run.sh routes failover   # selected suites
#   test/e2e/run.sh -l                # list suites
#
# Env: E2E_KEEP=1 keeps containers after the run, E2E_LOGDIR overrides the
# logs directory (default test/e2e/.logs), E2E_NO_BUILD=1 reuses the images.
#
# Every container is attached to the gmr-e2e network: memcached nodes are
# reachable as memcache1 and memcache2, routers by their container name.
# Requests are sent from the gmr-tools container. Routers run like in
# production: read-only root FS, non-root user, no capabilities.
# Checks are passed to eval in single quotes on purpose (variables they use look unused)
# shellcheck disable=SC2016,SC2034
set -u

REPO=$(cd "$(dirname "$0")/../.." && pwd)
E2E=$REPO/test/e2e
IMG=gomcrouter:e2e
TIMG=gomcrouter:e2e-tools
MEMCACHED=memcached:1.6-alpine
VERSION=v0.0.0-e2e
NET=gmr-e2e
# static addresses are used by the dns suite only
SUBNET=10.213.77.0/24
LOGDIR=${E2E_LOGDIR:-$E2E/.logs}
SUITES=(image flags protocol routes example failover late dns metrics load signals)
PASS=0 FAIL=0 FAILED=()
# router flags used by every suite: fast health checks and DNS refresh
FAST=(--healthcheck-interval 1s --dns-cache-ttl 2s --timeout-ms 500)

# ---------------------------------------------------------------- helpers

ok()  { echo "  PASS: $*"; PASS=$((PASS + 1)); }
bad() { echo "  FAIL: $*"; FAIL=$((FAIL + 1)); FAILED+=("$SUITE: $*"); }
# check <desc> <command...>
check() { local desc=$1; shift; if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi; }
# waitcheck <desc> <seconds> <command...>: retry the command every second
waitcheck() {
  local desc=$1 secs=$2 i; shift 2
  for ((i = 0; i < secs; i++)); do
    "$@" >/dev/null 2>&1 && { ok "$desc"; return; }
    sleep 1
  done
  bad "$desc"
}

# Helpers act on the current router container $CUR
tools()   { docker exec gmr-tools "$@"; }
# mc <request>...: send requests to the router (\r\n are unescaped), print the responses
mc()      { tools e2e-client -addr "$CUR:${PORT:-8080}" raw "$@"; }
# direct <1|2|host:port> <request>...: send requests to a memcached node, bypassing the router
direct()  { local a=$1; shift; case $a in 1|2) a=memcache$a:11211 ;; esac; tools e2e-client -addr "$a" raw "$@"; }
# expect <response> <command...>: the output of the command equals the response (\r\n are unescaped)
expect()  { local want; want=$(printf '%b' "$1"); shift; [ "$("$@")" = "$want" ]; }
# has <text> <command...>: the output of the command contains the text
has()     { local want=$1; shift; "$@" | grep -qF -- "$want"; }
metrics() { tools curl -sf "$CUR:${MPORT:-9090}/metrics"; }
metric()  { metrics | grep -qF -- "$1"; }
healthy() { metric "gomcrouter_upstream_healthy_hosts_total $1"; }
logs()    { docker logs "$CUR" 2>&1; }
has_log() { logs | grep -qF -- "$1"; }
running() { [ "$(docker inspect -f '{{.State.Running}}' "$CUR" 2>/dev/null)" = true ]; }
stopped() { ! running; }
exit_code() { [ "$(docker inspect -f '{{.State.ExitCode}}' "$CUR")" = "$1" ]; }
# key <name>: a key unique for the run
key()     { echo "e2e-$SUITE-$1-$RUN"; }
# set_via <command> <key> <value>: store a value with the command (set, add, ...)
store()   { mc "set $1 0 0 ${#2}\r\n$2\r\n"; }

# start <name> <config|-> [docker args...] [-- router args...]
# Starts a hardened router container, <config> is relative to the repo root
start() {
  CUR=gmr-$1; local cfg=$2 dargs=() rargs=(); shift 2
  while [ $# -gt 0 ]; do
    [ "$1" = "--" ] && { shift; rargs=("$@"); break; }
    dargs+=("$1"); shift
  done
  [ "$cfg" != - ] && rargs=(-f "/repo/$cfg" ${rargs[@]+"${rargs[@]}"})
  docker rm -f "$CUR" >/dev/null 2>&1
  docker run -d --name "$CUR" --network $NET \
    --read-only --security-opt no-new-privileges --cap-drop ALL \
    -v "$REPO/examples:/repo/examples:ro" -v "$E2E/configs:/repo/test/e2e/configs:ro" \
    ${dargs[@]+"${dargs[@]}"} \
    $TIMG /bin/gomcrouter "${FAST[@]}" ${rargs[@]+"${rargs[@]}"} >/dev/null
}
# finish [container]: save logs and remove the container
finish() {
  local c=${1:-$CUR}
  docker logs "$c" >"$LOGDIR/$c.log" 2>&1
  [ -n "${E2E_KEEP:-}" ] || docker rm -f "$c" >/dev/null 2>&1
}
# stop_with <signal>: send a signal to the router, return the seconds until it exited
stop_with() {
  local t0=$SECONDS
  docker kill -s "$1" "$CUR" >/dev/null
  for ((i = 0; i < 40; i++)); do stopped && break; sleep 0.5; done
  echo $((SECONDS - t0))
}

# memcached <name> <alias> [docker args...]: start a memcached node
memcached() {
  local name=$1 alias=$2; shift 2
  docker rm -f "$name" >/dev/null 2>&1
  docker run -d --name "$name" --network $NET --network-alias "$alias" "$@" $MEMCACHED >/dev/null
}
mc_up()   { docker start "gmr-mc$1" >/dev/null; }
mc_down() { docker stop -t 1 "gmr-mc$1" >/dev/null; }
# flush: clear both memcached nodes
flush()   { direct 1 'flush_all\r\n' >/dev/null; direct 2 'flush_all\r\n' >/dev/null; }

cleanup() {
  docker ps -aq --filter name='^gmr-' | xargs docker rm -f >/dev/null 2>&1
  docker network rm $NET >/dev/null 2>&1
}
teardown() {
  local c
  for c in gmr-mc1 gmr-mc2; do
    docker inspect "$c" >/dev/null 2>&1 && docker logs "$c" >"$LOGDIR/$c.log" 2>&1
  done
  [ -n "${E2E_KEEP:-}" ] || cleanup
}

# ---------------------------------------------------------------- suites

suite_image() {
  check "image runs as uid 10001" eval '[ "$(docker run --rm $IMG id -u)" = 10001 ]'
  check "binary is root-owned and read-only" eval \
    'docker run --rm $IMG stat -c "%u %a" /bin/gomcrouter | grep -qx "0 555"'
  check "--version" eval '[ "$(docker run --rm $IMG /bin/gomcrouter --version)" = "gomcrouter version $VERSION" ]'
  check "-v" eval 'docker run --rm $IMG /bin/gomcrouter -v | grep -qF "$VERSION"'
  check "--help lists the flags" eval 'docker run --rm $IMG /bin/gomcrouter --help | grep -q -- --router-config'
  check "OCI version label" eval \
    '[ "$(docker inspect -f "{{index .Config.Labels \"org.opencontainers.image.version\"}}" $IMG)" = "$VERSION" ]'
  local out rc
  out=$(docker run --rm $IMG 2>&1); rc=$?
  check "default command without a config: exit code 1" eval '[ $rc = 1 ]'
  check "default command without a config: error is printed" \
    eval 'grep -qF "open router.yaml: no such file or directory" <<<"$out"'
}

suite_flags() {
  start missing test/e2e/configs/nope.yaml
  waitcheck "missing router config: exits" 10 stopped
  check "missing router config: exit code 1" exit_code 1
  check "missing router config: error is printed" has_log "no such file or directory"
  finish
  start invalid test/e2e/configs/invalid.yaml
  waitcheck "invalid yaml: exits" 10 stopped
  check "invalid yaml: exit code 1" exit_code 1
  check "invalid yaml: error is printed" has_log "yaml:"
  finish
  start badlevel test/e2e/configs/default.yaml -- --log-level nope
  waitcheck "invalid --log-level: exits" 10 stopped
  check "invalid --log-level: exit code 1" exit_code 1
  finish

  start custom test/e2e/configs/default.yaml -- \
    -l tcp://:11311 -L :9191 -C 3 -n 2 -b 65536 -r 700 -t 3s -H 1s
  PORT=11311 MPORT=9191 waitcheck "custom --listen and --metrics-listen-addr" 15 healthy 2
  check "startup log: listen" has_log "listen: tcp://:11311"
  check "startup log: metrics listen" has_log "metrics listen: http://:9191"
  check "startup log: upstream connections" has_log "upstream connections: 3"
  check "startup log: server threads" has_log "server threads: 2"
  check "startup log: buffer size" has_log "buffer size: 65536"
  check "startup log: timeout" has_log "request timeout milliseconds: 700"
  check "startup log: dns cache ttl" has_log "dns cache ttl: 3s"
  PORT=11311 check "requests on the custom port" expect 'STORED\r\n' store "$(key custom)" v
  PORT=11311 check "load on 2 event loops and 3 upstream connections" \
    tools e2e-client -addr "$CUR:11311" load -conns 8 -ops 200 -pipeline 8
  check "default port is not used" eval '! tools e2e-client -timeout 1s -addr "$CUR:8080" raw "version\r\n"'
  finish

  start quiet test/e2e/configs/default.yaml -- --log-level error
  waitcheck "--log-level error: ready" 15 healthy 2
  check "--log-level error: no info logs" eval '! has_log "\"level\":\"info\""'
  finish
}

suite_protocol() {
  start proto test/e2e/configs/default.yaml
  waitcheck "ready" 15 healthy 2
  flush
  local k k2 cas
  k=$(key k) k2=$(key k2)
  check "get miss" expect 'END\r\n' mc "get $k\r\n"
  check "set" expect 'STORED\r\n' mc "set $k 5 0 3\r\nabc\r\n"
  check "get hit with flags" expect "VALUE $k 5 3\r\nabc\r\nEND\r\n" mc "get $k\r\n"
  check "add of an existing key" expect 'NOT_STORED\r\n' mc "add $k 0 0 1\r\nx\r\n"
  check "add of a new key" expect 'STORED\r\n' mc "add $k2 0 0 1\r\n1\r\n"
  check "multi-key get" expect "VALUE $k 5 3\r\nabc\r\nVALUE $k2 0 1\r\n1\r\nEND\r\n" mc "get $k $k2\r\n"
  check "replace of a missing key" expect 'NOT_STORED\r\n' mc "replace $(key none) 0 0 1\r\nx\r\n"
  check "replace" expect 'STORED\r\n' mc "replace $k 0 0 3\r\nxyz\r\n"
  check "append" expect 'STORED\r\n' mc "append $k 0 0 2\r\n12\r\n"
  check "prepend" expect 'STORED\r\n' mc "prepend $k 0 0 2\r\n00\r\n"
  check "append and prepend applied" has "00xyz12" mc "get $k\r\n"
  cas=$(mc "gets $k\r\n" | awk 'NR==1 {print $5}' | tr -d '\r')
  check "gets returns a cas unique" eval '[ -n "$cas" ]'
  check "cas with a stale unique" expect 'EXISTS\r\n' mc "cas $k 0 0 1 $((cas + 1000))\r\nz\r\n"
  check "cas with the current unique" expect 'STORED\r\n' mc "cas $k 0 0 1 $cas\r\nz\r\n"
  check "incr" expect '11\r\n' mc "incr $k2 10\r\n"
  check "decr" expect '8\r\n' mc "decr $k2 3\r\n"
  check "incr of a missing key" expect 'NOT_FOUND\r\n' mc "incr $(key none) 1\r\n"
  check "touch" expect 'TOUCHED\r\n' mc "touch $k 100\r\n"
  check "delete" expect 'DELETED\r\n' mc "delete $k\r\n"
  check "delete of a missing key" expect 'NOT_FOUND\r\n' mc "delete $k\r\n"
  check "version is proxied" has "VERSION 1." mc 'version\r\n'
  check "verbosity" expect 'OK\r\n' mc 'verbosity 1\r\n'
  check "stats" eval 'mc "stats\r\n" | grep -q "^STAT pid" && mc "stats\r\n" | tail -1 | grep -q "^END"'
  check "flush_all" expect 'OK\r\n' mc 'flush_all\r\n'
  check "flush_all applied" expect 'END\r\n' mc "get $k2\r\n"
  check "empty value" expect "STORED\r\nVALUE $k 0 0\r\n\r\nEND\r\n" mc "set $k 0 0 0\r\n\r\n" "get $k\r\n"
  check "pipelined requests in one packet" \
    expect "STORED\r\nVALUE $k 0 2\r\nhi\r\nEND\r\nDELETED\r\n" mc "set $k 0 0 2\r\nhi\r\nget $k\r\ndelete $k\r\n"
  check "request split across packets" eval \
    'tools e2e-client -addr "$CUR:8080" raw "set $k 0 0 4\r\nab" "cd\r\n" "get $k\r\n" | tr -d "\r" | tr "\n" " " | grep -q "STORED VALUE $k 0 4 abcd END"'
  check "binary-safe value" \
    expect "STORED\r\nVALUE $k 0 5\r\na\tb\rc\r\nEND\r\n" mc "set $k 0 0 5\r\na\tb\rc\r\n" "get $k\r\n"
  check "value of 200KB" tools e2e-client -addr "$CUR:8080" load -conns 2 -ops 10 -size 200000
  check "value of 900KB (not pooled)" tools e2e-client -addr "$CUR:8080" load -conns 1 -ops 3 -size 900000
  check "malformed request" has "CLIENT_ERROR" mc 'set\r\n'
  check "unknown command" has "ERROR" mc 'bogus\r\n'
  check "router keeps serving after errors" expect 'END\r\n' mc "get $(key none)\r\n"
  check "quit closes the connection" expect '' mc 'quit\r\n'
  check "replies to pipelined requests keep their order" \
    tools e2e-client -addr "$CUR:8080" load -conns 16 -ops 300 -pipeline 16
  check "no upstream errors (quit isn't sent to memcached)" eval '! has_log "route: "'
  finish
}

suite_routes() {
  local k
  start default test/e2e/configs/default.yaml
  waitcheck "DefaultRoute: ready" 15 healthy 2
  flush; k=$(key default)
  check "DefaultRoute: set" expect 'STORED\r\n' store "$k" d
  check "DefaultRoute: value is on memcache1" has "VALUE $k" direct 1 "get $k\r\n"
  check "DefaultRoute: value is not on memcache2" expect 'END\r\n' direct 2 "get $k\r\n"
  direct 2 "set $k-2 0 0 1\r\nx\r\n" >/dev/null
  check "DefaultRoute: get doesn't fail over to memcache2" expect 'END\r\n' mc "get $k-2\r\n"
  finish

  start allfastest test/e2e/configs/allfastest.yaml
  waitcheck "AllFastestRoute: ready" 15 healthy 2
  flush; k=$(key allfastest)
  check "AllFastestRoute: set" expect 'STORED\r\n' store "$k" a
  check "AllFastestRoute: value is on memcache1" has "VALUE $k" direct 1 "get $k\r\n"
  check "AllFastestRoute: value is on memcache2" has "VALUE $k" direct 2 "get $k\r\n"
  check "AllFastestRoute: delete" expect 'DELETED\r\n' mc "delete $k\r\n"
  check "AllFastestRoute: deleted on memcache1" expect 'END\r\n' direct 1 "get $k\r\n"
  check "AllFastestRoute: deleted on memcache2" expect 'END\r\n' direct 2 "get $k\r\n"
  finish

  start missfailover test/e2e/configs/missfailover.yaml
  waitcheck "MissFailoverRoute: ready" 15 healthy 2
  flush; k=$(key missfailover)
  direct 2 "set $k 0 0 2\r\nm2\r\n" >/dev/null
  check "MissFailoverRoute: miss on memcache1 is served by memcache2" expect "VALUE $k 0 2\r\nm2\r\nEND\r\n" mc "get $k\r\n"
  direct 1 "set $k 0 0 2\r\nm1\r\n" >/dev/null
  check "MissFailoverRoute: a hit on memcache1 wins" has "m1" mc "get $k\r\n"
  check "MissFailoverRoute: miss on all hosts" expect 'END\r\n' mc "get $(key none)\r\n"
  check "MissFailoverRoute: set goes to memcache1 only" eval \
    'store "$k-s" s >/dev/null; direct 1 "get $k-s\r\n" | grep -q VALUE && direct 2 "get $k-s\r\n" | grep -qx "END\r"'
  finish

  start local test/e2e/configs/local.yaml
  waitcheck "LocalRoute: ready" 15 healthy 2
  check "LocalRoute: version is answered by the router" expect "VERSION $VERSION gomcrouter\r\n" mc 'version\r\n'
  check "LocalRoute: other commands are rejected" has "SERVER_ERROR gomcrouter: request does not match policy handler" \
    mc "get $(key local)\r\n"
  check "LocalRoute: quit closes the connection" expect '' mc 'quit\r\n'
  finish

  start unknown test/e2e/configs/unknown-route.yaml
  waitcheck "unknown route type: ready" 15 healthy 2
  flush; k=$(key unknown)
  check "unknown route type: falls back to DefaultRoute" eval \
    'store "$k" u >/dev/null; direct 1 "get $k\r\n" | grep -q VALUE && direct 2 "get $k\r\n" | grep -qx "END\r"'
  finish

  start selector-default test/e2e/configs/selector-default.yaml
  waitcheck "OperationSelectorRoute without policies: ready" 15 healthy 2
  flush; k=$(key selector-default)
  check "OperationSelectorRoute without policies: DefaultRoute for every command" eval \
    'store "$k" s >/dev/null; direct 1 "get $k\r\n" | grep -q VALUE && direct 2 "get $k\r\n" | grep -qx "END\r"'
  check "OperationSelectorRoute without policies: version is proxied" has "VERSION 1." mc 'version\r\n'
  finish

  start empty test/e2e/configs/empty-pool.yaml
  waitcheck "empty pool: starts" 15 healthy 0
  check "empty pool: no healthy upstream" has "SERVER_ERROR gomcrouter: no healthy upstream" mc "get $(key empty)\r\n"
  finish
}

# examples/docker/gomcrouter.yaml: OperationSelectorRoute with every policy
suite_example() {
  local k
  start example examples/docker/gomcrouter.yaml
  waitcheck "ready" 15 healthy 2
  flush; k=$(key example)
  check "set fans out (AllFastestRoute)" eval \
    'store "$k" e | grep -q STORED && direct 1 "get $k\r\n" | grep -q VALUE && direct 2 "get $k\r\n" | grep -q VALUE'
  direct 2 "set $k-2 0 0 2\r\nm2\r\n" >/dev/null
  check "get fails over on a miss (MissFailoverRoute)" has "m2" mc "get $k-2\r\n"
  check "gets fails over on a miss (MissFailoverRoute)" has "m2" mc "gets $k-2\r\n"
  check "version is answered locally (LocalRoute)" expect "VERSION $VERSION gomcrouter\r\n" mc 'version\r\n'
  check "incr fans out (AllFastestRoute)" eval \
    'mc "set $k-n 0 0 1\r\n1\r\n" >/dev/null; mc "incr $k-n 4\r\n" | grep -q 5 && direct 2 "get $k-n\r\n" | grep -q "^5"'
  check "touch has no policy: DefaultRoute" expect 'TOUCHED\r\n' mc "touch $k 100\r\n"
  check "delete fans out (AllFastestRoute)" eval \
    'mc "delete $k\r\n" | grep -q DELETED && direct 1 "get $k\r\n" | grep -qx "END\r" && direct 2 "get $k\r\n" | grep -qx "END\r"'
  finish
}

suite_failover() {
  local k
  start failover examples/docker/gomcrouter.yaml
  waitcheck "ready with 2 healthy hosts" 15 healthy 2
  flush; k=$(key failover)

  mc_down 1
  waitcheck "memcache1 down: 1 healthy host" 20 healthy 1
  check "memcache1 down: set" expect 'STORED\r\n' store "$k-1" a
  check "memcache1 down: value is on memcache2" has "VALUE $k-1" direct 2 "get $k-1\r\n"
  check "memcache1 down: get" has "VALUE $k-1" mc "get $k-1\r\n"
  check "memcache1 down: log" has_log "host memcache1:11211 went unhealthy"
  mc_up 1
  waitcheck "memcache1 up: 2 healthy hosts" 20 healthy 2
  check "memcache1 up: log" has_log "host memcache1:11211 went healthy"
  check "memcache1 up: set fans out to both" eval \
    'store "$k-2" b | grep -q STORED && direct 1 "get $k-2\r\n" | grep -q VALUE && direct 2 "get $k-2\r\n" | grep -q VALUE'

  mc_down 2
  waitcheck "memcache2 down: 1 healthy host" 20 healthy 1
  check "memcache2 down: set" expect 'STORED\r\n' store "$k-3" c
  check "memcache2 down: get" has "VALUE $k-3" mc "get $k-3\r\n"

  mc_down 1
  waitcheck "both down: 0 healthy hosts" 20 healthy 0
  check "both down: no healthy upstream" has "no healthy upstream" mc "get $k-3\r\n"
  check "both down: router is running" running
  mc_up 1; mc_up 2
  waitcheck "both up: 2 healthy hosts" 20 healthy 2
  check "both up: set fans out to both" eval \
    'store "$k-4" d | grep -q STORED && direct 1 "get $k-4\r\n" | grep -q VALUE && direct 2 "get $k-4\r\n" | grep -q VALUE'

  # DefaultRoute serves from the first healthy host
  start failover-default test/e2e/configs/default.yaml
  waitcheck "DefaultRoute: ready" 15 healthy 2
  mc_down 1
  waitcheck "DefaultRoute: memcache1 down" 20 healthy 1
  check "DefaultRoute: memcache2 takes over" eval \
    'store "$k-5" e | grep -q STORED && direct 2 "get $k-5\r\n" | grep -q VALUE'
  mc_up 1
  waitcheck "DefaultRoute: memcache1 back" 20 healthy 2
  check "DefaultRoute: memcache1 is first again" eval \
    'store "$k-6" f | grep -q STORED && direct 1 "get $k-6\r\n" | grep -q VALUE'
  finish
  CUR=gmr-failover; finish
}

suite_late() {
  start late test/e2e/configs/late.yaml -- --log-level debug
  waitcheck "starts while the upstream doesn't exist" 15 has "no healthy upstream" mc "get $(key late)\r\n"
  check "0 healthy hosts" healthy 0
  check "unhealthy host is logged" has_log "added unhealthy host: memcache-late:11211"
  waitcheck "--log-level debug: resolve errors are logged" 10 has_log '"level":"debug"'
  memcached gmr-mc-late memcache-late
  waitcheck "upstream becomes healthy after it starts" 30 healthy 1
  check "requests are served" expect 'STORED\r\n' store "$(key late)" l
  check "value is on the new upstream" has "VALUE" direct memcache-late:11211 "get $(key late)\r\n"
  finish
  finish gmr-mc-late
}

suite_dns() {
  local k
  k=$(key dns)
  memcached gmr-dns-a memcache-dns --ip 10.213.77.10
  start dns test/e2e/configs/dns.yaml
  waitcheck "ready" 15 healthy 1
  check "set on the first address" expect 'STORED\r\n' store "$k" a
  check "value is on the first node" has "VALUE" direct 10.213.77.10:11211 "get $k\r\n"
  finish gmr-dns-a
  memcached gmr-dns-b memcache-dns --ip 10.213.77.11
  waitcheck "address change is detected" 20 has_log "address changed to 10.213.77.11:11211"
  waitcheck "healthy on the new address" 20 healthy 1
  check "requests go to the new node" eval \
    'store "$k-b" b | grep -q STORED && direct 10.213.77.11:11211 "get $k-b\r\n" | grep -q VALUE'
  finish
  finish gmr-dns-b
}

suite_metrics() {
  local k
  start metrics examples/docker/gomcrouter.yaml
  waitcheck "ready" 15 healthy 2
  flush; k=$(key metrics)
  store "$k" v >/dev/null
  mc "get $k\r\n" "get $k\r\n" "get $(key none)\r\n" >/dev/null
  waitcheck "requests_total" 15 metric 'gomcrouter_requests_total{cmd="get"} 3'
  check "requests_total for set" metric 'gomcrouter_requests_total{cmd="set"} 1'
  check "upstream_requests_total per host" metric 'gomcrouter_upstream_requests_total{cmd="set",host="memcache2:11211"} 1'
  check "upstream_get_hits_total" metric 'gomcrouter_upstream_get_hits_total{host="memcache1:11211"} 2'
  check "upstream_get_misses_total on both hosts" eval \
    'metric "gomcrouter_upstream_get_misses_total{host=\"memcache1:11211\"} 1" && metric "gomcrouter_upstream_get_misses_total{host=\"memcache2:11211\"} 1"'
  check "request_duration_milliseconds histogram" metric 'gomcrouter_request_duration_milliseconds_bucket{cmd="get",le='
  check "upstream_request_duration_milliseconds histogram" metric 'gomcrouter_upstream_request_duration_milliseconds_count{cmd="set",host="memcache1:11211"}'
  check "upstream_connections_total" metric 'gomcrouter_upstream_connections_total{host="memcache1:11211"}'
  check "healthy hosts" healthy 2
  check "version" metric "gomcrouter_version{version=\"$VERSION\"} 1"
  check "go runtime metrics" metric 'go_goroutines'
  check "only /metrics is served" eval '[ "$(tools curl -s -o /dev/null -w "%{http_code}" "$CUR:9090/debug/pprof/")" = 404 ]'
  mc_down 1
  waitcheck "healthy hosts after a failure" 20 healthy 1
  mc_up 1
  waitcheck "healthy hosts after the recovery" 20 healthy 2
  finish
}

suite_load() {
  start load examples/docker/gomcrouter.yaml
  waitcheck "ready" 15 healthy 2
  check "32 connections, pipeline 16 (fan-out writes, failover reads)" \
    tools e2e-client -addr "$CUR:8080" load -conns 32 -ops 500 -size 200 -pipeline 16
  check "4KB values" tools e2e-client -addr "$CUR:8080" load -conns 8 -ops 200 -size 4096 -pipeline 4
  check "no proxy errors" eval '! has_log "proxy error"'
  finish
  start load-loops test/e2e/configs/default.yaml -- -n 4 -C 4
  waitcheck "4 event loops, 4 upstream connections: ready" 15 healthy 2
  check "4 event loops, 4 upstream connections: 64 connections" \
    tools e2e-client -addr "$CUR:8080" load -conns 64 -ops 300 -size 100 -pipeline 8
  finish
}

suite_signals() {
  local s t
  for s in INT TERM; do
    start "sig-$s" test/e2e/configs/default.yaml
    waitcheck "SIG$s: ready" 15 healthy 2
    t=$(stop_with "$s")
    check "SIG$s: stops within 5s (${t}s)" eval '[ "$t" -le 5 ] && stopped'
    check "SIG$s: exit code 0" exit_code 0
    check "SIG$s: graceful shutdown" has_log "shutting down"
    finish
  done
  start stop test/e2e/configs/default.yaml
  waitcheck "docker stop: ready" 15 healthy 2
  t=$SECONDS; docker stop -t 10 "$CUR" >/dev/null; t=$((SECONDS - t))
  check "docker stop: graceful within 5s (${t}s)" eval '[ "$t" -le 5 ] && exit_code 0'
  finish
}

# ---------------------------------------------------------------- main

if [ "${1:-}" = -l ]; then printf '%s\n' "${SUITES[@]}"; exit 0; fi
selected=("$@"); [ $# -eq 0 ] && selected=("${SUITES[@]}")
for s in "${selected[@]}"; do
  declare -F "suite_$s" >/dev/null || { echo "unknown suite: $s (see -l)"; exit 2; }
done

rm -rf "$LOGDIR"; mkdir -p "$LOGDIR"
cleanup
trap teardown EXIT
RUN=$(date +%s)

SUITE=build
echo "### build"
if [ -z "${E2E_NO_BUILD:-}" ]; then
  docker build -q -t $IMG --build-arg VERSION=$VERSION -f "$REPO/Dockerfile" "$REPO" >/dev/null || { echo "image build failed"; exit 1; }
  docker build -q -t $TIMG --build-arg IMAGE=$IMG -f "$E2E/tools.Dockerfile" "$E2E" >/dev/null || { echo "tools image build failed"; exit 1; }
fi
docker network create --subnet $SUBNET $NET >/dev/null || { echo "network create failed"; exit 1; }
docker run -d --name gmr-tools --network $NET $TIMG sleep 86400 >/dev/null
memcached gmr-mc1 memcache1
memcached gmr-mc2 memcache2
waitcheck "memcached nodes are up" 15 eval 'direct 1 "version\r\n" | grep -q VERSION && direct 2 "version\r\n" | grep -q VERSION'

for SUITE in "${selected[@]}"; do
  echo "### $SUITE"
  t0=$SECONDS
  "suite_$SUITE"
  echo "  ($((SECONDS - t0))s)"
done

SUITE=logs
check "no panics in any log" eval "! grep -l 'panic\|fatal error\|DATA RACE' $LOGDIR/*.log"

echo
echo "RESULT: $PASS passed, $FAIL failed (logs: $LOGDIR)"
for f in ${FAILED[@]+"${FAILED[@]}"}; do echo "  - $f"; done
[ $FAIL = 0 ]
