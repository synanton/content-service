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
| `CONTENT_DB_MAX_CONNECTIONS` | `20` | Postgres pool size |
| `CONTENT_S3_ENDPOINT` / `CONTENT_S3_BUCKET` / `CONTENT_S3_REGION` | `http://localhost:9000` / `synanton-content` / `us-east-1` | Object storage location (bucket auto-created on startup) |
| `CONTENT_S3_USE_PATH_STYLE` | `true` | Path-style bucket addressing (required for MinIO) |
| `CONTENT_SERVER_HOST` | `0.0.0.0` | HTTP listen host |
| `CONTENT_SERVER_PORT` | `8080` | HTTP listen port |
| `CONTENT_GRPC_PORT` | `50051` | gRPC listen port (reflection enabled) |
| `CONTENT_S3_ACCESS_KEY` / `CONTENT_S3_SECRET_KEY` | `minioadmin` / `minioadmin` | Object storage credentials (dev defaults; production mounts from the secret store) |
| `CONTENT_PARQUET_TARGET_ROWS` | `1000` | Files below this row count are merge candidates |
| `CONTENT_PARQUET_MAX_FILES` | `32` | Max files merged per compactor run per tenant |
| `CONTENT_API_KEY` | — (empty) | Required by Compose; reserved for gateway auth (not yet enforced) |
| `CONTENT_MIGRATE_ON_STARTUP` | `false` | Opt-in; explicit false vetoes migration |
| `CONTENT_MIGRATIONS_DIR` | `migrations` | Override for inspect-first operators |
| `CONTENT_INLINE_THRESHOLD_BYTES` | `4096` | Below this: `inline` tier label |
| `CONTENT_OBJECT_THRESHOLD_BYTES` | `262144` | Above this: `s3` tier label; between: `parquet` |
| `CONTENT_PARQUET_COMPRESSION` / `CONTENT_PARQUET_BATCH_SIZE` | `zstd` / `1000` | Parquet file codec; legacy batch-size knob (single-row writes today) |
| `CONTENT_RETENTION_DEFAULT_DAYS` | `90` | Applied when the put omits `retention_days` |
| `CONTENT_VERIFY_ON_PUBLISH` | `true` | Digest verified at publication time |
| `CONTENT_VERIFY_ON_READ` | `false` | Full re-hash on read (expensive; replay/audit only) |
| `CONTENT_ORPHAN_GRACE_PERIOD` | `24h` | Minimum age before an unreferenced object is a candidate |
| `CONTENT_ORPHAN_SWEEP_INTERVAL` / `CONTENT_ORPHAN_DRY_RUN` | `1h` / `false` | Sweeper cadence; dry-run logs candidates without deleting |
| `CONTENT_KMS_KEY_ID` | — (empty) | Optional envelope encryption key (reserved) |
| `CONTENT_LOG_LEVEL` / `CONTENT_METRICS_ENABLED` / `CONTENT_TRACING_ENABLED` | `info` / `true` / `true` | Logging and observability toggles (`CONTENT_OTEL_ENDPOINT` for traces) |
| `CONTENT_AUDIT_ENABLED` | `true` | Legal-hold and retention changes audited |
| `CONTENT_S3_OP_TIMEOUT` / `CONTENT_S3_LIST_TIMEOUT` | `30s` / `2m` | Per-op and scan deadlines (retries included); slow backends fail closed |
| `CONTENT_S3_MAX_ATTEMPTS` / `CONTENT_S3_RETRY_BASE_DELAY` | `3` / `200ms` | Total tries; backoff doubles. 5xx/network only — 4xx, missing objects, validation never retry |
| `CONTENT_S3_BREAKER_THRESHOLD` / `CONTENT_S3_BREAKER_COOLDOWN` | `5` / `30s` | Consecutive backend failures to open; fast-fail window before a probe |

## 4. Health signals

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | Liveness: 200 while serving |
| `GET /readyz` | Readiness: 200 when Postgres pings, 503 otherwise |
| `GET /metrics` | Prometheus counters, histograms, gauges (see §8) |
| `GET /api/v1/capabilities` | Conformance level (`CORE`) and supported tiers |

## 5. Failure modes

| Failure | Symptom | Response |
|---|---|---|
| Postgres down | `/readyz` 503, puts/gets 500 | Restore Postgres; no data loss (writes fail fast, nothing half-persisted) |
| MinIO/S3 down | Inline puts/gets keep working; `parquet`/`s3` puts fail fast and their gets fail closed | Restore MinIO; no metadata corruption (failed puts leave no half-persisted rows) |
| MinIO/S3 slow (past `CONTENT_S3_OP_TIMEOUT`) | `503 UPSTREAM_TIMEOUT`, `event=s3.timeout` in logs; retries exhausted within the deadline | Check backend latency; timeouts bound total time including retries — do not raise the timeout past the revocation/UX budget without a benchmark |
| MinIO/S3 repeatedly failing (≥ `CONTENT_S3_BREAKER_THRESHOLD`) | `503 UPSTREAM_DEGRADED`, `event=s3.circuit state=open`; no backend traffic until cooldown probe | Same as down; breaker resets on first successful probe (`event=s3.circuit state=closed`) |

