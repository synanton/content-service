package security

import (
	"context"
	"log"
	"time"
)

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
}

func NewAuditor(enabled bool) *Auditor {
	return &Auditor{enabled: enabled}
}

func (a *Auditor) Record(ctx context.Context, event AuditEvent) {
	if !a.enabled {
		return
	}
	event.Timestamp = time.Now().UTC()
	log.Printf("AUDIT: actor=%s action=%s tenant=%s artifact=%s scope=%s reason=%s",
		event.Actor, event.Action, event.TenantID, event.ArtifactID, event.Scope, event.Reason)
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
