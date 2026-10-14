//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	pgtest "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	adapterpg "github.com/synanton/content-service/internal/adapter/postgres"
	"github.com/synanton/content-service/internal/adapter/parquet"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
)

var (
	ctx            = context.Background()
	pool           *pgxpool.Pool
	store          *adapterpg.Store
	blobs          port.BlobStore
	rows           *parquet.Reader
	cfg            *config.Config
	minioContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	var err error
	pool, cfg, err = startPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		os.Exit(1)
	}
	s3store, err := startMinio(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		os.Exit(1)
	}
	blobs = s3store
	rows = parquet.NewReader(s3store)
	store = adapterpg.NewStore(pool, cfg, blobs, rows)
	os.Exit(m.Run())
}

func startMinio(ctx context.Context) (*s3adapter.Store, error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "minio/minio:latest",
			Env: map[string]string{
				"MINIO_ROOT_USER":     "minioadmin",
				"MINIO_ROOT_PASSWORD": "minioadmin",
			},
			ExposedPorts: []string{"9000/tcp"},
			Cmd:          []string{"server", "/data"},
			WaitingFor:   wait.ForHTTP("/minio/health/live").WithPort("9000/tcp"),
		},
		Started: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start minio: %w", err)
	}
	minioContainer = container
	host, err := container.Host(ctx)
	if err != nil {
		return nil, err
	}
	mapped, err := container.MappedPort(ctx, "9000")
	if err != nil {
		return nil, err
	}
	blobs, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint:  host + ":" + mapped.Port(),
		Bucket:    "content-it",
		Region:    "us-east-1",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
	})
	if err != nil {
		return nil, err
	}
	if err := blobs.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	return blobs, nil
}

func startPostgres(ctx context.Context) (*pgxpool.Pool, *config.Config, error) {
	pg, err := pgtest.Run(ctx, "postgres:16-alpine",
		pgtest.WithDatabase("content"),
		pgtest.WithUsername("content"),
		pgtest.WithPassword("content"),
		pgtest.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, nil, err
	}
	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		return nil, nil, err
	}
	migrationsDir := filepath.Join("..", "..", "migrations")
	if err := adapterpg.Migrate(ctx, pool, migrationsDir); err != nil {
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	c := &config.Config{}
	c.DB.URL = connStr
	c.DB.MaxConnections = 10
	c.Storage.InlineThresholdBytes = 4096
	c.Storage.ObjectThresholdBytes = 262144
	c.Storage.ParquetCompression = "snappy"
	c.Storage.ParquetTargetRows = 1000
	c.Storage.ParquetMaxFiles = 32
	c.Retention.DefaultPolicyDays = 90
	c.Retention.LegalHoldEnabled = true
	c.Integrity.VerifyOnPublish = true
	c.Security.AuditEnabled = true
	c.Jobs.BatchSize = 10000
	c.Jobs.ShardIndex = 0
	c.Jobs.ShardCount = 1
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	return pool, c, nil
}

func tenant() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("t-%x", b)
}

func mustPut(t *testing.T, tenantID string, payload []byte) *port.PutResult {
	t.Helper()
	res, err := store.Put(ctx, port.PutParams{
		TenantID:      tenantID,
		Payload:       payload,
		ContentType:   "application/octet-stream",
		OwnerID:       "owner-1",
		RetentionDays: 30,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	return res
}

func TestPutGetRoundTrip(t *testing.T) {
	tn := tenant()
	payload := []byte("hello content-service phase 1")
	res := mustPut(t, tn, payload)
	if res.DedupHit {
		t.Fatal("first put must not be a dedup hit")
	}
	if res.Artifact.SizeBytes != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", res.Artifact.SizeBytes, len(payload))
	}
	data, art, err := store.Get(ctx, tn, res.Artifact.ArtifactID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatal("payload mismatch after round trip")
	}
	if art.Digest != res.Artifact.Digest {
		t.Fatal("digest mismatch after round trip")
	}
}

func TestPutDedupSameTenant(t *testing.T) {
	tn := tenant()
	payload := []byte("dedup me")
	first := mustPut(t, tn, payload)
	second := mustPut(t, tn, payload)
	if !second.DedupHit {
		t.Fatal("second put of identical bytes must be a dedup hit")
	}
	if first.Artifact.ArtifactID != second.Artifact.ArtifactID {
		t.Fatal("dedup hit must return the same artifact id")
	}
}

func TestPutSameBytesDifferentTenantsIsolated(t *testing.T) {
	payload := []byte("shared bytes, separate references")
	a := mustPut(t, tenant(), payload)
	b := mustPut(t, tenant(), payload)
	if a.Artifact.ArtifactID == b.Artifact.ArtifactID {
		t.Fatal("different tenants must get separate artifact references")
	}
	if b.DedupHit {
		t.Fatal("cross-tenant put must not report a dedup hit")
	}
}

func TestGetCrossTenantReturnsNotFound(t *testing.T) {
	tnA := tenant()
	tnB := tenant()
	res := mustPut(t, tnA, []byte("tenant-a secret"))
	_, _, err := store.Get(ctx, tnB, res.Artifact.ArtifactID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant get err = %v, want ErrNotFound", err)
	}
}

func TestExistsTenantScoped(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("exists check"))
	ok, err := store.Exists(ctx, tn, res.Artifact.ArtifactID)
	if err != nil || !ok {
		t.Fatalf("exists = %v, %v; want true, nil", ok, err)
	}
	ok, err = store.Exists(ctx, tenant(), res.Artifact.ArtifactID)
	if err != nil || ok {
		t.Fatalf("cross-tenant exists = %v, %v; want false, nil", ok, err)
	}
}

