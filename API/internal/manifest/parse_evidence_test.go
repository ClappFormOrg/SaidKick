package manifest

import (
	"context"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
)

func TestParse(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		if _, err := Parse(nil); err == nil {
			t.Fatal("expected error for empty payload")
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		if _, err := Parse([]byte("\tnot: [valid")); err == nil {
			t.Fatal("expected a yaml parse error")
		}
	})
	t.Run("valid", func(t *testing.T) {
		y := []byte(`
apiVersion: appstore.saidkick.nl/v1
kind: AppManifest
metadata:
  name: govchat
  display_name: GovChat
  license: EUPL-1.2
spec:
  chart: { path: ./chart, type: helm }
  ai: { risk_class: limited, data_residency: EU }
`)
		m, err := Parse(y)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if m.Metadata.Name != "govchat" || m.Spec.Chart.Type != "helm" || m.Spec.AI.DataResidency != "EU" {
			t.Fatalf("unexpected parse result: %+v", m)
		}
	})
}

func TestValidateEvidenceHappyPath(t *testing.T) {
	m := validManifest()
	fake := githubtest.MemoryClient{Files: githubtest.EvidenceFiles()}

	resolved, wouldBlock, errs := m.ValidateEvidence(context.Background(), fake, 1, "clappform", "govchat", "sha", SARIFGateEnforce)
	if len(errs) != 0 {
		t.Fatalf("expected no field errors, got %d: %+v", len(errs), errs)
	}
	if len(wouldBlock) != 0 {
		t.Fatalf("enforce mode must not return report-only findings, got %+v", wouldBlock)
	}
	// 5 audit/demo files + 4 screenshots.
	if len(resolved) != 9 {
		t.Fatalf("expected 9 resolved files, got %d", len(resolved))
	}
	for _, r := range resolved {
		if r.ContentSHA256 == "" || r.SizeBytes == 0 || r.ContentType == "" {
			t.Errorf("resolved file %q missing metadata: %+v", r.LogicalName, r)
		}
	}
}

func TestValidateEvidenceMissingFile(t *testing.T) {
	m := validManifest()
	files := githubtest.EvidenceFiles()
	delete(files, "audit/dpia.pdf")
	fake := githubtest.MemoryClient{Files: files}

	_, _, errs := m.ValidateEvidence(context.Background(), fake, 1, "clappform", "govchat", "sha", SARIFGateReport)
	var found bool
	for _, e := range errs {
		if e.Path == "spec.audit.dpia" && e.Code == "file_not_found" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a file_not_found error for spec.audit.dpia, got %+v", errs)
	}
}

// TestValidateEvidenceBlockingSARIF: a finding the legacy rule blocks
// (level error, result-level severity) blocks in both modes.
func TestValidateEvidenceBlockingSARIF(t *testing.T) {
	for _, mode := range []SARIFGateMode{SARIFGateReport, SARIFGateEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			m := validManifest()
			files := githubtest.EvidenceFiles()
			files["audit/scan.sarif"] = []byte(
				`{"runs":[{"results":[{"level":"error","ruleId":"sqli","properties":{"security-severity":"9.1"}}]}]}`)
			fake := githubtest.MemoryClient{Files: files}

			_, wouldBlock, errs := m.ValidateEvidence(context.Background(), fake, 1, "clappform", "govchat", "sha", mode)
			if countCode(errs, "sarif_high_severity_finding") != 1 {
				t.Fatalf("expected one blocking SARIF finding, got %+v", errs)
			}
			if len(wouldBlock) != 0 {
				t.Fatalf("a blocking finding must not also be reported, got %+v", wouldBlock)
			}
		})
	}
}

// TestValidateEvidenceSARIFGateModes: a Trivy-shaped finding (rule-level
// severity, level warning) only the stricter rule catches is reported but
// not blocking in report mode, and blocking in enforce mode.
func TestValidateEvidenceSARIFGateModes(t *testing.T) {
	trivy := []byte(`{"version":"2.1.0","runs":[{
		"tool":{"driver":{"name":"Trivy","rules":[{"id":"CVE-2024-0001","properties":{"security-severity":"9.8"}}]}},
		"results":[{"ruleId":"CVE-2024-0001","level":"warning","message":{"text":"Package: openssl"}}]}]}`)

	newEvidence := func() githubtest.MemoryClient {
		files := githubtest.EvidenceFiles()
		files["audit/scan.sarif"] = trivy
		return githubtest.MemoryClient{Files: files}
	}

	t.Run("report", func(t *testing.T) {
		resolved, wouldBlock, errs := validManifest().ValidateEvidence(
			context.Background(), newEvidence(), 1, "clappform", "govchat", "sha", SARIFGateReport)
		if len(errs) != 0 {
			t.Fatalf("report mode must not block a stricter-rule-only finding, got %+v", errs)
		}
		if len(resolved) != 9 {
			t.Fatalf("expected 9 resolved files, got %d", len(resolved))
		}
		if len(wouldBlock) != 1 {
			t.Fatalf("want 1 would-block finding, got %+v", wouldBlock)
		}
		f := wouldBlock[0]
		if f.RuleID != "CVE-2024-0001" || f.Severity != 9.8 || f.Source != SARIFSourceRule || f.Level != "warning" {
			t.Fatalf("unexpected finding %+v", f)
		}
		if f.BlocksOld || !f.BlocksNew {
			t.Fatalf("verdicts wrong: %+v", f)
		}
	})

	t.Run("enforce", func(t *testing.T) {
		_, wouldBlock, errs := validManifest().ValidateEvidence(
			context.Background(), newEvidence(), 1, "clappform", "govchat", "sha", SARIFGateEnforce)
		if countCode(errs, "sarif_high_severity_finding") != 1 {
			t.Fatalf("enforce mode must block, got %+v", errs)
		}
		if len(wouldBlock) != 0 {
			t.Fatalf("enforce mode must not report, got %+v", wouldBlock)
		}
	})
}
