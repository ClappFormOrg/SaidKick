package auth

import "github.com/ClappFormOrg/SaidKick/API/internal/metrics"

// authSucceeded counts a successful authentication attempt by method.
func authSucceeded(method string) {
	metrics.AuthAttemptsTotal(method, metrics.OutcomeSuccess).Inc()
}

// authFailed counts a failed authentication attempt by method, with its
// reason. reason is one of the metrics.AuthReason* constants.
func authFailed(method, reason string) {
	metrics.AuthAttemptsTotal(method, metrics.OutcomeFailure).Inc()
	metrics.AuthFailuresTotal(method, reason).Inc()
}

// noCredential counts a request that reached a route requiring a principal
// without one. It is not an attempt: nothing was presented.
func noCredential() {
	metrics.AuthFailuresTotal(metrics.AuthMethodNone, metrics.AuthReasonNoCredential).Inc()
}
