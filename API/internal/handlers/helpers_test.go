package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth/authtest"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	"github.com/ClappFormOrg/SaidKick/API/internal/handlers"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testJoinSecret is the HMAC key newTestHandlers configures for cluster
// join tokens.
const testJoinSecret = "test-join-secret"

// handlerDeps holds the dependencies a test may swap in. Nil fields fall
// back to the defaults handlers.New picks.
type handlerDeps struct {
	github      gh.Client
	deployer    orchestrator.Deployer
	oidcSecrets *auth.SecretRefStore
	pki         vault.PKISigner
}

type handlersOption func(*handlerDeps)

// withGitHub wires c as the GitHub client.
func withGitHub(c gh.Client) handlersOption {
	return func(d *handlerDeps) { d.github = c }
}

// withDeployer wires d as the orchestrator.
func withDeployer(dep orchestrator.Deployer) handlersOption {
	return func(d *handlerDeps) { d.deployer = dep }
}

// withOIDCSecrets wires s as the OIDC client secret store.
func withOIDCSecrets(s *auth.SecretRefStore) handlersOption {
	return func(d *handlerDeps) { d.oidcSecrets = s }
}

// withPKI wires s as the agent certificate signer.
func withPKI(s vault.PKISigner) handlersOption {
	return func(d *handlerDeps) { d.pki = s }
}

// newTestHandlers builds a Handlers on handle with a no-op audit logger and
// the test join secret and hub endpoint. handle may be nil for tests that
// return before the handler touches the database.
func newTestHandlers(t *testing.T, handle *db.Handle, opts ...handlersOption) *handlers.Handlers {
	t.Helper()
	var d handlerDeps
	for _, opt := range opts {
		opt(&d)
	}
	return handlers.New(handle, audit.NoopLogger{}, d.github, d.deployer, nil, d.oidcSecrets, nil, d.pki,
		[]byte(testJoinSecret), "http://localhost:8080", nil, "", "")
}

// serveRoute sends req through the routes h registers, including their
// role middleware, and returns the recorded response.
func serveRoute(h *handlers.Handlers, req *http.Request) *httptest.ResponseRecorder {
	e := echo.New()
	h.Register(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// requireAPIError asserts that err is an *apierror.Error with status and
// returns a copy of it. The copy is not an error value, so callers that only
// check the status may ignore it.
func requireAPIError(t *testing.T, err error, status int) apierror.Error {
	t.Helper()
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), "expected *apierror.Error, got %T: %v", err, err)
	assert.Equal(t, status, apiErr.Status, "unexpected status: %s", apiErr.Message)
	return *apiErr
}

// principal returns a session Principal for u in u's organisation holding
// roles.
func principal(u models.User, roles ...models.Role) *auth.Principal {
	return authtest.SessionPrincipal(u.ID, u.OrganizationID, roles...)
}

// testUser returns an unsaved user in a random organisation, for tests that
// never reach the database.
func testUser() models.User {
	return models.User{IDModel: models.IDModel{ID: uuid.New()}, OrganizationID: uuid.New()}
}

// newRequest builds a request for a direct handler call. A non-empty body is
// sent as JSON; an empty body sends no body and no content type.
func newRequest(method, target, body string) *http.Request {
	if body == "" {
		return httptest.NewRequest(method, target, http.NoBody)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return req
}

// asPrincipal attaches p to the request context, as the authenticator
// middleware would.
func asPrincipal(req *http.Request, p *auth.Principal) *http.Request {
	return req.WithContext(authtest.ContextWithPrincipal(req.Context(), p))
}

// asAgent attaches the agent identity for clusterID to the request context,
// as the agent authentication middleware would.
func asAgent(req *http.Request, clusterID uuid.UUID) *http.Request {
	return req.WithContext(auth.StoreAgentIdentity(req.Context(), auth.AgentIdentity{ClusterID: clusterID}))
}

// echoContext wraps req in an Echo context with a response recorder. params
// are path parameter name/value pairs.
func echoContext(e *echo.Echo, req *http.Request, params ...string) (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if len(params) > 0 {
		if len(params)%2 != 0 {
			panic("echoContext: params must be name/value pairs")
		}
		names := make([]string, 0, len(params)/2)
		values := make([]string, 0, len(params)/2)
		for i := 0; i < len(params); i += 2 {
			names = append(names, params[i])
			values = append(values, params[i+1])
		}
		c.SetParamNames(names...)
		c.SetParamValues(values...)
	}
	return c, rec
}
