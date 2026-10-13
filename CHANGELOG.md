# Changelog

Keep-a-Changelog (Added / Changed / Deprecated / Removed / Fixed / Security) per version. Normative behavior lives in the approved integration proposal v3 — this file records what changed, the proposal records why.

## [Unreleased]

### Added

- S3/MinIO backing for `parquet` and `s3` tiers (`BlobStore` port + minio adapter, content-addressed keys `<tenant>/<digest-hex>`, bucket auto-creation on startup). Postgres keeps inline bytes; object-tier rows store `payload NULL` and remain readable alongside legacy inline rows.
- gRPC surface (`content.v1.ContentService`: Put/Get/Head/Capabilities, server reflection) on `:50051` alongside the REST API; `proto/content/v1/content.proto` is the contract, `gen/` holds buf-generated stubs.
- `make proto` regenerates stubs (buf + protoc-gen-go + protoc-gen-go-grpc required).

### Changed

- `CONTENT_GRPC_PORT` (default 50051), `CONTENT_S3_ACCESS_KEY` / `CONTENT_S3_SECRET_KEY` configuration.
- Go directive stays `go 1.22`; new dependencies pinned to go1.22-compatible versions (minio-go v7.0.77, grpc v1.67.3, protobuf v1.36.5).

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
