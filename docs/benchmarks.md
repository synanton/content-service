# Benchmarks

Measured performance of the wired stack (real Postgres + MinIO, not mocks).
Numbers inform threshold and SLO decisions; they are **evidence, not gates** —
never assert absolute values in CI. Shared runners are too noisy (inline-get
p50 measured 48µs and 330µs across two runs on the same host).

## Environment provenance

| Fact | Value |
|---|---|
| Date | 2026-10-14 |
| CPU | 12th Gen Intel i9-12900T, linux/amd64, 24 threads (`-24` in output) |
| Toolchain | go 1.22.2, module directive `go 1.22` |
| Postgres | `postgres:16-alpine` via testcontainers |
| Object storage | `minio/minio:latest` via testcontainers (GenericContainer) |
| Iterations | `-benchtime=100x` (`make bench-integration`); micro-benches default scaling |

## How to reproduce

```bash
make bench                 # micro-benchmarks: digest + reference (no Docker)
make bench-integration     # full stack: tiers, handlers, jobs (needs Docker)
```

## Methodology notes (read before citing numbers)

- Each benchmark executes **twice** in this toolchain: an N=1 warmup run
  followed by the measured N=target run. Reported `ns/op` and percentiles
  come from the measured run; warmup output is ignored.
- All benchmarks in one process share one database. Sweep benchmarks
  (compactor, disposition, integrity) therefore also scan rows seeded by
  earlier benchmarks — that is realistic steady-state load (production
  sweeps also see every tenant), not contamination.
- Put benchmarks use unique payloads per operation (salted) so dedup
  never collapses the insert path, except `PutDedupHit`, which measures
  the dedup path deliberately.
- Percentiles come from per-operation timings collected in-test
  (`p50-ms`/`p95-ms`/`p99-ms` metrics), not from `ns/op` averages.

## Store: put path (100 ops, unique payloads)

| Benchmark | Size | avg | p50 | p95 | p99 | allocs/op |
|---|---|---|---|---|---|---|
| PutInline | 100 B | ~1.0 ms | 0.78 ms | 3.6 ms | 6.2 ms | 4.6 KB / 82 |
| PutParquet | 5 KiB | ~6.1 ms | 4.7 ms | 11.6 ms | 13.0 ms | 3.3 MB / 677 |
| PutS3 | 300 KiB | ~11.5 ms | 12.8 ms | 15.6 ms | 16.1 ms | 695 KB / 580 |
| PutDedupHit | 5 KiB | ~0.8 ms | 0.68 ms | 1.3 ms | 1.9 ms | 3.7 KB / 70 |

## Store: get path (100 ops, pre-seeded artifact)

| Benchmark | Size | avg | p50 | p95 | p99 | allocs/op |
|---|---|---|---|---|---|---|
| GetInline | 100 B | 0.05–0.42 ms | 0.05–0.33 ms | — | — | 2.0 KB / 38 |
| GetParquet | 5 KiB | ~20 ms | 22 ms | 26 ms | 27 ms | 352 KB / 2697 |
| GetS3 | 300 KiB | ~6.8 ms | 6.7 ms | 9.2 ms | 9.6 ms | 1.6 MB / 464 |
| GetMissing | — | ~0.06 ms | 0.06 ms | 0.09 ms | 0.16 ms | 1.1 KB / 24 |

## Handler roundtrips (1 KiB payload, put + get)

| Benchmark | avg | p50 | p95 | p99 |
|---|---|---|---|---|
| HTTP (httptest, no network) | ~1.9 ms | 1.1 ms | 5.7 ms | 7.3 ms |
| gRPC (bufconn, no network) | ~1.4 ms | 0.8 ms | 3.8 ms | 4.2 ms |

These measure handler + store cost only; add network RTT for deployed estimates.

## Workload scenarios (seeded, reproducible)

`test/integration/workload.go` generates deterministic mixed traffic
(size bands 100 B / 5 KiB / 300 KiB / 1 MiB at 50/30/15/5 weights,
4 tenants, 10% dedup replays, 50% reads). Workload identity is
`workload-v1/seed/tenants/dedup/read` — same identity replays the same
distributions (op order is deterministic only at parallelism 1).

| Scenario | Result |
|---|---|
| Mixed workload, 1 worker (n=100) | p50 3.07 ms, p95 13.43 ms, p99 23.49 ms |
| Sustained churn, 30 s (6230 ops, 12 inline sweeps) | 0 errors; p50 1.23 ms, p95 21.9 ms, p99 24.5 ms |

