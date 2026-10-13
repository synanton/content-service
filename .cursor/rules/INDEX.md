# content-service AI Rules - Index

Rules and skills for AI coding assistants (Cursor, Claude Code) working on the Synanton Content Service (Go reference implementation of Content Cache 1.26).

## Top-level rules (auto-applied by glob)

| File | Glob | Description |
|------|------|-------------|
| [`go-rules.mdc`](./go-rules.mdc) | `**/*.go` | Go code generation: hexagonal arch, ports, pgx, testing conventions |
| [`docker-rules.mdc`](./docker-rules.mdc) | `**/docker-compose.yml` | Docker Compose: env ports, persistent volumes, dev certs, migrate-on-startup |

## Skills (invoke by name)

### Go Testing

| Skill | Description |
|-------|-------------|
| `@210-go-testcontainers-postgres` | Testcontainers Postgres setup: pinned versions, TestMain pattern, migration application, tenant-isolated tests |

### Synanton Project Conventions

| Skill | Description |
|-------|-------------|
| `@211-synanton-go-conventions` | Equalix-go-style docs, commit conventions, branch naming, PR flow, maturity tables |

## Project Context

- **Service:** Content Service — tenant-scoped, content-addressable storage (Content Cache 1.26)
- **Design doc:** Synanton Design 1.26 + approved integration proposal v3 (platform repo)
- **Local spec:** `docs/design.md`, `docs/IMPLEMENTATION.md`
- **Build system:** Go 1.22 + `make` (`build`, `test`, `test-integration`, `test-conformance`, `vet`, `bench`)
- **Language:** Go 1.22+
- **Key dependencies:** `pgx/v5` (Postgres), `testcontainers-go` (integration tests), MinIO (S3-compatible object storage)
- **Architecture:** Hexagonal (ports and adapters)
- **Module layout:** `cmd/`, `internal/` (domain, port, adapter, config, security), `pkg/` (digest, reference), `test/integration/`, `migrations/`
