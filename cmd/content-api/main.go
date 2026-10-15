package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	grpchandler "github.com/synanton/content-service/internal/adapter/grpc"
	httphandler "github.com/synanton/content-service/internal/adapter/http"
	"github.com/synanton/content-service/internal/adapter/parquet"
	"github.com/synanton/content-service/internal/adapter/postgres"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/logging"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
	"github.com/synanton/security-provider/pkg/middleware"
)

var version = "dev"

// fatal logs and exits 1. It replaces log.Fatalf now that output is
// structured: slog has no fatal level, and the exit must be explicit.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		// No config, no level: report on stderr and exit.
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	logging.SetupDefault(cfg.Observability.LogLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		fatal("db connect failed", "err", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fatal("db ping failed", "err", err)
	}

	if cfg.Server.MigrateOnStartup {
		slog.Info("applying migrations", "dir", cfg.Server.MigrationsDir)
		if err := postgres.Migrate(ctx, pool, cfg.Server.MigrationsDir); err != nil {
			fatal("migrate failed", "err", err)
		}
		slog.Info("migrations applied")
	}

	blobs, rows, err := dialObjectStore(ctx, cfg)
	if err != nil {
		fatal("object storage failed", "err", err)
	}

	store := postgres.NewStore(pool, cfg, blobs, rows)
	h := httphandler.NewHandler(store)
	auditor := security.NewAuditor(cfg.Security.AuditEnabled)

	verifier, err := security.NewVerifier(cfg.Security.Auth)
	if err != nil {
		fatal("auth setup failed", "err", err)
	}
	requireAuth := func(next http.Handler) http.Handler {
		if verifier == nil {
			return next
		}
		return middleware.RequireAuth(verifier, middleware.WithAudit(func(ev middleware.AuditEvent) {
			if ev.Outcome == "denied" {
				auditor.Record(context.Background(), security.AuditEvent{
					Actor: "auth", Action: "auth.denied",
					TenantID: ev.TenantID, Reason: ev.ReasonCode,
				})
			}
		}))(next)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/artifacts", requireAuth(http.HandlerFunc(h.Put)))
	mux.Handle("GET /api/v1/artifacts/", requireAuth(http.HandlerFunc(h.Get)))
	mux.Handle("HEAD /api/v1/artifacts/", requireAuth(http.HandlerFunc(h.Head)))
	mux.HandleFunc("GET /api/v1/capabilities", h.Capabilities)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		readyCtx, readyCancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer readyCancel()
		if err := pool.Ping(readyCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("db unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("/metrics", promhttp.Handler())

	grpcLis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.GrpcPort))
	if err != nil {
		fatal("grpc listen failed", "err", err)
	}
	grpcOpts := []grpc.ServerOption{}
	if verifier != nil {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(middleware.UnaryServerInterceptor(verifier)))
		slog.Info("authentication enabled", "mode", "contexts validated, tenant derived from context")
	} else {
		slog.Warn("authentication disabled", "mode", "tenant scope is unauthenticated (dev only)")
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	contentv1.RegisterContentServiceServer(grpcSrv, grpchandler.NewServer(store))
	reflection.Register(grpcSrv)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	slog.Info("content-service listening", "version", version, "http", addr, "grpc", grpcLis.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		fatal("server failed", "err", err)
	case sig := <-sigCh:
		slog.Info("signal received, draining", "signal", sig.String())
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		grpcSrv.GracefulStop()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fatal("shutdown failed", "err", err)
		}
	}

	slog.Info("content-service stopped")
}

func dialObjectStore(
	ctx context.Context, cfg *config.Config,
) (port.BlobStore, *parquet.Reader, error) {
	endpoint := cfg.S3.Endpoint
	secure := strings.HasPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	store, err := s3adapter.NewStore(s3adapter.Config{
		Endpoint:  endpoint,
		Bucket:    cfg.S3.Bucket,
		Region:    cfg.S3.Region,
		AccessKey: cfg.S3.AccessKey,
		SecretKey: cfg.S3.SecretKey,
		UseSSL:    secure,
		PathStyle: cfg.S3.UsePathStyle,
	})
	if err != nil {
		return nil, nil, err
	}
	guarded, err := store.WithPolicy(s3adapter.Policy{
		OpTimeout:        cfg.S3.OpTimeout,
		ListTimeout:      cfg.S3.ListTimeout,
		MaxAttempts:      cfg.S3.MaxAttempts,
		RetryBaseDelay:   cfg.S3.RetryBaseDelay,
		BreakerThreshold: cfg.S3.BreakerThreshold,
		BreakerCooldown:  cfg.S3.BreakerCooldown,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := guarded.EnsureBucket(ctx); err != nil {
		return nil, nil, err
	}
	slog.Info("object storage ready", "bucket", cfg.S3.Bucket, "endpoint", cfg.S3.Endpoint)
	return guarded, parquet.NewReader(guarded), nil
}
