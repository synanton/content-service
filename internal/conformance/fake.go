// Package conformance runs the Design 1.26 Core behavioral suite against
// any port.ContentStore. The in-memory FakeStore below lets the suite run
// as a unit test; test/integration runs the same suite against the real
// Postgres + object-storage wiring.
package conformance

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/pkg/digest"
)

type fakeArtifact struct {
	artifact domain.Artifact
	payload  []byte
	owners   map[string]bool
	released map[string]bool
	hold     bool
}

// CorruptPayload flips one byte of the stored payload to simulate
// bit-rot or a tampered backend. The digest intentionally still records
// the original bytes, so the next Get must fail closed.
func (f *FakeStore) CorruptPayload(tenantID, artifactID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookup(tenantID, artifactID)
	if err != nil || len(a.payload) == 0 {
		return
	}
	a.payload[0] ^= 0xff
}

// FakeStore is an in-memory port.ContentStore with the production
// semantics under test: per-tenant dedup, tenant-scoped reads,
// digest verification, owner-scoped release, audited-by-construction
// legal hold. Test-only.
type FakeStore struct {
	mu        sync.Mutex
	artifacts map[string]*fakeArtifact
}

// NewFakeStore returns an empty fake.
func NewFakeStore() *FakeStore {
	return &FakeStore{artifacts: map[string]*fakeArtifact{}}
}

func fakeKey(tenant, artifact string) string {
	return tenant + "\x00" + artifact
}

func (f *FakeStore) Put(_ context.Context, p port.PutParams) (*port.PutResult, error) {
	if p.TenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", domain.ErrInvalidArgument)
	}
	if len(p.Payload) == 0 {
		return nil, fmt.Errorf("%w: payload is empty", domain.ErrInvalidArgument)
	}
	computed := digest.Compute(p.Payload)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.artifacts {
		if a.artifact.TenantID == p.TenantID && a.artifact.Digest == computed {
			owner := p.OwnerID
			if owner == "" {
				owner = "unknown"
			}
			a.owners[owner] = true
			delete(a.released, owner)
			art := a.artifact
			return &port.PutResult{Artifact: art, DedupHit: true}, nil
		}
	}
	id := uuid.NewString()
	owner := p.OwnerID
	if owner == "" {
		owner = "unknown"
	}
	art := domain.Artifact{
		TenantID:          p.TenantID,
		ArtifactID:        id,
		Digest:            computed,
		Generation:        1,
		SerializerVersion: p.SerializerVersion,
		ContentType:       p.ContentType,
		SizeBytes:         int64(len(p.Payload)),
		StorageTier:       "inline",
		CreatedAt:         time.Now().UTC(),
	}
	f.artifacts[fakeKey(p.TenantID, id)] = &fakeArtifact{
		artifact: art,
		payload:  append([]byte(nil), p.Payload...),
		owners:   map[string]bool{owner: true},
		released: map[string]bool{},
	}
	return &port.PutResult{Artifact: art, DedupHit: false}, nil
}

func (f *FakeStore) lookup(tenant, artifact string) (*fakeArtifact, error) {
	if tenant == "" || artifact == "" {
		return nil, fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	a, ok := f.artifacts[fakeKey(tenant, artifact)]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return a, nil
}

func (f *FakeStore) Get(_ context.Context, tenantID, artifactID string) ([]byte, *domain.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookup(tenantID, artifactID)
	if err != nil {
		return nil, nil, err
	}
	if subtle.ConstantTimeCompare([]byte(digest.Compute(a.payload)), []byte(a.artifact.Digest)) != 1 {
		return nil, nil, domain.ErrIntegrityMismatch
	}
	art := a.artifact
	return append([]byte(nil), a.payload...), &art, nil
}

func (f *FakeStore) Exists(_ context.Context, tenantID, artifactID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.lookup(tenantID, artifactID)
	if err != nil {
		if err == domain.ErrNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (f *FakeStore) MarkForDeletion(_ context.Context, tenantID, artifactID, ownerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookup(tenantID, artifactID)
	if err != nil {
		return err
	}
	if ownerID == "" {
		ownerID = "unknown"
	}
	a.released[ownerID] = true
	return nil
}

func (f *FakeStore) RegisterReference(_ context.Context, tenantID, dgst, ownerID string) error {
	if tenantID == "" || dgst == "" {
		return fmt.Errorf("%w: tenant_id and digest are required", domain.ErrInvalidArgument)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.artifacts {
		if a.artifact.TenantID == tenantID && a.artifact.Digest == dgst {
			if ownerID == "" {
				ownerID = "unknown"
			}
			a.owners[ownerID] = true
			delete(a.released, ownerID)
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *FakeStore) ReleaseReference(_ context.Context, tenantID, dgst, ownerID string) error {
	if tenantID == "" || dgst == "" {
		return fmt.Errorf("%w: tenant_id and digest are required", domain.ErrInvalidArgument)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.artifacts {
		if a.artifact.TenantID == tenantID && a.artifact.Digest == dgst {
			if ownerID == "" {
				ownerID = "unknown"
			}
			a.released[ownerID] = true
			return nil
		}
	}
	return nil
}

func (f *FakeStore) SetLegalHold(_ context.Context, tenantID, artifactID string, hold bool, _, reason string) error {
	if tenantID == "" || artifactID == "" {
		return fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	if reason == "" {
		return fmt.Errorf("%w: reason is required for legal hold changes", domain.ErrInvalidArgument)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookup(tenantID, artifactID)
	if err != nil {
		return err
	}
	a.hold = hold
	return nil
}

func (f *FakeStore) SetRetention(_ context.Context, tenantID, artifactID string, retentionDays int) error {
	if tenantID == "" || artifactID == "" {
		return fmt.Errorf("%w: tenant_id and artifact_id are required", domain.ErrInvalidArgument)
	}
	if retentionDays <= 0 {
		return fmt.Errorf("%w: retention_days must be positive", domain.ErrInvalidArgument)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.lookup(tenantID, artifactID); err != nil {
		return err
	}
	return nil
}
