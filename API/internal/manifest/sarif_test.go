package manifest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
)

// makeSARIF builds a syntactically valid SARIF 2.1.0 document with n results.
// Roughly one in ten results is a blocking finding (level=error,
// security-severity 7.0+); the rest are non-blocking (warnings, or low
// severity), which mirrors a real CodeQL/gitleaks scan where the bulk of
// results carry a properties bag we don't actually need to fully decode.
func makeSARIF(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"version":"2.1.0","runs":[{"results":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		if i%10 == 0 {
			// Blocking: error + high severity (encoded as a string, as CodeQL does).
			fmt.Fprintf(&sb,
				`{"level":"error","ruleId":"rule-%d","message":{"text":"finding %d"},`+
					`"properties":{"security-severity":"8.1","tags":["security","external/cwe/cwe-089"],"precision":"high"}}`,
				i, i)
		} else {
			// Non-blocking: warning with a low severity and a full properties bag.
			fmt.Fprintf(&sb,
				`{"level":"warning","ruleId":"rule-%d","message":{"text":"finding %d"},`+
					`"properties":{"security-severity":"3.5","tags":["maintainability","external/cwe/cwe-561"],"precision":"medium"}}`,
				i, i)
		}
	}
	sb.WriteString(`]}]}`)
	return []byte(sb.String())
}

func BenchmarkCheckSARIF(b *testing.B) {
	data := makeSARIF(500)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := checkSARIF(data, SARIFGateEnforce); err != nil {
			b.Fatal(err)
		}
	}
}

