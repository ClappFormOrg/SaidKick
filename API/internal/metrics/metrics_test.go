package metrics

import (
	"bytes"
	"strings"
	"testing"
)

// Hot path: MetricsMiddleware calls HTTPRequestsTotal + HTTPRequestDuration on
// every single request. The label set is bounded (a handful of methods, the
// route-pattern paths, and a small set of statuses), so the per-request work
// of re-building the metric identifier string and re-looking it up is pure
// overhead once the metric exists.

func BenchmarkHTTPRequestsTotal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = HTTPRequestsTotal("GET", "/api/v1/apps/:slug", 200)
	}
}

func BenchmarkHTTPRequestDuration(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = HTTPRequestDuration("GET", "/api/v1/apps/:slug")
	}
}

// BenchmarkMetricsMiddlewarePath mirrors the real middleware call pattern:
// two metric lookups per request, cycling across a realistic bounded set of
// (method, path, status) tuples.
func BenchmarkMetricsMiddlewarePath(b *testing.B) {
	routes := []string{
		"/api/v1/apps", "/api/v1/apps/:slug", "/api/v1/clusters",
		"/api/v1/deployments", "/api/v1/audit-submissions",
	}
	methods := []string{"GET", "POST"}
	statuses := []int{200, 404, 500}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := methods[i%len(methods)]
		p := routes[i%len(routes)]
		s := statuses[i%len(statuses)]
		HTTPRequestsTotal(m, p, s).Inc()
		HTTPRequestDuration(m, p).Update(0.001)
	}
}

// TestHTTPMetricsStableInstance guards the core invariant the optimisation
// must preserve: the same label set always maps to the same underlying
// counter/histogram instance (so increments accumulate rather than reset).
func TestHTTPMetricsStableInstance(t *testing.T) {
	c1 := HTTPRequestsTotal("GET", "/api/v1/apps", 200)
	c2 := HTTPRequestsTotal("GET", "/api/v1/apps", 200)
	if c1 != c2 {
		t.Fatal("same label set must resolve to the same counter instance")
	}
	c3 := HTTPRequestsTotal("POST", "/api/v1/apps", 200)
	if c1 == c3 {
		t.Fatal("different method must resolve to a different counter instance")
	}

	h1 := HTTPRequestDuration("GET", "/api/v1/apps")
	h2 := HTTPRequestDuration("GET", "/api/v1/apps")
	if h1 != h2 {
		t.Fatal("same label set must resolve to the same histogram instance")
	}
	h3 := HTTPRequestDuration("GET", "/api/v1/clusters")
	if h1 == h3 {
		t.Fatal("different path must resolve to a different histogram instance")
	}
}

// TestAgentMetricsSeparateFromHub guards the split between the two binaries:
// the hub exposition carries no agent series and the agent exposition no hub
// series.
func TestAgentMetricsSeparateFromHub(t *testing.T) {
	var hub, agent bytes.Buffer
	WritePrometheus(&hub, false)
	WriteAgentPrometheus(&agent, false)

	for _, name := range []string{"agent_up", "agent_heartbeats_total", "agent_commands_processed_total", "agent_command_duration_seconds", "agent_hub_request_duration_seconds"} {
		if strings.Contains(hub.String(), name) {
			t.Errorf("hub exposition carries agent series %s", name)
		}
	}
	for _, name := range []string{"agent_heartbeat_age_seconds", "agent_commands_pending", "audit_writes_total", "orchestrator_calls_total"} {
		if strings.Contains(agent.String(), name) {
			t.Errorf("agent exposition carries hub series %s", name)
		}
	}
	if !strings.Contains(agent.String(), "agent_up 1\n") {
		t.Errorf("agent exposition lacks agent_up 1:\n%s", agent.String())
	}
	// Every series of the closed sets exists before the first increment.
	for _, c := range AgentCommands {
		for _, o := range []string{OutcomeSuccess, OutcomeFailure} {
			want := `agent_commands_processed_total{command="` + c + `",outcome="` + o + `"}`
			if !strings.Contains(agent.String(), want) {
				t.Errorf("agent exposition lacks %s", want)
			}
		}
	}
}

// TestAgentCertMetrics checks the certificate renewal series on both sides:
// each binary exposes its own agent_cert_renewals_total with the closed
// outcome set, the agent its remaining validity, the hub the minimum
// across clusters, and neither carries a label beyond outcome.
func TestAgentCertMetrics(t *testing.T) {
	SetAgentCertMinRemainingSeconds(86400, true)
	HubAgentCertRenewalsTotal(OutcomeSuccess).Inc()
	AgentCertRenewalsTotal(OutcomeFailure).Inc()
	AgentCertRemainingSeconds.Set(3600)

	var hub, agent bytes.Buffer
	WritePrometheus(&hub, false)
	WriteAgentPrometheus(&agent, false)

	for _, want := range []string{
		`agent_cert_renewals_total{outcome="success"}`,
		`agent_cert_renewals_total{outcome="failure"}`,
		"agent_cert_min_remaining_seconds 86400\n",
	} {
		if !strings.Contains(hub.String(), want) {
			t.Errorf("hub exposition lacks %s", want)
		}
	}
	for _, want := range []string{
		`agent_cert_renewals_total{outcome="success"}`,
		`agent_cert_renewals_total{outcome="failure"}`,
		"agent_cert_remaining_seconds 3600\n",
	} {
		if !strings.Contains(agent.String(), want) {
			t.Errorf("agent exposition lacks %s", want)
		}
	}
	if strings.Contains(hub.String(), "agent_cert_remaining_seconds") {
		t.Error("hub exposition carries the agent's remaining-validity gauge")
	}
	if strings.Contains(agent.String(), "agent_cert_min_remaining_seconds") {
		t.Error("agent exposition carries the hub's minimum gauge")
	}
	for _, exp := range []string{hub.String(), agent.String()} {
		for line := range strings.Lines(exp) {
			if strings.HasPrefix(line, "agent_cert_renewals_total{") && !strings.HasPrefix(line, "agent_cert_renewals_total{outcome=") {
				t.Errorf("unexpected label on %s", line)
			}
		}
	}

	SetAgentCertMinRemainingSeconds(0, false)
	hub.Reset()
	WritePrometheus(&hub, false)
	if strings.Contains(hub.String(), "agent_cert_min_remaining_seconds") {
		t.Error("the minimum gauge is removed when no expiry is known")
	}
}

// TestHTTPMetricsCounterAccumulates confirms increments land on one series.
func TestHTTPMetricsCounterAccumulates(t *testing.T) {
	c := HTTPRequestsTotal("DELETE", "/api/v1/uniquepath-accum", 204)
	c.Set(0)
	HTTPRequestsTotal("DELETE", "/api/v1/uniquepath-accum", 204).Inc()
	HTTPRequestsTotal("DELETE", "/api/v1/uniquepath-accum", 204).Inc()
	if got := c.Get(); got != 2 {
		t.Fatalf("counter should accumulate across lookups: want 2, got %d", got)
	}
}
