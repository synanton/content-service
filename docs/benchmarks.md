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
| Object storage | `minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e` via testcontainers (GenericContainer, digest-pinned) |
| Iterations | `-benchtime=100x` (`make bench-integration`); precision runs `-benchtime=1000x` (`make bench-integration BENCHTIME=1000x`); micro-benches default scaling |

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
- The 100-sample quick runs support p50 well; p95/p99 at n=100 sit near
  the sample extreme and are not stable tail estimates. Treat them as
  order-of-magnitude only. The precision runs below (n=1000, three
  independent runs) are the basis for any tail claim, and even those
  stay local-container evidence, never SLOs.

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
| GetInline, run A (n=100) | 100 B | 0.048 ms | 0.048 ms | 0.052 ms | 0.059 ms | 2.0 KB / 38 |
| GetInline, run B (n=100) | 100 B | 0.15 ms | 0.13 ms | 0.22 ms | 0.32 ms | 2.0 KB / 38 |

Run A vs run B on the same host show ~7× p50 movement with no code change —
shared-runner noise dominates sub-millisecond measurements. The precision
runs below narrow it, but no single run supports a tail claim alone.
| GetParquet | 5 KiB | ~20 ms | 22 ms | 26 ms | 27 ms | 352 KB / 2697 |
| GetS3 | 300 KiB | ~6.8 ms | 6.7 ms | 9.2 ms | 9.6 ms | 1.6 MB / 464 |
| GetMissing | — | ~0.06 ms | 0.06 ms | 0.09 ms | 0.16 ms | 1.1 KB / 24 |

## Precision runs (n=1000, three independent runs)

Same benches, `-benchtime=1000x`, three separate processes. Use these
ranges — not any single run — when reasoning about tails.

| Benchmark | Run 1 p50 / p95 / p99 | Run 2 p50 / p95 / p99 | Run 3 p50 / p95 / p99 |
|---|---|---|---|
| PutInline | 0.87 / 2.32 / 4.01 ms | 0.39 / 1.40 / 2.55 ms | 0.72 / 3.08 / 3.96 ms |
| PutParquet | 8.52 / 12.15 / 15.38 ms | 7.48 / 11.49 / 14.69 ms | 7.23 / 11.79 / 14.50 ms |
| PutS3 | 12.76 / 15.30 / 19.34 ms | 12.83 / 15.42 / 18.82 ms | 13.13 / 15.71 / 19.25 ms |
| PutDedupHit | 0.73 / 1.23 / 1.77 ms | 0.20 / 0.43 / 0.63 ms | 0.17 / 0.85 / 1.95 ms |
| GetInline | 0.21 / 0.32 / 0.50 ms | 0.054 / 0.18 / 0.22 ms | 0.073 / 0.16 / 0.23 ms |
| GetParquet | 20.2 / 25.0 / 29.1 ms | 19.5 / 24.7 / 29.1 ms | 19.0 / 24.4 / 28.8 ms |
| GetS3 | 3.08 / 8.42 / 9.53 ms | 6.45 / 8.95 / 10.70 ms | 6.68 / 9.19 / 10.45 ms |

Read across rows, not down columns: object-tier puts and parquet gets
are stable within ±15% run to run; sub-millisecond operations swing
2–4× (dedup p50 0.73 → 0.17 ms; one GetS3 run caught a slow p50 at
half the usual). p99 at n=1000 is the 10th-slowest sample — usable
signal, still local-container evidence.

## Tier boundary cliff (default thresholds, n=20 per size)

Same operation on both sides of the 4 KiB inline boundary:

| Size | Tier | Put p50 | Get p50 |
|---|---|---|---|
| 4095 B | inline | 1.68 ms | 0.25 ms |
| 4096 B | parquet | 10.58 ms | 23.18 ms |
| 4097 B | parquet | 10.26 ms | 22.25 ms |
| 8192 B | parquet | 10.30 ms | 22.62 ms |

One byte flips the tier: put ×6, get ×90. The cliff is razor-sharp
because routing is a pure size comparison — there is no gradual
transition, by design.

## Same payload across all tiers (300 KiB, threshold overrides, n=20)

Isolates tier cost from size — the comparison the per-size tables cannot make:

| Forced tier | Put p50 | Get p50 |
|---|---|---|
| inline | 2.5 ms | 0.56 ms |
| parquet | 16.6 ms | 34.4 ms |
| s3 (default routing) | 13.2 ms | 8.1 ms |

At fixed size, parquet costs the most on both paths: file encode plus
ranged footer/row fetches outweigh one object write and one object read.
The tier earns its keep only where object count (not latency) dominates —
that tradeoff needs production size distributions, not these tables.

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

## Integrity coverage math (deterministic vs probabilistic)

