# Conformance + Benchmark Coverage Plan

Review of the README maturity matrix (2026-10-14) and plan to fill its
`⬜` cells. CB-0 (label correction + level definitions) already landed
as the first commit on this branch (`docs/conformance.md` v1). What
follows builds on it. Depends on content-service PR #14 (D.1/D.2 auth)
only where marked; everything else applies to `main` as-is.

## 1. Review findings (accepted)

The matrix overstated coverage exactly where adoption needs signal:
consumer-observable behavior. The 14 checks in
`internal/conformance/suite.go` are store-level and cannot observe HTTP
status codes, surface tenant handling, reference encoding, or gRPC code
translation. Root causes: conformance stops at the store port
(architectural, not accidental); Core vs Extended was never defined;
benchmarks measure only the healthy happy path.

## 2. Conformance track

Levels are defined in `docs/conformance.md` (v1): Core = the 14
store-level checks (implemented); Extended = Core + CB-1–CB-5 + CB-x
(defined, no suite yet). A row flips to ✅ only when its checks run
green in **both** `make test-conformance` (fake) and
`make test-integration` (real wiring).

| ID | Check | Target files | Gate |
|---|---|---|---|
| CB-0 | ✅ **Landed.** Correct inflated labels; define levels in `docs/conformance.md`; sweep `docs/design.md` + matrix for the same claim | `README.md`, `docs/design.md`, `docs/conformance.md` | Merged as first commit |
| CB-1 | **Surface mapping conformance.** `tests/conformance/surface_test.go` (fake, no Docker) + real-wiring mirror: PUT missing tenant → 400; GET unknown id → 404; PUT response carries parseable `reference` + `digest`; HEAD agrees with GET. gRPC mirror over bufconn: `NotFound` / `InvalidArgument` / `DataLoss` for the same matrix. Flips HTTP + gRPC rows. | `tests/conformance/surface_test.go`, `test/integration/` mirror | `test-conformance` + `test-integration` |
| CB-1a | **Cross-tenant 404 at the surface.** Same-artifact read under a foreign tenant returns a body indistinguishable from genuinely-missing — asserted over HTTP **and** gRPC, not just at the store. Security claim, currently only unit-tested. | Same files as CB-1 | Same runners |
| CB-1b | **Forged hint fails closed at the surface (auth-enabled).** Forged `X-Tenant-ID` / `?tenant_id=` / body `tenant_id` → 403; forged message field / `x-tenant-id` metadata → `PermissionDenied`; disagreeing `X-User-ID` → 403. D.2 negatives promoted from unit to conformance. | Same files as CB-1 | Same runners |
| CB-1c | **Default posture asserted (auth-disabled).** Same requests under `CONTENT_AUTH_ENABLED=false` succeed via legacy resolution. Uncomfortable and mandatory: without it the matrix is honest about auth-enabled behavior and silent about the actual default. Check name: `default_posture_unauthenticated`. | Same files as CB-1 | Same runners |
| CB-2 | **Tier-forcing conformance.** Extend the suite: 5 KiB put → tier `parquet` + byte-exact read; 300 KiB put → tier `s3` + byte-exact read. Runs against real wiring; fake pins tier by threshold logic. Flips S3/MinIO row. | `internal/conformance/suite.go` (+2), `internal/conformance/fake.go` | Same runners |
| CB-2a | **Inline→externalized promotion.** Artifact crossing the inline threshold on update: reference updated atomically, old inline copy removed, reads consistent throughout. Tier transition is uncovered today. | Same files as CB-2 | Same runners |
| CB-3 | **Lifecycle invariants as conformance.** Promote from `jobs_test.go` to named checks: disposition deletes iff (refs zero, no hold, grace passed); integrity sweep flags planted corruption; orphan sweeper collects unlisted objects post-grace; compactor output reads byte-exact. Heavy fixtures stay in `test/integration/`; the suite owns the invariant names. Flips jobs + parquet rows. | `test/integration/` (`TestConformance*`), suite index | `test-integration` |
| CB-3a | **`integrity_quarantine_not_implemented`.** Asserts current behavior (mismatch → audit-only, no quarantine) so the gap is documented in code, not hidden. Flips visibly when quarantine lands. | Same files as CB-3 | Same runners |
| CB-4a | **Auth battery without PR #14 dependency.** CB-1b/CB-1c plus token-present/token-absent mapping already cover the surface-auth cells using locally-minted contexts. Lands independently; flips the surface-auth cells even if #14 stalls. | Same files as CB-1 | Same runners |
| CB-4b | **Full auth battery (needs PR #14).** D.1 handshake end-to-end (middleware import, mTLS transport test) promoted to the level; `capabilities` advertises `context_auth` iff enabled. Flips the auth row. | `tests/conformance/auth_test.go`, extend `test/integration/auth_test.go` | Same runners |
| CB-5/5a | **Versioned capabilities contract.** `capabilities` response becomes a versioned JSON document with its schema in the repo (`capabilities/v1`); the suite asserts the response matches actual test results, so consumers never parse it ad hoc and the matrix and the binary cannot disagree on the tested level. | `internal/adapter/http/handler.go`, `internal/adapter/grpc/server.go`, schema file | Unit test |
| CB-x | **Integrity coverage guarantee (closes AC-4).** Asserts either (a) full coverage within a defined period, or (b) the sampling rate and its implications, against the lifecycle jobs. Without it AC-4 stays open after this plan completes. | Same files as CB-3 | Same runners |

**Explicit non-goal: delegation/actor-chain conformance.** If the security context carries delegation fields end-to-end (producer → envelope → consumer without widening), that is a Phase-4 consumer-contract item, not part of Extended. Stated here so no reviewer assumes coverage.

## 3. Benchmark track

Ground rules (unchanged from `docs/benchmarks.md`): evidence, not gates. Never assert absolute values in CI. Every bench PR updates the doc table with provenance or explicitly marks the cell deferred.

| ID | Measurement | Target | Notes |
|---|---|---|---|
| BB-1 | **Auth-overhead tax.** `BenchmarkHttpAuthOverhead` + `BenchmarkGrpcAuthOverhead`, each reporting three numbers: auth-enabled vs auth-disabled **latency** at the same concurrency, auth-enabled vs auth-disabled **throughput**, and **failure-path cost** (forged header, expired context — fast-fail or more expensive than success?). The adoption conversation needs all three. | `test/integration/bench_test.go` | Needs PR #14 merged (or branch-stacked) |
| BB-2 | **Concurrency scaling matrix.** Workers 1/4/16/64, mixed sizes, put-heavy + read-heavy mixes. **Reconcile with `CNT-perf-scenarios` before running** — same sizes, same mixes, same metrics — or the two number sets cannot be compared and reconciliation becomes post-hoc narrative. | Same + `docs/benchmarks.md` | Report ranges on shared runners, never points |
| BB-3a/b/c | **Specified contention, not "noisy neighbor".** (a) Concurrent large PUT during small GET. (b) Concurrent compaction during read. (c) Concurrent externalization (inline→S3 promotion) during read. Each has a different expected impact and mitigation; one blended number would be unactionable. | Same | Follows BB-2 harness |
| BB-4 | **Slow-backend bench.** Delayed MinIO proxy (30 s first-byte delay). **Ordering: land C-4 (storage deadlines) first, then BB-4, then the C-5 fault-injection test.** BB-4 without C-4 measures the absence of a policy, not the policy. | Same + fault-injection test | Depends on Track C.1 (C-4) |
| BB-5/5a | **Nightly trend harness with an owner.** Script runs `make bench-integration`, appends JSON + env provenance, renders delta-vs-baseline. Owner, runner, retention window, and regression alert threshold named in the task PR — a harness without these becomes a dashboard no one reads. Suggested: owning team on-call, CI schedule, 90-day retention, p99 regression > 20% over 7-day median pages the bench owner. Numbers never gate merges. | `scripts/bench-trend.sh` (new), CI schedule | Parallelizable after BB-2 |
| BB-6/6a | **Staging equivalence before SLO promotion.** "Staging" must be defined to make the gate enforceable: same hardware class as production, same storage backend (managed Postgres + S3, not laptop containers), same network topology (no localhost backends). Until that definition exists and an environment meets it, the `docs/benchmarks.md` SLO starter stays proposed, not promised. | `docs/benchmarks.md`, runbook | Explicitly blocked on staging |

## 4. Joint-plan mapping

Each task composes with the joint Content Service & Security Provider plan:

| This plan | Joint plan | Relation |
|---|---|---|
| CB-4a/4b (auth battery) | D.1 / D.2 | D.1/D.2 are proven by these checks; the matrix row is the D.2 exit receipt |
| BB-1 (auth overhead) | Adoption conversation | Supports, but is not itself, an AC |
| BB-4 (slow backend) | C-4 / C-5 / AC-2 | Ordered C-4 → BB-4 → C-5; closes AC-2 with C-5 |
| CB-1/1a/1b (surface mapping) | D.4 consumer flow | Prerequisite: commitix programs against this surface |
| CB-x (integrity coverage) | AC-4 | Closes the sampling-vs-full-coverage ambiguity |
| CB-3/3a (lifecycle + quarantine gap) | AC-5 (audit) + threat model | Quarantine decision feeds the security review |

**Evidence format dependency (A-6).** Every CB/BB task must emit its artifact in the joint plan's A-6 evidence template. If A-6 is not yet agreed when CB-1 lands, agree it first — otherwise the evidence will be inconsistent across tasks.

## 5. Sequencing

```text
CB-0 ✅ landed (docs, first commit)
 ├── CB-1 + CB-1a/b/c (surface + security surface + default posture)
 ├── CB-4a (auth cells, no #14 dependency) ──► surface-auth cells flip w/o #14
 ├── CB-4b (needs #14) ──► auth row flips
 ├── CB-2 + CB-2a (tiers + promotion) ──► S3/MinIO row flips
 ├── CB-3 + CB-3a + CB-x (lifecycle + quarantine gap + coverage) ──► jobs/parquet flip, AC-4 closes
 ├── CB-5/5a (capabilities contract)
 ├── BB-1 (auth tax) ──► adoption conversation has numbers
 ├── BB-2 (reconcile-first) → BB-3a/b/c (specified contention)
 ├── BB-5/5a (trends + owner, parallelizable after BB-2)
 └── BB-4 (after C-4 deadlines) → C-5 → BB-6/6a (after staging defined)
```

Critical path to "no ⬜ in conformance": CB-1abc → CB-4a → CB-4b → CB-2a → CB-3ax → CB-5a.
Critical path to "benchmarks cover adoption risks": BB-1 → BB-2 → BB-3abc → BB-5a.

## 6. Done criteria

- Every ⬜ flip names the test target and both runners; README never claims beyond them (the rule CB-0 enforced).
- `default_posture_unauthenticated` and `integrity_quarantine_not_implemented` exist as visible gap documentation until the behavior changes, then flip with the behavior.
- `docs/benchmarks.md` "What remains" shrinks item by item; each closed item links its table.
- No absolute-value assertions in CI; BB-5a reports trends against its threshold only.
- Capabilities output, the maturity table, and `docs/conformance.md` agree on the tested level (CB-5a).
