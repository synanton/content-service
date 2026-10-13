# Content Service — Content Cache 1.26 Implementation

**Content Service** is a Phase 1 implementation of *Synanton Content Cache 1.26*.
It guarantees **tenant-scoped, content-addressable storage** with integrity
verification, retention lifecycle, and opaque references for all Synanton planes.

If a payload enters the platform, it gets a digest. If it leaves, the digest
is verified. If a tenant asks for it, the tenant is authorized. If retention
expires, disposition follows the policy lattice — not an S3 lifecycle guess.

---

## Component Layout

```
content-service/
  ├── content-api/            gRPC/REST server — put, get, head, lifecycle RPCs
  ├── content-metadata/       PostgreSQL — artifact registry, tenant scope, retention
  ├── content-storage/        Storage tier abstraction (inline, Parquet, S3)
  └── content-jobs/           Background: disposition, integrity, orphan-sweeper
```

**Dependency direction:**

```
content-api              ←── no storage deps beyond content-metadata client
     │
content-metadata         ←── content-api (gRPC client)
     │
content-storage          ←── content-metadata (tier abstraction)
```

---

## Architecture

```
 ┌─────────────────────────────────────────────────────────────────┐
 │  content-api                                                    │
 │                                                                 │
 │   ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────────┐   │
 │   │  Put     │  │  Get     │  │  Head    │  │  Lifecycle    │   │
 │   │  path    │  │  path    │  │  path    │  │  path         │   │
 │   └────┬─────┘  └────┬─────┘  └────┬─────┘  └──────┬────────┘   │
 │        │             │             │               │            │
 │        ▼             ▼             ▼               ▼            │
 │   ┌─────────────────────────────────────────────────────────┐   │
 │   │  Tenant authorization + integrity verification          │   │
 │   └─────────────────────────────────────────────────────────┘   │
 └──────────┬──────────────────────────────────────────────────────┘
            │
            ▼
 ┌──────────────────────────────┐   ┌──────────────────────────────┐
 │  content-metadata            │   │  content-storage             │
 │                              │   │                              │
 │  Artifact registry           │   │  Inline (Postgres BYTEA)     │
 │  Retention policies          │   │  Parquet batches (S3)        │
 │  Legal hold state            │   │  Individual objects (S3)     │
 │  Reference counts            │   │                              │
 └──────────────────────────────┘   └──────────────────────────────┘
```

---

## Storage Tiers

On every `put`, the payload is routed by size:

```
  payload < 4 KiB          →  inline in Postgres (fast, transactional)
  4 KiB ≤ payload < 256 KiB →  append to Parquet batch on S3 (columnar)
  payload ≥ 256 KiB        →  individual S3 object (streaming)
```

Thresholds are configurable and benchmarked. The tier is an implementation
detail — the reference is opaque.

---
## Retention Precedence

Retention is evaluated at disposition time, not publication time:

| Priority | Policy | Behavior |
|:---:|---|---|
| 1 (high) | Legal hold | Blocks disposition regardless of retention. |
| 2 | Tenant retention | Minimum for all tenant content. Owner cannot shorten. |
| 3 | Owner retention | May extend tenant retention. Never shorten. |

S3 lifecycle manages tier transitions, never physical deletion.

---

## Getting Started

### Prerequisites

- Go 1.22+
- Docker & Docker Compose (Postgres + MinIO)
- `grpcurl`

### 1. Start dependencies

```bash
docker compose up -d postgres minio
```

### 2. Apply migrations

```bash
make migrate
```

### 3. Run the service

```bash
make run
```

### 4. Publish a payload

```bash
grpcurl -plaintext \
  -d '{
        "tenant_id": "acme",
        "payload": "<base64-encoded bytes>",
        "media_type": "application/json"
      }' \
  localhost:50051 content.v1.ContentService/Put
```

**Response:** `ContentReference` with a versioned opaque reference.

### 5. Retrieve a payload

```bash
grpcurl -plaintext \
  -d '{"tenant_id": "acme", "reference": "v1:..."}' \
  localhost:50051 content.v1.ContentService/Get
```

**Response:** payload bytes, after integrity verification.

### 6. Check capabilities

```bash
grpcurl -plaintext \
  -d '{}' \
  localhost:50051 content.v1.ContentService/Capabilities
```

**Response:** conformance level and optional features.

---

## API

