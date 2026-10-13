# Architecture

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

The domain package is pure Go: artifact types and sentinel errors only
(`internal/domain`). The port (`internal/port.ContentStore`) is the
sole seam the HTTP adapter and the background jobs program against.
The Postgres adapter (`internal/adapter/postgres`) owns dedup by
`(tenant_id, digest)`, tenant-scoped reads, the reference registry,
retention/legal-hold transitions, and the audit trail.

Phase 1 stores payload bytes in Postgres for every tier and records
the tier label; the S3/MinIO backing for `s3` and `parquet` tiers
lands in a later phase without changing the port. See
`docs/IMPLEMENTATION.md` for the phase order.
