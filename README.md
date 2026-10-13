# content-service

> Spec-first Go implementation of Synanton Content Cache 1.26 — tenant-scoped, content-addressable storage with integrity verification, retention lifecycle, and opaque references.

[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-pre--alpha-orange.svg)](#status)

`content-service` is the **reference implementation** of [Content Cache 1.26](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.26.md), built from the approved [integration proposal v3](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/proposals/Content-service/content-cache-commitix-integration-proposal.md).

If a payload enters the platform, it gets a digest. If it leaves, the digest is verified. If a tenant asks for it, the tenant is authorized. If retention expires, disposition follows the policy lattice — not an S3 lifecycle guess.

---

## Status

**Pre-alpha.** Under active development. Not production-ready. See [`docs/IMPLEMENTATION.md`](docs/IMPLEMENTATION.md) for the current phase and [`CONTRIBUTING.md`](CONTRIBUTING.md) for the acceptance gates.

*State as of 2026-10-13 — Phase 1 extended: S3/MinIO object tiers + gRPC surface, 24 integration tests green.*

### Maturity

| Area | Implemented | Unit/integration tested | Conformance-validated | Benchmark-validated |
|---|---|---|---|---|
| Domain core (artifacts, retention, references) | ✅ | ✅ | ✅ (Core) | ⬜ |
| Postgres adapter (put/get, dedup, tenant isolation) | ✅ | ✅ (testcontainers) | ✅ (Core+Extended suite) | ⬜ |
| HTTP surface (artifacts, capabilities, healthz/readyz) | ✅ | ✅ (contract tests) | ⬜ | ⬜ |
| gRPC surface (put/get/head/capabilities) | ✅ | ✅ (bufconn) | ⬜ | ⬜ |
| Reference registry (register/release) | ✅ | ✅ | ✅ (Core suite) | ⬜ |
| Legal hold + audit trail | ✅ | ✅ | ✅ (Core suite) | ⬜ |
| S3/MinIO backing for object tiers | ✅ | ✅ (testcontainers minio) | ⬜ | ⬜ |
| Background jobs (disposition, integrity, orphan sweeper) | ✅ | ✅ (testcontainers) | ⬜ | ⬜ |
| Parquet compaction (merge small files) | ✅ | ✅ (testcontainers) | ⬜ | ⬜ |

`⬜` = not yet; nothing in this table is claimed before its evidence exists.

Explicitly out of scope for v0.2.0: Parquet batching, multi-region storage, Prometheus dashboards. ⬜ rows above are deferred by decision, not pending by oversight.

---

## What It Is

`content-service` sits between platform planes and raw object storage. It solves a problem direct S3 usage handles poorly: **tenant-scoped, auditable, verifiable content storage with lifecycle policies**, without leaking bucket layouts or letting one consumer's delete remove another tenant's bytes.

It targets Commitix large-payload externalization first, then ingestion artifacts, knowledge chunks, and search projections.

### Core Mechanisms

- **Content-addressable dedup** by `(tenant_id, digest)` — same bytes, same tenant → same reference; same bytes, different tenant → separate references, no existence leak
- **Tier routing by size** — inline (< 4 KiB), Parquet batches (< 256 KiB), individual S3 objects (above); Phase 1 records the tier label with bytes in Postgres
- **Opaque v1 references** — URI-encoded `tenant_id`, `artifact_id` (UUID), `digest`, `generation`, `serializer_version`; 512-character bound; wire format is not public API
- **Two-phase deletion** — `markForDeletion` releases one owner's reference; physical deletion needs ref count zero, no legal hold, grace-period expiry
- **Retention lattice** — `legal_hold > tenant_retention > owner_retention`; erasure-vs-hold conflicts resolve outside the service
- **Integrity verification** — SHA-256 at publication time; full re-hash on replay/audit; storage-layer checksums trusted on the hot path

### Put/Get Lifecycle

```text
PUT (tenant, bytes) → digest → dedup? → store → reference
GET (tenant, artifact_id) → authorize → fetch → verify digest → bytes
```

Publication is atomic: the artifact row, retention row, and reference row commit together. A failed put leaves no half-persisted artifact.

---

## Architecture

Hexagonal (ports and adapters):

```text
┌─────────────────────────────────────────────────┐
│               cmd/content-api                   │
│              (wiring + HTTP)                    │
└───────────────────────┬─────────────────────────┘
                        │
         ┌──────────────┼──────────────┐
         │              │              │
┌────────▼──────┐ ┌─────▼──────┐ ┌─────▼──────┐
│  REST API     │ │  Metrics   │ │  Health    │
│  (net/http)   │ │  (stub)    │ │ (/readyz)  │
└────────┬──────┘ └─────┬──────┘ └─────┬──────┘
         │              │              │
         └──────────────┼──────────────┘
                        │
                ┌───────▼────────┐
                │  ContentStore  │
                │  port          │
                └───────┬────────┘
                        │
         ┌──────────────┼───────────────┐
         │              │               │
┌────────▼──────┐ ┌─────▼──────┐ ┌──────▼──────┐
│  PostgreSQL   │ │  S3/MinIO  │ │  Parquet    │
│  (pgx)        │ │  (planned) │ │  (planned)  │
└───────────────┘ └────────────┘ └─────────────┘
```

The domain package is pure Go: no `pgx`, no HTTP, no S3. Adapters depend on ports. This is what makes the lifecycle testable against containers.

See [`docs/architecture.md`](docs/architecture.md) for details.

---

## Quick Start

### Prerequisites

- Go 1.22+
- Docker + Docker Compose (for Postgres, MinIO, and integration tests)

### Build

```bash
git clone https://github.com/synanton/content-service.git
cd content-service
go build -o bin/content-service ./cmd/content-api
```

### Run with Docker Compose

```bash
make docker-up   # generates dev certs into ./certs/, then builds + starts
curl http://localhost:8080/readyz
```

Postgres and MinIO data persist in explicitly-named Docker volumes
(`content-postgres-data`, `content-minio-data`), so state survives
`docker compose down`. Drop them with `docker compose down -v`.
Dev mTLS material lives in `./certs/` (gitignored, generated by
`make certs`); production mounts real certificates from the secret
store. Host ports are overridable: `PG_PORT`, `MINIO_PORT`,
`MINIO_CONSOLE_PORT`, `APP_PORT`.

### Minimal Configuration

```yaml
server:
  host: 0.0.0.0
  port: 8080

database:
  url: postgres://content:content@localhost:5432/content?sslmode=disable

storage:
  inline_threshold_bytes: 4096
  object_threshold_bytes: 262144

retention:
  default_policy_days: 90
  legal_hold_enabled: true
  disposition_grace_period: 72h
```

Env vars (`CONTENT_*`) override file keys. See `configs/local.yaml` and [`docs/runbook.md`](docs/runbook.md) for the full reference.

---

## API

All `/api/v1/**` artifact routes are tenant-scoped via `X-Tenant-ID` (preferred), `?tenant_id=`, or the JSON body field. The same contract is served over gRPC (`content.v1.ContentService`, default `:50051`, server reflection enabled) — see [`docs/api.md`](docs/api.md) and `proto/content/v1/content.proto`.

| Method | Endpoint | Description |
| ------ | -------- | ----------- |
| `POST` | `/api/v1/artifacts` | Publish an artifact (idempotent by `(tenant_id, digest)`) |
| `GET` | `/api/v1/artifacts/{id}` | Retrieve payload bytes with integrity verification |
| `HEAD` | `/api/v1/artifacts/{id}` | Existence check (full metadata is a future capability) |
| `GET` | `/api/v1/capabilities` | Conformance level (`CORE`) and supported tiers |
| gRPC | `content.v1.ContentService/Put` | Same as `POST /api/v1/artifacts` |
| gRPC | `content.v1.ContentService/Get` | Same as `GET /api/v1/artifacts/{id}` |
| gRPC | `content.v1.ContentService/Head` | Same as `HEAD /api/v1/artifacts/{id}` |
| gRPC | `content.v1.ContentService/Capabilities` | Same as `GET /api/v1/capabilities` |

### Publish a Payload

```bash
PAYLOAD=$(echo -n "hello content-service" | base64 -w0)
curl -X POST http://localhost:8080/api/v1/artifacts \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: acme" \
  -d "{\"payload\":\"$PAYLOAD\",\"content_type\":\"text/plain\",\"owner_id\":\"demo\"}"
```

Response: `201 Created` with the opaque `reference`, `artifact_id`, `digest`, `dedup_hit`, and `storage_tier`.

### Retrieve a Payload

```bash
curl "http://localhost:8080/api/v1/artifacts/<artifact_id>?tenant_id=acme"
```

Unknown id — or an id owned by another tenant — returns an indistinguishable `404 NOT_FOUND` (gRPC: `NotFound`). See [`docs/api.md`](docs/api.md) for all endpoints.

### gRPC

```bash
grpcurl -plaintext \
  -d '{"tenant_id": "acme", "payload": "<base64>", "content_type": "text/plain"}' \
  localhost:50051 content.v1.ContentService/Put
```

---

## Configuration Reference

| Key | Default | Description |
| --- | ------- | ----------- |
| `CONTENT_DB_URL` | `postgres://localhost:5432/content?sslmode=disable` | PostgreSQL connection string |
| `CONTENT_SERVER_HOST` / `CONTENT_SERVER_PORT` | `0.0.0.0` / `8080` | HTTP listen address |
| `CONTENT_GRPC_PORT` | `50051` | gRPC listen port |
| `CONTENT_S3_ACCESS_KEY` / `CONTENT_S3_SECRET_KEY` | `minioadmin` / `minioadmin` | Object storage credentials (dev defaults) |
| `CONTENT_API_KEY` | — (empty) | Reserved for gateway auth (Compose requires it set) |
| `CONTENT_MIGRATE_ON_STARTUP` | `false` | Opt-in; applies `migrations/*.up.sql` before serving |
| `CONTENT_MIGRATIONS_DIR` | `migrations` | Override for inspect-first operators |
| `CONTENT_INLINE_THRESHOLD_BYTES` | `4096` | Below this: `inline` tier label |
| `CONTENT_OBJECT_THRESHOLD_BYTES` | `262144` | Above this: `s3` tier label; between: `parquet` |
| `CONTENT_RETENTION_DEFAULT_DAYS` | `90` | Applied when the put omits `retention_days` |
| `CONTENT_VERIFY_ON_PUBLISH` | `true` | Digest verified at publication time |
| `CONTENT_VERIFY_ON_READ` | `false` | Full re-hash on read (replay/audit only) |
| `CONTENT_ORPHAN_GRACE_PERIOD` | `24h` | Minimum age before an unreferenced object is a candidate |
| `CONTENT_AUDIT_ENABLED` | `true` | Legal-hold and retention changes audited |

---

## Observability

### Endpoints

- `GET /healthz` — liveness (200 while serving)
- `GET /readyz` — readiness (DB ping; 503 on failure)
- `GET /metrics` — Prometheus metrics (put/get latency and errors, integrity failures, disposition/orphan/parquet gauges)
- `GET /api/v1/capabilities` — conformance level and supported tiers

### Logging

Standard-library `log` today. Set container log level via the platform's log driver; structured JSON logging lands with the observability phase.

---

## Background Jobs

| Job | Scaling | Purpose |
| --- | ------- | ------- |
| `disposition-job` | Single leader (advisory lock) | Expired artifacts → check refs and legal holds → delete row first, then blob |
| `integrity-job` | Sharded by tenant (hash mod) | Re-read payloads, verify digests, audit on mismatch (no auto-quarantine yet) |
| `orphan-sweeper` | Single leader (advisory lock) | Metadata-without-payload: audit only. Payload-without-metadata: delete after grace (dry-run supported) |
| `parquet-compactor` | Merge small files | Merge sub-target files per tenant (oldest first), repoint rows by offset, delete old files/objects; every crash window heals via the orphan sweeper |

Run locally under the `jobs` profile (plain `up` stays API-only):

```bash
docker compose --profile jobs up -d
```

Each job runs immediately on start, then on its tick, and exits cleanly on SIGTERM. Lost leadership resolves to skip-and-retry, never an error. See [`docs/runbook.md`](docs/runbook.md) for intervals and sharding.

---

## Development

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for setup, workflow, gates, and review checklists.

Common commands:

```bash
make test              # unit tests
make test-integration  # requires Docker (testcontainers, postgres:16-alpine)
make test-conformance  # Design 1.26 Core checks
make vet               # go vet
make bench             # digest + reference benchmarks
```

---

## Roadmap

See [`docs/IMPLEMENTATION.md`](docs/IMPLEMENTATION.md) for the phased plan and the current milestone.

- Phase 0 — Project setup and contract alignment (done)
- Phase 1 — Core conformance: HTTP + gRPC + Postgres + S3 + integration suite (done)
- Phase 2 — Commitix adapter design (Java `ContentStore` port)
- Phase 3 — Optional large-payload adapter
- Phase 4 — Vertical slice (event envelope carries the reference)
- Phase 5 — Extended lifecycle: background jobs (done), Parquet compaction, full observability (current)
- Phase 6 — Adoption decision

---

## Relation to Design 1.26

Design 1.26 is the contract; this repo is one conformant implementation. The service:

- Preserves tenant isolation, content-addressable dedup, and the retention lattice
- Replaces storage-specific mechanisms (bucket layouts, lifecycle rules) with the port contract
- Is validated by the integration suite in `test/integration/` (12 tests against real Postgres)

Key references:

- Contract: Synanton Design 1.26 (Content Cache plane)
- Integration: approved proposal v3 (15 decisions in the TL;DR table)
- Local: `docs/design.md` (§3 tiers, §4 reference, §5 retention, §6 API)

---

## Contributing

Contributions welcome. Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) before opening a PR.

1. Fork the repository
2. Create a feature branch (`git checkout -b CNT-my-change`)
3. Add tests for new behavior
4. Ensure `make test` and `go vet ./...` pass
5. Submit a pull request (merging is manual — a maintainer merges after review)

---

## License

Apache License 2.0. See [LICENSE](LICENSE).
