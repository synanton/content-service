package grpc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
	"github.com/synanton/content-service/pkg/reference"
)

// Server implements the ContentService gRPC contract over a port.ContentStore.
// Tenant scope travels in request fields (same contract as the REST
// X-Tenant-ID header); cross-tenant reads fail with NotFound,
// indistinguishable from missing content. When authentication is enabled
// (interceptor injects a validated security context), the context tenant
// governs and a conflicting message field fails with PermissionDenied.
type Server struct {
	contentv1.UnimplementedContentServiceServer
	store   port.ContentStore
	auditor *security.Auditor
}

// NewServer wires the gRPC surface to the store port. A nil auditor
// disables upstream-failure audit emission (benchmarks, transport-only
// tests); production main always passes one.
func NewServer(store port.ContentStore, auditor *security.Auditor) *Server {
	return &Server{store: store, auditor: auditor}
}

// auditUpstream records an artifact-attributed audit event for an
// upstream-availability failure. Exactly one record per failed
// operation; see the HTTP Handler's auditUpstream for the contract.
func (s *Server) auditUpstream(ctx context.Context, action, tenantID, artifactID, reason string) {
	if s.auditor == nil {
		return
	}
	s.auditor.Record(ctx, security.AuditEvent{
		Actor:      "system",
		Action:     action,
		TenantID:   tenantID,
		ArtifactID: artifactID,
		Reason:     reason,
	})
}

// tenantOf resolves the request tenant. When a validated security
// context is present (authentication enabled via interceptor), its tenant
// governs and the message field is cross-checked: a mismatch fails with
// PermissionDenied. Otherwise the legacy unauthenticated message field
// applies.
func tenantOf(ctx context.Context, hint string) (string, error) {
	if tenant, err := security.ResolveTenant(ctx, hint); err != nil {
		return "", status.Error(codes.PermissionDenied, "conflicting tenant claim")
	} else if tenant != "" {
		return tenant, nil
	}
	return hint, nil
}

func (s *Server) Put(ctx context.Context, req *contentv1.PutRequest) (*contentv1.PutResponse, error) {
	start := time.Now()
	tenantID, err := tenantOf(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	res, err := s.store.Put(ctx, port.PutParams{
		TenantID:          tenantID,
		Payload:           req.GetPayload(),
		ContentType:       req.GetContentType(),
		OwnerID:           req.GetOwnerId(),
		RetentionDays:     int(req.GetRetentionDays()),
		SerializerVersion: int(req.GetSerializerVersion()),
	})
	if err != nil {
		observability.PutErrors.WithLabelValues(tenantID).Inc()
		return nil, s.toStatus(ctx, "put_failed", tenantID, "", err)
	}
	observability.PutLatency.WithLabelValues(
		tenantID, res.Artifact.StorageTier).Observe(time.Since(start).Seconds())
	ref := reference.New(
		tenantID,
		res.Artifact.ArtifactID,
		res.Artifact.Digest,
		res.Artifact.Generation,
		res.Artifact.SerializerVersion,
	)
	return &contentv1.PutResponse{
		Reference:   ref.Encode(),
		ArtifactId:  res.Artifact.ArtifactID,
		Digest:      res.Artifact.Digest,
		DedupHit:    res.DedupHit,
		StorageTier: res.Artifact.StorageTier,
	}, nil
}

func (s *Server) Get(ctx context.Context, req *contentv1.GetRequest) (*contentv1.GetResponse, error) {
	start := time.Now()
	tenantID, err := tenantOf(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	data, art, err := s.store.Get(ctx, tenantID, req.GetArtifactId())
	if err != nil {
		observability.GetErrors.WithLabelValues(tenantID).Inc()
		return nil, s.toStatus(ctx, "get_failed", tenantID, req.GetArtifactId(), err)
	}
	observability.GetLatency.WithLabelValues(
		tenantID, art.StorageTier).Observe(time.Since(start).Seconds())
	return &contentv1.GetResponse{
		Payload:     data,
		Digest:      art.Digest,
		ContentType: art.ContentType,
		ArtifactId:  art.ArtifactID,
	}, nil
}

func (s *Server) Head(ctx context.Context, req *contentv1.HeadRequest) (*contentv1.HeadResponse, error) {
	tenantID, err := tenantOf(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	ok, err := s.store.Exists(ctx, tenantID, req.GetArtifactId())
	if err != nil {
		return nil, s.toStatus(ctx, "head_failed", tenantID, req.GetArtifactId(), err)
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "artifact not found")
	}
	return &contentv1.HeadResponse{Exists: true, ArtifactId: req.GetArtifactId()}, nil
}

func (s *Server) Capabilities(
	ctx context.Context, _ *contentv1.CapabilitiesRequest,
) (*contentv1.CapabilitiesResponse, error) {
	return &contentv1.CapabilitiesResponse{
		ConformanceLevel: "CORE",
		Features:         []string{"tenant_isolation", "content_addressable_storage", "integrity_verification"},
		StorageTiers:     []string{"inline", "parquet", "s3"},
	}, nil
}

func (s *Server) toStatus(ctx context.Context, action, tenantID, artifactID string, err error) error {
	if reason, ok := s3adapter.AuditReason(err); ok {
		s.auditUpstream(ctx, action, tenantID, artifactID, reason)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.Unavailable, "storage backend timed out")
	case s3adapter.IsCircuitOpen(err):
		return status.Error(codes.Unavailable, "storage backend degraded")
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, "artifact not found")
	case errors.Is(err, domain.ErrIntegrityMismatch):
		return status.Error(codes.DataLoss, err.Error())
	case errors.Is(err, domain.ErrInvalidArgument),
		errors.Is(err, domain.ErrReferenceTooLong):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "request failed")
	}
}
