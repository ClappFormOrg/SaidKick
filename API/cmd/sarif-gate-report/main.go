// Command sarif-gate-report sizes the blast radius of switching the Stage-1
// SARIF gate from SARIF_GATE_MODE=report to enforce. It re-evaluates the
// security scan stored for every audit submission created in the last N days
// and prints how many would newly be rejected under the stricter rule.
//
// It reads only the database: the scan bytes come from evidence_blobs, which
// app creation, POST /apps/:slug/versions and webhook revalidation persist.
// Submissions without a stored scan (versions registered through
// POST /apps/:slug/versions before it stored evidence) are counted and
// listed but not fetched from GitHub. The command never writes.
//
// Usage:
//
//	go run ./cmd/sarif-gate-report              # last 30 days
//	go run ./cmd/sarif-gate-report -days 90     # wider window
//	go run ./cmd/sarif-gate-report -v           # also list unaffected submissions
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

func main() {
	days := flag.Int("days", 30, "look back this many days of audit submissions")
	verbose := flag.Bool("v", false, "list every submission, not only affected ones")
	flag.Parse()

	if err := run(*days, *verbose); err != nil {
		log.Fatalf("sarif-gate-report: %v", err)
	}
}

func run(days int, verbose bool) error {
	if days <= 0 {
		return fmt.Errorf("-days must be positive, got %d", days)
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

	since := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := loadRows(context.Background(), handle, since)
	if err != nil {
		return err
	}

	printReport(os.Stdout, since, days, evaluate(rows), verbose)
	return nil
}

// loadRows returns every submission created at or after since on a live app,
// oldest first, with its stored security scan when there is one.
func loadRows(ctx context.Context, h *db.Handle, since time.Time) ([]scanRow, error) {
	var rows []scanRow
	if err := h.RW.WithContext(ctx).Raw(recentScansSQL, since).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query submissions: %w", err)
	}
	return rows, nil
}

const recentScansSQL = `
SELECT s.id AS submission_id, s.created_at, v.id AS app_version_id, v.git_sha,
       a.slug AS app_slug, b.content AS sarif
FROM audit_submissions s
JOIN app_versions v ON v.id = s.app_version_id
JOIN apps a ON a.id = v.app_id AND a.deleted_at IS NULL
LEFT JOIN evidence_blobs b ON b.audit_submission_id = s.id AND b.logical_name = 'security_scan'
WHERE s.created_at >= ?
ORDER BY s.created_at`

// scanRow is one submission and its stored scan (nil when none is stored).
type scanRow struct {
	SubmissionID uuid.UUID
	CreatedAt    time.Time
	AppVersionID uuid.UUID
	GitSHA       string
	AppSlug      string
	SARIF        []byte
}

// outcome is the verdict for one submission.
type outcome struct {
	row        scanRow
	noScan     bool
	parseErr   error
	legacy     []manifest.SARIFFinding // blocked under both modes
	newlyBlock []manifest.SARIFFinding // blocked only under enforce
}

type summary struct {
	outcomes    []outcome
	noScan      int
	unparseable int
	legacy      int
	newly       int
}

// evaluate runs the gate rule over each stored scan.
func evaluate(rows []scanRow) summary {
	var s summary
	for _, r := range rows {
		o := outcome{row: r}
		switch {
		case len(r.SARIF) == 0:
			o.noScan = true
			s.noScan++
		default:
			findings, err := manifest.EvaluateSARIF(r.SARIF)
			if err != nil {
				o.parseErr = err
				s.unparseable++
				break
			}
			for _, f := range findings {
				if f.BlocksOld {
					o.legacy = append(o.legacy, f)
				} else {
					o.newlyBlock = append(o.newlyBlock, f)
				}
			}
			// A submission the legacy rule blocks fails today already, so it
			// is not counted as newly blocked even if it has other findings.
			switch {
			case len(o.legacy) > 0:
				s.legacy++
			case len(o.newlyBlock) > 0:
				s.newly++
			}
		}
		s.outcomes = append(s.outcomes, o)
	}
	return s
}

func printReport(w io.Writer, since time.Time, days int, s summary, verbose bool) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

	p("SARIF gate report: audit submissions since %s (%d days)\n\n", since.Format("2006-01-02"), days)
	p("  submissions                     %d\n", len(s.outcomes))
	p("  without a stored scan           %d\n", s.noScan)
	p("  stored scan not valid SARIF     %d\n", s.unparseable)
	p("  blocked by the legacy rule      %d\n", s.legacy)
	p("  newly blocked under enforce     %d\n\n", s.newly)

	for _, o := range s.outcomes {
		label := ""
		switch {
		case o.noScan:
			label = "NO-SCAN"
		case o.parseErr != nil:
			label = "INVALID"
		case len(o.legacy) > 0:
			label = "LEGACY"
		case len(o.newlyBlock) > 0:
			label = "NEW"
		}
		if label == "" && !verbose {
			continue
		}
		if label == "" {
			label = "PASS"
		}
		p("%-8s %s  app=%s version=%s submission=%s sha=%s",
			label, o.row.CreatedAt.UTC().Format(time.RFC3339), o.row.AppSlug,
			o.row.AppVersionID, o.row.SubmissionID, shortSHA(o.row.GitSHA))
		if len(o.newlyBlock) > 0 {
			p(" findings=%s", formatFindings(o.newlyBlock))
		}
		if o.parseErr != nil {
			p(" error=%q", o.parseErr.Error())
		}
		p("\n")
	}
}

func formatFindings(fs []manifest.SARIFFinding) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		level := f.Level
		if level == "" {
			level = "unset"
		}
		parts[i] = fmt.Sprintf("%s:%.1f:%s:%s", f.RuleID, f.Severity, f.Source, level)
	}
	return strings.Join(parts, ",")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
