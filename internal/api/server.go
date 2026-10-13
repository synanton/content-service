package api

import (
	"context"
	"log"
	"net"

	"github.com/synanton/content-service/internal/config"
	"github.com/synanton/content-service/internal/storage"
)

type Server struct {
	cfg     *config.Config
	store   storage.Storage
	listener net.Listener
}

func NewServer(cfg *config.Config) *Server {
	return &Server{
		cfg:   cfg,
		store: storage.NewTieredStorage(cfg),
	}
}

func (s *Server) Serve(listener net.Listener) error {
	s.listener = listener
	log.Printf("Content Service API listening on %s", listener.Addr().String())

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		go s.handleConnection(conn)
	}
}

func (s *Server) GracefulStop() {
	if s.listener != nil {
		s.listener.Close()
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()
	log.Printf("Connection from %s", conn.RemoteAddr())
}

func (s *Server) Capabilities(ctx context.Context) map[string]interface{} {
	return map[string]interface{}{
		"conformance_level": "CORE",
		"features": []string{
			"tenant_isolation",
			"content_addressable_storage",
			"integrity_verification",
		},
		"storage_tiers": []string{
			"inline",
			"s3",
		},
	}
}