// TestCheckSARIFBlocking verifies the security gate still fires on exactly the
// blocking findings after any optimisation. This and the rule-level tests
// below run in enforce mode, where the stricter rule blocks.
func TestCheckSARIFBlocking(t *testing.T) {
	// 3 results: high-sev error (block), high-sev warning (level does not
	// matter -> block), low-sev error (below threshold -> skip).
	// Expect exactly 2 FieldErrors.
	doc := []byte(`{"version":"2.1.0","runs":[{"results":[
		{"level":"error","ruleId":"sqli","message":{"text":"SQL injection"},"properties":{"security-severity":"9.8"}},
		{"level":"warning","ruleId":"xss","properties":{"security-severity":"8.0"}},
		{"level":"error","ruleId":"style","properties":{"security-severity":"2.1"}}
	]}]}`)
	errs, err := checkEnforce(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("want 2 blocking findings, got %d: %+v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Message, "sqli") || !strings.Contains(errs[0].Message, "9.8") {
		t.Fatalf("message should name the rule and severity: %q", errs[0].Message)
	}
	if !strings.Contains(errs[1].Message, "xss") || !strings.Contains(errs[1].Message, "8.0") {
		t.Fatalf("high-severity warning should block: %q", errs[1].Message)
	}
	for _, e := range errs {
		if e.Code != "sarif_high_severity_finding" {
			t.Fatalf("unexpected code %q", e.Code)
		}
	}
}

// TestCheckSARIFRuleLevelSeverity covers documents where security-severity
// lives on runs[].tool.driver.rules[].properties rather than on the result,
// as Trivy and CodeQL emit it.
func TestCheckSARIFRuleLevelSeverity(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		wantRules []string
	}{
		{
			// Trivy: rule-level severity as a string, results at level warning.
			name: "trivy critical at warning blocks",
			doc: `{"version":"2.1.0","runs":[{
				"tool":{"driver":{"name":"Trivy","rules":[
					{"id":"CVE-2024-0001","properties":{"security-severity":"9.8","tags":["vulnerability","security","CRITICAL"]}},
					{"id":"CVE-2024-0002","properties":{"security-severity":"4.3","tags":["vulnerability","security","MEDIUM"]}}
				]}},
				"results":[
					{"ruleId":"CVE-2024-0001","ruleIndex":0,"level":"warning","message":{"text":"Package: openssl"}},
					{"ruleId":"CVE-2024-0002","ruleIndex":1,"level":"warning","message":{"text":"Package: zlib"}}
				]}]}`,
			wantRules: []string{"CVE-2024-0001"},
		},
		{
			// CodeQL: rule-level severity, result carries no properties bag.
			name: "codeql rule severity 7.5 blocks",
			doc: `{"version":"2.1.0","runs":[{
				"tool":{"driver":{"name":"CodeQL","rules":[
					{"id":"js/sql-injection","properties":{"security-severity":"7.5","precision":"high"}}
				]}},
				"results":[
					{"ruleId":"js/sql-injection","level":"error","message":{"text":"query built from user input"}}
				]}]}`,
			wantRules: []string{"js/sql-injection"},
		},
		{
			name: "missing level blocks on severity",
			doc: `{"runs":[{"tool":{"driver":{"rules":[
					{"id":"r1","properties":{"security-severity":8.2}}
				]}},
				"results":[{"ruleId":"r1"}]}]}`,
			wantRules: []string{"r1"},
		},
		{
			name: "result-level severity overrides rule default",
			doc: `{"runs":[{"tool":{"driver":{"rules":[
					{"id":"r1","properties":{"security-severity":"9.0"}},
					{"id":"r2","properties":{"security-severity":"3.0"}}
				]}},
				"results":[
					{"ruleId":"r1","properties":{"security-severity":"2.0"}},
					{"ruleId":"r2","properties":{"security-severity":"7.1"}}
				]}]}`,
			wantRules: []string{"r2"},
		},
		{
			name: "ruleId absent from rules passes",
			doc: `{"runs":[{"tool":{"driver":{"rules":[
					{"id":"known","properties":{"security-severity":"9.9"}}
				]}},
				"results":[{"ruleId":"unknown","level":"error"},{"level":"error"}]}]}`,
			wantRules: nil,
		},
		{
			name: "rules without properties pass",
			doc: `{"runs":[{"tool":{"driver":{"rules":[{"id":"bare"}]}},
				"results":[{"ruleId":"bare","level":"error"}]}]}`,
			wantRules: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, err := checkEnforce([]byte(tt.doc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(errs) != len(tt.wantRules) {
				t.Fatalf("want %d blocking findings, got %d: %+v", len(tt.wantRules), len(errs), errs)
			}
			for i, rule := range tt.wantRules {
				if !strings.Contains(errs[i].Message, "rule "+rule+":") {
					t.Fatalf("finding %d should name rule %s: %q", i, rule, errs[i].Message)
				}
			}
		})
	}
}

// TestCheckSARIFBelowThresholdAnyLevel verifies a 6.9 finding passes whatever
// its level, whether the severity is on the result or on the rule.
func TestCheckSARIFBelowThresholdAnyLevel(t *testing.T) {
	for _, level := range []string{`"level":"error",`, `"level":"warning",`, `"level":"note",`, `"level":"none",`, ``} {
		resultLevel := `{"runs":[{"results":[{` + level + `"ruleId":"r","properties":{"security-severity":"6.9"}}]}]}`
		ruleLevel := `{"runs":[{"tool":{"driver":{"rules":[{"id":"r","properties":{"security-severity":6.9}}]}},` +
			`"results":[{` + level + `"ruleId":"r"}]}]}`
		for name, doc := range map[string]string{"result": resultLevel, "rule": ruleLevel} {
			for _, mode := range []SARIFGateMode{SARIFGateReport, SARIFGateEnforce} {
				errs, wouldBlock, err := checkSARIF([]byte(doc), mode)
				if err != nil {
					t.Fatalf("%s %s %s: parse: %v", mode, name, level, err)
				}
				if len(errs) != 0 || len(wouldBlock) != 0 {
					t.Fatalf("%s %s %s: 6.9 must neither block nor be reported, got %+v / %+v", mode, name, level, errs, wouldBlock)
				}
			}
		}
	}
}

// TestCheckSARIFSeverityFormats covers both encodings of security-severity:
// a JSON number and a JSON string. Both at/above 7.0 must block.
func TestCheckSARIFSeverityFormats(t *testing.T) {
	numeric := []byte(`{"runs":[{"results":[{"level":"error","ruleId":"n","properties":{"security-severity":7.0}}]}]}`)
	stringy := []byte(`{"runs":[{"results":[{"level":"error","ruleId":"s","properties":{"security-severity":"7.5"}}]}]}`)
	for name, data := range map[string][]byte{"numeric": numeric, "string": stringy} {
		errs, err := checkEnforce(data)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if len(errs) != 1 {
			t.Fatalf("%s: want 1 blocking finding, got %d", name, len(errs))
		}
	}
}

// TestCheckSARIFNoSeverity ensures a finding without a security-severity
// property is treated as non-blocking, and a missing message is tolerated.
func TestCheckSARIFNoSeverity(t *testing.T) {
	doc := []byte(`{"runs":[{"results":[
		{"level":"error","ruleId":"no-sev","properties":{"precision":"high"}},
		{"level":"error","ruleId":"no-props"},
		{"ruleId":"no-level-no-props"},
		{"level":"warning","ruleId":"null-sev","properties":{"security-severity":null}}
	]}]}`)
	errs, err := checkEnforce(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("want 0 blocking findings, got %d: %+v", len(errs), errs)
	}
}

func TestCheckSARIFInvalidJSON(t *testing.T) {
	for _, mode := range []SARIFGateMode{SARIFGateReport, SARIFGateEnforce} {
		if _, _, err := checkSARIF([]byte(`{not json`), mode); err == nil {
			t.Fatalf("%s: expected a parse error for invalid JSON", mode)
		}
	}
}

// mixedSARIF holds one finding of each kind the rollout distinguishes:
//   - legacy: level error + result-level 9.8, blocked by both rules
//   - warn: level warning + result-level 8.0, stricter rule only
//   - trivy: level warning, rule-level 9.8, stricter rule only
//   - codeql: level error, rule-level 7.5, stricter rule only (the legacy rule
//     never read rule-level severity)
//   - low: level error + result-level 6.9, neither
var mixedSARIF = []byte(`{"version":"2.1.0","runs":[{
	"tool":{"driver":{"rules":[
		{"id":"trivy","properties":{"security-severity":"9.8"}},
		{"id":"codeql","properties":{"security-severity":7.5}}
	]}},
	"results":[
		{"level":"error","ruleId":"legacy","properties":{"security-severity":"9.8"}},
		{"level":"warning","ruleId":"warn","properties":{"security-severity":"8.0"}},
		{"level":"warning","ruleId":"trivy"},
		{"level":"error","ruleId":"codeql"},
		{"level":"error","ruleId":"low","properties":{"security-severity":"6.9"}}
	]}]}`)

// TestCheckSARIFReportMode: report mode blocks exactly what the legacy rule
// blocked and returns the stricter-rule-only findings for reporting.
func TestCheckSARIFReportMode(t *testing.T) {
	errs, wouldBlock, err := checkSARIF(mixedSARIF, SARIFGateReport)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "rule legacy:") {
		t.Fatalf("report mode must block only the legacy finding, got %+v", errs)
	}
	want := []struct {
		rule   string
		source string
		sev    float64
	}{
		{"warn", SARIFSourceResult, 8.0},
		{"trivy", SARIFSourceRule, 9.8},
		{"codeql", SARIFSourceRule, 7.5},
	}
	if len(wouldBlock) != len(want) {
		t.Fatalf("want %d would-block findings, got %+v", len(want), wouldBlock)
	}
	for i, w := range want {
		f := wouldBlock[i]
		if f.RuleID != w.rule || f.Source != w.source || f.Severity != w.sev || f.BlocksOld || !f.BlocksNew {
			t.Fatalf("finding %d: want %+v, got %+v", i, w, f)
		}
	}
}

