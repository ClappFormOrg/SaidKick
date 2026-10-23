package manifest

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
)

// ValidateSchema runs every check that does not require talking to
// GitHub. It returns a slice of apierror.FieldError compatible with the
// API's validation envelope. An empty slice means the schema is valid.
//
// Callers should run ValidateSchema first; only if it returns no errors
// is it worth following up with ValidateEvidence (which makes network
// calls).
func (m *AppManifest) ValidateSchema() []apierror.FieldError {
	var errs []apierror.FieldError

	push := func(path, code, msg string) {
		errs = append(errs, apierror.FieldError{Path: path, Code: code, Message: msg})
	}

	if m.APIVersion != CurrentAPIVersion {
		push("apiVersion", "unsupported",
			fmt.Sprintf("must be %q (got %q)", CurrentAPIVersion, m.APIVersion))
	}
	if m.Kind != CurrentKind {
		push("kind", "unsupported",
			fmt.Sprintf("must be %q (got %q)", CurrentKind, m.Kind))
	}

	// metadata
	if !SlugRegex.MatchString(m.Metadata.Name) {
		push("metadata.name", "invalid_format",
			"must match ^[a-z][a-z0-9-]{1,58}[a-z0-9]$ (kebab-case, 3-60 chars)")
	}
	if strings.TrimSpace(m.Metadata.DisplayName) == "" {
		push("metadata.display_name", "required", "display_name is required")
	}
	if strings.TrimSpace(m.Metadata.Vendor) == "" {
		push("metadata.vendor", "required", "vendor is required")
	}
	if strings.TrimSpace(m.Metadata.Contact) == "" {
		push("metadata.contact", "required", "contact (email or URL) is required")
	}
	if _, ok := AllowedLicenses[m.Metadata.License]; !ok {
		push("metadata.license", "not_allow_listed",
			fmt.Sprintf("license %q is not in the platform allowlist (see ADR-7)", m.Metadata.License))
	}

	// spec.chart
	if strings.TrimSpace(m.Spec.Chart.Path) == "" {
		push("spec.chart.path", "required", "chart.path is required (relative to repo root)")
	}
	if m.Spec.Chart.Type != "helm" {
		push("spec.chart.type", "unsupported",
			fmt.Sprintf("only 'helm' is supported in V1 (got %q)", m.Spec.Chart.Type))
	}

	// spec.interfaces
	if len(m.Spec.Interfaces) == 0 {
		push("spec.interfaces", "min_items_1",
			"at least one interface entry of type 'api' or 'mcp' is required")
	}
	for i, iface := range m.Spec.Interfaces {
		// Build the "spec.interfaces[i]" prefix only when a check actually
		// fails — on the happy path (the common re-gate case) this loop then
		// allocates nothing.
		if _, ok := AllowedInterfaceTypes[iface.Type]; !ok {
			push(ifaceField(i, "type"), "invalid_value",
				fmt.Sprintf("type must be 'api' or 'mcp' (got %q)", iface.Type))
		}
		switch iface.Type {
		case "api":
			if strings.TrimSpace(iface.OpenAPI) == "" {
				push(ifaceField(i, "openapi"), "required", "api interfaces must declare an 'openapi' path")
			}
		case "mcp":
			if strings.TrimSpace(iface.ServerPath) == "" {
				push(ifaceField(i, "server_path"), "required", "mcp interfaces must declare a 'server_path'")
			}
		}
	}

	// spec.demo
	if strings.TrimSpace(m.Spec.Demo.Description) == "" {
		push("spec.demo.description", "required", "description path is required")
	}
	switch {
	case len(m.Spec.Demo.Screenshots) < MinScreenshots:
		push("spec.demo.screenshots", "min_items_3",
			fmt.Sprintf("at least %d screenshots are required (got %d)", MinScreenshots, len(m.Spec.Demo.Screenshots)))
	case len(m.Spec.Demo.Screenshots) > MaxScreenshots:
		push("spec.demo.screenshots", "max_items_20",
			fmt.Sprintf("at most %d screenshots are allowed (got %d)", MaxScreenshots, len(m.Spec.Demo.Screenshots)))
	}

	// spec.audit — every field is required
	if strings.TrimSpace(m.Spec.Audit.DPIA) == "" {
		push("spec.audit.dpia", "required", "DPIA evidence path is required")
	}
	if strings.TrimSpace(m.Spec.Audit.SecurityScan) == "" {
		push("spec.audit.security_scan", "required", "security scan (SARIF) path is required")
	}
	if strings.TrimSpace(m.Spec.Audit.AIRisk) == "" {
		push("spec.audit.ai_risk", "required", "AI risk assessment path is required")
	}
	if strings.TrimSpace(m.Spec.Audit.Accessibility) == "" {
		push("spec.audit.accessibility", "required", "accessibility statement path is required")
	}

	// spec.ai
	if _, ok := AllowedRiskClasses[m.Spec.AI.RiskClass]; !ok {
		push("spec.ai.risk_class", "invalid_value",
			"risk_class must be one of: minimal, limited, high")
	}
	if strings.TrimSpace(m.Spec.AI.BaseModel) == "" {
		push("spec.ai.base_model", "required",
			"base_model is required (e.g. gpt-4o-mini, llama-3-8b-instruct)")
	}
	if m.Spec.AI.DataResidency != "EU" {
		push("spec.ai.data_residency", "must_be_eu",
			"only EU data residency is supported (per Principe 7)")
	}

	return errs
}

