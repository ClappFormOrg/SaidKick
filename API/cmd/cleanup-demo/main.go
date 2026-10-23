// Command cleanup-demo removes demo apps and clusters from a database. It
// reads a list of names and, by default, only prints what it would delete.
//
// Usage:
//
//	go run ./cmd/cleanup-demo -names ../scripts/demo-names.txt          # dry run
//	go run ./cmd/cleanup-demo -names ../scripts/demo-names.txt -apply   # delete
//
// The names file has one entry per line: "app:<slug>" or "cluster:<name>".
// Blank lines and lines starting with # are ignored. Cluster names are not
// unique, so every cluster with a listed name matches.
//
// With -apply:
//
//   - An app is soft-deleted (deleted_at). Its versions and audit submissions
//     have no deleted_at column; every read path reaches them through a live
//     app, so they drop out with it. Deployment rows already in state
//     deleted are soft-deleted too. An app with any other live deployment is
//     skipped: tear those down through DELETE /api/v1/deployments/{id} first,
//     because this command cannot reach the orchestrator or the secret store.
//   - A cluster is removed the way DELETE /api/v1/clusters/{id} removes it.
//     Clusters have no deleted_at column, so that is a row delete. A cluster
//     that any deployment row still references, live or soft-deleted, is
//     skipped.
//
// Every deleted row gets one demo_data.deleted audit event.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/gorm"
)

// auditEventType is written once for every row the command deletes.
const auditEventType = "demo_data.deleted"

func main() {
	namesPath := flag.String("names", "scripts/demo-names.txt", "file listing app:<slug> and cluster:<name> entries")
	apply := flag.Bool("apply", false, "delete the matching rows; without it the command only prints them")
	flag.Parse()

	if err := run(*namesPath, *apply); err != nil {
		log.Fatalf("cleanup-demo: %v", err)
	}
}

func run(namesPath string, apply bool) error {
	f, err := os.Open(namesPath)
	if err != nil {
		return fmt.Errorf("open names file: %w", err)
	}
	list, err := parseNames(f)
	if cErr := f.Close(); cErr != nil && err == nil {
		err = cErr
	}
	if err != nil {
		return fmt.Errorf("%s: %w", namesPath, err)
	}

	_ = godotenv.Load()

	dbCfg, err := db.Load()
	if err != nil {
		return fmt.Errorf("load db config: %w", err)
	}
	handle, err := db.Open(dbCfg)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if cErr := handle.Close(); cErr != nil {
			slog.Warn("close db", "error", cErr)
		}
	}()

	return cleanup(context.Background(), handle, audit.NewLogger(handle), os.Stdout, list, apply)
}

// cleanup prints the plan for n to w and, when apply is set, carries it out.
func cleanup(ctx context.Context, h *db.Handle, logger audit.Logger, w io.Writer, n names, apply bool) error {
	p, err := buildPlan(ctx, h, n)
	if err != nil {
		return err
	}
	printPlan(w, p, apply)
	if !apply {
		return nil
	}
	deleted, err := applyPlan(ctx, h, logger, p)
	_, _ = fmt.Fprintf(w, "deleted %d rows\n", deleted)
	return err
}

// names is the parsed content of a names file.
type names struct {
	Apps     []string
	Clusters []string
}

// parseNames reads one "app:<slug>" or "cluster:<name>" entry per line.
func parseNames(r io.Reader) (names, error) {
	var n names
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		kind, value, ok := strings.Cut(text, ":")
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return names{}, fmt.Errorf("line %d: want app:<slug> or cluster:<name>, got %q", line, text)
		}
		switch strings.TrimSpace(kind) {
		case "app":
			n.Apps = append(n.Apps, value)
		case "cluster":
			n.Clusters = append(n.Clusters, value)
		default:
			return names{}, fmt.Errorf("line %d: unknown kind %q, want app or cluster", line, kind)
		}
	}
	if err := sc.Err(); err != nil {
		return names{}, err
	}
	return n, nil
}

// appPlan is one live app and what blocks or accompanies its deletion.
type appPlan struct {
	App models.App
	// Deleted holds the app's live deployment rows in state deleted; they
	// are soft-deleted with the app.
	Deleted []models.Deployment
	// Active counts live deployments in any other state. Above zero the app
	// is skipped.
	Active int64
}

// clusterPlan is one cluster and the number of deployment rows, live or
// soft-deleted, that still reference it. Above zero it is skipped.
type clusterPlan struct {
	Cluster models.Cluster
	Refs    int64
}

// plan is what the command matched in the database.
type plan struct {
	Apps     []appPlan
	Clusters []clusterPlan
	// Missing lists names that matched nothing, as "app:<slug>" or
	// "cluster:<name>".
	Missing []string
}

// buildPlan looks up every listed name. It only reads.
func buildPlan(ctx context.Context, h *db.Handle, n names) (plan, error) {
	var p plan
	tx := h.RW.WithContext(ctx)

	for _, slug := range n.Apps {
		var app models.App
		err := tx.Where("slug = ?", slug).First(&app).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			p.Missing = append(p.Missing, "app:"+slug)
			continue
		}
		if err != nil {
			return plan{}, fmt.Errorf("look up app %q: %w", slug, err)
		}
		ap := appPlan{App: app}
		if err := tx.Where("app_id = ? AND state = ?", app.ID, models.DeploymentDeleted).
			Order("created_at").Find(&ap.Deleted).Error; err != nil {
			return plan{}, fmt.Errorf("list deployments of app %q: %w", slug, err)
		}
		if err := tx.Model(&models.Deployment{}).
			Where("app_id = ? AND state <> ?", app.ID, models.DeploymentDeleted).
			Count(&ap.Active).Error; err != nil {
			return plan{}, fmt.Errorf("count deployments of app %q: %w", slug, err)
		}
		p.Apps = append(p.Apps, ap)
	}

	for _, name := range n.Clusters {
		var clusters []models.Cluster
		if err := tx.Where("name = ?", name).Order("id").Find(&clusters).Error; err != nil {
			return plan{}, fmt.Errorf("look up cluster %q: %w", name, err)
		}
		if len(clusters) == 0 {
			p.Missing = append(p.Missing, "cluster:"+name)
			continue
		}
		for _, cl := range clusters {
			cp := clusterPlan{Cluster: cl}
			if err := tx.Unscoped().Model(&models.Deployment{}).
				Where("cluster_id = ?", cl.ID).Count(&cp.Refs).Error; err != nil {
				return plan{}, fmt.Errorf("count deployments on cluster %q: %w", name, err)
			}
			p.Clusters = append(p.Clusters, cp)
		}
	}
	return p, nil
}

