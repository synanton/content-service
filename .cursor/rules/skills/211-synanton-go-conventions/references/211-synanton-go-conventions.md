# 211-synanton-go-conventions — Reference

Conventions extracted from equalix-go and applied to content-service (2026-10-13). New Go services in the family should copy this skeleton verbatim, then fill in domain specifics.

## Docs skeleton (every service)

| File | Purpose | Non-negotiables |
|---|---|---|
| `README.md` | Identity + entry point | Badges (go, license, status); `Status` section with freshness line; maturity table (see below); What It Is; architecture diagram; quick start; API table; config reference; observability; development; roadmap |
| `CONTRIBUTING.md` | Contribution bar | Prerequisites; setup; repo layout with the I/O rule; workflow; branch naming; commit conventions; testing-requirements table; training/production gates; review checklist |
| `docs/IMPLEMENTATION.md` | What to build next | Guiding principles; phases with tasks + definitions of done; "What to Do Today"; per-phase done criteria |
| `docs/design.md` | Normative behavior | Tiers, reference format, retention, API, security, config |
| `docs/api.md` | REST contract | Conventions (auth, tenant scope, error shape) + per-endpoint request/response + curl cookbook |
| `docs/runbook.md` | Operator guide | Startup order, shutdown, config table, health signals, failure modes, retention/legal-hold ops, migrations, observability. Every claim code-pinned or observed-and-dated; guidance without provenance is a guess |
| `docs/schema.sql` | Consolidated DDL | Mirrors `migrations/*.up.sql` lexical order; regenerate after any migration change |
| `docs/architecture.md` | Hexagonal diagram | Ports/adapters boxes; what is pure vs adapter; phase notes |
| `CHANGELOG.md` | Keep-a-Changelog | Added/Changed/Deprecated/Removed/Fixed/Security per version; spec records why, changelog records what |
| `VERSION` | Single source of truth | Every build path stamps from here (`-ldflags -X main.version=$(VERSION)`); missing file falls back to `dev`, never fails the build |

## Maturity table rules

The README maturity table is evidence-gated:

- `⬜` = not yet; nothing is claimed before its evidence exists.
- Deferred rows are "deferred by decision, not pending by oversight" — say so explicitly.
- The italic freshness line under Status names the latest phase-landing merge and its date. Bump on phase transitions (merged epic scope or gate), **not every merge**.

## Testing-requirements table (CONTRIBUTING)

| Layer | Tool | Required Coverage |
|---|---|---|
| Domain core | `go test`, table-driven | 90%+ |
| Adapters | `testcontainers-go` | 80%+ |
| Conformance | Custom harness | All Core invariants |
| Security | Negative tests | Cross-tenant, auth bypass |
| Load | `k6`/`vegeta` | Target throughput |

Every PR changing domain logic includes unit tests; every adapter PR includes integration tests; every lifecycle-behavior PR updates conformance.

## Commits, branches, PRs

- Conventional commits: `feat(domain): ...`, `fix(postgres): ...`, `test(integration): ...`, `docs(runbook): ...`, `chore: ...`.
- Branch prefixes: `CNT-` (content-service), `EQLX-` (equalix-go). Never push directly to `main`.
- Stage explicit paths after `git status`; never blind `git add -A` on an unverified tree (five near-misses of unrelated files riding into docs commits is why this rule exists — equalix-go CONTRIBUTING).
- Merging is manual — a maintainer merges after review. Do not merge your own PR without approval.
- Keep comments concise; never use comments for chain-of-thought. Doc comments on exported identifiers only.

## Review checklist (minimum)

- Matches the spec (`docs/design.md` + normative proposal)?
- Domain free of I/O dependencies; new ports are interfaces?
- Errors wrapped with context; tenant checks on every path?
- Cross-tenant failures indistinguishable?
- Legal-hold/retention changes audited with actor + reason?
- Race detector clean; new metrics registered and documented?
- CHANGELOG updated if user-visible?
