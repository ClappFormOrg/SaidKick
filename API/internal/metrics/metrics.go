// Package metrics exposes Prometheus-compatible metrics for VictoriaMetrics.
// All counters and histograms are module-level singletons; register them once
// at init time and increment from any goroutine without further setup.
package metrics

import (
	"fmt"
	"io"
	"sync"

	vm "github.com/VictoriaMetrics/metrics"
)

// The HTTP counter/histogram lookups run on every request via MetricsMiddleware.
// The label set is bounded (a few methods × the route-pattern paths × a small
// set of statuses), so re-building the metric identifier string and re-parsing
// it through vm.GetOrCreate* on every request is pure overhead. We memoise the
// resolved instances behind an RWMutex-guarded map keyed by the label tuple:
// the hot path is an RLock + a struct-keyed map read, which allocates nothing
// (unlike sync.Map, whose interface-boxed keys would allocate per lookup).
type counterKey struct {
	method string
	path   string
	status int
}

type durationKey struct {
	method string
	path   string
}

var (
	metricMu     sync.RWMutex
	reqCounters  = make(map[counterKey]*vm.Counter)
	reqDurations = make(map[durationKey]*vm.Histogram)
)

// PathUnmatched is the path label of a request that matched no route. The
// raw URL is never a label value.
const PathUnmatched = "unmatched"

// HTTP request counter — label set is {method, path, status}.
// Call Inc(method, path, status) after each request.
func HTTPRequestsTotal(method, path string, status int) *vm.Counter {
	k := counterKey{method: method, path: path, status: status}

	metricMu.RLock()
	c := reqCounters[k]
	metricMu.RUnlock()
	if c != nil {
		return c
	}

	metricMu.Lock()
	defer metricMu.Unlock()
	// Re-check under the write lock in case another goroutine created it.
	if existing := reqCounters[k]; existing != nil {
		return existing
	}
	c = vm.GetOrCreateCounter(
		fmt.Sprintf(`http_requests_total{method=%q,path=%q,status="%d"}`, method, path, status),
	)
	reqCounters[k] = c
	return c
}

// HTTP request latency histogram — label set is {method, path}.
// Observe(seconds) from the metrics middleware.
func HTTPRequestDuration(method, path string) *vm.Histogram {
	k := durationKey{method: method, path: path}

	metricMu.RLock()
	h := reqDurations[k]
	metricMu.RUnlock()
	if h != nil {
		return h
	}

	metricMu.Lock()
	defer metricMu.Unlock()
	// Re-check under the write lock in case another goroutine created it.
	if existing := reqDurations[k]; existing != nil {
		return existing
	}
	h = vm.GetOrCreateHistogram(
		fmt.Sprintf(`http_request_duration_seconds{method=%q,path=%q}`, method, path),
	)
	reqDurations[k] = h
	return h
}

// DeploymentStateGauge tracks live deployment counts per state.
// Set(n) from the reconciler or any periodic job.
func DeploymentStateGauge(state string) *vm.Gauge {
	return vm.GetOrCreateGauge(
		fmt.Sprintf(`deployment_state_total{state=%q}`, state),
		nil,
	)
}

// SARIFGateWouldBlockTotal counts SARIF findings that the stricter gate rule
// would block but the legacy rule lets through, while SARIF_GATE_MODE=report.
// The label set is {source} where source is "result" or "rule": where the
// security-severity was read from. Call Inc() once per finding.
func SARIFGateWouldBlockTotal(source string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`sarif_gate_would_block_total{source=%q}`, source),
	)
}

// AuditWritesTotal counts rows appended to the audit log: incremented once
// per committed row, never for an attempt. No labels: actor, subject and
// event type belong in the row, not in the series.
var AuditWritesTotal = vm.NewCounter(`audit_writes_total`)

// AuditWriteFailuresTotal counts audit rows that could not be appended:
// every audit.Logger.Log call that returns an error, including a write the
// database rolled back. The audit_dlq table holds the failed events; this
// counter makes their rate visible without querying it.
var AuditWriteFailuresTotal = vm.NewCounter(`audit_write_failures_total`)

// Outcome label values for counters that record success or failure.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
)

// Stage-1 manifest gates: the closed label set of
// manifest_gate_rejections_total. Rule IDs and field paths stay in the API
// response, never in a label.
const (
	GateSARIF  = "sarif"   // spec.audit.security_scan: missing or unparseable scan, blocking findings
	GateDPIA   = "dpia"    // spec.audit.dpia evidence
	GateAIRisk = "ai_risk" // spec.audit.ai_risk evidence and the spec.ai block
	GateChart  = "chart"   // Chart.yaml, values.schema.json, spec.chart
	GateSchema = "schema"  // the manifest itself and every other evidence file
)

