# Content Service — Design Document

**Status:** Approved for Phase 0 implementation\
**Implements:** Synanton Design 1.26 (Content Cache Plane)\
**Language:** Go 1.22+\
**Date:** 2026-10-13

---

## 1. Overview

Content Service is the reference implementation of Synanton Content Cache
1.26. It provides tenant-scoped, content-addressable storage with
integrity verification, retention lifecycle, and opaque references for
all Synanton planes.

The service is deliberately storage-independent at the contract level.
The implementation uses PostgreSQL for metadata and inline payloads plus
S3-compatible object storage (MinIO) for `parquet` and `s3` tiers, keyed
content-addressably as `<tenant>/<digest-hex>` with the bucket created on
startup. The Parquet batching tier currently shares the object backend
(compaction lands with the background jobs).

---

## 2. Architecture

```text
                    ┌─────────────────────┐
                    │   content-api       │
                    │   (gRPC server)     │
                    └──────────┬──────────┘
                               │
                    ┌──────────┴──────────┐
                    │  Tenant auth +      │
                    │  integrity verify   │
                    └──────────┬──────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
              ▼                ▼                ▼
       ┌────────────┐  ┌────────────┐  ┌────────────┐
       │  metadata  │  │  inline    │  │  object    │
       │  (Postgres)│  │  (Postgres)│  │  (S3)      │
       └────────────┘  └────────────┘  └────────────┘
```

### 2.1 Components

| Component | Responsibility | Scaling |
|-----------|---------------|---------|
| `content-api` | gRPC/REST server, tenant authorization, integrity verification | Horizontal |
| `content-metadata` | Artifact registry, retention policies, legal hold, reference counts | HA, read replicas |
| `content-storage` | Tier routing (inline, Parquet, S3), payload read/write | Horizontal |
| `disposition-job` | Expired artifact disposition, legal hold check, physical delete | Single leader |
| `integrity-job` | Background digest verification, quarantine on mismatch | Sharded by tenant |
| `orphan-sweeper` | Metadata/object-store reconciliation, orphan deletion | Single leader |
| `parquet-compactor` | Small batch merging into larger Parquet files | Sharded by partition |

---

## 3. Storage Tiers

Payloads are routed by size at `put` time:

```text
payload < inline_threshold       →  inline in Postgres (BYTEA)
inline_threshold ≤ payload       →  append to Parquet batch on S3
  < object_threshold
payload ≥ object_threshold       →  individual S3 object
```

Default thresholds:
- `inline_threshold`: 4096 bytes (4 KiB)
- `object_threshold`: 262144 bytes (256 KiB)

Thresholds are configurable via environment variables or configuration
file. The tier selection is an implementation detail — the
`ContentReference` returned to the caller is opaque and does not
reveal the storage tier.

---

## 4. Content Reference Format

```text
v1:tenant_id=<uuid>&artifact_id=<uuid>&digest=sha256:<hex>&generation=<int>&serializer_version=<int>
```

- **Version prefix:** `v1:` allows format evolution. The wire format
  is not part of the public API contract.
- **Fields:** `tenant_id`, `artifact_id` (UUID, not digest), `digest`
  (SHA-256 content hash), `generation` (increments on reprocessing),
  `serializer_version` (enables safe dedup within a serializer
  version).
- **Encoding:** URI-encoded key=value pairs separated by `&`.
- **Maximum length:** 512 characters (enforced by database CHECK
  constraint).
- **Composition:** Content Cache composes the reference. The adapter
  passes `serializer_version` in metadata; Content Cache includes it
  in the returned reference.

---

## 5. Retention and Lifecycle

### 5.1 Retention Precedence

```text
legal_hold > tenant_retention > owner_retention
```

- **Legal hold** overrides tenant and owner retention. Set by
  authorized administrative operation (`content.admin.legal_hold`
  role). The conflict between erasure requests and legal hold is
  resolved outside Content Cache by the administrative workflow that
  issues both directives.
- **Tenant retention** is the minimum retention for all tenant
  content. Owner cannot shorten.
- **Owner retention** may extend beyond tenant minimum, never shorten.

### 5.2 Two-Phase Deletion

1. **Logical expiration:** artifact becomes ineligible for normal
   retrieval when its retention period expires.
2. **Physical deletion:** asynchronous, after all live references
   are accounted for, no legal hold applies, and the grace period
   has elapsed.

`markForDeletion` releases a consumer's logical reference. It is not
permission to physically delete shared content. Physical deletion
requires: ref count = 0, no retention requirement, no legal hold,
grace period elapsed.

