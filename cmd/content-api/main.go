package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/synanton/content-service/internal/adapter/postgres"
	httphandler "github.com/synanton/content-service/internal/adapter/http"
	"github.com/synanton/content-service/internal/config"
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

	store := postgres.NewStore(pool, cfg)
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

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	log.Printf("content-service %s listening on %s", version, addr)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("shutdown: %v", err)
		}
	}

	log.Printf("content-service stopped")
}