All operations require `tenant_id`. Cross-tenant access returns an
indistinguishable error.

  -----------------------------------------------------------------------
  Method            Endpoint                            Description
  ----------------- ----------------------------------- -------------------
  `Put`             `content.v1.ContentService/Put`     Publish artifact;
                                                        idempotent by
                                                        `(tenant_id, digest)`

  `Get`             `content.v1.ContentService/Get`     Retrieve with
                                                        integrity
                                                        verification

  `Head`            `content.v1.ContentService/Head`    Metadata only, no
                                                        payload bytes

  `MarkForDeletion` `.../MarkForDeletion`               Release logical
                                                        reference (not
                                                        physical delete)

  `SetRetention`    `.../SetRetention`                  Set/update
                                                        retention policy

  `SetLegalHold`    `.../SetLegalHold`                  Place/release legal
                                                        hold

  `Capabilities`    `.../Capabilities`                  Advertise conformance
                                                        and features

  `VerifyIntegrity` `.../VerifyIntegrity`               Verify payload
                                                        against stored digest

  `GetAuditTrail`   `.../GetAuditTrail`                 Access and lifecycle
                                                        audit entries
  -----------------------------------------------------------------------

---

## Configuration

```yaml
content:
  db:
    url: ${CONTENT_DB_URL}
    max-connections: 20

  s3:
    endpoint: ${CONTENT_S3_ENDPOINT}
    bucket: ${CONTENT_S3_BUCKET:-synanton-content}
    region: ${CONTENT_S3_REGION:-us-east-1}
    use-path-style: true

  storage:
    inline-threshold-bytes: 4096
    object-threshold-bytes: 262144
    parquet-batch-size: 1000
    parquet-compression: zstd

  retention:
    default-policy-days: 90
    legal-hold-enabled: true
    disposition-grace-period: 72h

  integrity:
    verify-on-read: false
    verify-on-publish: true
    verification-interval: 24h
    sample-rate: 0.01

  orphan:
    grace-period: 24h
    sweep-interval: 1h
    dry-run: false

  security:
    mtls-cert: ${CONTENT_MTLS_CERT}
    mtls-key: ${CONTENT_MTLS_KEY}
    kms-key-id: ${CONTENT_KMS_KEY_ID}
    audit-enabled: true

  observability:
    log-level: info
    metrics-enabled: true
    tracing-enabled: true
    tracing-endpoint: ${OTEL_ENDPOINT}
```

---

## Background Jobs

  ------------------------------------------------------------------------
  Job                 Scaling                Purpose
  ------------------- ---------------------- ------------------------------
  `disposition-job`   Single leader          Expired artifacts → check refs
                                             and legal holds → delete

  `integrity-job`     Sharded by tenant     Re-read payloads, verify
                                             digests, alert on mismatch

  `orphan-sweeper`    Single leader          Reconcile metadata vs object
                                             store, delete orphans after
                                             grace period

  `parquet-compactor` Sharded by partition   Merge small Parquet batches
                                             into larger files
  ------------------------------------------------------------------------

---

## Design Decisions

See `docs/adr/` for the rationale behind key architectural choices.

| ADR | Title |
|-----|-------|
| *(pending)* | Module placement and dependency direction |
| *(pending)* | Reference format and versioning |
| *(pending)* | Checksum semantics and verification cadence |
| *(pending)* | Threshold configuration and benchmark methodology |
| *(pending)* | Retention precedence lattice |
| *(pending)* | Orphan reconciliation protocol |
| *(pending)* | Cross-tenant leak prevention |
| *(pending)* | Two-phase deletion semantics |

## Documentation

- [Design Document](docs/design.md) — Architecture, storage tiers, API, security
- [Implementation Plan](docs/implementation-plan.md) — Phased delivery with exit criteria
- [Synanton Design 1.26](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.26.md) — Content Cache contract
- [Integration Proposal v3](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/proposals/Content-service/content-cache-commitix-integration-proposal.md) — Approved proposal

---

## Phase 1 Acceptance Criteria

| # | Deliverable | Status |
|---|-------------|--------|
| 1 | Tenant-scoped put/get with opaque reference | pending |
| 2 | Content-addressable dedup by `(tenant_id, digest)` | pending |
| 3 | Inline/Parquet/S3 tier routing | pending |
| 4 | Cross-tenant leak prevention (indistinguishable error) | pending |
| 5 | Atomic publication — all-or-nothing visibility | pending |
| 6 | Retention lattice: legal_hold > tenant > owner | pending |
| 7 | `markForDeletion` as reference release, not delete | pending |
| 8 | Capability discovery — conformance level advertised | pending |
| 9 | Integrity verification at publication and on replay | pending |
| 10 | Orphan reconciliation with grace period | pending |
| 11 | Commitix adapter contract tests | pending |

**Phase 2+ (not in scope):** SLO-backed operations, multi-region storage,
Web UI, Prometheus dashboards.

---

## License

[Apache 2.0](LICENSE)

---

## References

- [Synanton Platform Architecture 1.0](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-platform-architecture-1.0.md)
- [Synanton Design 1.26 — Content Cache](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.26.md)
- [Synanton Design 1.27 — Eventing and Workflow](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.27.md)
- [Commitix White Paper 1.0](https://raw.githubusercontent.com/synanton/commitix/refs/heads/main/docs/Commitix-White-Paper-v1.0.md)

---

**Status:** Proposal / PoC. Content Service is a candidate implementation of
Content Cache 1.26. Not yet approved for production use.
