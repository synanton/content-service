package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	"github.com/synanton/content-service/internal/adapter/postgres"
	httphandler "github.com/synanton/content-service/internal/adapter/http"
	grpchandler "github.com/synanton/content-service/internal/adapter/grpc"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/port"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	if cfg.Server.MigrateOnStartup {
		log.Printf("applying migrations from %s", cfg.Server.MigrationsDir)
		if err := postgres.Migrate(ctx, pool, cfg.Server.MigrationsDir); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		log.Printf("migrations applied")
	}

	var blobs port.BlobStore
	blobs, err = dialObjectStore(ctx, cfg)
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}

	store := postgres.NewStore(pool, cfg, blobs)
	h := httphandler.NewHandler(store)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/artifacts", h.Put)
	mux.HandleFunc("GET /api/v1/artifacts/", h.Get)
	mux.HandleFunc("HEAD /api/v1/artifacts/", h.Head)
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
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "# content-service metrics (prometheus wiring lands in Phase 5)")
	})

	grpcLis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.GrpcPort))
	if err != nil {
		log.Fatalf("grpc listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	contentv1.RegisterContentServiceServer(grpcSrv, grpchandler.NewServer(store))
	reflection.Register(grpcSrv)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	log.Printf("content-service %s listening on %s (http) and %s (grpc)", version, addr, grpcLis.Addr())

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
		log.Fatalf("server: %v", err)
	case sig := <-sigCh:
		log.Printf("signal %v: draining", sig)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		grpcSrv.GracefulStop()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("shutdown: %v", err)
		}
	}

	log.Printf("content-service stopped")
}

func dialObjectStore(ctx context.Context, cfg *config.Config) (port.BlobStore, error) {
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
		return nil, err
	}
	if err := store.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	log.Printf("object storage ready: bucket %q at %s", cfg.S3.Bucket, cfg.S3.Endpoint)
	return store, nil
}
