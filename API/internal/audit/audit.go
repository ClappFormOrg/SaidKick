// Package audit is the canonical entry point for writing rows to the
// platform's append-only AuditLog. Domain code never inserts directly into
// the audit_logs table — it calls audit.Log instead.
//
// The Logger writes through the audit-writer GORM handle so the role
// separation in db.Handle is exercised end-to-end (including in V0 dev
// where both handles share a DSN; see internal/db/db.go).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// Event is the value passed to Logger.Log. All fields except Payload are
// required.
type Event struct {
	// Type is a dotted-name event identifier: "app.submitted",
	// "app.audit_decision", "cluster.scope_changed", ...
	Type string

	// ActorUserID is the User performing the action. Leave nil for
	// system-generated events (migrations, periodic jobs).
	ActorUserID *uuid.UUID

	// ActorOrganizationID is the org the actor belongs to. Optional;
	// derivable from ActorUserID but stored explicitly for query speed.
	ActorOrganizationID *uuid.UUID

	// SubjectType is the entity type being acted upon: "app", "cluster",
	// "deployment", "role_assignment", ...
	SubjectType string

	// SubjectID is the UUID of the affected entity.
	SubjectID uuid.UUID

	// Payload is event-specific structured data — typically a before/after
	// diff for state transitions, or the rationale + decision details for
	// audit decisions. Nil is allowed and stored as `{}`.
	Payload any
}

// Logger is the canonical interface. There is exactly one production
// implementation backed by *db.Handle.AuditWriter; tests may swap in a
// fake to assert call patterns.
type Logger interface {
	Log(ctx context.Context, e Event) error
}

// NewLogger wires the production logger.
func NewLogger(h *db.Handle) Logger {
	return &gormLogger{handle: h}
}

type gormLogger struct {
	handle *db.Handle
}

// Log appends e to the audit log. It counts the outcome: audit_writes_total
// once the row commits, audit_write_failures_total on every error return.
func (g *gormLogger) Log(ctx context.Context, e Event) error {
	if err := g.write(ctx, e); err != nil {
		metrics.AuditWriteFailuresTotal.Inc()
		return err
	}
	metrics.AuditWritesTotal.Inc()
	return nil
}

func (g *gormLogger) write(ctx context.Context, e Event) error {
	if g == nil || g.handle == nil || g.handle.AuditWriter == nil {
		return errors.New("audit: logger not initialised")
	}
	if e.Type == "" {
		return errors.New("audit: event type required")
	}
	if e.SubjectType == "" {
		return errors.New("audit: subject_type required")
	}
	if e.SubjectID == uuid.Nil {
		return errors.New("audit: subject_id required")
	}

	payload := datatypes.JSON(`{}`)
	if e.Payload != nil {
		raw, err := json.Marshal(e.Payload)
		if err != nil {
			return fmt.Errorf("audit: marshal payload: %w", err)
		}
		payload = raw
	}

	row := &models.AuditLog{
		EventType:           e.Type,
		ActorUserID:         e.ActorUserID,
		ActorOrganizationID: e.ActorOrganizationID,
		SubjectType:         e.SubjectType,
		SubjectID:           e.SubjectID,
		Payload:             payload,
	}

	writeErr := g.handle.AuditWriter.WithContext(ctx).Create(row).Error
	if writeErr == nil {
		return nil
	}

	// Primary write failed — emit structured JSON to stderr so ops have a
	// recoverable record, then attempt to persist to the DLQ table via the
	// RW pool (different connection, different PG role).
	dlqEntry := map[string]any{
		"audit_dlq":    true,
		"event_type":   e.Type,
		"subject_type": e.SubjectType,
		"subject_id":   e.SubjectID.String(),
		"write_error":  writeErr.Error(),
	}
	if enc, jErr := json.Marshal(dlqEntry); jErr == nil {
		slog.Error("audit DLQ: failed to persist event", "entry", string(enc))
	}

	if g.handle.RW != nil {
		dlq := &models.AuditDLQ{
			EventType:   e.Type,
			SubjectType: e.SubjectType,
			SubjectID:   e.SubjectID,
			Payload:     payload,
			WriteError:  writeErr.Error(),
		}
		_ = g.handle.RW.WithContext(ctx).Create(dlq).Error
	}

	return writeErr
}

// NoopLogger is a sink implementation used by tests that don't care to
// observe audit calls.
type NoopLogger struct{}

func (NoopLogger) Log(_ context.Context, _ Event) error { return nil }
