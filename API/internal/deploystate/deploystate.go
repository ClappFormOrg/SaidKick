// Package deploystate is the single write path for Deployment.state. Every
// persisted state change goes through it so deployment_transitions_total
// counts each change exactly once, with the state the row actually left.
package deploystate

import (
	"context"
	"errors"
	"maps"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// unknownState labels the from side of a deployment's first transition,
// where the state is still the zero value models.DeploymentUnknown.
const unknownState = "unknown"

func label(s models.DeploymentState) string {
	if s == models.DeploymentUnknown {
		return unknownState
	}
	return string(s)
}

// count records n transitions from -> to. A write that leaves the state as
// it was is not a transition.
func count(from, to models.DeploymentState, n int64) {
	if from == to || n <= 0 {
		return
	}
	metrics.DeploymentTransitionsTotal(label(from), label(to)).Add(int(n))
}

// Created counts the transition of a newly inserted deployment from unknown
// into its initial state. Call it once the row is committed.
func Created(dep models.Deployment) {
	count(models.DeploymentUnknown, dep.State, 1)
}

// Set moves dep's row to state to, writing extra columns in the same UPDATE,
// and counts the transition once it commits. It locks the row and reads the
// state it leaves, so the count is right even when dep is stale. A row that
// no longer exists (or is soft-deleted) is left alone without error, as a
// plain UPDATE would. On success dep.State is to.
//
// Do not call Set inside a caller's transaction: it commits its own, and a
// later rollback of the caller's work would leave the count inflated.
func Set(ctx context.Context, db *gorm.DB, dep *models.Deployment, to models.DeploymentState, extra map[string]any) error {
	cols := map[string]any{"state": to}
	maps.Copy(cols, extra)

	var from models.DeploymentState
	moved := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur models.Deployment
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "id = ?", dep.ID).Error; err != nil {
			return err
		}
		from = cur.State
		res := tx.Model(&cur).Updates(cols)
		moved = res.RowsAffected > 0
		return res.Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	dep.State = to
	if moved {
		count(from, to, 1)
	}
	return nil
}

// SetWhere moves every deployment that q matches and that is in state from
// to state to, and counts each one. q must be scoped to
// models.Deployment, e.g. db.Model(&models.Deployment{}).Where(...). It
// returns the number of deployments moved.
func SetWhere(q *gorm.DB, from, to models.DeploymentState) (int64, error) {
	res := q.Where("state = ?", from).Update("state", to)
	if res.Error != nil {
		return 0, res.Error
	}
	count(from, to, res.RowsAffected)
	return res.RowsAffected, nil
}
