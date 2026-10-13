---
name: 210-go-testcontainers-postgres
description: Use when you need to set up, review, or debug Go integration tests with testcontainers-go and Postgres - including pinning compatible dependency versions, writing the TestMain container bootstrap, applying migrations, isolating tests per tenant, diagnosing container-creation hangs, or fixing go mod tidy pruning of integration-tagged dependencies.
metadata:
  author: synanton-platform
  version: 1.0.0
---
# Go Testcontainers Postgres Guidelines

Set up robust integration tests against containerized Postgres using testcontainers-go, following the equalix-go proven pattern.

**Core areas:** pinned dependency set, `TestMain` bootstrap with `postgres.Run` + `BasicWaitStrategies`, migration application from `migrations/*.up.sql`, per-test tenant isolation (not schema churn), `integration` build tag, `make test-integration` with the Ryuk override, and the known failure modes below.

**Prerequisites:** Run `go build ./...` before applying any change. If compilation fails, **stop immediately** and do not proceed.

**Before applying changes:** Read the reference for version pins, good/bad patterns, and hard-won failure modes.

## Reference

For versions, patterns, and constraints, see [references/210-go-testcontainers-postgres.md](references/210-go-testcontainers-postgres.md).
