# gomcrouter docker example

Minimal docker-compose setup for local testing: two memcached nodes, gomcrouter
built from the repo, a writer that produces hits/misses, and a metrics watcher
that highlights counters as they change.

## How it works

```text
                    ┌────────────────────────────────────────────────┐
                    │                docker network                  │
                    │                                                │
   ┌──────────┐     │    set / get         ┌──────────────────┐      │
   │          │     │   (memcached         │                  │      │
   │  writer  │─────┼─▶   text proto)      │    gomcrouter    │      │
   │          │     │    :8080             │                  │      │
   └──────────┘     │                      │  route:          │      │
      busybox       │                      │  OperationSelect │      │
   loop 1s:         │                      │                  │      │
   • SET mykey      │                      │  ┌─ set/add/... ─┼──┐   │
     ttl=3600s      │                      │  │ AllFastestRt  │  │   │
   • 10× GET mykey  │                      │  │               │  │   │
   • 1×  GET wrong  │                      │  ├─ get/gets ────┼──┤   │
                    │                      │  │ MissFailover  │  │   │
                    │                      │  │               │  │   │
                    │                      │  └───────────────┼──┤   │
                    │                      │                  │  │   │
                    │            ┌─────────┤  metrics :9090   │  │   │
                    │            │ GET     │  /metrics        │  │   │
                    │            │         └──────┬───────────┘  │   │
                    │            │                │              │   │
                    │            │                │ upstream     │   │
                    │            │                ▼              │   │
                    │            │         ┌──────────────┐      │   │
                    │            │         │  memcache1   │ ◀────┘   │
                    │            │         │   :11211     │          │
                    │            │         └──────────────┘          │
                    │            │                                   │
                    │            │         ┌──────────────┐          │
                    │            │         │  memcache2   │ ◀────────┤
                    │            │         │   :11211     │  (miss   │
                    │            │         └──────────────┘  failover│
                    │            │                            or all │
                    │            │                            fastest)
                    │   ┌────────┴────────┐                          │
                    │   │ metrics-watcher │                          │
                    │   │    busybox      │                          │
                    │   │  poll every 2s  │                          │
                    │   │  * changed      │                          │
                    │   │  + new          │                          │
                    │   └─────────────────┘                          │
                    │                                                │
                    └────────────────────────────────────────────────┘

  host ports:  8080 → gomcrouter (memcached)    11211 → memcache1
               9090 → gomcrouter (metrics)      11212 → memcache2
```

Request flow per writer tick:

- `SET mykey 0 3600 …` → gomcrouter → `AllFastestRoute` → fans out to **both**
  memcaches, returns first `STORED`.
- `GET mykey` → `MissFailoverRoute` → tries memcache1 first, on `END`
  (miss) falls back to memcache2; on `VALUE` returns immediately.
- `GET missingkey` → same route, both miss, client sees `END`. This is the
  metric you will see ticking up on `gomcrouter_upstream_get_misses_total`.

## Layout

- `docker-compose.yml` — services: `memcache1`, `memcache2`, `gomcrouter`, `writer`, `metrics-watcher`
- `gomcrouter.yaml` — router config (two-node `orderedPool`, miss-failover on `get`/`gets`)
- `writer.sh` — `SET mykey` with TTL 1h, then loops: 10 × `get mykey` + 1 × `get missingkey` per second
- `metrics.sh` — polls `http://gomcrouter:9090/metrics` every 2s; prefixes lines with `*` (changed), `+` (new), or spaces (unchanged)
- `test.sh` — host-side end-to-end tests (see [Tests](#tests))

## Run

Bring everything up (builds the gomcrouter image from the repo root):

```sh
docker compose up --build
```

Watch only the metrics view (recommended in a second terminal):

```sh
docker compose logs -f metrics-watcher
```

Watch the writer's hit/miss heartbeat:

```sh
docker compose logs -f writer
```

Hit the router directly from the host (requires a memcached client, e.g. `nc`):

```sh
printf "get mykey\r\n" | nc -w 1 localhost 8080
```

Scrape metrics from the host:

```sh
curl -s http://localhost:9090/metrics | grep ^gomcrouter_
```

Tear it all down:

```sh
docker compose down -v
```

## Example metrics-watcher output

```text
=== gomcrouter metrics @ 12:03:47 ===

  gomcrouter_version{version="dev"} 1
* gomcrouter_requests_total{cmd="get"} 132
* gomcrouter_requests_total{cmd="set"} 1
* gomcrouter_upstream_get_hits_total{host="memcache1:11211"} 120
* gomcrouter_upstream_get_misses_total{host="memcache1:11211"} 12
* gomcrouter_upstream_get_misses_total{host="memcache2:11211"} 12
  gomcrouter_upstream_healthy_hosts_total 2
  gomcrouter_upstream_connections_total{host="memcache1:11211"} 1
  gomcrouter_upstream_connections_total{host="memcache2:11211"} 1
```

Lines prefixed with `*` changed since the last poll, `+` appeared for the
first time, spaces mean the value is stable.

## Endpoints

- `tcp://localhost:8080` — gomcrouter memcached protocol
- `http://localhost:9090/metrics` — Prometheus metrics
- `tcp://localhost:11211` — memcache1 (direct)
- `tcp://localhost:11212` — memcache2 (direct)

## Tests

`test.sh` is a host-side integration test against the running compose stack.
It exercises the routing topology end-to-end and asserts on both the
gomcrouter-facing protocol and the underlying memcaches.

### Prerequisites

- The stack is up: `docker compose up -d`
- Host tools: `nc` (BSD or GNU), `curl`, `docker`, plus standard `awk`/`grep`/`printf`
- Free local ports: 8080 (gomcrouter), 9090 (metrics), 11211 (memcache1), 11212 (memcache2)

### Run

```sh
docker compose up -d
./test.sh
```

### What it covers

1. **Fan-out write.** SET via gomcrouter must land in **both** memcaches
   (because `set` → `AllFastestRoute` in `gomcrouter.yaml`). The script
   verifies the value via direct connections to ports `11211` and `11212`.
2. **memcache1 down.** Stop `memcache1`, wait for gomcrouter to mark it
   unhealthy via `/metrics`, then SET a new key. The router should still
   accept the write, the value should appear in `memcache2`, and a GET via
   the router should return the value.
3. **memcache1 back up.** Restart `memcache1`, wait for it to be re-marked
   healthy, then SET and verify the value lands in **both** memcaches.
4. **memcache2 down.** Symmetric to (2) for `memcache2`.
5. **memcache2 back up.** Symmetric to (3) for `memcache2`.

The script waits up to `HEALTHCHECK_GRACE=25s` for the
`gomcrouter_upstream_healthy_hosts_total` gauge to reflect a transition —
that bound covers the worst case of healthcheck interval (10s) plus the
metrics scraper interval (5s) plus slack.

### Tunables

```sh
ROUTER=localhost:8080 \
MC1=localhost:11211 \
MC2=localhost:11212 \
HEALTHCHECK_GRACE=25 \
NC_TIMEOUT=2 \
./test.sh
```

### Output

The script prints `PASS` / `FAIL` per assertion and a summary at the end.
Exit code is 0 if every check passed, non-zero otherwise. On interrupt or
failure it best-effort restarts both memcaches so the stack is left in a
runnable state.
