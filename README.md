# 🧭 gomcrouter

gomcrouter is a high performance memcache request router written in Go.  
It accepts memcache protocol traffic, routes operations using configurable policies, and forwards requests to upstream memcached nodes using an event driven upstream engine.

Designed for low latency, high concurrency, and predictable routing behavior.

Supports memcache text protocol.

---

## Features

- Flexible routing policies per operation
- Multiple backend servers via ordered pools
- Active health checks
- Prometheus metrics endpoint
- Configurable server event loops
- Multiple upstream connections per host
- Configurable timeouts and buffers


---

## Quick Start

### Build from source

```bash
go build -o gomcrouter
```

Run with config file:

```bash
./gomcrouter -f router.yaml
```

---

### Run with Docker

Build image:

```bash
docker build -t gomcrouter .
```

Run container:

```bash
docker run \
  -p 8080:8080 \
  -p 9090:9090 \
  -v $(pwd)/router.yaml:/app/router.yaml \
  gomcrouter \
  -f /app/router.yaml
```

Ports:

- 8080 router listen
- 9090 metrics endpoint


---

## Command Line Options

```
gomcrouter [flags]
```

### General

```
-f, --router-config string
    path to router config file
    default router.yaml

-l, --listen string
    listen address
    default tcp://:8080

--log-level string
    log level
    default info
```

### Runtime

```
-n, --server-event-loops int
    number of server event loops
    default 1

-C, --upstream-connections int
    upstream connections per host
    default 2

-r, --timeout-ms int
    upstream timeout in milliseconds
    default 1000

-b, --buffer-size int
    socket read / write buffer sizes in bytes
    default 1048576
```

### Health and DNS

```
-H, --healthcheck-interval duration
    upstream healthcheck interval
    default 10s

-t, --dns-cache-ttl duration
    dns cache ttl
    default 30s
```

### Metrics

```
-L, --metrics-listen-addr string
    metrics listen address
    default :9090
```

---

## Example Router Configuration

router.yaml

```yaml
orderedPool:
  - memcache1:11211
  - memcache2:11211

route:
  type: OperationSelectorRoute
  operationPolicies:
    add: AllFastestRoute
    append: AllFastestRoute
    decr: AllFastestRoute
    delete: AllFastestRoute
    get: MissFailoverRoute
    gets: MissFailoverRoute
    incr: AllFastestRoute
    prepend: AllFastestRoute
    replace: AllFastestRoute
    set: AllFastestRoute
    version: LocalRoute
    quit: LocalRoute
```

---

## Routing

The `route.type` field defines which routing strategy is used.  
Any available route implementation can be configured here.

Example:

```yaml
route:
  type: OperationSelectorRoute
```

### Route Types

| Route | Description |
|--------|-------------|
| OperationSelectorRoute | Select routing policy based on memcache command |
| AllFastestRoute | Send request to all hosts and return fastest response |
| MissFailoverRoute | Try next host on miss or failure |
| DefaultRoute | Route to the first host in pool order |
| LocalRoute | Handle request locally in router |

Route specific options may require additional configuration fields depending on the selected type.


---

## Protocol Support

| Protocol | Status              |
|----------|---------------------|
| Memcached ASCII | ✅ Supported         |
| Memcached Binary | ❌ Not supported yet |

---

## Architecture

High-level request flow inside **gomcrouter**:

```text
Clients (Memcached ASCII)
          │
          ▼
┌────────────────────────┐
│ gnet frontend          │
│ - multicore loops      │
│ - TCP accept/read/write│
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ parser + router        │
│ - decode cmd           │
│ - route/hash/dispatch  │
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ pools                  │
│ - hosts + health state │
│ - selection/failover   │
└───────────┬────────────┘
            ▼
┌────────────────────────┐
│ io.Engine              │
│ - pollGroups (shards)  │
│ - netpoll + socket I/O │
│ - rings/batching       │
└───────────┬────────────┘
            ▼
Memcached backends
```

 
📊 Full architecture diagram (SVG):

[Architecture diagram](docs/architecture.svg)

---

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.