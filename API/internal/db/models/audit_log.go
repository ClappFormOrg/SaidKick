package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AuditLogAdvisoryLockKey is a 64-bit constant passed to
// pg_advisory_xact_lock so concurrent AuditLog inserts serialise on the
// hash-chain. Value chosen arbitrarily; only its uniqueness within the DB
// matters. Keep it stable across releases — changing it would allow a
// concurrent insert window during deploy.
const AuditLogAdvisoryLockKey int64 = 0x53414944_4B49434B //nolint:revive // "SAIDKICK" packed as ASCII bytes

// AuditLog is the append-only, hash-chained record of every change with
// compliance significance: role grants, audit decisions, cluster scope
// changes, deployment creates, etc.
//
// Append-only is enforced at the database role layer: the application
// writes via a dedicated `audit_writer` PG role that holds INSERT but not
// UPDATE/DELETE. Retention is at least 7 years (BIO).
//
// Tamper-evidence: each row carries IngestHash = sha256(previous_hash ||
// canonical(payload) || event_type || subject_type || subject_id). The
// chain is verifiable by replaying from the genesis row.
type AuditLog struct {
	IDModel

	EventType string `gorm:"type:varchar(128);not null;index" json:"event_type"`

	ActorUserID         *uuid.UUID `gorm:"type:uuid;index" json:"actor_user_id,omitempty"`
	ActorOrganizationID *uuid.UUID `gorm:"type:uuid"       json:"actor_organization_id,omitempty"`

	SubjectType string    `gorm:"type:varchar(64);not null"    json:"subject_type"`
	SubjectID   uuid.UUID `gorm:"type:uuid;not null;index"     json:"subject_id"`

	Payload datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"payload"`

	OccurredAt time.Time `gorm:"autoCreateTime;index" json:"occurred_at"`

	// IngestHash is the SHA-256 in lowercase hex of the chain advance.
	IngestHash string `gorm:"type:char(64);not null" json:"ingest_hash"`

	// PreviousHash links to the immediately-preceding row by IngestHash. An
	// empty string is reserved for the genesis row (the very first audit
	// log entry, written by the migrator). varchar (not char) so the empty
	// genesis value is stored as "" rather than padded to 64 spaces.
	PreviousHash string `gorm:"type:varchar(64);not null;default:''" json:"previous_hash"`
}

// BeforeCreate acquires a Postgres advisory transaction lock, reads the
// most-recent IngestHash, computes the new hash and persists it. Because
// pg_advisory_xact_lock is held for the duration of the transaction, two
// concurrent inserts will serialise without overlapping; the second waits
// for the first to commit (or roll back) before computing its own
// PreviousHash.
//
// Callers must NOT preset IngestHash or PreviousHash; the hook overwrites
// them. Calling this hook outside of a transaction is allowed (GORM wraps
// Create in an implicit transaction) but for batch inserts use Save inside
// an explicit tx and let GORM iterate row-by-row so each gets its own lock
// boundary.
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if a.EventType == "" {
		return errors.New("audit_log: event_type required")
	}
	if a.SubjectType == "" {
		return errors.New("audit_log: subject_type required")
	}
	if a.SubjectID == uuid.Nil {
		return errors.New("audit_log: subject_id required")
	}

	// Mint UUIDv7 if not already set (IDModel hook is not invoked because
	// AuditLog has its own BeforeCreate that GORM finds first; we must mint
	// the ID ourselves to preserve time-ordering.)
	if a.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("audit_log: uuid: %w", err)
		}
		a.ID = id
	}

	// Serialise the chain advance.
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", AuditLogAdvisoryLockKey).Error; err != nil {
		return fmt.Errorf("audit_log: advisory lock: %w", err)
	}

	// Read the current chain head. ORDER BY occurred_at then id makes the
	// ordering deterministic within a single timestamp (UUIDv7 is
	// time-prefixed, so id-desc inside the same millisecond is correct).
	var prev AuditLog
	err := tx.Unscoped().
		Order("occurred_at DESC, id DESC").
		Limit(1).
		Take(&prev).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		a.PreviousHash = "" // genesis
	case err != nil:
		return fmt.Errorf("audit_log: read head: %w", err)
	default:
		a.PreviousHash = prev.IngestHash
	}

	// Compute the new IngestHash. We canonicalise just enough to be
	// deterministic: previous_hash, event_type, subject_type, subject_id,
	// payload (JSON bytes as stored).
	h := sha256.New()
	h.Write([]byte(a.PreviousHash))
	h.Write([]byte{0x1f}) // unit separator
	h.Write([]byte(a.EventType))
	h.Write([]byte{0x1f})
	h.Write([]byte(a.SubjectType))
	h.Write([]byte{0x1f})
	h.Write(a.SubjectID[:])
	h.Write([]byte{0x1f})
	if a.Payload != nil {
		payloadBytes, mErr := a.Payload.MarshalJSON()
		if mErr != nil {
			return fmt.Errorf("audit_log: marshal payload: %w", mErr)
		}
		h.Write(payloadBytes)
	}
	a.IngestHash = hex.EncodeToString(h.Sum(nil))

	return nil
}
