package security

import (
	"context"
	"log/slog"
	"time"
)

// AuditEvent is the allowlisted audit field set. Only identifiers,
// actions, and operator-supplied reasons cross this boundary: raw
// payload bytes, tokens, and credential material must never be placed in
// any field. The redaction tests pin this.
type AuditEvent struct {
	Timestamp   time.Time
	Actor       string
	Action      string
	TenantID    string
	ArtifactID  string
	Scope       string
	Reason      string
	BeforeState string
	AfterState  string
}

type Auditor struct {
	enabled bool
	log     *slog.Logger
}

func NewAuditor(enabled bool) *Auditor {
	return &Auditor{enabled: enabled, log: slog.Default()}
}

// NewAuditorWithLogger injects the sink. Tests capture output through it;
// production passes the process default.
func NewAuditorWithLogger(enabled bool, log *slog.Logger) *Auditor {
	return &Auditor{enabled: enabled, log: log}
}

func (a *Auditor) Record(ctx context.Context, event AuditEvent) {
	if !a.enabled {
		return
	}
	event.Timestamp = time.Now().UTC()
	// Every record carries event=audit so audit events are extractable
	// from the shared JSON stream with a single field filter.
	a.log.InfoContext(ctx, "audit",
		"event", "audit",
		"actor", event.Actor,
		"action", event.Action,
		"tenant", event.TenantID,
		"artifact", event.ArtifactID,
		"scope", event.Scope,
		"reason", event.Reason,
		"before", event.BeforeState,
		"after", event.AfterState,
	)
}

func (a *Auditor) RecordLegalHold(ctx context.Context, actor, tenantID, artifactID, scope, action, reason string) {
	a.Record(ctx, AuditEvent{
		Actor:      actor,
		Action:     "legal_hold_" + action,
		TenantID:   tenantID,
		ArtifactID: artifactID,
		Scope:      scope,
		Reason:     reason,
	})
}

func (a *Auditor) RecordRetentionChange(ctx context.Context, actor, tenantID, artifactID, before, after string) {
	a.Record(ctx, AuditEvent{
		Actor:       actor,
		Action:      "retention_change",
		TenantID:    tenantID,
		ArtifactID:  artifactID,
		BeforeState: before,
		AfterState:  after,
	})
}

func (a *Auditor) RecordDisposition(ctx context.Context, tenantID, artifactID, result string) {
	a.Record(ctx, AuditEvent{
		Actor:      "system",
		Action:     "disposition",
		TenantID:   tenantID,
		ArtifactID: artifactID,
		AfterState: result,
	})
}
