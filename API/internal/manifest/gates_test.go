package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

func TestRejectedGates(t *testing.T) {
	fe := func(path, code string) apierror.FieldError { return apierror.FieldError{Path: path, Code: code} }

	// 30 blocking SARIF findings are one rejection of the sarif gate.
	sarif := make([]apierror.FieldError, 30)
	for i := range sarif {
		sarif[i] = fe("spec.audit.security_scan", "sarif_high_severity_finding")
	}

	cases := []struct {
		name string
		errs []apierror.FieldError
		want []string
	}{
		{"none", nil, nil},
		{"sarif findings count once", sarif, []string{metrics.GateSARIF}},
		{"missing scan", []apierror.FieldError{fe("spec.audit.security_scan", "file_not_found")}, []string{metrics.GateSARIF}},
		{"dpia", []apierror.FieldError{fe("spec.audit.dpia", "required")}, []string{metrics.GateDPIA}},
		{"ai risk evidence and ai block", []apierror.FieldError{
			fe("spec.audit.ai_risk", "file_not_found"), fe("spec.ai.risk_class", "invalid_value"), fe("spec.ai.data_residency", "must_be_eu"),
		}, []string{metrics.GateAIRisk}},
		{"chart", []apierror.FieldError{fe("spec.chart.type", "unsupported"), fe("values.schema.json", "unenforceable_schema")}, []string{metrics.GateChart}},
		{"schema", []apierror.FieldError{fe("metadata.name", "invalid_format"), fe("spec.demo.screenshots[0]", "file_not_found")}, []string{metrics.GateSchema}},
		{"several gates in fixed order", []apierror.FieldError{
			fe("metadata.license", "not_allow_listed"), fe("spec.audit.security_scan", "sarif_parse_failed"),
			fe("Chart.yaml", "yaml_to_json_failed"), fe("spec.audit.dpia", "disallowed_content_type"),
		}, []string{metrics.GateSARIF, metrics.GateDPIA, metrics.GateChart, metrics.GateSchema}},
		{"infrastructure errors name no gate", []apierror.FieldError{
			fe("spec.audit.dpia", "fetch_failed"), fe("spec.demo.description", "github_not_configured"),
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RejectedGates(tc.errs))
		})
	}
}
