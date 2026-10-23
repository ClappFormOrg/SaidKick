package auth

import (
	"context"
	"log/slog"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SessionCleanupInterval is how often RunSessionCleanup sweeps expired rows
// from revoked_sessions.
const SessionCleanupInterval = time.Hour

// revocationChecker reports whether a session jti has been revoked.
type revocationChecker interface {
	isRevoked(ctx context.Context, jti uuid.UUID) (bool, error)
}

// revocationStore is the full set of revocation operations the OIDC
// authenticator needs: the per-request check and the logout write.
type revocationStore interface {
	revocationChecker
	revoke(ctx context.Context, jti, userID uuid.UUID, expiresAt time.Time) error
}

// dbRevocations is the revocationStore backed by the revoked_sessions table.
type dbRevocations struct {
	handle *db.Handle
}

func (r dbRevocations) isRevoked(ctx context.Context, jti uuid.UUID) (bool, error) {
	var n int64
	err := r.handle.RW.WithContext(ctx).
		Model(&models.RevokedSession{}).
		Where("jti = ?", jti).
		Count(&n).Error
	return n > 0, err
}

// revoke records jti as revoked until expiresAt. Revoking the same jti twice
// is a no-op.
func (r dbRevocations) revoke(ctx context.Context, jti, userID uuid.UUID, expiresAt time.Time) error {
	return r.handle.RW.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&models.RevokedSession{JTI: jti, UserID: userID, ExpiresAt: expiresAt}).Error
}

// deleteExpiredRevocations removes revoked_sessions rows whose token expired
// before now. Such tokens fail the expiry check on their own, so their rows
// no longer serve any purpose.
func deleteExpiredRevocations(ctx context.Context, gdb *gorm.DB, now time.Time) (int64, error) {
	res := gdb.WithContext(ctx).
		Where("expires_at < ?", now).
		Delete(&models.RevokedSession{})
	return res.RowsAffected, res.Error
}

// RunSessionCleanup deletes expired revoked_sessions rows every interval
// until ctx is cancelled. Call it in a goroutine from main.
func RunSessionCleanup(ctx context.Context, handle *db.Handle, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slog.Info("session cleanup: started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			slog.Info("session cleanup: stopped")
			return
		case <-ticker.C:
			n, err := deleteExpiredRevocations(ctx, handle.RW, time.Now())
			if err != nil {
				slog.Error("session cleanup: delete expired revocations", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("session cleanup: deleted expired revocations", "rows", n)
			}
		}
	}
}
