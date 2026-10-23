package testutil

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// HistogramCount returns the number of observations h has recorded.
func HistogramCount(h *vm.Histogram) uint64 {
	var n uint64
	h.VisitNonZeroBuckets(func(_ string, count uint64) { n += count })
	return n
}

var (
	labelValueRe = regexp.MustCompile(`="((?:[^"\\]|\\.)*)"`)
	uuidRe       = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	emailRe      = regexp.MustCompile(`[^@\s]+@[^@\s]+\.[A-Za-z]{2,}`)
)

// RequireNoIdentifyingLabels renders every registered metric, the hub's and
// the cluster agent's, and fails t if any label value contains a UUID or an
// email address.
func RequireNoIdentifyingLabels(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	metrics.WritePrometheus(&buf, false)
	metrics.WriteAgentPrometheus(&buf, false)
	for _, line := range identifyingLabelLines(buf.String()) {
		t.Errorf("metric label carries an identifier: %s", line)
	}
}

// identifyingLabelLines returns the exposition lines with a label value
// that contains a UUID or an email address.
func identifyingLabelLines(exposition string) []string {
	var out []string
	for line := range strings.Lines(exposition) {
		open := strings.IndexByte(line, '{')
		closing := strings.LastIndexByte(line, '}')
		if strings.HasPrefix(line, "#") || open < 0 || closing < open {
			continue
		}
		for _, m := range labelValueRe.FindAllStringSubmatch(line[open:closing], -1) {
			if uuidRe.MatchString(m[1]) || emailRe.MatchString(m[1]) {
				out = append(out, strings.TrimSpace(line))
				break
			}
		}
	}
	return out
}
