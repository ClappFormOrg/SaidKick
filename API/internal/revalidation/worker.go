// Package revalidation runs a background worker that re-validates app manifests
// when a push to an app's audited branch is received via the GitHub webhook.
//
// The webhook push handler creates a RevalidationJob row; this worker polls
// for pending jobs, re-runs Stage-1 manifest + evidence validation at the new
// HEAD commit, and creates a new AppVersion + AuditSubmission on success.
package revalidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/sarifgate"
	"github.com/ClappFormOrg/SaidKick/API/internal/valuesschema"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	defaultInterval = 30 * time.Second
	manifestFile    = "appstore.yaml"
)

// Worker polls for pending RevalidationJobs and processes them.
type Worker struct {
	db        *db.Handle
	github    gh.Client
	audit     audit.Logger
	sarifMode manifest.SARIFGateMode
	reporter  sarifgate.Reporter
	interval  time.Duration
}

// New constructs a Worker. sarifMode selects the Stage-1 SARIF rule (see
// manifest.SARIFGateMode). Pass interval=0 to use the default (30s).
func New(h *db.Handle, ghClient gh.Client, auditor audit.Logger, sarifMode manifest.SARIFGateMode, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Worker{
		db:        h,
		github:    ghClient,
		audit:     auditor,
		sarifMode: sarifMode,
		reporter:  sarifgate.NewReporter(auditor),
		interval:  interval,
	}
}

