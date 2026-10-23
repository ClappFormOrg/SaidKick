package manifest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
)

// sarifBlockThreshold is the CVSS score at or above which a finding blocks.
const sarifBlockThreshold = 7.0

// SARIFGateMode selects which SARIF rule blocks a submission.
//
// The gate is rolling out in two steps. In SARIFGateReport mode only the
// legacy rule blocks (level "error" AND result-level security-severity >= 7.0),
// and findings that only the stricter rule would block are returned to the
// caller for reporting. In SARIFGateEnforce mode the stricter rule blocks:
// any result whose security-severity, read from the result or from its rule,
// is >= 7.0, whatever its level.
type SARIFGateMode string

const (
	// SARIFGateReport blocks on the legacy rule and reports the rest.
	SARIFGateReport SARIFGateMode = "report"
	// SARIFGateEnforce blocks on the stricter rule.
	SARIFGateEnforce SARIFGateMode = "enforce"
)

// ParseSARIFGateMode validates a configured mode string. Matching ignores
// case and surrounding whitespace. Any value other than "report" or
// "enforce" is an error.
func ParseSARIFGateMode(s string) (SARIFGateMode, error) {
	switch mode := SARIFGateMode(strings.ToLower(strings.TrimSpace(s))); mode {
	case SARIFGateReport, SARIFGateEnforce:
		return mode, nil
	default:
		return "", fmt.Errorf("SARIF gate mode %q is not supported (valid: %q, %q)", s, SARIFGateReport, SARIFGateEnforce)
	}
}

// blocks reports whether f blocks a submission under mode. Any mode other
// than SARIFGateEnforce, including the zero value, falls back to the legacy
// rule so an unset mode never tightens the gate.
func (mode SARIFGateMode) blocks(f SARIFFinding) bool {
	if mode == SARIFGateEnforce {
		return f.BlocksNew
	}
	return f.BlocksOld
}

// SARIF severity sources, recorded on each finding so reports can show
// whether the score came from the result or from its rule.
const (
	SARIFSourceResult = "result"
	SARIFSourceRule   = "rule"
)

// SARIFFinding is one SARIF result at or above the blocking threshold under
// the stricter rule, with the verdict of both rules.
type SARIFFinding struct {
	RuleID   string
	Level    string // as written in the document; empty when omitted
	Message  string
	Severity float64
	Source   string // SARIFSourceResult or SARIFSourceRule

	// BlocksOld is the legacy verdict: level "error" and a result-level
	// security-severity >= 7.0.
	BlocksOld bool
	// BlocksNew is the stricter verdict: resolved security-severity >= 7.0,
	// whatever the level.
	BlocksNew bool
}

// sarifDocument is the subset of a SARIF 2.1.0 document we inspect for
// blocking high-severity security findings.
type sarifDocument struct {
	Runs []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Rules []sarifRule `json:"rules"`
}

// sarifRule is a SARIF reportingDescriptor. Trivy and CodeQL put
// security-severity on the rule's properties rather than on each result.
type sarifRule struct {
	ID         string      `json:"id"`
	Properties *sarifProps `json:"properties"`
}

// sarifResult decodes "level" only for the legacy rule. The stricter rule
// ignores it, so a high finding reported as a warning (or with no level,
// which SARIF defaults to warning) still blocks under enforce.
type sarifResult struct {
	Level      string        `json:"level"`
	RuleID     string        `json:"ruleId"`
	Message    *sarifMessage `json:"message"`
	Properties *sarifProps   `json:"properties"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

// sarifProps decodes only the one property the security gate needs. Keeping
// this a typed struct (rather than map[string]any) means the JSON decoder
// skips the rest of the properties bag — tags, precision, CWE arrays — instead
// of allocating a boxed map + slice per result. On a scan with hundreds of
// findings that is the difference between thousands of allocations and a
// handful. security-severity is left as RawMessage because SARIF tools encode
// it as either a JSON string ("7.5") or a JSON number (7.5).
type sarifProps struct {
	SecuritySeverity json.RawMessage `json:"security-severity"`
}

// severity returns the parsed security-severity, or false when the bag is
// absent or carries no parseable value.
func (p *sarifProps) severity() (float64, bool) {
	if p == nil {
		return 0, false
	}
	return sarifSecuritySeverity(p.SecuritySeverity)
}

// EvaluateSARIF parses data as SARIF 2.1.0 and returns every result the
// stricter rule would block, each carrying the verdict of both rules.
// Results below the threshold are omitted; since the legacy rule is a subset
// of the stricter one, nothing the legacy rule blocks is lost.
//
// Severity is resolved from the result's own properties first (so a tool can
// override its rule default), then from the runs[].tool.driver.rules[] entry
// matching the result's ruleId. A result with no resolvable severity is
// omitted. A non-nil error means the bytes are not parseable as SARIF JSON.
func EvaluateSARIF(data []byte) ([]SARIFFinding, error) {
	var doc sarifDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("not valid SARIF JSON: %w", err)
	}

	var findings []SARIFFinding
	for _, run := range doc.Runs {
		ruleSeverity := make(map[string]float64, len(run.Tool.Driver.Rules))
		for _, rule := range run.Tool.Driver.Rules {
			if sev, ok := rule.Properties.severity(); ok {
				ruleSeverity[rule.ID] = sev
			}
		}
		for _, result := range run.Results {
			source := SARIFSourceResult
			sev, ok := result.Properties.severity()
			if !ok {
				source = SARIFSourceRule
				sev, ok = ruleSeverity[result.RuleID]
			}
			if !ok || sev < sarifBlockThreshold {
				continue
			}
			f := SARIFFinding{
				RuleID:    result.RuleID,
				Level:     result.Level,
				Severity:  sev,
				Source:    source,
				BlocksOld: source == SARIFSourceResult && result.Level == "error",
				BlocksNew: true,
			}
			if result.Message != nil {
				f.Message = result.Message.Text
			}
			findings = append(findings, f)
		}
	}
	return findings, nil
}

// checkSARIF applies the gate in the given mode. It returns one FieldError
// per blocking finding and, outside enforce mode, the findings that only the
// stricter rule would block (for the caller to report). A non-nil error means
// the bytes are not parseable as SARIF JSON at all.
func checkSARIF(data []byte, mode SARIFGateMode) (errs []apierror.FieldError, wouldBlock []SARIFFinding, err error) {
	findings, err := EvaluateSARIF(data)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range findings {
		switch {
		case mode.blocks(f):
			errs = append(errs, sarifFieldError(f))
		case f.BlocksNew:
			wouldBlock = append(wouldBlock, f)
		}
	}
	return errs, wouldBlock, nil
}

func sarifFieldError(f SARIFFinding) apierror.FieldError {
	msg := fmt.Sprintf("rule %s: security-severity=%.1f blocks submission", f.RuleID, f.Severity)
	if f.Message != "" {
		msg = fmt.Sprintf("rule %s: %s (security-severity=%.1f blocks submission)", f.RuleID, f.Message, f.Severity)
	}
	return apierror.FieldError{
		Path:    "spec.audit.security_scan",
		Code:    "sarif_high_severity_finding",
		Message: msg,
	}
}

// sarifSecuritySeverity parses the raw "security-severity" value, which SARIF
// tools encode as either a JSON string ("7.5") or a JSON number (7.5). We parse
// the RawMessage directly — stripping the optional surrounding quotes — rather
// than round-tripping through json.Unmarshal, so no interface boxing occurs.
func sarifSecuritySeverity(raw json.RawMessage) (float64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	s = strings.Trim(s, `"`) // handles the JSON-string encoding ("7.5")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
