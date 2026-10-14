# Conformance Levels (v1)

Versioned definitions of what "Core" and "Extended" mean for
content-service. The README maturity table references these levels; a row
flips to ✅ only when the named level runs green in **both** runners:

- `make test-conformance` — `tests/conformance/` against the in-memory fake
- `make test-integration` — `test/integration/conformance_test.go` against real Postgres + MinIO

## Core (implemented)

The 14 store-level checks in `internal/conformance/suite.go`, each run
against fresh tenants so checks never interfere:

1. `AtomicPublication` — published bytes read back byte-identical with digest
2. `DigestIntegrity` — reads verify the digest before returning bytes
3. `ReferenceBinding` — distinct payloads yield distinct artifacts; `Exists` agrees
4. `DedupIdempotent` — republish returns the same artifact with `dedup_hit`
5. `TenantIsolation` — cross-tenant reads fail as `ErrNotFound`; per-tenant dedup
6. `Validation` — empty tenant / empty payload fail fast
7. `ReferenceRelease` — unknown release fails; release is idempotent, never deletes
8. `RegisterRelease` — register/release round-trips; unknown-owner release is safe
9. `LegalHoldGates` — holds require a reason; set/release round-trips
10. `RetentionValidation` — non-positive windows fail; unknown artifacts fail
11. `TierRecorded` — small payloads record `inline`; tier label always known
12. `ReleaseVsRetention` — fully released artifacts stay readable (retention owns deletion)
13. `HeadConsistency` — `Exists` agrees with put; unknown ids report absent
14. `RegisterIdempotent` — double registration is safe and re-arms release

Core is store-level by construction: it cannot observe HTTP status
codes, gRPC codes, tenant-header handling, or reference encoding. Any
claim about consumer-observable behavior requires Extended.

## Extended (defined, no suite yet)

Extended = Core **plus** all of:

- **Surface mapping** (`CB-1`): HTTP 400/404 mapping and gRPC code mapping for the Core matrix, run against fake and real wiring.
- **Security surface** (`CB-1a/b/c`): cross-tenant reads return indistinguishable 404s at the surface; forged tenant hints fail closed under auth; the default unauthenticated posture is asserted, not assumed.
- **Tier forcing** (`CB-2/2a`): parquet and S3 paths forced by size; inline→externalized promotion atomicity.
- **Lifecycle invariants** (`CB-3/3a`): disposition, integrity-sweep detection, orphan collection, compactor byte-exactness — plus an explicit `integrity_quarantine_not_implemented` check documenting the audit-only gap.
- **Auth battery** (`CB-4a/4b`): D.1/D.2 promoted to a level; `default_posture_unauthenticated` asserted.
- **Integrity coverage** (`CB-x`): full-coverage-period or sampling-rate guarantee asserted (closes AC-4).
- **Capabilities contract** (`CB-5/5a`): versioned capabilities schema asserted against actual results.

Until the Extended suite exists, no row may claim it. See
`docs/conformance-benchmark-plan.md` for the task sequence.