## Throughput vs concurrency (400 mixed ops, closed-loop)

| Workers | Throughput, run A | Throughput, run B |
|---|---|---|
| 1 | 134 ops/sec | 179 ops/sec |
| 4 | 774 ops/sec | 944 ops/sec |
| 16 | 2649 ops/sec | 4689 ops/sec |
| 64 | 3131 ops/sec | 6176 ops/sec |

Near-linear to 4 workers, ~5× at 16, saturating past it while p99
degrades (17 ms at 16 workers → ~49 ms at 64 in warmup runs). The
likely ceiling is the Postgres pool (20 connections, 64 workers
contending). Per-op percentiles stay usable throughout — saturation
shows up first in throughput flattening, not in error rate (0 errors
at every level).

## Failure injection

| Scenario | Result |
|---|---|
| MinIO killed mid-test | writes and object-tier reads fail immediately with errors; inline traffic unaffected |
| MinIO restarted | fresh client on the re-resolved endpoint reads pre-outage bytes back intact within seconds |
| 2 s degraded object store | reads still succeed with intact bytes (no server-side deadline today — tracked as follow-up) |

Note for operators: Docker reallocates auto-assigned host ports across
stop/start, so any reconnecting client must re-resolve the endpoint
rather than caching it — the test does exactly this.

## Jobs

| Benchmark | Result |
|---|---|
| Compactor merge (10 single-row files → 1) | ~30–300 ms (p50 ≈ 90 ms across runs) |
| Disposition sweep (1 expired + released artifact) | ~1.5–2.0 ms |
| Integrity full sweep (~1600 mixed-tier artifacts) | ~20 s, 0 failures (≈ 80 artifacts/sec) |

## Micro-benchmarks (`make bench`, no Docker)

| Benchmark | Result |
|---|---|
| Digest compute | ~918 ns/op, 1.5 GB/s, 208 B / 3 allocs |
| Digest verify | ~896 ns/op |
| Reference encode | ~1479 ns/op, 656 B / 12 allocs |
| Reference parse | ~1121 ns/op, 288 B / 11 allocs |

## Analysis

**Tier cliffs are real and large.** Inline→parquet at 4 KiB jumps put latency ~10× (1 ms → 6–10 ms): Parquet file encode + S3 write per put. The 4 KiB default stands only until production size distributions say otherwise — if the workload is dominated by 1–8 KiB payloads, the cliff lands in the hot path and the inline threshold should move.

**Parquet reads cost more than plain S3 reads at these sizes.** A 5 KiB parquet get (~20 ms p50, high variance, 350 KiB/2697 allocs) runs 3× slower than a 300 KiB S3 object get (~7 ms). Ranged footer + row fetches dominate small reads. The tier's value is object-count and storage-cost amortization plus merge-driven compaction — not latency. Keep it behind the port (it is), and revisit if reads dominate the workload.

**Write amplification to watch:** parquet puts allocate ~3.3 MB per 5 KiB payload (writer buffering); S3 puts ~2.3× payload bytes. At high put concurrency this pressures GC — profile before raising throughput targets.

**Sweep rates bound operations, not correctness:** integrity at ~80 artifacts/sec means a million-artifact estate needs ~3.5 h per full pass — sharding (`SHARD_INDEX`/`COUNT`) and sampling (`SAMPLE_RATE`) exist for exactly this; verify coverage math per tenant size in runbooks.

## SLO starter (proposed, not promised)

Local-container observations only — adopt as SLOs after staging measurements:

| Operation | Proposed p99 |
|---|---|
| Put inline | < 50 ms |
| Put object tiers | < 100 ms |
| Get inline | < 5 ms |
| Get object tiers | < 100 ms |
| Compactor merge (10 files) | < 1 s |
| Integrity sweep coverage | 100% of artifacts within 30 days (via sharding + sampling) |

## What remains

- Production payload-size distribution (p50/p95/p99 per intent type over 30 days, per proposal Phase 6) to finalize thresholds.
- Open-loop arrival patterns (k6 scale) and multi-MiB payloads — current numbers are closed-loop only.
- Sustained multi-hour soaks (`CONTENT_BENCH_SOAK_SECONDS` raises the churn benchmark window; default 30 s).
- Server-side deadlines for slow (not dead) object storage.
- Nightly CI trend tracking (results committed per run; regressions as trends, not anecdotes).
- Grafana dashboards over the existing Prometheus metrics.
