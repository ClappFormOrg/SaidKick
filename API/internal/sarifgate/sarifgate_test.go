package sarifgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
)

type fakeAudit struct {
	events []audit.Event
	err    error
}

func (f *fakeAudit) Log(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return f.err
}

func newTestReporter(a audit.Logger) (*reporter, *[]string, *bytes.Buffer) {
	var counted []string
	var buf bytes.Buffer
	return &reporter{
		audit: a,
		count: func(source string) { counted = append(counted, source) },
		log:   slog.New(slog.NewJSONHandler(&buf, nil)),
	}, &counted, &buf
}

var testFindings = []manifest.SARIFFinding{
	{RuleID: "CVE-2024-0001", Level: "warning", Severity: 9.8, Source: manifest.SARIFSourceRule, BlocksNew: true},
	{RuleID: "xss", Level: "warning", Severity: 8.0, Source: manifest.SARIFSourceResult, BlocksNew: true},
}

func testSubject() Subject {
	user, org := uuid.New(), uuid.New()
	return Subject{
		AppID:               uuid.New(),
		AppSlug:             "govchat",
		AppVersionID:        uuid.New(),
		AuditSubmissionID:   uuid.New(),
		GitSHA:              "abc1234",
		ActorUserID:         &user,
		ActorOrganizationID: &org,
	}
}

func TestReportWouldBlock(t *testing.T) {
	fa := &fakeAudit{}
	r, counted, buf := newTestReporter(fa)
	s := testSubject()

	r.ReportWouldBlock(context.Background(), s, testFindings)

	// Metric: one increment per finding, labelled by severity source.
	if got := strings.Join(*counted, ","); got != "rule,result" {
		t.Fatalf("counter calls = %q, want %q", got, "rule,result")
	}

	// Log: one structured line per finding carrying the IDs and the finding.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %s", len(lines), buf.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	for k, want := range map[string]any{
		"app_id":              s.AppID.String(),
		"app_slug":            "govchat",
		"app_version_id":      s.AppVersionID.String(),
		"audit_submission_id": s.AuditSubmissionID.String(),
		"git_sha":             "abc1234",
		"rule_id":             "CVE-2024-0001",
		"severity":            9.8,
		"source":              "rule",
		"mode":                "report",
	} {
		if line[k] != want {
			t.Errorf("log %s = %v, want %v", k, line[k], want)
		}
	}

	// Audit: one event per submission listing every finding.
	if len(fa.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(fa.events))
	}
	e := fa.events[0]
	if e.Type != "sarif_gate.would_block" {
		t.Fatalf("event type = %q", e.Type)
	}
	if e.SubjectType != "app" || e.SubjectID != s.AppID || e.ActorUserID != s.ActorUserID || e.ActorOrganizationID != s.ActorOrganizationID {
		t.Fatalf("event subject/actor wrong: %+v", e)
	}
	p, ok := e.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", e.Payload)
	}
	if p["audit_submission_id"] != s.AuditSubmissionID.String() || p["app_version_id"] != s.AppVersionID.String() || p["finding_count"] != 2 {
		t.Fatalf("payload IDs/count wrong: %+v", p)
	}
	fs, ok := p["findings"].([]map[string]any)
	if !ok || len(fs) != 2 || fs[0]["rule_id"] != "CVE-2024-0001" || fs[1]["source"] != "result" {
		t.Fatalf("payload findings wrong: %+v", p["findings"])
	}
}

func TestReportWouldBlockNoFindings(t *testing.T) {
	fa := &fakeAudit{}
	r, counted, buf := newTestReporter(fa)

	r.ReportWouldBlock(context.Background(), testSubject(), nil)

	if len(*counted) != 0 || len(fa.events) != 0 || buf.Len() != 0 {
		t.Fatalf("no findings must report nothing: counts=%v events=%d log=%q", *counted, len(fa.events), buf.String())
	}
}

// TestReportWouldBlockAuditFailure: an audit write failure is logged and
// swallowed; the metric and per-finding log still happen.
func TestReportWouldBlockAuditFailure(t *testing.T) {
	fa := &fakeAudit{err: errors.New("db down")}
	r, counted, buf := newTestReporter(fa)

	r.ReportWouldBlock(context.Background(), testSubject(), testFindings[:1])

	if len(*counted) != 1 {
		t.Fatalf("counter must still increment, got %v", *counted)
	}
	if !strings.Contains(buf.String(), "audit event failed") {
		t.Fatalf("audit failure must be logged: %s", buf.String())
	}
}

// TestReportWouldBlockWithoutAppID: no audit subject means no audit event,
// but the finding is still logged and counted.
func TestReportWouldBlockWithoutAppID(t *testing.T) {
	fa := &fakeAudit{}
	r, counted, _ := newTestReporter(fa)

	r.ReportWouldBlock(context.Background(), Subject{AppSlug: "govchat"}, testFindings[:1])

	if len(fa.events) != 0 {
		t.Fatalf("audit event needs an app ID, got %+v", fa.events)
	}
	if len(*counted) != 1 {
		t.Fatalf("counter must still increment, got %v", *counted)
	}
}

// TestNewReporterIncrementsMetric checks the production wiring hits the
// sarif_gate_would_block_total counter.
func TestNewReporterIncrementsMetric(t *testing.T) {
	c := metrics.SARIFGateWouldBlockTotal(manifest.SARIFSourceRule)
	before := c.Get()

	NewReporter(&fakeAudit{}).ReportWouldBlock(context.Background(), testSubject(), testFindings[:1])

	if got := c.Get(); got != before+1 {
		t.Fatalf("sarif_gate_would_block_total{source=\"rule\"} = %d, want %d", got, before+1)
	}
}