// ManifestGates lists every Stage-1 gate.
var ManifestGates = []string{GateSARIF, GateDPIA, GateAIRisk, GateChart, GateSchema}

// AuditSubmissionsTotal counts Stage-1 submissions (CreateApp,
// CreateAppVersion and the push revalidation worker): outcome "success"
// when the AuditSubmission row is created, "failure" when a manifest gate
// rejects it. Infrastructure errors (GitHub or the database unavailable)
// are not counted.
func AuditSubmissionsTotal(outcome string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`audit_submissions_total{outcome=%q}`, outcome))
}

// ManifestGateRejectionsTotal counts Stage-1 rejections per gate: one
// increment per rejected submission per gate that rejected it, however many
// findings that gate produced. gate is one of ManifestGates.
func ManifestGateRejectionsTotal(gate string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`manifest_gate_rejections_total{gate=%q}`, gate))
}

// RecordSubmission counts one Stage-1 submission. With no gates it is a
// success; otherwise a failure, and each gate in gates is counted once.
func RecordSubmission(gates []string) {
	if len(gates) == 0 {
		AuditSubmissionsTotal(OutcomeSuccess).Inc()
		return
	}
	AuditSubmissionsTotal(OutcomeFailure).Inc()
	for _, g := range gates {
		ManifestGateRejectionsTotal(g).Inc()
	}
}

// AuditSubmissionStateGauge reports how many audit submissions sit in each
// AppVersion state (the auditor queue is state "submitted"). Set from the
// reconciler's periodic collector, with explicit zeros for empty states.
// Every API replica reports the same cluster-wide value: query it with
// max by (state).
func AuditSubmissionStateGauge(state string) *vm.Gauge {
	return vm.GetOrCreateGauge(fmt.Sprintf(`audit_submission_state_total{state=%q}`, state), nil)
}

// AuditDecisionsTotal counts committed auditor decisions. decision is
// approve, request_changes or reject.
func AuditDecisionsTotal(decision string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`audit_decisions_total{decision=%q}`, decision))
}

// AuditDecisionLatencySeconds observes auditor turnaround: the time from the
// AuditSubmission's creation to its committed decision.
var AuditDecisionLatencySeconds = vm.NewHistogram(`audit_decision_latency_seconds`)

// Orchestrator operations: the closed label set of orchestrator_calls_total
// and orchestrator_call_duration_seconds, one per orchestrator.Deployer
// method.
const (
	OperationApply    = "apply"
	OperationStatus   = "status"
	OperationDelete   = "delete"
	OperationRollback = "rollback"
)

// OrchestratorOperations lists every orchestrator operation.
var OrchestratorOperations = []string{OperationApply, OperationStatus, OperationDelete, OperationRollback}

// OrchestratorCallsTotal counts orchestrator.Deployer calls by operation and
// outcome. No cluster, organisation or namespace label: those would grow
// with the customer count.
func OrchestratorCallsTotal(operation, outcome string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`orchestrator_calls_total{operation=%q,outcome=%q}`, operation, outcome))
}

// OrchestratorCallDuration times orchestrator.Deployer calls, in seconds.
func OrchestratorCallDuration(operation string) *vm.Histogram {
	return vm.GetOrCreateHistogram(fmt.Sprintf(`orchestrator_call_duration_seconds{operation=%q}`, operation))
}

// DeploymentTransitionsTotal counts persisted deployment state changes. from
// and to are deployment states; a newly created deployment comes from
// "unknown".
func DeploymentTransitionsTotal(from, to string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`deployment_transitions_total{from=%q,to=%q}`, from, to))
}

// AgentHeartbeatAgeSeconds is the age of the stalest agent heartbeat across
// clusters that are not decommissioned and have reported at least once: one
// series answers "is any agent stale" without a series per cluster. Set by
// the reconciler's collector; 0 when no cluster has reported. Every API
// replica reports the same value: query it with max().
var AgentHeartbeatAgeSeconds = vm.NewGauge(`agent_heartbeat_age_seconds`, nil)

// AgentCommandsPending counts agent commands that are neither acknowledged
// nor expired, across all clusters. Set by the reconciler's collector. Every
// API replica reports the same value: query it with max().
var AgentCommandsPending = vm.NewGauge(`agent_commands_pending`, nil)

// HubAgentCertRenewalsTotal counts agent certificate renewals the hub
// handled on POST /clusters/{id}/agent/renew: outcome "success" when a
// certificate was issued, "failure" when an authenticated agent's request
// was refused or could not be served. Exposed as agent_cert_renewals_total
// on the hub's /metrics; the agent has its own series of that name.
func HubAgentCertRenewalsTotal(outcome string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`agent_cert_renewals_total{outcome=%q}`, outcome))
}

