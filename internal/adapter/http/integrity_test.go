package http

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/synanton/content-service/internal/conformance"
)

// TestCorruptPayloadMaps409 proves the surface half of the CB-x
// guarantee: bytes that fail verification surface as 409
// INTEGRITY_MISMATCH with no payload body — never 200 with corrupt
// bytes, never an indistinguishable 404.
func TestCorruptPayloadMaps409(t *testing.T) {
	store := conformance.NewFakeStore()
	h := NewHandler(store, nil)
	payload := base64.StdEncoding.EncodeToString([]byte("surface integrity probe"))
	putBody := `{"tenant_id":"acme","payload":"` + payload + `","content_type":"text/plain","owner_id":"t"}`
	putReq := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", strings.NewReader(putBody))
	putRec := httptest.NewRecorder()
	h.Put(putRec, putReq)
	if putRec.Code != http.StatusCreated {
		t.Fatalf("setup put: %d", putRec.Code)
	}
	var out putResponse
	if err := json.Unmarshal(putRec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	store.CorruptPayload("acme", out.ArtifactID)

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+out.ArtifactID+"?tenant_id=acme", nil)
	getRec := httptest.NewRecorder()
	h.Get(getRec, getReq)
	if getRec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", getRec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "INTEGRITY_MISMATCH" {
		t.Fatalf("code = %q, want INTEGRITY_MISMATCH", body.Code)
	}
}