// TestCheckSARIFEnforceMode: enforce blocks every finding the stricter rule
// catches and reports nothing.
func TestCheckSARIFEnforceMode(t *testing.T) {
	errs, wouldBlock, err := checkSARIF(mixedSARIF, SARIFGateEnforce)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(errs) != 4 {
		t.Fatalf("enforce must block 4 findings, got %+v", errs)
	}
	if len(wouldBlock) != 0 {
		t.Fatalf("enforce must not report, got %+v", wouldBlock)
	}
}

// TestCheckSARIFZeroModeIsReport: an unset mode never tightens the gate.
func TestCheckSARIFZeroModeIsReport(t *testing.T) {
	errs, wouldBlock, err := checkSARIF(mixedSARIF, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(errs) != 1 || len(wouldBlock) != 3 {
		t.Fatalf("zero mode must behave as report, got %d errs / %d would-block", len(errs), len(wouldBlock))
	}
}

// TestEvaluateSARIFVerdicts pins the per-finding verdicts both modes share,
// including that a result-level severity below threshold overrides a high
// rule default and so is omitted.
func TestEvaluateSARIFVerdicts(t *testing.T) {
	doc := []byte(`{"runs":[{"tool":{"driver":{"rules":[{"id":"r","properties":{"security-severity":"9.0"}}]}},
		"results":[
			{"level":"error","ruleId":"r","properties":{"security-severity":"2.0"}},
			{"level":"error","ruleId":"r","message":{"text":"from rule"}}
		]}]}`)
	findings, err := EvaluateSARIF(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", findings)
	}
	f := findings[0]
	if f.Source != SARIFSourceRule || f.Severity != 9.0 || f.Message != "from rule" || f.BlocksOld || !f.BlocksNew {
		t.Fatalf("unexpected finding %+v", f)
	}
}

func TestParseSARIFGateMode(t *testing.T) {
	valid := []struct {
		in   string
		want SARIFGateMode
	}{
		{"report", SARIFGateReport},
		{"enforce", SARIFGateEnforce},
		{" REPORT ", SARIFGateReport}, // case and surrounding whitespace ignored
	}
	for _, tc := range valid {
		got, err := ParseSARIFGateMode(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseSARIFGateMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "off", "enforced", "block"} {
		if _, err := ParseSARIFGateMode(in); err == nil {
			t.Fatalf("ParseSARIFGateMode(%q): want error", in)
		}
	}
}

// checkEnforce runs the gate in enforce mode, where the stricter rule blocks.
func checkEnforce(data []byte) ([]apierror.FieldError, error) {
	errs, _, err := checkSARIF(data, SARIFGateEnforce)
	return errs, err
}

func countCode(errs []apierror.FieldError, code string) int {
	n := 0
	for _, e := range errs {
		if e.Code == code {
			n++
		}
	}
	return n
}
