# Changelog

Keep-a-Changelog (Added / Changed / Deprecated / Removed / Fixed / Security) per version. Normative behavior lives in the approved integration proposal v3 — this file records what changed, the proposal records why.

## [Unreleased]

### Added

- S3/MinIO backing for `parquet` and `s3` tiers (`BlobStore` port + minio adapter, content-addressed keys, bucket auto-creation on startup). Postgres keeps inline bytes; object-tier rows store `payload NULL` and remain readable alongside legacy inline rows.
- gRPC surface (`content.v1.ContentService`: Put/Get/Head/Capabilities, server reflection) on `:50051` alongside the REST API; `proto/content/v1/content.proto` is the contract, `gen/` holds buf-generated stubs.
- `make proto` regenerates stubs (buf + protoc-gen-go + protoc-gen-go-grpc required).
- Real Parquet compaction: `parquet`-tier puts write single-row Parquet files
  (`parquet/<tenant>/<uuid>.parquet`, indexed by `parquet_files` + artifact
  row pointers); the compactor merges sub-target files per tenant with exact
  row-offset repointing; ranged S3 row reads (no full downloads); legacy
  per-artifact objects still read via fallback.
- Design 1.26 conformance suite extended to 14 checks (tier recording,
  release-vs-retention separation, head consistency, register idempotency)
  and run against the fake (unit) and Postgres + MinIO (integration).
- Benchmarks: tier put/get/dedup percentiles, HTTP/gRPC roundtrips,
  compactor/disposition/integrity sweeps (`make bench-integration`),
  recorded with provenance in `docs/benchmarks.md`.
- Workload scenarios (`test/integration/workload.go` + `scenario_test.go`):
  seeded mixed-size traffic, throughput vs concurrency (1–64 workers),
  sustained churn with retention lifecycle, MinIO kill/recovery and
  slow-store failure injection.
- mdBook developer documentation (`docs/book.toml`, `docs/SUMMARY.md`,
  `docs/index.md`) published to GitHub Pages by `.github/workflows/docs.yml`
  on every merge touching `docs/`.
- Background jobs (`cmd/disposition-job`, `cmd/integrity-job`, `cmd/orphan-sweeper`, `cmd/parquet-compactor`): ticker runner with immediate first run, Postgres advisory-lock leadership, tenant-hash sharding, Prometheus metrics, Compose `jobs` profile.
- Migration `000005_parquet_files` (`parquet_files` table + artifact row pointers).

### Changed

- `CONTENT_GRPC_PORT` (default 50051), `CONTENT_S3_ACCESS_KEY` / `CONTENT_S3_SECRET_KEY` configuration.
- New `CONTENT_JOBS_*` (intervals, batch size, shard index/count) and `CONTENT_PARQUET_TARGET_ROWS` / `CONTENT_PARQUET_MAX_FILES` configuration.
- `/metrics` serves real Prometheus counters, histograms, and gauges.
- Go directive stays `go 1.22`; new dependencies pinned to go1.22-compatible versions (minio-go v7.0.77, grpc v1.67.3, protobuf v1.36.5, parquet-go v0.25.0).

### Fixed

- `BlobKey` took the pre-colon half of the digest, collapsing every tenant's objects onto one S3 key; keys are now `<tenant>/<digest-hex>`.
- Single-row Parquet files stored `parquet_row_idx=1` for 0-indexed rows; fresh files now record row 0.
- Concurrent duplicate puts no longer fail with foreign-key violations: insert losers roll back and adopt the winner's reference (`dedupHit=true`), retrying whole-heartedly if disposition raced the put.
- Review response: `docs/reviews/2026-10-14-external-review-response.md` maps the external review's P0/P1 findings to fixed/tested/planned with file and test pointers.

## [0.1.0] - 2026-10-13

First milestone. Pre-alpha: API unstable, deployable locally, integration-tested.

### Added

- HTTP service (`cmd/content-api`): `POST /api/v1/artifacts`, `GET /api/v1/artifacts/{id}`, `HEAD /api/v1/artifacts/{id}`, `GET /api/v1/capabilities`, `/healthz`, `/readyz`.
- PostgreSQL adapter (`internal/adapter/postgres`): put/get with content-addressable dedup by `(tenant_id, digest)`, tenant-scoped reads, reference registry, retention and legal-hold operations with audit trail.
- Content reference format v1 (`pkg/reference`): URI-encoded `tenant_id`, `artifact_id` (UUID), `digest`, `generation`, `serializer_version`; 512-character bound.
- SHA-256 digest helpers (`pkg/digest`): `sha256:<hex>` compute and verify.
- Migrations 000001–000004 (artifacts, retention policies, reference registry, audit log), plus migrate-on-startup.
- Testcontainers integration suite (`test/integration`, `integration` build tag): put/get round trip, same-tenant dedup, cross-tenant isolation, legal-hold audit, reference register/release, retention update, tier routing, migration presence.
- Docker Compose stack (Postgres + MinIO + service), multi-stage Dockerfile, `VERSION` source of truth.
- Docs: `docs/design.md`, `docs/implementation-plan.md`, `docs/api.md`, `docs/runbook.md`, `docs/schema.sql`, `docs/architecture.md`, `docs/IMPLEMENTATION.md`, `CONTRIBUTING.md`.

### Fixed

- `reference_registry` foreign key now targets a real `UNIQUE` constraint on `artifacts(tenant_id, digest)` instead of a bare unique index.
- `artifacts` carries an inline `payload BYTEA` so Phase 1 round trips work before the S3 backing lands.

### Known limitations

- Payload bytes live in Postgres for every tier; the tier label is recorded but S3/MinIO backing is not yet wired.
- No gRPC surface yet (HTTP only); `Head` returns existence only, not full metadata.
- No background jobs yet (disposition, integrity sweep, orphan sweeper, Parquet compactor).
