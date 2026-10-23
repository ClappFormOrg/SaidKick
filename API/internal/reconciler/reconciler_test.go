package reconciler

import (
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
)

// TestSetStateGauges checks that every known state gets a series and that a
// state missing from a later count drops back to 0.
func TestSetStateGauges(t *testing.T) {
	setStateGauges(map[models.DeploymentState]int64{
		models.DeploymentReady:    3,
		models.DeploymentDegraded: 1,
	})
	for _, s := range models.AllDeploymentStates {
		want := 0.0
		switch s {
		case models.DeploymentReady:
			want = 3
		case models.DeploymentDegraded:
			want = 1
		}
		if got := metrics.DeploymentStateGauge(string(s)).Get(); got != want {
			t.Errorf("state %q: want %v, got %v", s, want, got)
		}
	}

	setStateGauges(map[models.DeploymentState]int64{models.DeploymentReady: 2})
	if got := metrics.DeploymentStateGauge(string(models.DeploymentReady)).Get(); got != 2 {
		t.Errorf("ready after update: want 2, got %v", got)
	}
	if got := metrics.DeploymentStateGauge(string(models.DeploymentDegraded)).Get(); got != 0 {
		t.Errorf("degraded after it emptied: want 0, got %v", got)
	}
}
