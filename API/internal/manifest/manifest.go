// Package manifest parses + validates a developer's appstore.yaml. The
// Stage-1 auto-validator runs every check that does not require human
// judgement — schema completeness, evidence-file presence, content-type
// allow-listing, SARIF 2.1.0 parsing + CVSS ≥ 7.0 blocking,
// license SPDX-allow-listing — and returns a list of FieldError.
// Stage 2 (human audit) is owned by the audit subsystem.
//
// The wire schema mirrors the example in the design document:
//
//	apiVersion: appstore.saidkick.nl/v1
//	kind: AppManifest
//	metadata:
//	  name: govchat
//	  display_name: GovChat
//	  vendor: Gemeente Voorbeeld
//	  contact: contact@voorbeeld.example.nl
//	  license: EUPL-1.2
//	spec:
//	  chart:
//	    path: ./chart
//	    type: helm
//	  interfaces:
//	    - { type: api, openapi: ./api/openapi.yaml }
//	  demo:
//	    description: ./demo/description.md
//	    screenshots: [ ./demo/screenshots/01.png, ./demo/screenshots/02.png, ./demo/screenshots/03.png ]
//	    video: ./demo/video.mp4        # optional
//	  audit:
//	    dpia: ./audit/dpia.pdf
//	    security_scan: ./audit/security-scan.sarif
//	    ai_risk: ./audit/ai-risk.pdf
//	    accessibility: ./audit/wcag-2.2.pdf
//	    model_card: ./audit/model-card.md
//	  categories: [chatbot, rag, dutch]
//	  ai:
//	    risk_class: limited
//	    base_model: gpt-4o-mini
//	    data_residency: EU
package manifest

import (
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// CurrentAPIVersion is the only apiVersion the platform currently recognises.
const CurrentAPIVersion = "appstore.saidkick.nl/v1"

// CurrentKind is the only kind the platform currently recognises.
const CurrentKind = "AppManifest"

// AppManifest is the deserialised appstore.yaml.
type AppManifest struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind"       json:"kind"`
	Metadata   Metadata `yaml:"metadata"   json:"metadata"`
	Spec       Spec     `yaml:"spec"       json:"spec"`
}

// Metadata is the identifying + licensing block.
type Metadata struct {
	Name        string `yaml:"name"         json:"name"`
	DisplayName string `yaml:"display_name" json:"display_name"`
	Vendor      string `yaml:"vendor"       json:"vendor"`
	Contact     string `yaml:"contact"      json:"contact"`
	License     string `yaml:"license"      json:"license"`
}

// Spec is the technical descriptor block.
type Spec struct {
	Chart      Chart       `yaml:"chart"      json:"chart"`
	Interfaces []Interface `yaml:"interfaces" json:"interfaces"`
	Demo       Demo        `yaml:"demo"       json:"demo"`
	Audit      Audit       `yaml:"audit"      json:"audit"`
	Categories []string    `yaml:"categories" json:"categories,omitempty"`
	AI         AIBlock     `yaml:"ai"         json:"ai"`
}

// Chart is the Helm-chart pointer (relative path inside the repo).
type Chart struct {
	Path string `yaml:"path" json:"path"`
	Type string `yaml:"type" json:"type"` // "helm" only in V1
}

// Interface describes how the application exposes itself programmatically.
// Either type="api" (with openapi spec path) or type="mcp" (with server path).
type Interface struct {
	Type       string `yaml:"type"                  json:"type"`
	OpenAPI    string `yaml:"openapi,omitempty"     json:"openapi,omitempty"`
	ServerPath string `yaml:"server_path,omitempty" json:"server_path,omitempty"`
}

// Demo holds the mandatory voorkant assets per Principe 4.
type Demo struct {
	Description string   `yaml:"description"     json:"description"`
	Screenshots []string `yaml:"screenshots"     json:"screenshots"`
	Video       string   `yaml:"video,omitempty" json:"video,omitempty"`
}

// Audit holds paths to the evidence files committed alongside the chart.
// Every path is checked for existence + content-type in Stage-1 validation.
type Audit struct {
	DPIA          string `yaml:"dpia"                    json:"dpia"`
	SecurityScan  string `yaml:"security_scan"           json:"security_scan"`
	AIRisk        string `yaml:"ai_risk"                 json:"ai_risk"`
	Accessibility string `yaml:"accessibility"           json:"accessibility"`
	ModelCard     string `yaml:"model_card,omitempty"    json:"model_card,omitempty"`
}

// AIBlock declares classification, base model, and the data-residency
// invariant (EU-only).
type AIBlock struct {
	RiskClass      string `yaml:"risk_class"      json:"risk_class"`
	BaseModel      string `yaml:"base_model"      json:"base_model"`
	DataResidency  string `yaml:"data_residency"  json:"data_residency"`
}

// SlugRegex is the allowed pattern for Metadata.Name (and the App.Slug
// derived from it). 3–60 chars, kebab-case, must start with a letter,
// must end with letter or digit.
var SlugRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,58}[a-z0-9]$`)

// AllowedLicenses is the SPDX allowlist. Anything outside the list is
// rejected at Stage-1 — including proprietary licenses.
var AllowedLicenses = map[string]struct{}{
	"EUPL-1.2":      {},
	"Apache-2.0":    {},
	"MIT":           {},
	"BSD-3-Clause":  {},
	"GPL-3.0-only":  {},
	"LGPL-3.0-only": {},
	"MPL-2.0":       {},
}

// AllowedRiskClasses follows the EU AI Act taxonomy as adopted by sAIdkick.
var AllowedRiskClasses = map[string]struct{}{
	"minimal": {},
	"limited": {},
	"high":    {},
}

// AllowedInterfaceTypes restricts spec.interfaces[].type.
var AllowedInterfaceTypes = map[string]struct{}{
	"api": {},
	"mcp": {},
}

// MinScreenshots is the lower bound enforced by Principe 4.
const MinScreenshots = 3

// MaxScreenshots is an upper bound to prevent repo-stuffing DoS.
const MaxScreenshots = 20

// Parse deserialises a YAML manifest. Returns a structured AppManifest
// or a typed parse error.
func Parse(yamlBytes []byte) (*AppManifest, error) {
	if len(yamlBytes) == 0 {
		return nil, errors.New("manifest: empty payload")
	}
	var m AppManifest
	if err := yaml.Unmarshal(yamlBytes, &m); err != nil {
		return nil, fmt.Errorf("manifest: yaml parse: %w", err)
	}
	return &m, nil
}
