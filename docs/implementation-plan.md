# Content Service — Implementation Plan

**Status:** Approved for Phase 0 implementation\
**Language:** Go 1.22+\
**Date:** 2026-10-13

---

## Phase 0: Project Setup and Contract Alignment

### Deliverables

1. Go module structure with hexagonal architecture
2. `content-api` gRPC server skeleton with tenant middleware
3. `content-metadata` PostgreSQL schema (artifact registry, retention, legal hold, reference counts)
4. `content-storage` tier routing interface (inline, Parquet, S3)
5. Configuration loading and environment variable support
6. Docker Compose for local development (Postgres + MinIO)

### Exit Criteria

- `go build ./...` passes
- `go test ./...` passes
- Docker Compose starts Postgres and MinIO
- gRPC server starts and responds to health checks
- Tenant middleware rejects unauthenticated requests

### Acceptance Tests

- Server starts without errors
- `Capabilities` returns conformance level `NONE` (not yet implemented)
- Cross-tenant access returns `NOT_FOUND`
- Health check endpoint returns `SERVING`

---

## Phase 1: Core Conformance

### Deliverables

1. `Put` operation with SHA-256 digest computation
2. Storage tier routing (inline for small payloads)
3. `Get` operation with tenant authorization
4. Content-addressable dedup by `(tenant_id, digest)`
5. `MarkForDeletion` reference release
6. `SetRetention` and `SetLegalHold` operations
7. `Capabilities` returning Core conformance level
8. Audit logging for administrative operations

### Exit Criteria

- Core conformance test suite passes
- Put/Get round-trip with integrity verification works
- Dedup: same `(tenant_id, digest)` returns same reference
- Cross-tenant access returns indistinguishable `NOT_FOUND`
- `MarkForDeletion` releases reference (ref count decremented)
- Legal hold prevents disposition
- Audit trail records all administrative operations

### Acceptance Tests

- Put 1 MiB payload, get it back, verify digest matches
- Put same payload twice, verify same reference returned
- Put payload for tenant A, try to get as tenant B, verify NOT_FOUND
- Set legal hold, try to delete, verify blocked
- Release all references, verify physical deletion after grace period
- Verify audit log entries for legal hold and retention operations

---

## Phase 2: Commitix Adapter Design

### Deliverables

1. ADR for `ContentStore` module placement and reference format
2. Java `ContentStore` interface (put, get, exists)
3. Reference format parser/composer (v1 prefix)
4. Feature flag hierarchy (per-tenant, per-intent-type, threshold, kill switch)
5. Failure classification (transient, terminal, integrity)
6. Schema migration plan (payload_kind enum, content_ref, payload_sha256)

### Exit Criteria

- ADR approved by Commitix maintainers
- Interface defined and reviewed
- Reference format parser handles v1 correctly
- Feature flag resolution tested (veto chain semantics)
- Migration plan reviewed and approved

### Acceptance Tests

- Reference format parse/compose round-trip
- Feature flag resolution: disabled → inline, enabled + above threshold → external
- Kill switch: all new intents use inline
- Schema migration: DDL ordering verified on fresh database
- Backfill: existing rows get payload_sha256 populated

---

## Phase 3: Optional Large-Payload Adapter

### Deliverables

1. `ContentStore` implementation calling Content Cache gRPC
2. Schema migration applied (payload_kind, content_ref, payload_sha256)
3. Execution path integration (resolve reference, verify digest)
4. Timeout and deadline propagation (2s connect, 5s read, 10s declare budget)
5. Observability metrics (externalized count, threshold histogram, put/get latency)

### Exit Criteria

- Contract tests for commit/rollback pass
- Remote outage simulation: declare() fails fast, no orphan created
- Digest verification: corruption detected, execution fails closed
- Orphan cleanup: unreferenced artifacts deleted after grace period
- Cross-tenant leak tests pass
- Metrics emitted and queryable

### Acceptance Tests

- Declare with large payload externalizes correctly
- Rollback leaves no committed intent, orphan reconciled after grace period
- Content Cache down: declare() fails, no orphan
- Content Cache slow: declare() times out, no orphan
- Digest mismatch: execution fails closed
- Payload-less intents (NONE) accepted and processed

---

## Phase 4: Vertical Slice

### Deliverables

1. Ingestion → Content Cache → Eventing → Knowledge/Search wiring
2. Event envelope carries content_reference
3. End-to-end test with scripted path
4. Tenant isolation and authorization verified across planes

### Exit Criteria

