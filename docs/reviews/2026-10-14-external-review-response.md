# External review response — 2026-10-14

Response to the external review challenging the service to prove safety
under concurrency, failures, and real integration. Each finding below is
marked **confirmed** (reproduced or verified in code), **addressed**
(fixed + tested this turn), or **accepted as open work** (owned by a
milestone, not claimed as done).

Scope note: the review assessed public documentation, not a code audit.
Verifications below cite files and tests so every claim is checkable.

## P0 — Authentication and authorization are not complete

**Confirmed.** Tenant scope arrives via `X-Tenant-ID` header, query
parameter, or body field (`internal/adapter/http/handler.go`,
`tenantOf`); nothing validates that the caller owns the tenant, and no
mTLS/JWT/capability layer is enforced. `docs/design.md` §8 states this
plainly since the docs review pass — the gap is disclosed, not hidden.

Not changed this turn. Required actions (workload identity, signed
claims, tenant derived from identity, privileged lifecycle operations,
conflicting-identifier rejection) belong to Milestone 1. Until then the
documented rule stands: do not expose the service to untrusted networks
or treat the tenant boundary as production-grade security.

## P0 — End-to-end adoption is not complete

**Confirmed.** The event envelope does not yet carry
`content_reference`, no scripted Compose demo exercises the full
write → commit → publish → consume → retrieve → verify → recover path,
and the Commitix adapter's bounded-retry behavior lives in other
repositories. Nothing in this turn claims otherwise.

## P1 — The implemented contract is narrower than the platform design

**Confirmed accurate.** Only Put/Get/Head/Capabilities exist on either
surface; `docs/design.md` §6 now splits implemented vs reserved RPCs so
clients cannot invent the rest. Provenance, classification, and
generation/reprocessing semantics are unmodeled (`docs/design.md` §11
says so explicitly). No full Design 1.26 conformance is claimed — the
maturity table claims our own Core+Extended suite only.

## P1 — Storage correctness under concurrency and failure

**One real bug found and fixed (this turn).** Concurrent puts of
identical bytes for one tenant all missed the dedup lookup then raced
the unique insert; losers failed with foreign-key violations instead of
adopting the winner:

- Reproduced first: `TestConcurrentDedup{SingleArtifact,ObjectTier,ParquetTier}`
  failed on all three tiers with `retention_policies ... violates foreign
  key constraint` (`test/integration/concurrency_test.go`).
- Fix (`internal/adapter/postgres/store.go`): the artifact insert checks
  `RowsAffected`; on conflict the transaction rolls back and the loser
  re-reads the winner by digest, registers its own owner, and returns
  `dedupHit=true`. If the winner vanished mid-flight (disposition raced
  the put), the put retries whole-heartedly up to 3 times, then fails
  with a retryable error instead of corrupt state.
- Side effects are bounded by construction: s3-tier losers rewrite
  identical bytes to the same content-addressed key (harmless); parquet
  losers leave one unlisted file object that the orphan sweeper collects
  after grace (covered by `TestSweeperDeletesOrphanBlob`-class sweeps).
- Post-fix: 32 inline + 8 s3-tier + 8 parquet-tier racers, `-race` clean,
  exactly one artifact row, exactly N−1 dedup hits.

**Failure evidence added (this turn**, `test/integration/failure_test.go`):

- `TestObjectStoreOutageDegradesGracefully`: object store pointed at a
  closed port — object-tier puts fail fast with errors, inline puts and
  gets keep working, reads of already-externalized payloads fail closed
  (never empty bytes, never panic).
- `TestConcurrentReleaseAndGet`: 8 releasers × 8 readers × 25 rounds —
  reads never observe release (release is not deletion, by design).
- `TestDispositionRerunIsNoOp` / `TestCompactorRerunIsNoOp`: repeated
  sweeps change nothing and audit exactly once — crash recovery is
  re-running the sweep.

**Known behavior, not changed:** a hard object-store outage costs ~8.6s
per operation under minio-go's default retry budget before surfacing.
Bounding the S3 retry/timeout budget is Milestone 3 work (needs latency
evidence first, not a drive-by constant).

**Explicitly still open:** kill-process-mid-sweep tests (recovery is by
re-run; each crash window is documented in the compactor/disposition
code but no SIGKILL harness exists), database-outage mid-transaction
(detection via `/readyz` is covered; in-flight abort recovery is not),
and statistical timing side-channel tests (kept out of PR gates as
flaky by design).

## P1 — Operational readiness and performance are unproven

**Confirmed.** Micro-benchmarks exist (digest ~0.9µs, reference
encode ~1.2µs) but no tier/latency/throughput evidence, no dashboards,
no structured logs, no quarantine (mismatches audit only). This is
Milestone 3/4 work; the maturity table marks benchmark validation ⬜
everywhere and stays that way until the benchmark matrix lands.

## Parquet: retain but measure

Agreed with the review's recommendation. Current evidence: Parquet
compaction is functionally correct (5→1 merges, byte-exact post-merge
reads, old objects collected, crash windows heal via the sweeper).
Missing: the p50/p95/p99, throughput-vs-concurrency, cost-per-million,
and direct-S3-baseline numbers that decide whether the tier earns its
complexity. That matrix is specified in the benchmark plan; until it
runs, Parquet stays an implementation detail behind the port, never a
contract requirement.

## Verdict

Agreed: worth continuing, not ready for production. What would change
each part of that verdict:

- Security readiness → Milestone 1 (workload identity + tenant derivation).
- Production evidence → Milestones 3–4 (benchmark matrix, SLOs, dashboards, runbook exercises).
- The concurrency/failure-evidence portion of this review is now
  addressed by the tests above; the remainder is tracked, not claimed.
