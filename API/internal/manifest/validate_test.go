package manifest

import "testing"

// validManifest returns a manifest that passes every ValidateSchema check.
// It carries several interfaces so the per-interface work in ValidateSchema
// is exercised on the happy path (where no error strings should be built).
func validManifest() *AppManifest {
	return &AppManifest{
		APIVersion: CurrentAPIVersion,
		Kind:       CurrentKind,
		Metadata: Metadata{
			Name:        "govchat",
			DisplayName: "GovChat",
			Vendor:      "Gemeente Voorbeeld",
			Contact:     "contact@voorbeeld.example.nl",
			License:     "EUPL-1.2",
		},
		Spec: Spec{
			Chart: Chart{Path: "./chart", Type: "helm"},
			Interfaces: []Interface{
				{Type: "api", OpenAPI: "./api/openapi.yaml"},
				{Type: "mcp", ServerPath: "./mcp/server.py"},
				{Type: "api", OpenAPI: "./api/openapi-v2.yaml"},
			},
			Demo: Demo{
				Description: "./demo/description.md",
				Screenshots: []string{
					"./demo/01.png", "./demo/02.png", "./demo/03.png", "./demo/04.png",
				},
			},
			Audit: Audit{
				DPIA:          "./audit/dpia.pdf",
				SecurityScan:  "./audit/scan.sarif",
				AIRisk:        "./audit/ai-risk.pdf",
				Accessibility: "./audit/wcag.pdf",
			},
			Categories: []string{"chatbot", "rag", "dutch"},
			AI: AIBlock{
				RiskClass:     "limited",
				BaseModel:     "gpt-4o-mini",
				DataResidency: "EU",
			},
		},
	}
}

// BenchmarkValidateSchemaValid measures the happy path — the common case on
// re-gate, where a previously-published manifest is re-validated on every
// push to the linked branch and should produce zero errors.
func BenchmarkValidateSchemaValid(b *testing.B) {
	m := validManifest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if errs := m.ValidateSchema(); len(errs) != 0 {
			b.Fatalf("valid manifest produced %d errors: %+v", len(errs), errs)
		}
	}
}

func TestValidateSchemaValid(t *testing.T) {
	if errs := validManifest().ValidateSchema(); len(errs) != 0 {
		t.Fatalf("valid manifest should have no errors, got %d: %+v", len(errs), errs)
	}
}

func TestValidateSchemaCatchesErrors(t *testing.T) {
	m := validManifest()
	m.APIVersion = "wrong/v0"
	m.Kind = "Nope"
	m.Metadata.Name = "X" // fails slug regex
	m.Metadata.License = "Proprietary"
	m.Spec.Chart.Type = "kustomize"
	m.Spec.Interfaces = []Interface{{Type: "grpc"}} // invalid type
	m.Spec.Demo.Screenshots = []string{"a.png"}     // too few
	m.Spec.AI.RiskClass = "extreme"
	m.Spec.AI.DataResidency = "US"

	errs := m.ValidateSchema()
	wantCodes := map[string]bool{
		"apiVersion":              false,
		"kind":                    false,
		"metadata.name":           false,
		"metadata.license":        false,
		"spec.chart.type":         false,
		"spec.interfaces[0].type": false,
		"spec.demo.screenshots":   false,
		"spec.ai.risk_class":      false,
		"spec.ai.data_residency":  false,
	}
	for _, e := range errs {
		if _, ok := wantCodes[e.Path]; ok {
			wantCodes[e.Path] = true
		}
	}
	for path, seen := range wantCodes {
		if !seen {
			t.Errorf("expected a validation error at path %q, none found", path)
		}
	}
}

// TestValidateSchemaInterfaceRequirements checks the api/mcp sub-requirements.
func TestValidateSchemaInterfaceRequirements(t *testing.T) {
	m := validManifest()
	m.Spec.Interfaces = []Interface{
		{Type: "api"},                       // missing openapi
		{Type: "mcp"},                       // missing server_path
		{Type: "api", OpenAPI: "./ok.yaml"}, // fine
	}
	errs := m.ValidateSchema()
	var haveOpenAPI, haveServerPath bool
	for _, e := range errs {
		switch e.Path {
		case "spec.interfaces[0].openapi":
			haveOpenAPI = true
		case "spec.interfaces[1].server_path":
			haveServerPath = true
		}
	}
	if !haveOpenAPI {
		t.Error("expected spec.interfaces[0].openapi required error")
	}
	if !haveServerPath {
		t.Error("expected spec.interfaces[1].server_path required error")
	}
}
