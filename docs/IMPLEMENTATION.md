# Implementation Guide

This document tells you **what to build next** and **in what order**. It is the operational companion to `docs/implementation-plan.md` and the approved integration proposal v3.

The goal is to reach a working, testable slice as fast as possible, then grow outward from the store core.

Normative references used throughout:

- Contract: Synanton Design 1.26 (Content Cache plane)
- Integration: approved Content Cache / Commitix proposal v3 (TL;DR table of 15 decisions)
- Local: `docs/design.md` (§3 tiers, §4 reference, §5 retention, §6 API)

---

## Guiding Principles

1. **Contract first.** Tenant isolation and integrity verification must be correct and fully tested before any new tier exists.
2. **Spec before code.** If a behavior is not written down, write it down first.
3. **No infrastructure in the domain.** `internal/domain` imports only the standard library.
4. **Prove it in a test.** Every lifecycle transition must have a test that would fail if the transition were wrong.
5. **Indistinguishable failures.** Cross-tenant reads fail exactly like missing content — same code, same message, no timing leak.

---

## Phase 0 — Project Setup and Contract Alignment (done)

**Goal:** Skeleton, config, migrations, Docker Compose, docs foundation.

### Tasks

- [x] Go module with hexagonal layout (`cmd/`, `internal/`, `pkg/`, `test/`, `migrations/`)
- [x] Environment-first configuration (`internal/config`) with validation
- [x] Migrations 000001–000004 (artifacts, retention, reference registry, audit log)
- [x] Docker Compose (Postgres + MinIO + service) with healthchecks
- [x] `docs/design.md`, `docs/implementation-plan.md`

**Definition of done:** `go build ./...` and `go test ./...` pass; `docker compose up` starts Postgres and MinIO.

---

## Phase 1 — Core Conformance (done)

**Goal:** A fully tested put/get core against real Postgres that can be demonstrated in an integration test.

### First Milestone: "Round Trip in a Test"

```go
// test/integration/postgres_test.go
func TestPutGetRoundTrip(t *testing.T) {
    // Put payload for tenant A, get it back, verify bytes and digest.
    // Put identical bytes again, assert same artifact id (dedup hit).
    // Get as tenant B, assert ErrNotFound (indistinguishable).
}
```

This test exercises: digest computation, tier routing, dedup by `(tenant_id, digest)`, tenant-scoped reads, integrity verification — all against a testcontainers Postgres. It mirrors the proposal §5 acceptance criteria 1–5.

### Tasks

- [x] `internal/domain/artifact.go` — Artifact, RetentionPolicy, sentinel errors
- [x] `internal/port/store.go` — ContentStore port (Put/Get/Exists/MarkForDeletion/Register/Release/SetLegalHold/SetRetention)
- [x] `pkg/digest`, `pkg/reference` — hashing and v1 reference encode/parse
- [x] `internal/adapter/postgres` — pgx implementation with dedup, tenant scoping, audit
- [x] `internal/adapter/http` — net/http REST adapter (put/get/head/capabilities/healthz/readyz)
- [x] `cmd/content-api` — wiring, migrate-on-startup, graceful shutdown
- [x] `test/integration/postgres_test.go` — TestMain with testcontainers Postgres, 12 tests
- [x] S3/MinIO backing for `s3` and `parquet` tiers (`BlobStore` port + minio adapter, content-addressed keys, bucket auto-creation; `test/integration/s3_test.go`, 6 tests against containerized MinIO)
- [x] gRPC surface (`proto/content/v1`, buf codegen to `gen/`, `test/integration/grpc_test.go` via bufconn, 6 tests)
- [x] Background jobs — see Phase 5 (disposition, integrity, orphan sweeper, real Parquet compaction)

**Definition of done:** `make test-integration` passes with `-race`; cross-tenant tests return indistinguishable `NOT_FOUND`; legal-hold changes are audited.

---

## Phase 2 — Commitix Adapter Design (largely landed in platform/commitix repos)

**Goal:** Java `ContentStore` port in the platform repo, agreed with this service.

### Tasks

- [ ] ADR for module placement (`java:content-cache-api`) and reference format
- [x] Java `ContentStore` interface (put/get/exists/register/release) + `ContentReference` parser
- [x] Contract tests shared with the Go suite (dedup, isolation, legal hold)
- [x] Feature flag hierarchy (per-tenant → per-intent-type → threshold → kill switch)
- [ ] Migration plan for `commitix_intents` (`payload_kind`, `content_ref`, `payload_sha256`) — deferred by design: the envelope approach needs no migration

**Definition of done:** Java contract tests green against both the in-memory fake and the Postgres-backed store.

---

## Phase 3 — Optional Large-Payload Adapter

**Goal:** End-to-end externalization with failure taxonomy covered by tests.

### Tasks

- [x] Commitix `ContentStore` HTTP client calling this service
- [x] Commitix `ContentStore` gRPC client calling this service
- [x] Timeout and deadline propagation (2s connect, 5s read/RPC)
- [ ] Retry bounded by the outer deadline (no extension on retry)
- [x] Observability metrics (put/get latency; externalized count and threshold histogram live client-side)

**Definition of done:** Commit/rollback, remote-outage, checksum-failure, and orphan-cleanup contract tests pass.

---

## Phase 4 — Vertical Slice

**Goal:** Ingestion → Content Cache → Eventing → Knowledge/Search with the reference in the event envelope.

### Tasks

- [ ] Event envelope carries `content_reference` (envelope-only, not payload)
- [ ] Scripted end-to-end path reproducible via Docker Compose
- [ ] All 12 acceptance criteria from proposal §5 green

---

## Phase 5 — Extended Lifecycle (current)

**Goal:** Operable service.

- [x] Disposition, integrity, orphan-sweeper jobs + real Parquet compaction
- [ ] Multi-region S3
- [x] Prometheus metrics endpoint (`/metrics` serves registered counters/histograms/gauges)
- [x] Design 1.26 conformance suite (14 checks, fake + real wiring)
- [ ] Dashboards
- [ ] `docs/runbook.md` incident procedures exercised

---

## What to Do Today

1. Run `make test-integration` and confirm all 51 tests green.
2. Run `make docker-up` and `curl localhost:8080/readyz`, plus a grpcurl `Put` against `:50051`.
3. Run `make bench-integration` and record results in `docs/benchmarks.md` with hardware provenance.
4. Pick an open Phase 5 item above (dashboards, structured logging, runbook incident exercises).
5. Write the test first, then implement; update the maturity table only with evidence in hand.

---

## Definition of Done (per phase)

A phase is complete when:

- All tasks are checked off.
- All tests pass with `-race`.
- `go vet ./...` is clean.
- The phase's acceptance criteria (above) are met.
- `docs/design.md` reflects any behavior discovered during implementation.
- `CHANGELOG.md` is updated.
