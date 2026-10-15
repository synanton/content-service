package security

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// allowlistedKeys is the exact JSON key set an audit record may carry:
// slog envelope (time, level, msg) plus the audit field allowlist. A
// future code change that logs a new key (payload, token, header, …)
// fails here by construction.
var allowlistedKeys = map[string]struct{}{
	"time": {}, "level": {}, "msg": {},
	"event": {}, "actor": {}, "action": {}, "tenant": {},
	"artifact": {}, "scope": {}, "reason": {},
	"before": {}, "after": {},
}

func TestAuditEmitsOnlyAllowlistedKeys(t *testing.T) {
	ctx := context.Background()
	auditor, buf := captureAuditor(t, true)
	auditor.RecordLegalHold(ctx, "admin-1", "tenant:acme", "artifact-1", "tenant", "place", "litigation")
	auditor.RecordRetentionChange(ctx, "admin-1", "tenant:acme", "artifact-1", "30d", "90d")
	auditor.RecordDisposition(ctx, "tenant:acme", "artifact-1", "deleted")
	auditor.Record(ctx, AuditEvent{Actor: "auth", Action: "auth.denied", TenantID: "tenant:acme", Reason: "CONFLICTING_TENANT"})
	for i, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("record %d is not JSON: %v", i, err)
		}
		for key := range decoded {
			if _, ok := allowlistedKeys[key]; !ok {
				t.Errorf("record %d carries non-allowlisted key %q: %v", i, key, decoded)
			}
		}
		if strings.Contains(line, "Bearer ") {
			t.Errorf("record %d contains bearer token shape: %s", i, line)
		}
	}
}

func captureAuditor(t *testing.T, enabled bool) (*Auditor, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	return NewAuditorWithLogger(enabled, log), &buf
}

func decodeRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("audit output is not JSON: %v\n%s", err, buf.String())
	}
	return decoded
}

func TestAuditRecordIsExtractable(t *testing.T) {
	auditor, buf := captureAuditor(t, true)
	auditor.Record(context.Background(), AuditEvent{
		Actor: "admin-1", Action: "legal_hold_place",
		TenantID: "tenant:acme", ArtifactID: "artifact-1",
		Scope: "tenant", Reason: "litigation",
	})
	decoded := decodeRecord(t, buf)
	if decoded["event"] != "audit" {
		t.Fatalf("audit record lacks event=audit: %v", decoded)
	}
	for _, field := range []string{"actor", "action", "tenant", "artifact", "reason"} {
		if decoded[field] == nil || decoded[field] == "" {
			t.Errorf("audit record missing %q: %v", field, decoded)
		}
	}
	if decoded["actor"] != "admin-1" || decoded["tenant"] != "tenant:acme" {
		t.Errorf("audit fields wrong: %v", decoded)
	}
}

func TestAuditRecordersEmitExtractableEvents(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(*Auditor)
	}{
		{"legal hold", func(a *Auditor) {
			a.RecordLegalHold(ctx, "admin-1", "tenant:acme", "artifact-1", "tenant", "place", "litigation")
		}},
		{"retention change", func(a *Auditor) {
			a.RecordRetentionChange(ctx, "admin-1", "tenant:acme", "artifact-1", "30d", "90d")
		}},
		{"disposition", func(a *Auditor) {
			a.RecordDisposition(ctx, "tenant:acme", "artifact-1", "deleted")
		}},
		{"auth denied", func(a *Auditor) {
			a.Record(ctx, AuditEvent{Actor: "auth", Action: "auth.denied", TenantID: "tenant:acme", Reason: "CONFLICTING_TENANT"})
		}},
	}
	for _, tc := range cases {
		auditor, buf := captureAuditor(t, true)
		tc.call(auditor)
		if decoded := decodeRecord(t, buf); decoded["event"] != "audit" {
			t.Errorf("%s: not extractable: %v", tc.name, decoded)
		}
	}
}

func TestAuditDisabledWritesNothing(t *testing.T) {
	auditor, buf := captureAuditor(t, false)
	auditor.Record(context.Background(), AuditEvent{Actor: "x", Action: "y"})
	if buf.Len() != 0 {
		t.Fatalf("disabled auditor wrote %q", buf.String())
	}
}