### 5.3 Cross-Tenant Isolation

All cross-tenant queries fail with an indistinguishable error
(`NOT_FOUND`). Response timing does not differ between "exists under
other tenant" and "does not exist." Metadata queries are tenant-scoped
and do not leak shared-backend structure.

---

## 6. API

All operations require `tenant_id`. Cross-tenant access is denied at
the API layer. The same contract is served over REST and gRPC
(`content.v1.ContentService`, `proto/content/v1/content.proto`); gRPC
errors map as NotFound / InvalidArgument / DataLoss (integrity) / Internal.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `Put` | `content.v1.ContentService/Put` | Publish artifact; idempotent by `(tenant_id, digest)` |
| `Get` | `content.v1.ContentService/Get` | Retrieve with integrity verification |
| `Head` | `content.v1.ContentService/Head` | Metadata only (future capability) |
| `MarkForDeletion` | `content.v1.ContentService/MarkForDeletion` | Release logical reference |
| `SetRetention` | `content.v1.ContentService/SetRetention` | Set/update retention policy |
| `SetLegalHold` | `content.v1.ContentService/SetLegalHold` | Place/release legal hold |
| `Capabilities` | `content.v1.ContentService/Capabilities` | Advertise conformance and features |
| `VerifyIntegrity` | `content.v1.ContentService/VerifyIntegrity` | Verify payload against stored digest |
| `GetAuditTrail` | `content.v1.ContentService/GetAuditTrail` | Access and lifecycle audit entries |

---

## 7. Observability

| Metric | Type | Labels |
|--------|------|--------|
| `content.put.latency` | histogram | tenant_id, tier |
| `content.get.latency` | histogram | tenant_id, tier |
| `content.put.errors` | counter | tenant_id, error_type |
| `content.get.errors` | counter | tenant_id, error_type |
| `content.integrity.failures` | counter | tenant_id |
| `content.orphan.candidates` | gauge | tenant_id, kind |
| `content.disposition.duration` | histogram | — |
| `content.put.latency` / `content.get.latency` | histogram | tenant_id, tier |
| `content.put.errors` / `content.get.errors` | counter | tenant_id |

All metrics include `tenant_id` as a label. Metrics do not include
payload bytes or individual digests. There is deliberately no
cross-tenant-denial counter: denied reads are indistinguishable from
missing content by design, so counting them separately would itself be
the side channel.

---

## 8. Security

- **Authentication:** mTLS with SPIFFE/SPIRE workload identity.
- **Tenant scope:** JWT `tenant_id` claim + path-level enforcement +
  reference validation.
- **Authorization:** capability-based (workload identity determines
  accessible endpoints).
- **Audit:** all administrative operations (legal hold, retention
  changes, disposition) are audited with actor, timestamp, scope,
  action, reason, and before/after state.
- **Cross-tenant leak prevention:** indistinguishable errors,
  constant-time comparison, tenant-scoped metadata queries.

---

## 9. Deployment

| Component | Type | Scaling |
|-----------|------|---------|
| `content-api` | Stateless gRPC/REST server | Horizontal |
| `disposition-job` | Background job | Single leader |
| `integrity-job` | Background job | Sharded by tenant |
| `orphan-sweeper` | Background job | Single leader |
| `parquet-compactor` | Background job | Sharded by partition |
| `postgres` | Metadata + retention | HA, read replicas |
| `s3` | Payload storage | Multi-region for critical tenants |

---

## 10. Configuration

```yaml
content:
  db:
    url: ${CONTENT_DB_URL}
    max-connections: 20

  s3:
    endpoint: ${CONTENT_S3_ENDPOINT}
    bucket: ${CONTENT_S3_BUCKET:-synanton-content}
    region: ${CONTENT_S3_REGION:-us-east-1}
    access-key: ${CONTENT_S3_ACCESS_KEY:-minioadmin}
    secret-key: ${CONTENT_S3_SECRET_KEY:-minioadmin}
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

## 11. Conformance

The service MUST pass the Design 1.26 conformance test suite before
advertising any conformance level. The initial target is Core
conformance:

- Identity, put/get, atomic publication
- Integrity verification
- Tenant isolation, authorization
- Provenance, errors, capabilities

Extended and Full conformance claims require corresponding test
evidence.

---

## 12. References

- [Synanton Design 1.26 — Content Cache](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.26.md)
- [Content Cache / Commitix Integration Proposal v3](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/proposals/Content-service/content-cache-commitix-integration-proposal.md)
- [Synanton Platform Architecture 1.0](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-platform-architecture-1.0.md)