// agentCertMinRemainingName is the hub gauge SetAgentCertMinRemainingSeconds
// writes.
const agentCertMinRemainingName = `agent_cert_min_remaining_seconds`

// SetAgentCertMinRemainingSeconds publishes agent_cert_min_remaining_seconds:
// the shortest remaining validity, in seconds, of the active agent
// certificates across clusters that are not decommissioned and have a known
// expiry. known=false removes the series, so a hub with no such cluster
// reports nothing rather than a 0 that would read as an expired
// certificate. Set by the reconciler's collector; every API replica reports
// the same value, so query it with min().
func SetAgentCertMinRemainingSeconds(seconds float64, known bool) {
	if !known {
		vm.UnregisterMetric(agentCertMinRemainingName)
		return
	}
	vm.GetOrCreateGauge(agentCertMinRemainingName, nil).Set(seconds)
}

// Authentication methods: the closed method label set of
// auth_attempts_total and auth_failures_total. AuthMethodNone appears only
// on auth_failures_total{reason="no_credential"}: a request that presented
// nothing has no method and is not an attempt.
const (
	AuthMethodOIDC   = "oidc"
	AuthMethodAPIKey = "apikey"
	AuthMethodDev    = "dev"
	AuthMethodNone   = "none"
)

// Authentication failure reasons: the closed reason label set of
// auth_failures_total. Never the credential, a username or an email.
const (
	// AuthReasonInvalidSignature: the credential failed verification (a
	// forged or mismatched state, an id_token that does not verify, an
	// authorization code the IdP refused).
	AuthReasonInvalidSignature = "invalid_signature"
	// AuthReasonExpired: an expired API key, login state or id_token.
	AuthReasonExpired = "expired"
	// AuthReasonRevoked: a revoked API key.
	AuthReasonRevoked = "revoked"
	// AuthReasonUnknownKey: an API key, or a dev-mode user ID, that matches
	// nothing.
	AuthReasonUnknownKey = "unknown_key"
	// AuthReasonNoCredential: a route that requires a principal got none.
	AuthReasonNoCredential = "no_credential"
	// AuthReasonMalformed: a credential that cannot be parsed (an API key of
	// the wrong shape, a dev user ID that is not a UUID, a callback without
	// code, state or id_token).
	AuthReasonMalformed = "malformed"
	// AuthReasonNoRoles: a valid API key whose scope no longer overlaps any
	// of its owner's roles.
	AuthReasonNoRoles = "no_roles"
)

// AuthAttemptsTotal counts presented credentials by method and outcome: API
// keys, OIDC callbacks (never the redirect to the IdP) and dev-mode headers.
// A non-zero method="dev" series in production is a misconfiguration.
// Infrastructure errors (the database or IdP unreachable) are not counted.
func AuthAttemptsTotal(method, outcome string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`auth_attempts_total{method=%q,outcome=%q}`, method, outcome))
}

// AuthFailuresTotal counts authentication failures by method and reason.
func AuthFailuresTotal(method, reason string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`auth_failures_total{method=%q,reason=%q}`, method, reason))
}

// AuthzDenialsTotal counts 403s from auth.RequireAnyRole. path is the Echo
// route pattern (c.Path()), never the raw URL, so cardinality is bounded by
// the route table.
func AuthzDenialsTotal(path string) *vm.Counter {
	return vm.GetOrCreateCounter(fmt.Sprintf(`authz_denials_total{path=%q}`, path))
}

// APIKeyActiveTotal counts API keys that are neither revoked nor expired.
// Set by the reconciler's collector. Every API replica reports the same
// value: query it with max().
var APIKeyActiveTotal = vm.NewGauge(`apikey_active_total`, nil)

// ─── Cluster agent (cmd/agent) ────────────────────────────────────────────────
//
// The agent's metrics live in agentSet, which is never registered with the
// default set: the hub's /metrics carries no agent series (agent_up would
// read 1 there) and the agent's /metrics carries none of the hub series
// declared above (agent_heartbeat_age_seconds would read 0 there). Write
// them with WriteAgentPrometheus. No metric carries a cluster ID label: an
// agent serves one cluster, so the identity belongs on the scrape target.

var agentSet = vm.NewSet()

// Agent command types: the closed command label set of
// agent_commands_processed_total and agent_command_duration_seconds. A type
// the agent does not handle is labelled AgentCommandUnknown, never with the
// type string the hub sent.
const (
	AgentCommandApply    = "apply"
	AgentCommandDelete   = "delete"
	AgentCommandRollback = "rollback"
	AgentCommandUnknown  = "unknown"
)

