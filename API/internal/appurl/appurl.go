// Package appurl derives the browsable URL of a deployed application from
// the non-secret Helm values stored on its Deployment.
//
// The platform stores no URL for a deployed app. It has a convention: the
// deploy form pre-fills values.ingress.host from the target cluster's
// base_domain, the deployer confirms or overrides it, and the chart turns it
// into an Ingress. This package reads that convention back.
//
// ingress.host is deployer-typed free text that ends up in an href shown to
// every member of an organisation, so it is validated as a DNS hostname
// first. The scheme follows ingress.tls, so a chart deployed with TLS off
// gets a link that can connect.
//
// The URL is a prediction, not an observation: nothing here contacts the
// cluster. A chart that ignores ingress.host derives KindNone, and the
// caller shows the app without an "open" link.
package appurl

import (
	"encoding/json"
	"net"
	"strings"
)

// Kind reports whether a deployment exposes a browsable web interface.
type Kind string

const (
	// KindWeb means a usable absolute URL was derived.
	KindWeb Kind = "web"
	// KindNone means the values describe no web interface this package is
	// willing to link to.
	KindNone Kind = "none"
)

// Access is the derived, never-stored access descriptor for one deployment.
type Access struct {
	Kind Kind
	// URL is absolute and scheme-qualified. Empty whenever Kind is KindNone.
	URL string
}

const (
	// maxHostnameLen is the RFC 1035 limit on a presentation-format name.
	maxHostnameLen = 253
	// maxLabelLen is the RFC 1035 limit on a single DNS label.
	maxLabelLen = 63
)

var none = Access{Kind: KindNone}

// Derive reports whether a deployment's values describe a reachable web
// interface, and at which URL. It never fails: an unparseable, hostile or
// web-less values blob yields KindNone, which every caller treats as "no
// link".
func Derive(values []byte) Access {
	if len(values) == 0 {
		return none
	}

	// A narrow struct rather than map[string]any: an ingress that is a
	// string, number or array fails the unmarshal instead of needing type
	// assertions.
	var doc struct {
		Ingress *struct {
			Enabled *bool  `json:"enabled"`
			Host    string `json:"host"`
			TLS     *bool  `json:"tls"`
		} `json:"ingress"`
	}
	if err := json.Unmarshal(values, &doc); err != nil || doc.Ingress == nil {
		return none
	}
	// An absent enabled leaves the decision to the chart, and charts that
	// follow docs/app-helm-chart-guide.md enable the Ingress. Only an
	// explicit false opts out.
	if doc.Ingress.Enabled != nil && !*doc.Ingress.Enabled {
		return none
	}

	host, ok := normaliseHost(doc.Ingress.Host)
	if !ok {
		return none
	}

	scheme := "https"
	if doc.Ingress.TLS != nil && !*doc.Ingress.TLS {
		scheme = "http"
	}
	return Access{Kind: KindWeb, URL: scheme + "://" + host}
}

// normaliseHost returns raw lowercased when it is a multi-label DNS
// hostname. That excludes every character that could change the meaning of
// the URL it is placed in (':', '/', '?', '#', '@', whitespace) and every
// non-ASCII byte, so no port, path, userinfo or lookalike can slip through.
func normaliseHost(raw string) (string, bool) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || len(host) > maxHostnameLen {
		return "", false
	}
	// An IPv4 literal passes the label rules below. A launcher should not
	// present a bare IP as an organisation's app: it has no DNS name and no
	// certificate to match.
	if net.ParseIP(host) != nil {
		return "", false
	}
	labels := strings.Split(host, ".")
	// A single label is an internal short name. This also rejects a
	// trailing dot, whose final label is empty.
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if !isDNSLabel(label) {
			return "", false
		}
	}
	// A numeric top-level label is not a DNS name (RFC 3696 section 2) and
	// catches IP-like forms net.ParseIP rejects, such as "127.1".
	if isAllDigits(labels[len(labels)-1]) {
		return "", false
	}
	return host, true
}

// isDNSLabel reports whether s is 1 to 63 bytes of lowercase letters,
// digits and hyphens, not starting or ending with a hyphen. Callers
// lowercase first.
func isDNSLabel(s string) bool {
	if s == "" || len(s) > maxLabelLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i != 0 && i != len(s)-1:
		default:
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
