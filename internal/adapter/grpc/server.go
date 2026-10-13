package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	contentv1 "github.com/synanton/content-service/gen/content/v1"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/pkg/reference"
)

// Server implements the ContentService gRPC contract over a port.ContentStore.
// Tenant scope travels in request fields (same contract as the REST
// X-Tenant-ID header); cross-tenant reads fail with NotFound,
// indistinguishable from missing content.
type Server struct {
	contentv1.UnimplementedContentServiceServer
	store port.ContentStore
}

// NewServer wires the gRPC surface to the store port.
func NewServer(store port.ContentStore) *Server {
	return &Server{store: store}
}

func (s *Server) Put(ctx context.Context, req *contentv1.PutRequest) (*contentv1.PutResponse, error) {
	res, err := s.store.Put(ctx, port.PutParams{
		TenantID:          req.GetTenantId(),
		Payload:           req.GetPayload(),
		ContentType:       req.GetContentType(),
		OwnerID:           req.GetOwnerId(),
		RetentionDays:     int(req.GetRetentionDays()),
		SerializerVersion: int(req.GetSerializerVersion()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	ref := reference.New(
		req.GetTenantId(),
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
	data, art, err := s.store.Get(ctx, req.GetTenantId(), req.GetArtifactId())
	if err != nil {
		return nil, toStatus(err)
	}
	return &contentv1.GetResponse{
		Payload:     data,
		Digest:      art.Digest,
		ContentType: art.ContentType,
		ArtifactId:  art.ArtifactID,
	}, nil
}

func (s *Server) Head(ctx context.Context, req *contentv1.HeadRequest) (*contentv1.HeadResponse, error) {
	ok, err := s.store.Exists(ctx, req.GetTenantId(), req.GetArtifactId())
	if err != nil {
		return nil, toStatus(err)
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

func toStatus(err error) error {
	switch {
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
