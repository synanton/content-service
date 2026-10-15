package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	s3adapter "github.com/synanton/content-service/internal/adapter/s3"
	"github.com/synanton/content-service/internal/domain"
	"github.com/synanton/content-service/internal/observability"
	"github.com/synanton/content-service/internal/port"
	"github.com/synanton/content-service/internal/security"
	"github.com/synanton/content-service/pkg/reference"
)

type Handler struct {
	store port.ContentStore
}

func NewHandler(store port.ContentStore) *Handler {
	return &Handler{store: store}
}

type putRequest struct {
	TenantID          string `json:"tenant_id"`
	Payload           string `json:"payload"`
	ContentType       string `json:"content_type"`
	OwnerID           string `json:"owner_id"`
	RetentionDays     int    `json:"retention_days"`
	SerializerVersion int    `json:"serializer_version"`
}

type putResponse struct {
	Reference   string `json:"reference"`
	ArtifactID  string `json:"artifact_id"`
	Digest      string `json:"digest"`
	DedupHit    bool   `json:"dedup_hit"`
	StorageTier string `json:"storage_tier"`
}

type getResponse struct {
	Payload     string `json:"payload"`
	Digest      string `json:"digest"`
	ContentType string `json:"content_type"`
	ArtifactID  string `json:"artifact_id"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// tenantOf resolves the request tenant. When a validated security
// context is present (authentication enabled), its tenant governs and
// every caller hint (header, query, body) is cross-checked: a mismatch
// fails with ErrTenantMismatch and must surface as 403. Otherwise the
// legacy unauthenticated header > query > body resolution applies.
func (h *Handler) tenantOf(r *http.Request, bodyTenant string) (string, error) {
	if tenant, err := security.ResolveTenant(r.Context(),
		r.Header.Get("X-Tenant-ID"), r.URL.Query().Get("tenant_id"), bodyTenant,
	); err != nil {
		return "", err
	} else if tenant != "" {
		return tenant, nil
	}
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t, nil
	}
	if t := r.URL.Query().Get("tenant_id"); t != "" {
		return t, nil
	}
	return bodyTenant, nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Code: code, Message: msg})
}

// isCircuitOpen reports a fail-fast breaker rejection. Both surfaces
// map it to 503/Unavailable: the backend is degraded, not the request.
func isCircuitOpen(err error) bool {
	return s3adapter.IsCircuitOpen(err)
}

func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	var req putRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	tenantID, err := h.tenantOf(r, req.TenantID)
	if err != nil {
		writeError(w, http.StatusForbidden, "CONFLICTING_TENANT", "tenant hint conflicts with authenticated context")
		return
	}
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "tenant_id is required")
		return
	}
	if req.Payload == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "payload is required")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "payload must be base64")
		return
	}
	start := time.Now()
	res, err := h.store.Put(r.Context(), port.PutParams{
		TenantID:          tenantID,
		Payload:           raw,
		ContentType:       req.ContentType,
		OwnerID:           req.OwnerID,
		RetentionDays:     req.RetentionDays,
		SerializerVersion: req.SerializerVersion,
	})
	if err != nil {
		observability.PutErrors.WithLabelValues(tenantID).Inc()
		if errors.Is(err, domain.ErrInvalidArgument) || errors.Is(err, domain.ErrReferenceTooLong) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "put failed")
		return
	}
	observability.PutLatency.WithLabelValues(tenantID, res.Artifact.StorageTier).Observe(time.Since(start).Seconds())
	ref := reference.New(tenantID, res.Artifact.ArtifactID, res.Artifact.Digest, res.Artifact.Generation, res.Artifact.SerializerVersion)
	writeJSON(w, http.StatusCreated, putResponse{
		Reference:   ref.Encode(),
		ArtifactID:  res.Artifact.ArtifactID,
		Digest:      res.Artifact.Digest,
		DedupHit:    res.DedupHit,
		StorageTier: res.Artifact.StorageTier,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "artifact id is required")
		return
	}
	tenantID, err := h.tenantOf(r, "")
	if err != nil {
		writeError(w, http.StatusForbidden, "CONFLICTING_TENANT", "tenant hint conflicts with authenticated context")
		return
	}
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "tenant_id is required")
		return
	}
	start := time.Now()
	data, art, err := h.store.Get(r.Context(), tenantID, id)
	if err != nil {
		observability.GetErrors.WithLabelValues(tenantID).Inc()
		h.writeStoreError(w, err)
		return
	}
	observability.GetLatency.WithLabelValues(tenantID, art.StorageTier).Observe(time.Since(start).Seconds())
	writeJSON(w, http.StatusOK, getResponse{
		Payload:     base64.StdEncoding.EncodeToString(data),
		Digest:      art.Digest,
		ContentType: art.ContentType,
		ArtifactID:  art.ArtifactID,
	})
}

func (h *Handler) Head(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "artifact id is required")
		return
	}
	tenantID, err := h.tenantOf(r, "")
	if err != nil {
		writeError(w, http.StatusForbidden, "CONFLICTING_TENANT", "tenant hint conflicts with authenticated context")
		return
	}
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "tenant_id is required")
		return
	}
	ok, err := h.store.Exists(r.Context(), tenantID, id)
	if err != nil {
		h.writeStoreError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "artifact not found")
		return
	}
	w.Header().Set("X-Artifact-ID", id)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_TIMEOUT", "storage backend timed out")
	case isCircuitOpen(err):
		writeError(w, http.StatusServiceUnavailable, "UPSTREAM_DEGRADED", "storage backend degraded")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "artifact not found")
	case errors.Is(err, domain.ErrIntegrityMismatch):
		writeError(w, http.StatusConflict, "INTEGRITY_MISMATCH", err.Error())
	case errors.Is(err, domain.ErrInvalidArgument):
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "request failed")
	}
}

func (h *Handler) Capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"conformance_level": "CORE",
		"features":          []string{"tenant_isolation", "content_addressable_storage", "integrity_verification"},
		"storage_tiers":     []string{"inline", "parquet", "s3"},
	})
}
