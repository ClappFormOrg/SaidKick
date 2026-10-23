// Package valuesschema enforces a chart's values.schema.json against the
// deployment values a user submits.
//
// It exists to close the audit-gate bypass (finding INPUTVAL-001): the whole
// appstore control is "only auditor-approved software runs on the estate", but
// that guarantee is worthless if a deployer can smuggle arbitrary Helm values
// (image.repository, command, securityContext, …) into an approved chart. So
// this package validates the full schema — types, enums, formats, required —
// and, crucially, rejects any property the schema did not declare
// (additionalProperties:false, injected by policy rather than trusted from the
// author). It also fails closed: a schema that cannot be compiled is treated
// as a hard error at deploy time, never silently skipped (finding INPUTVAL-002).
//
// The secret/non-secret split is preserved: top-level properties tagged
// "format":"secret" must be supplied via secret_inputs (routed to Vault), never
// in values, and required secrets must be present in secret_inputs.
package valuesschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
)

// schemaURL is the in-memory location the schema is registered under; it never
// leaves this package.
const schemaURL = "mem://values.schema.json"

// maxFieldErrors caps how many per-field errors a single validation returns, so
// a pathological payload cannot flood the response.
const maxFieldErrors = 50

// Enforceable reports whether raw is a values.schema.json the deploy-time
// validator can actually compile and enforce. Schema-ingestion points
// (app/version creation, revalidation) call this so an unenforceable schema can
// never be stored on a published version and reach a deployment. A nil return
// means Validate will never fail closed for this schema.
func Enforceable(raw []byte) error {
	_, err := compile(raw)
	return err
}

// Validate checks values against the chart's rawSchema and enforces the
// secret/non-secret split.
//
//   - errs holds per-field validation failures (map to a 422 validation error).
//   - fatal is non-nil when the schema itself cannot be compiled/enforced; the
//     caller MUST reject the deployment (fail closed) rather than proceed.
//
// A nil errs and nil fatal means the values are valid.
func Validate(rawSchema []byte, values json.RawMessage, secretInputs map[string]string) (errs []apierror.FieldError, fatal error) {
	// The values payload must be a JSON object — Helm values are always a
	// mapping. Arrays, scalars, and malformed JSON are rejected outright.
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(values))
	if err != nil {
		return []apierror.FieldError{{Path: "values", Code: "invalid_json", Message: "values must be a JSON object"}}, nil
	}
	instMap, ok := inst.(map[string]any)
	if !ok {
		return []apierror.FieldError{{Path: "values", Code: "invalid_type", Message: "values must be a JSON object"}}, nil
	}

	secretFields, requiredSecrets := parseSecretMeta(rawSchema)

	// Secret fields must never appear in values (they route to Vault via
	// secret_inputs). Remove any that do before schema validation so the same
	// mistake is not also reported as an "unexpected property".
	for field := range secretFields {
		if _, present := instMap[field]; present {
			errs = append(errs, apierror.FieldError{
				Path:    "values." + field,
				Code:    "secret_in_values",
				Message: field + " is a secret field and must be supplied in secret_inputs, not values",
			})
			delete(instMap, field)
		}
	}

	// Required secrets must be supplied via secret_inputs.
	for _, field := range requiredSecrets {
		if _, present := secretInputs[field]; !present {
			errs = append(errs, apierror.FieldError{
				Path:    "secret_inputs." + field,
				Code:    "required_secret_missing",
				Message: field + " is required and must be supplied in secret_inputs",
			})
		}
	}

	// Fail closed: an unparseable/unenforceable schema is a hard error, never a
	// silently-skipped guard.
	sch, err := compile(rawSchema)
	if err != nil {
		return errs, err
	}

	if verr := sch.Validate(instMap); verr != nil {
		errs = append(errs, mapValidationError(verr)...)
	}

	if len(errs) > maxFieldErrors {
		errs = errs[:maxFieldErrors]
	}
	return errs, nil
}

// compile decodes raw, applies the default-deny policy (additionalProperties:
// false) and strips secret fields from the instance-validation schema, then
// compiles it as draft-07. Any failure means the schema is unenforceable.
func compile(raw []byte) (*jsonschema.Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("values schema is empty")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("values schema is not valid JSON: %w", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("values schema must be a JSON object")
	}

	// Secret fields are supplied out-of-band via secret_inputs, so they must
	// not be part of the schema that validates the (non-secret) values
	// instance — otherwise a required secret would fail validation and a
	// secret property would be allowed through additionalProperties.
	stripSecretFields(root, parseSecretPropertyNames(root))
	denyUndeclaredProperties(root, true)

	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft7)
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("register values schema: %w", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile values schema: %w", err)
	}
	return sch, nil
}