// Run blocks until ctx is cancelled. Call it in a goroutine from main.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	slog.Info("revalidation worker: started", "interval", w.interval)
	for {
		select {
		case <-ctx.Done():
			slog.Info("revalidation worker: stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	var jobs []models.RevalidationJob
	if err := w.db.RW.WithContext(ctx).
		Where("status = ?", models.RevalidationPending).
		Order("created_at ASC").
		Limit(10).
		Find(&jobs).Error; err != nil {
		slog.Error("revalidation worker: query jobs", "error", err)
		return
	}

	for _, job := range jobs {
		w.processJob(ctx, job)
	}
}

func (w *Worker) processJob(ctx context.Context, job models.RevalidationJob) {
	// Mark as processing so other workers (future multi-instance) don't claim it.
	now := time.Now().UTC()
	if err := w.db.RW.WithContext(ctx).Model(&job).
		Updates(map[string]any{
			"status":     models.RevalidationProcessing,
			"started_at": now,
		}).Error; err != nil {
		slog.Error("revalidation worker: mark job processing", "error", err, "job_id", job.ID)
		return
	}

	if err := w.validate(ctx, job); err != nil {
		errMsg := err.Error()
		finished := time.Now().UTC()
		if dbErr := w.db.RW.WithContext(ctx).Model(&job).Updates(map[string]any{
			"status":      models.RevalidationFailed,
			"error":       errMsg,
			"finished_at": finished,
		}).Error; dbErr != nil {
			slog.Error("revalidation worker: persist job failure", "error", dbErr, "job_id", job.ID)
		}
		slog.Error("revalidation worker: job failed", "error", err, "job_id", job.ID)
		_ = w.audit.Log(ctx, audit.Event{
			Type:        "app.revalidation_failed",
			SubjectType: "app",
			SubjectID:   job.AppID,
			Payload:     map[string]any{"new_head": job.NewHead, "error": errMsg},
		})
		return
	}

	finished := time.Now().UTC()
	if dbErr := w.db.RW.WithContext(ctx).Model(&job).Updates(map[string]any{
		"status":      models.RevalidationDone,
		"finished_at": finished,
	}).Error; dbErr != nil {
		slog.Error("revalidation worker: persist job success", "error", dbErr, "job_id", job.ID)
	}
	slog.Info("revalidation worker: job done", "job_id", job.ID)
}

// validate performs the full re-validation run for one job. Returns a
// non-nil error when validation fails or a DB/GitHub error occurs.
func (w *Worker) validate(ctx context.Context, job models.RevalidationJob) error {
	// 1. Load app.
	var app models.App
	if err := w.db.RW.WithContext(ctx).First(&app, "id = ?", job.AppID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("app %q not found (deleted?)", job.AppID)
		}
		return fmt.Errorf("load app: %w", err)
	}

	// 2. Dedup: skip if we already have a version at this SHA.
	var existing int64
	if err := w.db.RW.WithContext(ctx).Model(&models.AppVersion{}).
		Where("app_id = ? AND git_sha = ?", app.ID, job.NewHead).
		Count(&existing).Error; err != nil {
		return fmt.Errorf("dedup check: %w", err)
	}
	if existing > 0 {
		slog.Info("revalidation worker: version already exists, skipping", "job_id", job.ID, "sha", shortSHA(job.NewHead))
		return nil
	}

	// 3. Fetch appstore.yaml at NewHead.
	manifestBytes, _, err := w.github.GetFileAtRef(ctx,
		app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName,
		manifestFile, job.NewHead)
	if err != nil {
		countFetchRejection(err, metrics.GateSchema)
		return fmt.Errorf("fetch appstore.yaml at %s: %w", shortSHA(job.NewHead), err)
	}

	// 4. Parse + schema validate.
	m, err := manifest.Parse(manifestBytes)
	if err != nil {
		metrics.RecordSubmission([]string{metrics.GateSchema})
		return fmt.Errorf("parse appstore.yaml: %w", err)
	}
	if schemaErrs := m.ValidateSchema(); len(schemaErrs) > 0 {
		countRejection(manifest.RejectedGates(schemaErrs))
		msgs := make([]string, len(schemaErrs))
		for i, e := range schemaErrs {
			msgs[i] = e.Path + ": " + e.Message
		}
		return fmt.Errorf("schema validation failed: %s", strings.Join(msgs, "; "))
	}

	// 5. Stage-1 evidence validation.
	evidence, sarifWouldBlock, evErrs := m.ValidateEvidence(ctx, w.github,
		app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName, job.NewHead, w.sarifMode)
	if len(evErrs) > 0 {
		countRejection(manifest.RejectedGates(evErrs))
		msgs := make([]string, len(evErrs))
		for i, e := range evErrs {
			msgs[i] = e.Path + ": " + e.Message
		}
		return fmt.Errorf("evidence validation failed: %s", strings.Join(msgs, "; "))
	}

	// 6. Fetch Chart.yaml + values.schema.json.
	chartDir := strings.TrimPrefix(m.Spec.Chart.Path, "./")
	chartYamlBytes, _, err := w.github.GetFileAtRef(ctx,
		app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName,
		path.Join(chartDir, "Chart.yaml"), job.NewHead)
	if err != nil {
		countFetchRejection(err, metrics.GateChart)
		return fmt.Errorf("fetch Chart.yaml at %s: %w", shortSHA(job.NewHead), err)
	}
	valuesSchemaBytes, _, err := w.github.GetFileAtRef(ctx,
		app.GitHubInstallationID, app.GitHubRepoOwner, app.GitHubRepoName,
		path.Join(chartDir, "values.schema.json"), job.NewHead)
	if err != nil {
		countFetchRejection(err, metrics.GateChart)
		return fmt.Errorf("fetch values.schema.json at %s: %w", shortSHA(job.NewHead), err)
	}

	// 7. Normalise YAML → JSON for jsonb columns.
	manifestJSON, err := yamlToJSON(manifestBytes)
	if err != nil {
		metrics.RecordSubmission([]string{metrics.GateSchema})
		return fmt.Errorf("normalise appstore.yaml: %w", err)
	}
	chartMetaJSON, err := yamlToJSON(chartYamlBytes)
	if err != nil {
		metrics.RecordSubmission([]string{metrics.GateChart})
		return fmt.Errorf("normalise Chart.yaml: %w", err)
	}
	if err := valuesschema.Enforceable(valuesSchemaBytes); err != nil {
		metrics.RecordSubmission([]string{metrics.GateChart})
		return fmt.Errorf("values.schema.json cannot be enforced: %w", err)
	}

	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	// 8. Persist new AppVersion + AuditSubmission + EvidenceBlobs.
	var verUUID, subUUID uuid.UUID
	err = w.db.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		subID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for submission: %w", err)
		}
		verID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("uuid for version: %w", err)
		}

		sub := models.AuditSubmission{
			AppVersionID:        verID,
			FormResponses:       datatypes.JSON(`{}`),
			FormVersion:         "v1.0",
			EvidenceFiles:       datatypes.JSON(evidenceJSON),
			AutoValidationState: models.AutoValPassed,
		}
		sub.ID = subID

		ver := models.AppVersion{
			AppID:             app.ID,
			GitRef:            app.GitHubDefaultBranch,
			GitSHA:            job.NewHead,
			AppstoreYAML:      datatypes.JSON(manifestJSON),
			ValuesSchema:      datatypes.JSON(valuesSchemaBytes),
			ChartMetadata:     datatypes.JSON(chartMetaJSON),
			AuditSubmissionID: subID,
			State:             models.AppVersionDraft,
		}
		ver.ID = verID

		if err := tx.Create(&sub).Error; err != nil {
			return fmt.Errorf("create submission: %w", err)
		}
		if err := tx.Create(&ver).Error; err != nil {
			return fmt.Errorf("create version: %w", err)
		}

		for _, ev := range evidence {
			blob := models.EvidenceBlob{
				AuditSubmissionID: subID,
				LogicalName:       ev.LogicalName,
				RepoPath:          ev.RepoPath,
				ContentType:       ev.ContentType,
				Content:           ev.Content,
			}
			if err := tx.Create(&blob).Error; err != nil {
				return fmt.Errorf("persist evidence blob %q: %w", ev.LogicalName, err)
			}
		}

		_ = w.audit.Log(ctx, audit.Event{
			Type:        "app.revalidation_complete",
			SubjectType: "app",
			SubjectID:   app.ID,
			Payload: map[string]any{
				"new_head":       job.NewHead,
				"app_version_id": ver.ID.String(),
				"evidence_count": len(evidence),
			},
		})
		verUUID, subUUID = ver.ID, subID
		return nil
	})
	if err != nil {
		return err
	}
	metrics.RecordSubmission(nil)

	// 9. Report SARIF findings only the stricter rule would block (report
	// mode), now that the submission IDs exist.
	if len(sarifWouldBlock) > 0 {
		w.reporter.ReportWouldBlock(ctx, sarifgate.Subject{
			AppID:             app.ID,
			AppSlug:           app.Slug,
			AppVersionID:      verUUID,
			AuditSubmissionID: subUUID,
			GitSHA:            job.NewHead,
		}, sarifWouldBlock)
	}
	return nil
}