// ifaceField builds a "spec.interfaces[i].<suffix>" JSON path. It is only
// called on a validation-failure branch, so the concatenation cost never lands
// on the happy path.
func ifaceField(i int, suffix string) string {
	return "spec.interfaces[" + strconv.Itoa(i) + "]." + suffix
}

// EvidenceFileInfo describes one file recorded after Stage-1 fetched it
// from GitHub. The slice produced by ValidateEvidence is persisted into
// AuditSubmission.EvidenceFiles so reviewers can inspect what the
// platform actually saw at submission time.
//
// Content carries the raw bytes in-memory during the submission transaction
// so the handler can persist them to evidence_blobs. It is never serialised
// to JSON (json:"-").
type EvidenceFileInfo struct {
	LogicalName   string `json:"logical_name"`
	RepoPath      string `json:"repo_path"`
	ContentSHA256 string `json:"content_sha256"`
	SizeBytes     int    `json:"size_bytes"`
	ContentType   string `json:"content_type"`
	Content       []byte `json:"-"`
}

// AllowedContentTypes is the set of MIME types we accept as evidence
// files. Extension-based detection runs first; if it cannot decide, the
// file is rejected.
var AllowedContentTypes = map[string]string{
	".pdf":   "application/pdf",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".mp4":   "video/mp4",
	".sarif": "application/sarif+json",
	".md":    "text/markdown",
}

