# Runbook — operating content-service

For the operator who has never seen the project. Every claim here is either code-pinned (file cited) or observed-and-dated.

## 1. Startup

```sh
# Env-only (no config file needed):
CONTENT_DB_URL='postgres://content:content@HOST:5432/content?sslmode=disable' \
CONTENT_API_KEY=secret \
CONTENT_MIGRATE_ON_STARTUP=true \
./content-service
```

Or with Compose (recommended for local development):

```sh
make docker-up   # generates ./certs/ dev certificates, then builds + starts
```

Compose details:

- Ports are env-overridable so stacked environments don't collide:
  `PG_PORT` (default 5432), `MINIO_PORT` (9000),
  `MINIO_CONSOLE_PORT` (9001), `APP_PORT` (8080).
- Data persists in named volumes `content-postgres-data` and
  `content-minio-data` (explicit `name:`, so state survives
  `docker compose down` and compose-project renames). Drop with
  `docker compose down -v`.
- Certificates bind-mount from `./certs/` read-only (`make certs`
  generates dev-only self-signed material; the directory is
  gitignored). Never commit real certificates.

Startup order (fail-fast — any step failing exits 1 before listeners bind):

1. Config resolve (env first, `configs/local.yaml` as reference; `internal/config/config.go`).
2. Optional migrate (`CONTENT_MIGRATE_ON_STARTUP=true`): applies `migrations/*.up.sql` in lexical order via `internal/adapter/postgres.Migrate`, then continues. See §7.
3. Readiness probes (DB ping, S3 bucket ensure) — must pass before bind.
4. Listener bind (HTTP `CONTENT_SERVER_HOST`/`CONTENT_SERVER_PORT`, default `0.0.0.0:8080`; gRPC `CONTENT_GRPC_PORT`, default `50051`) → serving.

## 2. Shutdown

SIGTERM (or SIGINT) → in-flight HTTP drains within 30s → exit 0. `/healthz` stays 200 throughout drain; `/readyz` flips 503 when Postgres is unreachable. Startup failure (bad config, failed ping, failed migration) exits 1 with the cause on stderr.

## 3. Config reference

| Env | Default | Notes |
|---|---|---|
| `CONTENT_DB_URL` | `postgres://localhost:5432/content?sslmode=disable` | Postgres DSN; must reach the metadata database |
| `CONTENT_SERVER_HOST` | `0.0.0.0` | HTTP listen host |
| `CONTENT_SERVER_PORT` | `8080` | HTTP listen port |
| `CONTENT_GRPC_PORT` | `50051` | gRPC listen port (reflection enabled) |
| `CONTENT_S3_ACCESS_KEY` / `CONTENT_S3_SECRET_KEY` | `minioadmin` / `minioadmin` | Object storage credentials (dev defaults; production mounts from the secret store) |
| `CONTENT_API_KEY` | — (empty) | Required by Compose; reserved for gateway auth (not yet enforced) |
| `CONTENT_MIGRATE_ON_STARTUP` | `false` | Opt-in; explicit false vetoes migration |
| `CONTENT_MIGRATIONS_DIR` | `migrations` | Override for inspect-first operators |
| `CONTENT_INLINE_THRESHOLD_BYTES` | `4096` | Below this: `inline` tier label |
| `CONTENT_OBJECT_THRESHOLD_BYTES` | `262144` | Above this: `s3` tier label; between: `parquet` |
| `CONTENT_RETENTION_DEFAULT_DAYS` | `90` | Applied when the put omits `retention_days` |
| `CONTENT_VERIFY_ON_PUBLISH` | `true` | Digest verified at publication time |
| `CONTENT_VERIFY_ON_READ` | `false` | Full re-hash on read (expensive; replay/audit only) |
| `CONTENT_ORPHAN_GRACE_PERIOD` | `24h` | Minimum age before an unreferenced object is a candidate |
| `CONTENT_AUDIT_ENABLED` | `true` | Legal-hold and retention changes audited |

## 4. Health signals

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | Liveness: 200 while serving |
| `GET /readyz` | Readiness: 200 when Postgres pings, 503 otherwise |
| `GET /api/v1/capabilities` | Conformance level (`CORE`) and supported tiers |

## 5. Failure modes

| Failure | Symptom | Response |
|---|---|---|
| Postgres down | `/readyz` 503, puts/gets 500 | Restore Postgres; no data loss (writes fail fast, nothing half-persisted) |
| MinIO/S3 down | No Phase 1 impact (bytes stay in Postgres) | Restore MinIO before enabling the S3 tier |
| Digest mismatch on read | `409 INTEGRITY_MISMATCH` | Quarantine artifact, alert; do not serve bytes |
| Cross-tenant probe | `404 NOT_FOUND` (indistinguishable) | Expected; alert only on volume anomaly |
| Legal hold in place | Disposition skipped for held artifacts | Release requires `content.admin.legal_hold` role + reason; audited |

## 6. Retention and legal hold

Precedence: `legal_hold > tenant_retention > owner_retention`. Erasure-vs-hold conflicts are resolved outside the service by the administrative workflow; the service executes the resolved directive it receives.

## 7. Background jobs

Run under the Compose `jobs` profile (`docker compose --profile jobs up -d`); plain `up` stays API-only. Jobs never run migrations — the API service owns the schema.

| Job | Interval env (default) | Scaling |
|---|---|---|
| disposition | `CONTENT_JOBS_DISPOSITION_INTERVAL` (1h) | Single leader (advisory lock `0xC0DE0001`) |
| integrity | `CONTENT_JOBS_INTEGRITY_INTERVAL` (24h) | Tenant shards (`CONTENT_JOBS_SHARD_INDEX`/`COUNT`) |
| orphan sweeper | `CONTENT_JOBS_ORPHAN_INTERVAL` (1h) | Single leader (advisory lock `0xC0DE0002`) |
| parquet compactor | `CONTENT_JOBS_COMPACTOR_INTERVAL` (6h) | Inventory only |

- `CONTENT_JOBS_BATCH_SIZE` (default 1000) bounds every sweep query.
- `CONTENT_SAMPLE_RATE` (default 0.01) samples integrity verification per run.
- Lost leadership logs `peer leads, skipping tick` and retries next interval — never an error, never dual execution.
- Disposition deletes the database row first, then the blob; a crash between the two leaves an orphan blob the sweeper heals. Metadata rows whose bytes are missing are audited, never auto-deleted.
- The sweeper honors `CONTENT_ORPHAN_DRY_RUN=true` (log candidates, delete nothing).

`markForDeletion` releases one owner's reference row (`released_at = now()`). Physical deletion additionally requires ref count zero, no legal hold, and grace-period expiry (background jobs land in Phase 5).

## 7. Migrations

Files: `migrations/000001_create_artifacts.{up,down}.sql` through `000004_create_audit_log.*`. Applied lexically. The service applies `.up.sql` files only; rollbacks are manual via `.down.sql` in reverse order. `000001` carries the `payload BYTEA` column and the `uq_artifacts_tenant_digest` unique constraint that `reference_registry` foreign-keys against — do not split them.

## 8. Observability

Structured logs via the standard library `log` package today. Prometheus wiring lands in Phase 5; the `/metrics` endpoint currently serves a placeholder. Per-request fields to expect once wired: `tenant_id`, tier, latency histograms for put/get, integrity-failure counters, orphan-candidate gauges.