// printPlan writes one line per matched row and per unmatched name.
func printPlan(w io.Writer, p plan, apply bool) {
	verb := "would delete"
	if apply {
		verb = "deleting"
	}
	for _, ap := range p.Apps {
		if ap.Active > 0 {
			_, _ = fmt.Fprintf(w, "app %s (%s): skipped, %d live deployment(s); delete them through the API first\n",
				ap.App.Slug, ap.App.ID, ap.Active)
			continue
		}
		_, _ = fmt.Fprintf(w, "app %s (%s, %s): %s\n", ap.App.Slug, ap.App.ID, ap.App.State, verb)
		for _, d := range ap.Deleted {
			_, _ = fmt.Fprintf(w, "  deployment %s (namespace %s): %s\n", d.ID, d.Namespace, verb)
		}
	}
	for _, cp := range p.Clusters {
		if cp.Refs > 0 {
			_, _ = fmt.Fprintf(w, "cluster %q (%s): skipped, referenced by %d deployment row(s)\n",
				cp.Cluster.Name, cp.Cluster.ID, cp.Refs)
			continue
		}
		_, _ = fmt.Fprintf(w, "cluster %q (%s, %s): %s\n", cp.Cluster.Name, cp.Cluster.ID, cp.Cluster.State, verb)
	}
	for _, m := range p.Missing {
		_, _ = fmt.Fprintf(w, "%s: not found\n", m)
	}
}

// applyPlan deletes every row in p that is not blocked and writes one audit
// event per deleted row. It returns the number of rows deleted. A failure
// stops the run; rows deleted before it stay deleted.
func applyPlan(ctx context.Context, h *db.Handle, logger audit.Logger, p plan) (int, error) {
	deleted := 0
	for _, ap := range p.Apps {
		if ap.Active > 0 {
			continue
		}
		if err := deleteApp(ctx, h, ap); err != nil {
			return deleted, fmt.Errorf("app %s: %w", ap.App.Slug, err)
		}
		for _, d := range ap.Deleted {
			if err := logDeleted(ctx, logger, "deployment", d.ID, map[string]any{
				"app_slug": ap.App.Slug, "namespace": d.Namespace, "cluster_id": d.ClusterID.String(),
			}); err != nil {
				return deleted, err
			}
			deleted++
		}
		if err := logDeleted(ctx, logger, "app", ap.App.ID, map[string]any{
			"slug": ap.App.Slug, "state": ap.App.State,
		}); err != nil {
			return deleted, err
		}
		deleted++
	}

	for _, cp := range p.Clusters {
		if cp.Refs > 0 {
			continue
		}
		if err := deleteCluster(ctx, h, cp.Cluster); err != nil {
			return deleted, fmt.Errorf("cluster %q: %w", cp.Cluster.Name, err)
		}
		if err := logDeleted(ctx, logger, "cluster", cp.Cluster.ID, map[string]any{
			"name": cp.Cluster.Name, "scope": cp.Cluster.Scope, "state": cp.Cluster.State,
		}); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// errChanged reports that a row changed between the plan and the delete.
var errChanged = errors.New("changed since the plan was printed; run again")

// deleteApp soft-deletes the app and its deployment rows in state deleted,
// re-checking inside the transaction that no other live deployment appeared.
func deleteApp(ctx context.Context, h *db.Handle, ap appPlan) error {
	return h.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&models.Deployment{}).
			Where("app_id = ? AND state <> ?", ap.App.ID, models.DeploymentDeleted).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errChanged
		}
		for i := range ap.Deleted {
			if err := tx.Delete(&ap.Deleted[i]).Error; err != nil {
				return err
			}
		}
		res := tx.Delete(&ap.App)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errChanged
		}
		return nil
	})
}

// deleteCluster deletes the cluster row, re-checking inside the transaction
// that no deployment row references it.
func deleteCluster(ctx context.Context, h *db.Handle, cl models.Cluster) error {
	return h.RW.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refs int64
		if err := tx.Unscoped().Model(&models.Deployment{}).
			Where("cluster_id = ?", cl.ID).Count(&refs).Error; err != nil {
			return err
		}
		if refs > 0 {
			return errChanged
		}
		res := tx.Delete(&cl)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errChanged
		}
		return nil
	})
}

// logDeleted writes the demo_data.deleted event for one row. The command
// has no user, so the actor is left empty as for other system events.
func logDeleted(ctx context.Context, logger audit.Logger, subjectType string, id uuid.UUID, payload map[string]any) error {
	if err := logger.Log(ctx, audit.Event{
		Type:        auditEventType,
		SubjectType: subjectType,
		SubjectID:   id,
		Payload:     payload,
	}); err != nil {
		return fmt.Errorf("audit %s %s: %w", subjectType, id, err)
	}
	return nil
}
