# 🚀 gomcrouter — Benchmark Results

## 📦 Test Environment

- **Cloud:** Google Cloud Platform (GCP)
- **Instance:** `c4-standard-4`
- **Protocol:** memcache_text
- **Client:** memtier_benchmark (redislabs/memtier_benchmark)
- **Test duration:** 30 seconds per run
- **Workload:** 50% SET / 50% GET (`--ratio=1:1`)
- **Key pattern:** random (`R:R`)

These benchmarks represent a mixed read/write workload with a relatively **low GET hit-rate (~20–30%)**, meaning results reflect:
- miss path cost
- write path cost
- router + upstream I/O behavior under contention

---

## ⚙️ gomcrouter Run Parameters

```
/bin/gomcrouter \
  --server-event-loops 2 \
  --upstream-connections 1 \
  --timeout-ms 250 \
  --healthcheck-interval 10s
```

---

## 🧭 Router Configuration

```yaml
orderedPool:
  - 10.10.10.10:11211

route:
  type: DefaultRoute
```

**Topology characteristics:**

- Single upstream memcached node
- DefaultRoute (no fanout / no broadcast)
- 1 upstream connection per worker
- 2 server event loops

---

## 📊 Results — Non‑Pipelined

**Client profile:** 8 threads × 16 connections (128 total), pipeline=1

| Value Size | Ops/sec | Throughput | Avg Lat | p99 | p99.9 | GET Hit |
|------------|----------|------------|---------|------|--------|---------|
| 1 KB  | **174,219** | **111 MB/s** | 0.73 ms | 1.83 ms | 5.50 ms | 21% |
| 10 KB | **129,176** | **670 MB/s** | 0.99 ms | 2.59 ms | 6.02 ms | 22% |
| 50 KB | **32,508**  | **821 MB/s** | 3.94 ms | 19.46 ms | 215 ms | 21% |

---

## 🚄 Results — Pipeline = 64

**Client profile:** 4 threads × 4 connections (16 total), pipeline depth = 64

| Value Size | Ops/sec | Throughput | Avg Lat | p99 | p99.9 | GET Hit |
|------------|----------|------------|---------|------|--------|---------|
| 1 KB  | **300,394** | **222 MB/s** | 3.40 ms | 5.89 ms | 8.58 ms | 25% |
| 10 KB | **161,823** | **882 MB/s** | 6.32 ms | 17.9 ms | 213 ms | 30% |
| 50 KB | **32,601**  | **843 MB/s** | 31.4 ms | 234 ms | 272 ms | 30% |

---

## 🔍 Observations

### Small objects (1 KB)
- Latency-bound in non‑pipeline mode (sub‑millisecond average).
- Pipeline significantly increases throughput (~1.7×).
- Expected tradeoff: higher per‑request latency due to deeper in‑flight queues.

### Medium objects (10 KB)
- Transition zone from latency-bound → bandwidth-bound.
- Strong throughput gains with pipeline.
- Tail latency (p99.9) grows noticeably under deep pipeline.

### Large objects (50 KB)
- Clearly **bandwidth / copy bound**.
- Throughput plateaus around **~800–880 MB/s**.
- Pipeline provides little ops/sec gain but increases tail latency.

### Router behavior under this config
- With **DefaultRoute + single upstream**, results mainly measure:
    - protocol parsing
    - routing decision cost
    - upstream socket I/O
    - buffer copy + writeback path
- Even with only:
    - 2 server event loops
    - 1 upstream connection  
      the proxy saturates high payload throughput — bottlenecks shift to network and upstream capacity.

---

## 🧠 Interpretation Notes

- These are **mixed miss-heavy workloads**, not warm-cache read benchmarks.
- GET latency here should not be interpreted as best‑case cache hit latency.
- Numbers are suitable for:
    - capacity planning
    - router overhead estimation
    - payload scaling behavior
    - pipeline tradeoff analysis.