// ValidateEvidence fetches each declared evidence file from the linked
// GitHub repo at the supplied SHA, checks its content-type, and returns
// the resolved EvidenceFileInfo records on success or a slice of
// FieldErrors on failure. Both slices may be returned simultaneously —
// the caller decides whether partial success is acceptable (it isn't:
// any errors mean the submission is rejected).
//
// sarifMode selects the SARIF gate rule (see SARIFGateMode). Outside enforce
// mode, wouldBlock lists the findings that only the stricter rule would
// block; they do not appear in errs and the caller reports them.
//
// Network calls are performed sequentially for simplicity; the file
// count is small (~6) so concurrent fetches aren't worth the
// goroutine/error-fan-in complexity for V0.
func (m *AppManifest) ValidateEvidence(
	ctx context.Context,
	client gh.Client,
	installationID int64,
	owner, repo, ref string,
	sarifMode SARIFGateMode,
) (resolved []EvidenceFileInfo, wouldBlock []SARIFFinding, errs []apierror.FieldError) {
	type target struct {
		logical string
		field   string
		pathStr string
	}
	targets := make([]target, 0, 7+len(m.Spec.Demo.Screenshots))
	targets = append(targets,
		target{logical: "description",   field: "spec.demo.description",        pathStr: m.Spec.Demo.Description},
		target{logical: "dpia",          field: "spec.audit.dpia",              pathStr: m.Spec.Audit.DPIA},
		target{logical: "security_scan", field: "spec.audit.security_scan",     pathStr: m.Spec.Audit.SecurityScan},
		target{logical: "ai_risk",       field: "spec.audit.ai_risk",           pathStr: m.Spec.Audit.AIRisk},
		target{logical: "accessibility", field: "spec.audit.accessibility",     pathStr: m.Spec.Audit.Accessibility},
	)
	for i, s := range m.Spec.Demo.Screenshots {
		targets = append(targets, target{
			logical: fmt.Sprintf("screenshot[%d]", i),
			field:   fmt.Sprintf("spec.demo.screenshots[%d]", i),
			pathStr: s,
		})
	}
	if strings.TrimSpace(m.Spec.Audit.ModelCard) != "" {
		targets = append(targets, target{logical: "model_card", field: "spec.audit.model_card", pathStr: m.Spec.Audit.ModelCard})
	}
	if strings.TrimSpace(m.Spec.Demo.Video) != "" {
		targets = append(targets, target{logical: "demo_video", field: "spec.demo.video", pathStr: m.Spec.Demo.Video})
	}

	resolved = make([]EvidenceFileInfo, 0)

	for _, t := range targets {
		cleaned := strings.TrimPrefix(t.pathStr, "./")
		content, blobSHA, err := client.GetFileAtRef(ctx, installationID, owner, repo, cleaned, ref)
		if err != nil {
			switch {
			case errors.Is(err, gh.ErrFileNotFound):
				errs = append(errs, apierror.FieldError{
					Path: t.field, Code: "file_not_found",
					Message: fmt.Sprintf("%q does not exist at the audited commit", cleaned),
				})
			case errors.Is(err, gh.ErrFileTooLarge):
				errs = append(errs, apierror.FieldError{
					Path: t.field, Code: "file_too_large",
					Message: fmt.Sprintf("%q exceeds the platform file-size limit", cleaned),
				})
			case errors.Is(err, gh.ErrNotConfigured):
				errs = append(errs, apierror.FieldError{
					Path: t.field, Code: "github_not_configured",
					Message: "GitHub App not configured; evidence validation skipped",
				})
				return resolved, wouldBlock, errs
			default:
				errs = append(errs, apierror.FieldError{
					Path: t.field, Code: "fetch_failed",
					Message: fmt.Sprintf("could not fetch %q: %v", cleaned, err),
				})
			}
			continue
		}

		ext := strings.ToLower(path.Ext(cleaned))
		mime, ok := AllowedContentTypes[ext]
		if !ok {
			errs = append(errs, apierror.FieldError{
				Path: t.field, Code: "disallowed_content_type",
				Message: fmt.Sprintf("extension %q is not in the allow-list (PDF/PNG/JPEG/MP4/SARIF/MD)", ext),
			})
			continue
		}

		// SARIF security gate: block submissions with high-severity findings.
		if t.logical == "security_scan" {
			sarifErrs, sarifWouldBlock, parseErr := checkSARIF(content, sarifMode)
			if parseErr != nil {
				errs = append(errs, apierror.FieldError{
					Path: t.field, Code: "sarif_parse_failed",
					Message: fmt.Sprintf("security_scan must be a valid SARIF 2.1.0 file: %v", parseErr),
				})
				continue
			}
			errs = append(errs, sarifErrs...)
			wouldBlock = append(wouldBlock, sarifWouldBlock...)
		}

		resolved = append(resolved, EvidenceFileInfo{
			LogicalName:   t.logical,
			RepoPath:      cleaned,
			ContentSHA256: blobSHA,
			SizeBytes:     len(content),
			ContentType:   mime,
			Content:       content,
		})
	}

	return resolved, wouldBlock, errs
}