// countRejection counts a submission rejected by gates. With no gates (only
// infrastructure errors) nothing is counted.
func countRejection(gates []string) {
	if len(gates) > 0 {
		metrics.RecordSubmission(gates)
	}
}

// countFetchRejection counts a missing or oversized required file as a
// rejection by gate. Other fetch errors say nothing about the submission.
func countFetchRejection(err error, gate string) {
	if errors.Is(err, gh.ErrFileNotFound) || errors.Is(err, gh.ErrFileTooLarge) {
		metrics.RecordSubmission([]string{gate})
	}
}

// shortSHA returns the first 7 characters of a git SHA, or the full string
// when it is shorter (guards against malformed DB values).
func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

// yamlToJSON converts YAML bytes to canonical JSON for jsonb storage.
func yamlToJSON(in []byte) ([]byte, error) {
	var root any
	if err := yaml.Unmarshal(in, &root); err != nil {
		return nil, err
	}
	clean, err := normaliseForJSON(root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(clean)
}

func normaliseForJSON(v any) (any, error) {
	switch typed := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			key, ok := k.(string)
			if !ok {
				key = fmt.Sprintf("%v", k)
			}
			cleaned, err := normaliseForJSON(val)
			if err != nil {
				return nil, err
			}
			out[key] = cleaned
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, el := range typed {
			cleaned, err := normaliseForJSON(el)
			if err != nil {
				return nil, err
			}
			out[i] = cleaned
		}
		return out, nil
	default:
		return v, nil
	}
}
