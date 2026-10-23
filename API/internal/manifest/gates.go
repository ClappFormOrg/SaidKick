package manifest

import (
	"slices"
	"strings"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// RejectedGates maps Stage-1 validation errors to the gates that rejected
// the submission (metrics.ManifestGates), each gate once, in
// metrics.ManifestGates order. Errors that say nothing about the submission
// (the GitHub App is not configured, or a fetch failed) map to no gate.
func RejectedGates(errs []apierror.FieldError) []string {
	var gates []string
	for _, e := range errs {
		if e.Code == "github_not_configured" || e.Code == "fetch_failed" {
			continue
		}
		g := gateForPath(e.Path)
		if !slices.Contains(gates, g) {
			gates = append(gates, g)
		}
	}
	slices.SortFunc(gates, func(a, b string) int {
		return slices.Index(metrics.ManifestGates, a) - slices.Index(metrics.ManifestGates, b)
	})
	return gates
}

// gateForPath names the gate that owns a validation error's field path.
func gateForPath(p string) string {
	switch {
	case p == "spec.audit.security_scan":
		return metrics.GateSARIF
	case p == "spec.audit.dpia":
		return metrics.GateDPIA
	case p == "spec.audit.ai_risk", strings.HasPrefix(p, "spec.ai."):
		return metrics.GateAIRisk
	case strings.HasPrefix(p, "spec.chart."), p == "Chart.yaml", p == "values.schema.json":
		return metrics.GateChart
	default:
		return metrics.GateSchema
	}
}