// denyUndeclaredProperties injects "additionalProperties": false into object
// schemas so a deployer cannot submit any key the chart author did not declare.
//
// Policy:
//   - The top-level schema ALWAYS denies undeclared properties — unconditionally,
//     even overriding an author-supplied "additionalProperties":true. This is
//     the security boundary: it is where whole new value trees would be smuggled
//     in, and a propertyless top-level schema means "no configurable values",
//     not "anything goes" (closing the fail-open gap). patternProperties, if
//     present, still permit pattern-matched keys alongside the deny.
//   - Nested object schemas deny undeclared properties only when they declare
//     "properties" (i.e. the author described the shape) and have not already
//     set additionalProperties or patternProperties — so intentionally free-form
//     nested maps (podAnnotations, nodeSelector, …) keep working.
//
// Combinators (allOf/anyOf/oneOf/if/then/else) are intentionally left untouched:
// injecting additionalProperties into a branch would break composition, since a
// branch cannot see sibling branches' properties.
func denyUndeclaredProperties(node any, topLevel bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}

	if topLevel {
		m["additionalProperties"] = false
	} else if _, hasProps := m["properties"]; hasProps {
		_, hasAdditional := m["additionalProperties"]
		_, hasPattern := m["patternProperties"]
		if !hasAdditional && !hasPattern {
			m["additionalProperties"] = false
		}
	}

	if props, ok := m["properties"].(map[string]any); ok {
		for _, sub := range props {
			denyUndeclaredProperties(sub, false)
		}
	}
	switch items := m["items"].(type) {
	case map[string]any:
		denyUndeclaredProperties(items, false)
	case []any:
		for _, sub := range items {
			denyUndeclaredProperties(sub, false)
		}
	}
	for _, defsKey := range []string{"$defs", "definitions"} {
		if defs, ok := m[defsKey].(map[string]any); ok {
			for _, sub := range defs {
				denyUndeclaredProperties(sub, false)
			}
		}
	}
}

// parseSecretPropertyNames returns the top-level property names tagged
// "format":"secret" in an already-decoded schema root.
func parseSecretPropertyNames(root map[string]any) map[string]struct{} {
	out := make(map[string]struct{})
	props, ok := root["properties"].(map[string]any)
	if !ok {
		return out
	}
	for name, raw := range props {
		if prop, ok := raw.(map[string]any); ok {
			if format, ok := prop["format"].(string); ok && format == "secret" {
				out[name] = struct{}{}
			}
		}
	}
	return out
}

// stripSecretFields removes secret properties from properties and required so
// the instance-validation schema only governs non-secret values.
func stripSecretFields(root map[string]any, secretFields map[string]struct{}) {
	if len(secretFields) == 0 {
		return
	}
	if props, ok := root["properties"].(map[string]any); ok {
		for field := range secretFields {
			delete(props, field)
		}
	}
	if required, ok := root["required"].([]any); ok {
		kept := make([]any, 0, len(required))
		for _, r := range required {
			if name, ok := r.(string); ok {
				if _, isSecret := secretFields[name]; isSecret {
					continue
				}
			}
			kept = append(kept, r)
		}
		root["required"] = kept
	}
}

// parseSecretMeta extracts the secret field names and the subset of them that
// are required, straight from the raw schema bytes (independent of the mutated
// compile-time copy).
func parseSecretMeta(rawSchema []byte) (secretFields map[string]struct{}, requiredSecrets []string) {
	secretFields = make(map[string]struct{})
	var s struct {
		Properties map[string]struct {
			Format string `json:"format"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(rawSchema, &s); err != nil {
		return secretFields, nil
	}
	for name, prop := range s.Properties {
		if prop.Format == "secret" {
			secretFields[name] = struct{}{}
		}
	}
	for _, name := range s.Required {
		if _, isSecret := secretFields[name]; isSecret {
			requiredSecrets = append(requiredSecrets, name)
		}
	}
	return secretFields, requiredSecrets
}

// mapValidationError flattens a jsonschema validation error into per-field
// API errors, keyed by the JSON pointer of the offending value.
func mapValidationError(err error) []apierror.FieldError {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return []apierror.FieldError{{Path: "values", Code: "schema_violation", Message: err.Error()}}
	}

	out := make([]apierror.FieldError, 0)
	seen := make(map[string]struct{})
	var walk func(u *jsonschema.OutputUnit)
	walk = func(u *jsonschema.OutputUnit) {
		if u.Error != nil {
			path := instancePath(u.InstanceLocation)
			key := path + "|" + u.Error.String()
			if _, dup := seen[key]; !dup {
				seen[key] = struct{}{}
				out = append(out, apierror.FieldError{
					Path:    path,
					Code:    "schema_violation",
					Message: u.Error.String(),
				})
			}
		}
		for i := range u.Errors {
			walk(&u.Errors[i])
		}
	}
	walk(verr.BasicOutput())
	return out
}

// instancePath converts a JSON-pointer instance location ("/image/tag") into a
// dotted, values-prefixed field path ("values.image.tag").
func instancePath(pointer string) string {
	trimmed := strings.TrimPrefix(pointer, "/")
	if trimmed == "" {
		return "values"
	}
	return "values." + strings.ReplaceAll(trimmed, "/", ".")
}