Deadline mechanics (C-4): one overall bound per call covers all attempts including backoffs — retries never multiply the timeout. Backoff is deadline-aware (attempts that cannot fit their budget do not run, keeping attempt counts exact). The breaker is per store, shared across operations: a dead backend trips List and Get together. The minio SDK's own retry budget is neutralized (`MaxRetry = 1` at client construction) so the decorator is the single retry owner; without that, SDK retries would multiply decorator attempts. Retryable: 5xx (incl. 503 SlowDown), 429 under load, transport errors. Never retried: 4xx, missing objects, validation errors, caller cancellation (none of these trip the breaker either).

Audit scope (AC-2): every storage operation that fails on upstream availability — request surface (HTTP Put/Get/Head, gRPC Put/Get/Head) and job paths (sweeper, disposition, compactor, integrity verification) — emits exactly one artifact-attributed `event=audit` (`put/get/head_failed`, `sweep/disposition/compact/integrity_failed`; reason `UPSTREAM_TIMEOUT`/`UPSTREAM_DEGRADED` from the single `s3.AuditReason` taxonomy). Scan-level failures carry empty tenant. Audit is stream-side, never transactional: no storage rollback can erase it.

Integrity guarantee (AC-4), stated exactly: every byte-serving read recomputes SHA-256 over the fetched bytes and fails closed on mismatch (full deterministic verification on the read path — not sampling; one shared routine for all tiers, also serving the integrity job's verification reads). Head/Exists are metadata-only (`SELECT 1`, no bytes served) and therefore outside the guarantee by construction. The background sweep is sampled (default rate 0.01; exact miss-probability math in `docs/benchmarks.md` "Integrity coverage math") and must never be presented as coverage. There is no quarantine: a corrupt artifact stays listed and fails closed per-read until removed by disposition or operator action (`integrity_quarantine_not_implemented` documents the gap; implementing quarantine is out of scope for AC-4). Core's 14 do not inject corruption; the fail-closed path is pinned by CB-x (fake + real-backend tests).
| Digest mismatch on read | `409 INTEGRITY_MISMATCH` (HTTP) / `DataLoss` (gRPC); bytes never served | Alert; mismatch is recorded but no quarantine state exists (known limitation, see below) |
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
| parquet compactor | `CONTENT_JOBS_COMPACTOR_INTERVAL` (6h) | Merge sub-target files per tenant; crash windows heal via the sweeper |

- `CONTENT_JOBS_BATCH_SIZE` (default 1000) bounds every sweep query.
- `CONTENT_JOBS_SHARD_INDEX` / `CONTENT_JOBS_SHARD_COUNT` (default 0/1) shard integrity verification by tenant hash.
- `CONTENT_SAMPLE_RATE` (default 0.01) samples integrity verification per run.
- Lost leadership logs `peer leads, skipping tick` and retries next interval — never an error, never dual execution.
- Disposition deletes the database row first, then the blob; a crash between the two leaves an orphan blob the sweeper heals. Metadata rows whose bytes are missing are audited, never auto-deleted.
- The sweeper honors `CONTENT_ORPHAN_DRY_RUN=true` (log candidates, delete nothing).

`markForDeletion` releases one owner's reference row (`released_at = now()`). Physical deletion additionally requires ref count zero, no legal hold, and grace-period expiry, enforced by `disposition-job`. Parquet file rows left unreferenced are reaped in the same transaction; their objects are collected by the orphan sweeper.

## 8. Migrations

Files: `migrations/000001_create_artifacts.{up,down}.sql` through `000005_parquet_files.*`. Applied lexically. The service applies `.up.sql` files only; rollbacks are manual via `.down.sql` in reverse order. `000001` carries the `payload BYTEA` column and the `uq_artifacts_tenant_digest` unique constraint that `reference_registry` foreign-keys against — do not split them. `000005` adds the `parquet_files` table plus artifact row pointers (`parquet_file_id`, `parquet_row_idx`).

## 9. Observability

Structured JSON logs via `log/slog` to stdout (level from `CONTENT_LOG_LEVEL`); audit events carry `event=audit`. `/metrics` serves Prometheus counters, histograms, and gauges (put/get latency and errors by tenant and tier, integrity failures, disposition/orphan/parquet-compaction signals). Cardinality note: `tenant_id` labels are unbounded by construction — cap tenants per deployment or front the endpoint accordingly.
