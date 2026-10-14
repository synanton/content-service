# 210-go-testcontainers-postgres — Reference

Findings recorded 2026-10-13 while bringing the content-service integration suite green. Reuse these pins and patterns verbatim before improvising.

## Pinned dependency set (proven combo)

These exact versions work together in this environment (Docker 29.x). Do not float any of them without re-running the full suite:

```text
github.com/jackc/pgx/v5 v5.7.0
github.com/testcontainers/testcontainers-go v0.33.0
github.com/testcontainers/testcontainers-go/modules/postgres v0.33.0
github.com/docker/docker v27.1.1+incompatible
github.com/docker/go-connections v0.5.0
github.com/moby/patternmatcher v0.6.0
github.com/moby/sys/sequential v0.5.0
github.com/moby/sys/user v0.1.0
github.com/moby/term v0.5.0
```

Pin with explicit `go get module@version`, never by editing `go.mod` by hand:

```bash
go get github.com/jackc/pgx/v5@v5.7.0 \
  github.com/testcontainers/testcontainers-go/modules/postgres@v0.33.0 \
  github.com/testcontainers/testcontainers-go@v0.33.0 \
  github.com/docker/go-connections@v0.5.0
```

## Known failure modes

### testcontainers-go v0.44.0 hangs at container creation

**Symptom:** test binary prints `🐳 Creating container for image postgres:16-alpine` and never progresses — no container appears in `docker ps`, no error, killed after 10 minutes. Occurs with core v0.44.0 against Docker 29.8.2 in this environment.

**Fix:** pin core + postgres module to v0.33.0 (the equalix-go proven combo). With v0.33.0 the same suite starts Ryuk + Postgres and runs 12 tests in ~3s.

**Rule:** when container creation hangs with zero output, suspect the testcontainers version first, not Docker (verify Docker separately with `docker run --rm postgres:16-alpine echo ok`).

### go-connections v0.6+ breaks the docker client build

**Symptom:** `client.go:453:19: undefined: sockets.DialPipe` when building the integration test package.

**Cause:** `sockets.DialPipe` was removed in `go-connections` v0.6.0, but `docker/docker v27.1.1` still references it.

**Fix:** pin `github.com/docker/go-connections v0.5.0` (plus the moby pins above).

### go mod tidy prunes integration dependencies

**Symptom:** `make test-integration` breaks for the next person after you ran `go mod tidy`.

**Cause:** under go 1.22, tidy drops deps only referenced from `integration`-tagged test files.

**Fix:** never run plain `go mod tidy`; use explicit `go get module@version`.

### Killing stray test processes kills your own shell

**Symptom:** `pkill -f "go test"` returns `Unknown: ChildProcess.kill` and the session dies.

**Cause:** `-f` matches full command lines, including the shell running the pkill itself.

**Fix:** use the bracket trick so the pattern cannot match the killer: `pkill -f "integration[.]test"`. Prefer `kill <pid>` from an explicit `ps` listing.

### Tool timeouts hide test output

Foreground `go test` streams output only on completion; a hung suite shows nothing until the tool kills it. For long runs, detach with `setsid` (new session survives the tool's process-group kill), redirect to a file, and poll:

```bash
setsid bash -c 'go test -race -tags=integration ./test/integration/... -v > /tmp/itest.log 2>&1' < /dev/null > /dev/null 2>&1 &
# then poll: tail /tmp/itest.log
```

## TestMain bootstrap pattern

```go
//go:build integration

package integration

var (
    ctx   = context.Background()
    pool  *pgxpool.Pool
    store *adapterpg.Store
)

func TestMain(m *testing.M) {
    var err error
    pool, cfg, err = startPostgres(ctx)  // postgres.Run + BasicWaitStrategies
    if err != nil {
        fmt.Fprintln(os.Stderr, "integration setup:", err)
        os.Exit(1)
    }
    store = adapterpg.NewStore(pool, cfg)
    os.Exit(m.Run())
}
```

- Import the testcontainers postgres module under an alias (`pgtest`) — the package name `postgres` collides with the adapter package.
- Apply migrations by reading `migrations/*.up.sql` in lexical order and executing each via `pgxpool` (same approach as equalix-go `applyMigration`).
- Test isolation comes from **per-test tenant ids** (random suffix), not schema churn or per-test databases.
- Cross-tenant assertions must expect the indistinguishable error (`ErrNotFound`), never a distinct code.

## Makefile wiring

```make
export TESTCONTAINERS_RYUK_CONTAINER_IMAGE ?= testcontainers/ryuk:0.12.0

test-integration:
	TESTCONTAINERS_RYUK_CONTAINER_IMAGE="$(TESTCONTAINERS_RYUK_CONTAINER_IMAGE)" \
	$(GO) test -race -tags=integration ./test/integration/... -v
```

Both `ryuk:0.8.1` (v0.33.0 default) and `ryuk:0.12.0` (override) are cached locally; the override matches equalix-go and avoids reaper-image pull surprises.

Pin every container image used in benchmarks by digest, not tag
(`minio/minio@sha256:14cea...`, never `:latest`): historical benchmark
comparisons silently drift otherwise. Record the pinned digest in
`docs/benchmarks.md` provenance alongside the run.

## Benchmark methodology (observed 2026-10-14, keep in mind when reading logs)

- In this toolchain, `go test -bench` with count-form `-benchtime=Nx`
  executes each benchmark **twice**: an N=1 warmup run followed by the
  N=N measured run (verified: single-benchmark runs show one `ops=`
  line at N=1x and two at N=2x; summaries reflect the measured run).
  Do not mistake warmup output for duplicate execution bugs.
- All benchmarks in one process share one database. Sweep benchmarks
  (compactor, disposition, integrity) therefore also process rows seeded
  by earlier benchmarks — realistic steady-state load, but account for
  it when attributing per-tenant merge counts in logs.
- Give every benchmark its own tenant (atomic counter) and every put
  operation unique bytes (salted), except the dedicated dedup benchmark.
  Global sweeps then stay correct by construction.
- Report percentiles from per-operation timings (`p50-ms`/`p95-ms`/
  `p99-ms` via `ReportMetric`), not from `ns/op` averages — especially
  for sweeps, where one invocation covers the whole table.
- Never gate PRs on absolute benchmark numbers; shared runners are too
  noisy (inline-get p50 measured 48µs and 330µs on the same host).
  Record results with hardware provenance in `docs/benchmarks.md`.

## Cross-repo note (platform/Java)

The same suite shape applies to `java:content-cache-postgres` (`PostgresTestBase`: one `postgres:16-alpine` container per JVM, `ensureSchema`, least-privilege `app` role, visible-skip). Two Java-side findings worth reusing:

- pgjdbc `setObject` with `java.time.Instant` fails (`Can't infer the SQL type`); bind `OffsetDateTime` instead.
- `GRANT ALL ON TABLES` does not cover sequences; the `app` role additionally needs `GRANT USAGE, SELECT ON ALL SEQUENCES` (plus default privileges) or `audit_log_id_seq` inserts fail.