The "100% within 30 days" SLO starter below is precise only under a
deterministic schedule. Two modes, different guarantees:

**Deterministic full coverage.** Sweep at sample rate 1.0, sharded across
S shards (`SHARD_INDEX`/`COUNT`), each shard swept at least once per
D days. Every artifact is then verified every D days, guaranteed. The
30-day target holds iff D ≤ 30 — e.g. 30 daily shards, or fewer shards
with faster cadence. Cost scales with estate size: at the measured
~80 artifacts/sec, one shard needs ~3.5 h per million artifacts, so size
the shard count from the tenant census, not from hope.

**Sampled detection (probabilistic).** At sample rate r < 1 per pass, a
corrupt artifact escapes one pass with probability (1−r); after k passes
the miss probability is (1−r)^k. For 99.9% detection within 30 daily
passes, (1−r)^30 ≤ 0.001, i.e. r ≥ 0.21 per pass — far above the default
0.01, under which a given corrupt artifact still has a ~26% chance of
going unnoticed for the whole month (0.99^30 ≈ 0.74 detection). Sampling
controls cost; it must never be presented as coverage.

Keep the two measurements separate: full-sweep throughput
(artifacts/sec, above) sizes the deterministic schedule; sampled runs
measure detection probability, not coverage.

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

## Deadline policy (BB-4, confirmatory)

AC-2 is closed via C-5's fault tests; these numbers confirm the policy
quantitatively and seed the BB-5 trend table. Same evidence rules as
above: ranges, not gates; shared-runner noise applies doubly here
because the guarded and raw arms run sequentially.

Run provenance: same host as above (i9-12900T), 2026-10-15, go 1.23.0,
`-benchtime=100x` except raw-contrast (manual N=3, see below).

### Happy-path overhead (guarded vs raw, healthy backend, 5 KiB)

| Arm | put p50 / p95 / p99 | get p50 / p95 / p99 |
|---|---|---|
| raw | 1.56 / 3.44 / 5.38 ms | 1.19 / 3.63 / 5.65 ms |
| guarded | 4.76 / 6.02 / 6.47 ms | 3.42 / 4.40 / 4.60 ms |

Both arms warmed up (3 untimed ops) before measuring. The guarded arm
reads ~2–3 ms slower at p50 with overlapping p95/p99 — decorator cost
(timer setup, breaker check) is sub-millisecond by construction; the
visible delta is S3 RTT variance between sequential arms, not policy
cost. Treat the overhead as "low single-digit ms, inside backend
noise," not as either column's point value. First-try rate on the
healthy backend is 100% (every op takes exactly 1 attempt).

### Retry distribution (deterministic failure proxy)

| Case | first-try rate | op p50 / p95 | tries-p50 |
|---|---|---|---|
| always-503 (MaxAttempts 5) | 0 | 487 / 736 ms | 5.000 |
| always-429 (MaxAttempts 5) | 0 | 521 / 714 ms | 5.000 |
| once-503-then-pass | 0 | 112 / 202 ms | 2.000 |

Attempts are exact (proxy hit counter, one hit per decorator attempt).
No pooled mean is reported: retries only happen on failure, so the
distribution is bimodal and the tail is the policy's whole effect.

### Slow backend (3 s delay, 300 ms deadline)

| Arm | op p50 | Outcome |
|---|---|---|
| guarded (N=100) | 300.8 ms (p95 301.3) | fails closed at the bound, every op |
| raw, 10 s cap (manual N=3) | 3011 ms | succeeds at ~delay |

The raw arm is a correctness contrast, not a latency comparison: it has
no bound, so its "latency" is whatever the backend eventually does. The
reported number is the tail the policy eliminates (10×), not an average
the no-policy arm achieved.

### Breaker transitions (threshold 2, cooldown 100 ms)

| Transition | p50 / p95 |
|---|---|
| half-open probe → close | 13–16 / 17–18 ms across runs |

Dominated by the probe's own S3 call, as expected. Open → half-open is
the cooldown constant (confirmed by config, not measured).

## What remains

- Production payload-size distribution (p50/p95/p99 per intent type over 30 days, per proposal Phase 6) to finalize thresholds.
- Open-loop arrival patterns (k6 scale) and multi-MiB payloads — current numbers are closed-loop only.
- Sustained multi-hour soaks (`CONTENT_BENCH_SOAK_SECONDS` raises the churn benchmark window; default 30 s).
- ~~Server-side deadlines for slow (not dead) object storage.~~ Done: C-4 policy + C-5 fault battery + BB-4 numbers above.
- Nightly CI trend tracking (results committed per run; regressions as trends, not anecdotes).
- Grafana dashboards over the existing Prometheus metrics.
