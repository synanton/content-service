package reference

import (
	"fmt"
	"net/url"
	"strings"
)

type Reference struct {
	TenantID          string
	ArtifactID        string
	Digest            string
	Generation        int
	SerializerVersion int
}

func (r *Reference) Encode() string {
	params := url.Values{}
	params.Set("tenant_id", r.TenantID)
	params.Set("artifact_id", r.ArtifactID)
	params.Set("digest", r.Digest)
	params.Set("generation", fmt.Sprintf("%d", r.Generation))
	params.Set("serializer_version", fmt.Sprintf("%d", r.SerializerVersion))
	return "v1:" + params.Encode()
}

func Parse(raw string) (*Reference, error) {
	if !strings.HasPrefix(raw, "v1:") {
		return nil, fmt.Errorf("unsupported reference format: expected v1 prefix")
	}

	raw = strings.TrimPrefix(raw, "v1:")

	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse reference: %w", err)
	}

	ref := &Reference{
		TenantID:   values.Get("tenant_id"),
		ArtifactID: values.Get("artifact_id"),
		Digest:     values.Get("digest"),
	}

	if ref.TenantID == "" {
		return nil, fmt.Errorf("missing tenant_id in reference")
	}
	if ref.ArtifactID == "" {
		return nil, fmt.Errorf("missing artifact_id in reference")
	}
	if ref.Digest == "" {
		return nil, fmt.Errorf("missing digest in reference")
	}

	if gen, err := fmt.Sscanf(values.Get("generation"), "%d", &ref.Generation); err != nil || gen != 1 {
		ref.Generation = 1
	}

	if ver, err := fmt.Sscanf(values.Get("serializer_version"), "%d", &ref.SerializerVersion); err != nil || ver != 1 {
		ref.SerializerVersion = 1
	}

	return ref, nil
}

func New(tenantID, artifactID, digest string, generation, serializerVersion int) *Reference {
	return &Reference{
		TenantID:          tenantID,
		ArtifactID:        artifactID,
		Digest:            digest,
		Generation:        generation,
		SerializerVersion: serializerVersion,
	}
}
