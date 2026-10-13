# Architecture

Hexagonal (ports and adapters):

```text
┌─────────────────────────────────────────────────┐
│               cmd/content-api                   │
│            (wiring + HTTP/gRPC)                 │
└───────────────────────┬─────────────────────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
┌────────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
│  REST + gRPC  │ │  Metrics    │ │  Health    │
│  adapters     │ │ (Prometheus)│ │ (/readyz)  │
└────────┬──────┘ └─────┬──────┘ └─────┬──────┘
         │               │               │
         └───────────────┼───────────────┘
                         │
                 ┌───────▼────────┐
                 │  ContentStore  │
                 │  port          │
                 └───────┬────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
┌────────▼──────┐ ┌──────▼──────┐ ┌──────▼──────┐
│  PostgreSQL   │ │  S3/MinIO   │ │  Parquet    │
│  (pgx)        │ │ (minio-go)  │ │ (parquet-go)│
└───────────────┘ └────────────┘ └─────────────┘
```

The domain package is pure Go: artifact types and sentinel errors only
(`internal/domain`). The port (`internal/port.ContentStore`) is the
sole seam the HTTP/gRPC adapters and the background jobs program against.
The Postgres adapter (`internal/adapter/postgres`) owns dedup by
`(tenant_id, digest)`, tenant-scoped reads, the reference registry,
retention/legal-hold transitions, and the audit trail.

Inline payloads stay in Postgres; `parquet` and `s3` tiers live in
object storage behind the same port — single-row Parquet files at
write time, merged by the compactor, read back by row index. See
`docs/IMPLEMENTATION.md` for the phase order.
