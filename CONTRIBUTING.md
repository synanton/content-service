# Contributing to content-service

This document explains how to set up, develop, test, and submit changes to `content-service`. It also defines the two acceptance gates (training and production) that every contribution should be aware of.

Design references: Synanton Design 1.26 (Content Cache contract), the approved Content Cache / Commitix integration proposal v3 (platform `docs/architecture/proposals/Content-service/`), and the local `docs/design.md`.

---

## Prerequisites

- **Go 1.22+**
- **Docker** and **Docker Compose** (for integration tests)
- **PostgreSQL 16** and **MinIO** (provided by Docker Compose)
- **golangci-lint** (install via `make tools`)
- **migrate** CLI (only for manual migration runs; the service migrates on startup when `CONTENT_MIGRATE_ON_STARTUP=true`)

---

## Setup

```bash
git clone https://github.com/synanton/content-service.git
cd content-service

# Start local dependencies (Postgres + MinIO + service).
# make docker-up also generates dev-only mTLS certs into ./certs/ (gitignored).
make docker-up

# Run unit tests
make test

# Run integration tests (needs Docker; pulls postgres:16-alpine and minio/minio on first run)
make test-integration

# Regenerate gRPC stubs after editing proto/content/v1/*.proto
# (needs buf + protoc-gen-go + protoc-gen-go-grpc on PATH)
make proto
```

> **Do not run plain `go mod tidy` blindly.** Under go 1.22, tidy prunes the
> dependencies of `integration`-tagged test files (e.g. testcontainers) from
> `go.mod`, silently breaking `make test-integration` for the next person.
> Add adapter dependencies with explicit `go get module@version` instead.

> **Tooling note:** if `gh pr edit` fails silently in this environment
> (GraphQL blocked), update PR metadata via the REST API instead, e.g.
> `gh api repos/synanton/content-service/pulls/<n> -X PATCH -f body="..."`.

---

## Repository Layout

```text
cmd/content-api/        # HTTP + gRPC server entrypoint
cmd/disposition-job/    # expired-artifact disposition
cmd/integrity-job/      # digest re-verification sweeps
cmd/orphan-sweeper/     # metadata/object reconciliation
cmd/parquet-compactor/  # small-file merges
internal/
  domain/               # pure domain types + sentinel errors (no I/O)
  port/                 # interfaces (ContentStore, BlobStore ports)
  adapter/
    postgres/           # pgx adapter (metadata, lifecycle queries)
    http/               # net/http REST adapter
    grpc/               # gRPC service implementation
    s3/                 # minio-go object adapter (incl. ranged reads)
    parquet/            # Parquet file encode + ranged row reads
  config/               # environment-first configuration loading
  security/             # tenant context + audit log writer
  jobs/                 # runner, leadership, sweeps (disposition/integrity/sweeper/compactor)
  jobsboot/             # job-process wiring (config + pool + blobs)
  conformance/          # Design 1.26 suite + in-memory fake
  observability/        # Prometheus metrics
pkg/
  digest/               # SHA-256 helpers (sha256:<hex>)
  reference/            # v1 ContentReference encode/parse
proto/content/v1/       # gRPC contract (buf codegen to gen/)
gen/content/v1/         # generated protobuf/gRPC stubs (committed; see make proto)
docs/                   # architecture, API, runbook, implementation plan
configs/                # example YAML configs (local.yaml)
certs/                  # dev-only mTLS material (gitignored; see make certs)
migrations/             # PostgreSQL migrations (*.up.sql / *.down.sql)
test/
  integration/          # testcontainers-based tests (build tag: integration)
tests/
  conformance/          # Design 1.26 conformance checks
```

**Rule:** `internal/domain/` must not import any package that performs I/O. If you find yourself reaching for `pgx`, `net/http`, or S3 SDKs inside domain, define a port instead.

---

## Development Workflow

1. Pick a task from the current phase in `docs/IMPLEMENTATION.md`.
2. Write the spec first. If the behavior is not already documented in `docs/design.md` or the approved proposal, add it before implementing.
3. Write tests first for domain logic (table-driven) or integration tests for adapters.
4. Implement.
5. Run `make test vet` (and `go test -race ./...`) before committing.
6. Open a PR with a clear description linking the proposal section. Merging is manual — a maintainer merges after review.
7. Freshness marker: the italic line under README Status names the latest phase-landing merge and its date. Bump it on phase transitions, not every merge.

---

## Branch Naming

```text
CNT-<short-scope>        e.g. CNT-docs-foundation
CNT-<phase>-<topic>      e.g. CNT-adapter-postgres
```

Do not push directly to `main`.

---

## Commit Conventions

Use conventional commits:

```text
feat(postgres): add put/get with dedup by (tenant_id, digest)
fix(http): return indistinguishable NOT_FOUND across tenants
test(integration): add legal-hold audit coverage
docs(runbook): document migrate-on-startup
chore: initialize module and repo skeleton
```

Commit from status-verified-clean trees: run `git status` before staging and stage explicit paths.

---

## Testing Requirements

| Layer        | Tool                          | Required Coverage        |
| ------------ | ----------------------------- | ------------------------ |
| Domain core  | `go test`, table-driven       | 90%+                     |
| Adapters     | `testcontainers-go`           | 80%+                     |
| Conformance  | Custom harness                | All Core+Extended checks (14) |
| Security     | Negative tests                | Cross-tenant, auth bypass|
| Load         | `k6` or `vegeta`              | Target throughput        |

Every PR that changes domain logic must include unit tests. Every PR that changes an adapter must include integration tests. Every PR that changes lifecycle behavior must update the conformance suite.

---

## Code Review Checklist

- [ ] Does the change match `docs/design.md` and the approved proposal v3?
- [ ] Is the domain core free of I/O dependencies?
- [ ] Are new ports defined as interfaces, not concrete types?
- [ ] Are errors wrapped with context (`fmt.Errorf("...: %w", err)`)?
- [ ] Are tenant checks present on every read, write, and metadata path?
- [ ] Are cross-tenant failures indistinguishable (`NOT_FOUND`, no timing leak)?
- [ ] Are legal-hold changes audited with actor, reason, and before/after state?
- [ ] Is the race detector clean?
- [ ] Are new metrics registered and documented?
- [ ] Is the change reflected in `CHANGELOG.md` if user-visible?

---

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
