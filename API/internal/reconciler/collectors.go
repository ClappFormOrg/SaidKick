package reconciler

import (
	"context"
	"log/slog"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// Periodic gauge collectors. Each runs one query per tick and, like
// setStateGauges, writes every series of its closed label set, with explicit
// zeros, so a value that empties out stops reporting its last count. Every
// API replica runs the reconciler and reports the same cluster-wide values:
// query these gauges with max by (...).

// recordAuditSubmissionStates publishes audit_submission_state_total: audit
// submissions per state of the AppVersion they belong to, which is where the
// review lifecycle lives (submitted is the auditor queue). Submissions of
// soft-deleted apps are excluded, as in the auditor queue.
func (r *Reconciler) recordAuditSubmissionStates(ctx context.Context) {
	var rows []struct {
		State models.AppVersionState
		Count int64
	}
	if err := r.db.RW.WithContext(ctx).
		Model(&models.AuditSubmission{}).
		Select("app_versions.state AS state, COUNT(*) AS count").
		Joins("JOIN app_versions ON app_versions.audit_submission_id = audit_submissions.id").
		Joins("JOIN apps ON apps.id = app_versions.app_id AND apps.deleted_at IS NULL").
		Group("app_versions.state").
		Scan(&rows).Error; err != nil {
		slog.Error("reconciler: audit submission state query", "error", err)
		return
	}
	counts := make(map[models.AppVersionState]int64, len(rows))
	for _, row := range rows {
		counts[row.State] = row.Count
	}
	setAuditSubmissionStateGauges(counts)
}

// recordAgentGauges publishes agent_heartbeat_age_seconds, the age of the
// stalest heartbeat across clusters that are not decommissioned (0 when none
// has reported), and agent_commands_pending, the commands that are neither
// acknowledged nor expired. Neither carries a cluster label.
func (r *Reconciler) recordAgentGauges(ctx context.Context) {
	var age float64
	if err := r.db.RW.WithContext(ctx).
		Model(&models.Cluster{}).
		Select("COALESCE(MAX(EXTRACT(EPOCH FROM (now() - last_heartbeat_at))), 0)").
		Where("state <> ? AND last_heartbeat_at IS NOT NULL", models.ClusterDecommissioned).
		Scan(&age).Error; err != nil {
		slog.Error("reconciler: heartbeat age query", "error", err)
	} else {
		metrics.AgentHeartbeatAgeSeconds.Set(max(age, 0))
	}

	var pending int64
	if err := r.db.RW.WithContext(ctx).
		Model(&models.AgentCommand{}).
		Where("acked_at IS NULL AND expires_at > now()").
		Count(&pending).Error; err != nil {
		slog.Error("reconciler: pending agent commands query", "error", err)
	} else {
		metrics.AgentCommandsPending.Set(float64(pending))
	}

	r.recordAgentCertMinRemaining(ctx)
}

// recordAgentCertMinRemaining publishes agent_cert_min_remaining_seconds:
// the shortest remaining validity of the active agent certificates across
// clusters that are not decommissioned. Clusters whose expiry is unknown
// (agent_cert_not_after NULL) are ignored; with none left the series is
// removed. Negative values mean a certificate has already expired.
func (r *Reconciler) recordAgentCertMinRemaining(ctx context.Context) {
	var minRemaining *float64
	if err := r.db.RW.WithContext(ctx).
		Model(&models.Cluster{}).
		Select("MIN(EXTRACT(EPOCH FROM (agent_cert_not_after - now())))").
		Where("state <> ? AND agent_cert_not_after IS NOT NULL", models.ClusterDecommissioned).
		Scan(&minRemaining).Error; err != nil {
		slog.Error("reconciler: agent cert expiry query", "error", err)
		return
	}
	if minRemaining == nil {
		metrics.SetAgentCertMinRemainingSeconds(0, false)
		return
	}
	metrics.SetAgentCertMinRemainingSeconds(*minRemaining, true)
}

// recordActiveAPIKeys publishes apikey_active_total: keys that are neither
// revoked nor expired. Every key has a deadline since the scoping migration
// (#69), so a key without expires_at is not counted as active.
func (r *Reconciler) recordActiveAPIKeys(ctx context.Context) {
	var active int64
	if err := r.db.RW.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("revoked_at IS NULL AND expires_at > now()").
		Count(&active).Error; err != nil {
		slog.Error("reconciler: active api key query", "error", err)
		return
	}
	metrics.APIKeyActiveTotal.Set(float64(active))
}

// setAuditSubmissionStateGauges sets audit_submission_state_total for every
// state in models.AllAppVersionStates, writing 0 for states missing from
// counts.
func setAuditSubmissionStateGauges(counts map[models.AppVersionState]int64) {
	for _, s := range models.AllAppVersionStates {
		metrics.AuditSubmissionStateGauge(string(s)).Set(float64(counts[s]))
	}
}