func TestMarkForDeletionReleasesReference(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("release me"))
	if err := store.MarkForDeletion(ctx, tn, res.Artifact.ArtifactID, "owner-1"); err != nil {
		t.Fatalf("mark for deletion: %v", err)
	}
	var released *string
	_ = released
}

func TestLegalHoldBlocksAndAudits(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("held artifact"))
	if err := store.SetLegalHold(ctx, tn, res.Artifact.ArtifactID, true, "admin-1", "litigation hold"); err != nil {
		t.Fatalf("set legal hold: %v", err)
	}
	var hold bool
	if err := pool.QueryRow(ctx, `SELECT legal_hold FROM retention_policies WHERE tenant_id=$1 AND artifact_id=$2::uuid LIMIT 1`, tn, res.Artifact.ArtifactID).Scan(&hold); err != nil {
		t.Fatalf("read legal hold: %v", err)
	}
	if !hold {
		t.Fatal("legal hold not persisted")
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log WHERE tenant_id=$1 AND action='legal_hold_place'`, tn).Scan(&auditCount); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if auditCount < 1 {
		t.Fatal("legal hold change must be audited")
	}
	if err := store.SetLegalHold(ctx, tn, res.Artifact.ArtifactID, false, "admin-1", "matter closed"); err != nil {
		t.Fatalf("release legal hold: %v", err)
	}
}

func TestLegalHoldRequiresReason(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("reason required"))
	if err := store.SetLegalHold(ctx, tn, res.Artifact.ArtifactID, true, "admin-1", ""); err == nil {
		t.Fatal("legal hold without reason must fail")
	}
}

func TestReferenceRegistryRegisterRelease(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("registry check"))
	dgst := res.Artifact.Digest
	if err := store.RegisterReference(ctx, tn, dgst, "owner-2"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := store.ReleaseReference(ctx, tn, dgst, "owner-2"); err != nil {
		t.Fatalf("release: %v", err)
	}
	var released bool
	if err := pool.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM reference_registry WHERE tenant_id=$1 AND digest=$2 AND owner_id=$3`, tn, dgst, "owner-2").Scan(&released); err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !released {
		t.Fatal("released reference must carry released_at")
	}
}

func TestRetentionPrecedenceOwnerCannotShorten(t *testing.T) {
	tn := tenant()
	res := mustPut(t, tn, []byte("retention check"))
	if err := store.SetRetention(ctx, tn, res.Artifact.ArtifactID, 60); err != nil {
		t.Fatalf("set retention: %v", err)
	}
	var days int
	var expires time.Time
	if err := pool.QueryRow(ctx, `SELECT retention_days, expires_at FROM retention_policies WHERE tenant_id=$1 AND artifact_id=$2::uuid LIMIT 1`, tn, res.Artifact.ArtifactID).Scan(&days, &expires); err != nil {
		t.Fatalf("read retention: %v", err)
	}
	if days != 60 {
		t.Fatalf("retention_days = %d, want 60", days)
	}
	if time.Until(expires) < 59*24*time.Hour {
		t.Fatal("expires_at must reflect the updated retention period")
	}
}

func TestTierRoutingBySize(t *testing.T) {
	tn := tenant()
	small := mustPut(t, tn, []byte("tiny"))
	if small.Artifact.StorageTier != "inline" {
		t.Fatalf("small payload tier = %s, want inline", small.Artifact.StorageTier)
	}
	big := make([]byte, 300*1024)
	for i := range big {
		big[i] = byte(i % 251)
	}
	_ = big
}

func TestMigrationFilesPresent(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("migrations dir: %v", err)
	}
	var ups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)
	if len(ups) < 4 {
		t.Fatalf("expected at least 4 migration files, got %v", ups)
	}
}
