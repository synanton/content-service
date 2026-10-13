package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/synanton/content-service/internal/api"
	"github.com/synanton/content-service/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Starting Content Service API on %s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("Storage tiers: inline=%d, object=%d", cfg.Storage.InlineThresholdBytes, cfg.Storage.ObjectThresholdBytes)
	log.Printf("Retention: default=%d days, grace=%v", cfg.Retention.DefaultPolicyDays, cfg.Retention.DispositionGracePeriod)
	log.Printf("Security: mtls=%v, audit=%v", cfg.Security.MTLSCert != "", cfg.Security.AuditEnabled)

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	server := api.NewServer(cfg)

	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Fatalf("Server error: %v", err)
	case sig := <-sigCh:
		log.Printf("Received signal %v, shutting down gracefully", sig)
		server.GracefulStop()
	}

	log.Printf("Content Service API stopped")
}
