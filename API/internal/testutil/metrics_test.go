package testutil

import (
	"testing"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/stretchr/testify/assert"
)

func TestIdentifyingLabelLines(t *testing.T) {
	exposition := `# HELP x
http_requests_total{method="GET",path="/api/v1/apps/:slug",status="200"} 3
deployment_state_total{state="ready"} 1
audit_writes_total 4
bad_uuid{cluster="0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"} 1
bad_email{user="dev@example.nl"} 1
`
	assert.Equal(t, []string{
		`bad_uuid{cluster="0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"} 1`,
		`bad_email{user="dev@example.nl"} 1`,
	}, identifyingLabelLines(exposition))
}

func TestHistogramCount(t *testing.T) {
	var h vm.Histogram
	assert.Zero(t, HistogramCount(&h))
	h.Update(0.5)
	h.Update(12)
	assert.Equal(t, uint64(2), HistogramCount(&h))
}