// AgentCommands lists every agent command label value.
var AgentCommands = []string{AgentCommandApply, AgentCommandDelete, AgentCommandRollback, AgentCommandUnknown}

// AgentCommandLabel maps a command type received from the hub to its label
// value: the type itself when it is in the closed set, AgentCommandUnknown
// otherwise.
func AgentCommandLabel(commandType string) string {
	switch commandType {
	case AgentCommandApply, AgentCommandDelete, AgentCommandRollback:
		return commandType
	default:
		return AgentCommandUnknown
	}
}

// AgentHeartbeatsTotal counts heartbeats from the agent's own view: outcome
// "success" when the hub accepted it, "failure" for any error, including a
// hub that cannot be reached or rejects the client certificate.
func AgentHeartbeatsTotal(outcome string) *vm.Counter {
	return agentSet.GetOrCreateCounter(fmt.Sprintf(`agent_heartbeats_total{outcome=%q}`, outcome))
}

// AgentCommandsProcessedTotal counts commands the agent executed, by command
// (one of AgentCommands) and outcome. A command that fails is still
// acknowledged to the hub; its outcome here is "failure".
func AgentCommandsProcessedTotal(command, outcome string) *vm.Counter {
	return agentSet.GetOrCreateCounter(fmt.Sprintf(`agent_commands_processed_total{command=%q,outcome=%q}`, command, outcome))
}

// AgentCommandDuration times command execution, in seconds: the Argo CD
// operation plus, for apply, the state report that follows it.
func AgentCommandDuration(command string) *vm.Histogram {
	return agentSet.GetOrCreateHistogram(fmt.Sprintf(`agent_command_duration_seconds{command=%q}`, command))
}

// AgentHubRequestDuration times every agent request to the hub (heartbeat,
// state report, command ack), in seconds, up to the response headers or the
// error. Set against agent_command_duration_seconds it separates a slow
// network or hub from slow work on the spoke.
var AgentHubRequestDuration = agentSet.NewHistogram(`agent_hub_request_duration_seconds`)

// AgentCertRenewalsTotal counts the agent's own certificate renewal
// attempts: outcome "success" once the hub issued a certificate and the
// agent switched to it, "failure" for any attempt that did not.
func AgentCertRenewalsTotal(outcome string) *vm.Counter {
	return agentSet.GetOrCreateCounter(fmt.Sprintf(`agent_cert_renewals_total{outcome=%q}`, outcome))
}

// AgentCertRemainingSeconds is the remaining validity of the client
// certificate the agent currently presents, in seconds. Updated every
// heartbeat cycle; 0 in dev mode, where the agent has no certificate.
var AgentCertRemainingSeconds = agentSet.NewGauge(`agent_cert_remaining_seconds`, nil)

// WriteAgentPrometheus writes the agent's metrics in Prometheus text format,
// the counterpart of WritePrometheus for the agent binary. Pass
// exposeProcessMetrics=true to include Go runtime / process metrics.
func WriteAgentPrometheus(w io.Writer, exposeProcessMetrics bool) {
	agentSet.WritePrometheus(w)
	if exposeProcessMetrics {
		vm.WriteProcessMetrics(w)
	}
}

func init() {
	// agent_up is a constant 1: a scraper's up series and this gauge's
	// absence both say the agent is gone.
	agentSet.NewGauge(`agent_up`, func() float64 { return 1 })

	// Expose every series of the closed sets from the first scrape, so a
	// first increment shows up in increase() and rate().
	for _, o := range []string{OutcomeSuccess, OutcomeFailure} {
		AgentHeartbeatsTotal(o)
		AgentCertRenewalsTotal(o)
		HubAgentCertRenewalsTotal(o)
		for _, c := range AgentCommands {
			AgentCommandsProcessedTotal(c, o)
		}
		AuditSubmissionsTotal(o)
		for _, op := range OrchestratorOperations {
			OrchestratorCallsTotal(op, o)
		}
		for _, m := range []string{AuthMethodOIDC, AuthMethodAPIKey, AuthMethodDev} {
			AuthAttemptsTotal(m, o)
		}
	}
	for _, g := range ManifestGates {
		ManifestGateRejectionsTotal(g)
	}
}

// WritePrometheus writes all registered metrics in Prometheus text format.
// Pass exposeProcessMetrics=true to include Go runtime / process metrics.
func WritePrometheus(w io.Writer, exposeProcessMetrics bool) {
	vm.WritePrometheus(w, exposeProcessMetrics)
}