- Single scripted end-to-end path is reproducible
- Event envelope carries content_reference
- All 12 acceptance criteria from proposal §5 pass
- Cross-plane security tests pass

### Acceptance Tests

- Document ingested, artifact published, event fired
- Event consumer retrieves payload via content_reference
- Tenant isolation enforced across all planes
- Unauthorized retrieval fails safely
- Expired content returns NOT_FOUND
- Legal hold blocks deletion

---

## Phase 5: Extended Lifecycle

### Deliverables

1. Background jobs: disposition, integrity, orphan-sweeper, parquet-compactor
2. Parquet batching tier for medium payloads
3. Multi-region S3 support for critical tenants
4. Production observability (metrics, tracing, dashboards)

### Exit Criteria

- Extended/Full conformance claims backed by tests
- Orphan rate < 0.01% after grace period
- Integrity verification covers 100% of artifacts within 30 days
- Parquet batching reduces S3 object count for medium payloads

### Acceptance Tests

- Disposition job deletes expired artifacts after grace period
- Integrity job detects and quarantines corrupted payloads
- Orphan sweeper reconciles metadata vs object store
- Parquet compactor merges small batches into larger files
- Metrics dashboard shows put/get latency, error rates, orphan count

---

## Phase 6: Adoption Decision

### Deliverables

1. Measured payload size distribution (p50/p95/p99 per intent_type across 30 days)
2. Externalization rate report
3. Per-tenant dedup hit rate
4. Latency and cost analysis
5. Failure test results
6. Operational readiness assessment

### Exit Criteria

- Decision on whether remote storage becomes default for any workload
- Decision rule: adopt if p99 latency stays within SLO and externalization rate is acceptable
- Operational runbook published
- On-call training completed

### Acceptance Tests

- Load test with production-like workload
- Failure injection: Content Cache down, slow, corrupt
- Failover test: multi-region S3
- Rollback test: kill switch activated, all new intents inline
- Monitoring and alerting verified

---

## Module Layout

```text
content-service/
  ├── cmd/
  │   ├── content-api/           gRPC server main
  │   ├── disposition-job/       Background disposition job
  │   ├── integrity-job/         Background integrity verification
  │   ├── orphan-sweeper/        Orphan reconciliation
  │   └── parquet-compactor/     Parquet batch compaction
  ├── internal/
  │   ├── api/                   gRPC service implementation
  │   ├── metadata/              PostgreSQL metadata store
  │   ├── storage/               Tier routing and payload storage
  │   ├── retention/             Retention and lifecycle logic
  │   ├── security/              Tenant authorization and audit
  │   └── observability/         Metrics and tracing
  ├── pkg/
  │   ├── reference/             ContentReference format
  │   ├── digest/                SHA-256 computation
  │   └── config/                Configuration loading
  ├── migrations/                SQL migrations
  ├── tests/
  │   ├── conformance/           Design 1.26 conformance tests
  │   ├── integration/           End-to-end integration tests
  │   └── security/              Security negative tests
  └── docs/                      Design and implementation docs
```

---

## Dependency Direction

```text
cmd/                     ←── no internal deps beyond internal/
     │
internal/api             ←── internal/metadata, internal/storage
     │
internal/metadata        ←── internal/api (gRPC client)
     │
internal/storage         ←── internal/metadata (tier abstraction)
```

---

## Testing Strategy

| Test Type | Scope | Frequency |
|-----------|-------|-----------|
| Unit | Individual packages | Every commit |
| Contract | Storage adapter interface | Every commit |
| Conformance | Design 1.26 conformance suite | Every PR |
| Integration | End-to-end with Docker Compose | Every PR |
| Security | Cross-tenant leak, auth bypass | Every PR |
| Load | Production-like workload | Pre-release |
| Failure | Chaos engineering | Pre-release |

---

## Configuration Reference

See [design.md](design.md) section 10 for the full configuration
reference. All values can be overridden with environment variables.

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| S3 ETag not content hash for multipart uploads | Use Content-MD5 or application-level digest, not ETag |
| Cross-tenant existence leak | Indistinguishable errors, constant-time comparison, security tests |
| Orphan accumulation | Grace period, hourly sweep, ref count tracking |
| Schema migration failure | DDL ordering, NOT VALID + VALIDATE, resumable backfill |
| Parquet batching complexity | Start with inline + S3 only, add Parquet in Phase 5 |
| Commitix adapter coupling | ContentStore interface in separate module, no commitix-core dependency |
