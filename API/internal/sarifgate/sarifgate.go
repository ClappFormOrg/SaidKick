// Package sarifgate reports SARIF findings that the stricter security-scan
// rule would block while the gate runs with SARIF_GATE_MODE=report.
//
// The rule itself lives in internal/manifest (EvaluateSARIF and
// SARIFGateMode). This package only records its report-mode output, in three
// places so the blast radius of switching to enforce can be sized: one
// structured log line per finding, the sarif_gate_would_block_total counter,
// and one audit event per submission.
package sarifgate

import (
	"context"
	"log/slog"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
)

// EventWouldBlock is the audit event type written once per submission that
// carries at least one would-block finding.
const EventWouldBlock = "sarif_gate.would_block"

// Subject identifies the submission the findings belong to. AppID is
// required for the audit event; the other IDs are recorded when known.
type Subject struct {
	AppID               uuid.UUID
	AppSlug             string
	AppVersionID        uuid.UUID
	AuditSubmissionID   uuid.UUID
	GitSHA              string
	ActorUserID         *uuid.UUID
	ActorOrganizationID *uuid.UUID
}

// Reporter records findings the stricter rule would block. Implementations
// are best-effort: a failure to report never fails the submission.
type Reporter interface {
	ReportWouldBlock(ctx context.Context, s Subject, findings []manifest.SARIFFinding)
}

// NewReporter returns the production Reporter: slog, the VictoriaMetrics
// counter, and the given audit logger.
func NewReporter(a audit.Logger) Reporter {
	return &reporter{
		audit: a,
		count: func(source string) { metrics.SARIFGateWouldBlockTotal(source).Inc() },
	}
}

type reporter struct {
	audit audit.Logger
	count func(source string)
	log   *slog.Logger // nil means slog.Default() at call time
}

func (r *reporter) ReportWouldBlock(ctx context.Context, s Subject, findings []manifest.SARIFFinding) {
	if len(findings) == 0 {
		return
	}
	log := r.log
	if log == nil {
		log = slog.Default()
	}

	payloadFindings := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		log.WarnContext(ctx, "sarif gate: finding would block under enforce",
			"mode", string(manifest.SARIFGateReport),
			"app_id", s.AppID.String(),
			"app_slug", s.AppSlug,
			"app_version_id", s.AppVersionID.String(),
			"audit_submission_id", s.AuditSubmissionID.String(),
			"git_sha", s.GitSHA,
			"rule_id", f.RuleID,
			"severity", f.Severity,
			"source", f.Source,
			"level", f.Level,
		)
		r.count(f.Source)
		payloadFindings = append(payloadFindings, map[string]any{
			"rule_id":  f.RuleID,
			"severity": f.Severity,
			"source":   f.Source,
			"level":    f.Level,
		})
	}

	if r.audit == nil || s.AppID == uuid.Nil {
		return
	}
	if err := r.audit.Log(ctx, audit.Event{
		Type:                EventWouldBlock,
		ActorUserID:         s.ActorUserID,
		ActorOrganizationID: s.ActorOrganizationID,
		SubjectType:         "app",
		SubjectID:           s.AppID,
		Payload: map[string]any{
			"mode":                string(manifest.SARIFGateReport),
			"app_slug":            s.AppSlug,
			"app_version_id":      s.AppVersionID.String(),
			"audit_submission_id": s.AuditSubmissionID.String(),
			"git_sha":             s.GitSHA,
			"finding_count":       len(findings),
			"findings":            payloadFindings,
		},
	}); err != nil {
		log.WarnContext(ctx, "sarif gate: audit event failed", "error", err, "app_id", s.AppID.String())
	}
}
