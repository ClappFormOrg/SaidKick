// Package handlers contains the HTTP transport layer of the API. One file
// per business domain; the Handlers struct is the shared dependency
// container.
package handlers

import (
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/db/models"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/secrets"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/ClappFormOrg/SaidKick/API/internal/sarifgate"
	"github.com/labstack/echo/v4"
)

// Handlers carries the dependencies every handler needs. Construction is
// done once at server start.
type Handlers struct {
	DB               *db.Handle
	Audit            audit.Logger
	GitHub           gh.Client
	Orchestrator     orchestrator.Deployer
	Secrets          secrets.Store
	OIDCSecrets      *auth.SecretRefStore
	OIDCAuth         *auth.OIDCAuthenticator
	PKI              vault.PKISigner // signs agent CSRs via Vault PKI
	JoinSecret       []byte          // HMAC key for cluster join-token JWTs
	HubEndpoint      string          // public base URL returned in handshake response
	HubAgentEndpoint string          // agent mTLS listener URL; printed as agent.hubAgentUrl in the install command
	VaultMount       string          // KV v2 mount; printed as vault.mount in the agent install command
	// AgentChartRepo is the OCI repository of the agent chart in the install
	// command (AGENT_CHART_REPO); empty uses config.DefaultAgentChartRepo.
	AgentChartRepo string
	// AgentChartVersion is printed as --version in the install command
	// (AGENT_CHART_VERSION); empty leaves the flag out.
	AgentChartVersion string
	AgentAuth         echo.MiddlewareFunc // mTLS / dev-mode agent authentication
	CommunityToken    string              // GitHub PAT for discussions proxy (empty = disabled)
	CommunityRepo     string              // "owner/repo" of the community GitHub repo

	// AgentCertTTL is the lifetime of agent client certificates issued by
	// the handshake and the renew route. Zero means 90 days.
	AgentCertTTL time.Duration

	// SARIFGateMode selects the Stage-1 SARIF rule. The zero value behaves
	// as manifest.SARIFGateReport.
	SARIFGateMode manifest.SARIFGateMode
	// SARIFReporter records findings that only the stricter SARIF rule
	// would block. New wires the production reporter; tests may swap it.
	SARIFReporter sarifgate.Reporter
}

// New constructs a Handlers.
func New(
	handle *db.Handle,
	logger audit.Logger,
	ghClient gh.Client,
	deployer orchestrator.Deployer,
	secretStore secrets.Store,
	oidcSecrets *auth.SecretRefStore,
	oidcAuth *auth.OIDCAuthenticator,
	pkiClient vault.PKISigner,
	joinSecret []byte,
	hubEndpoint string,
	agentAuth echo.MiddlewareFunc,
	communityToken string,
	communityRepo string,
) *Handlers {
	if ghClient == nil {
		ghClient = gh.NullClient{}
	}
	if deployer == nil {
		deployer = orchestrator.NullDeployer{}
	}
	if secretStore == nil {
		secretStore = secrets.NewInMemoryStore()
	}
	if pkiClient == nil {
		pkiClient = vault.NullPKIClient{}
	}
	if agentAuth == nil {
		// Fallback: reject all agent calls (safe default).
		agentAuth = func(_ echo.HandlerFunc) echo.HandlerFunc {
			return func(_ echo.Context) error {
				return echo.ErrUnauthorized
			}
		}
	}
	return &Handlers{
		DB:             handle,
		Audit:          logger,
		GitHub:         ghClient,
		Orchestrator:   deployer,
		Secrets:        secretStore,
		OIDCSecrets:    oidcSecrets,
		OIDCAuth:       oidcAuth,
		PKI:            pkiClient,
		JoinSecret:     joinSecret,
		HubEndpoint:    hubEndpoint,
		AgentAuth:      agentAuth,
		CommunityToken: communityToken,
		CommunityRepo:  communityRepo,
		SARIFGateMode:  manifest.SARIFGateReport,
		SARIFReporter:  sarifgate.NewReporter(logger),
	}
}

// Register wires every route under /api/v1/. Handlers that are not yet
// fully implemented return 501 via server.NotImplemented so the OpenAPI
// surface is stable from day one.
func (h *Handlers) Register(e *echo.Echo) {
	// Health & readiness — outside the /api/v1/ namespace so liveness
	// probes don't need authentication or versioning.
	e.GET("/healthz", h.GetHealthz)
	e.GET("/readyz", h.GetReadyz)

	v1 := e.Group("/api/v1")

	// Current user profile.
	v1.GET("/me", h.GetMe, auth.RequireAuthenticated)

	// Federated org discovery — public, used by the login page.
	v1.GET("/orgs", h.ListFederatedOrgs)

	// Community discussions proxy — public, cached 5 min server-side.
	v1.GET("/community/discussions", h.ListCommunityDiscussions)

	// Advisories — platform security and operational notices.
	v1.GET("/advisories", h.ListAdvisories, auth.RequireAuthenticated)
	v1.POST("/advisories", h.CreateAdvisory, auth.RequireAnyRole(models.RolePlatformAdmin))

	// Catalogue (anonymous read).
	v1.GET("/apps", h.ListApps)
	v1.GET("/apps/:slug", h.GetApp)

	// App submission + versions (Developer).
	v1.POST("/apps", h.CreateApp, auth.RequireAnyRole(models.RoleDeveloper))
	v1.GET("/apps/:slug/versions", h.ListAppVersions, auth.RequireAnyRole(models.RoleDeveloper, models.RoleAuditor))
	v1.POST("/apps/:slug/versions", h.CreateAppVersion, auth.RequireAnyRole(models.RoleDeveloper))
	v1.POST("/apps/:slug/submit", h.SubmitApp, auth.RequireAnyRole(models.RoleDeveloper))

	// Audit queue + decisions (Auditor).
	auditor := auth.RequireAnyRole(models.RoleAuditor)
	v1.GET("/audit-submissions", h.ListAuditSubmissions, auditor)
	v1.GET("/audit-submissions/:id", h.GetAuditSubmission, auditor)
	v1.GET("/audit-submissions/:id/evidence/:name", h.GetAuditEvidence, auditor)
	v1.GET("/audit-submissions/:id/comments", h.ListAuditComments, auditor)
	v1.POST("/audit-submissions/:id/comments", h.CommentOnAuditSubmission, auditor)
	v1.POST("/audit-submissions/:id/decision", h.DecideAuditSubmission, auditor)

	// Clusters (user-facing).
	v1.GET("/clusters", h.ListClusters, auth.RequireAuthenticated)
	v1.POST("/clusters", h.CreateCluster, auth.RequireAnyRole(models.RoleClusterAdmin, models.RolePlatformAdmin))
	v1.GET("/clusters/:id", h.GetCluster, auth.RequireAuthenticated)
	v1.PATCH("/clusters/:id", h.PatchCluster, auth.RequireAnyRole(models.RoleClusterAdmin, models.RolePlatformAdmin))
	v1.GET("/clusters/:id/access", h.ListClusterAccessGrants, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.POST("/clusters/:id/handshake", h.HandshakeCluster) // auth via one-time join-token JWT
	v1.POST("/clusters/:id/scope", h.ChangeClusterScope, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.POST("/clusters/:id/access", h.GrantClusterAccess, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.DELETE("/clusters/:id/access/:org_id", h.RevokeClusterAccess, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.DELETE("/clusters/:id", h.DeleteCluster, auth.RequireAnyRole(models.RoleClusterAdmin, models.RolePlatformAdmin))
	v1.POST("/clusters/:id/rotate-credential", h.RotateClusterCredential, auth.RequireAnyRole(models.RoleClusterAdmin, models.RolePlatformAdmin))

	// Cluster agent endpoints are registered separately by RegisterAgent so
	// they are served only on the agent mTLS listener.

	// Deployments.
	v1.GET("/deployments", h.ListDeployments, auth.RequireAuthenticated)
	v1.POST("/deployments", h.CreateDeployment, auth.RequireAnyRole(models.RoleDeployer))
	v1.GET("/deployments/:id", h.GetDeployment, auth.RequireAuthenticated)
	v1.POST("/deployments/:id/upgrade", h.UpgradeDeployment, auth.RequireAnyRole(models.RoleDeployer))
	v1.POST("/deployments/:id/rollback", h.RollbackDeployment, auth.RequireAnyRole(models.RoleDeployer))
	v1.DELETE("/deployments/:id", h.DeleteDeployment, auth.RequireAnyRole(models.RoleDeployer))
	v1.GET("/deployments/:id/events", h.StreamDeploymentEvents, auth.RequireAuthenticated)

	// App launcher: the caller's own organisation's live deployments, open
	// to every signed-in principal. Its own route rather than a wider
	// /deployments because the response carries no values, namespace or
	// raw state.
	v1.GET("/my-apps", h.ListMyApps, auth.RequireAuthenticated)

	// Orgs + members.
	v1.POST("/orgs", h.CreateOrganization, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.POST("/orgs/:slug/oidc", h.ConfigureOrganizationOIDC, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.GET("/orgs/:slug/members", h.ListMembers, auth.RequireAnyRole(models.RoleOrgAdmin, models.RolePlatformAdmin))
	v1.POST("/orgs/:slug/members/:user_id/roles", h.AssignMemberRoles, auth.RequireAnyRole(models.RoleOrgAdmin, models.RolePlatformAdmin))

	// API keys.
	v1.GET("/api-keys", h.ListAPIKeys, auth.RequireAuthenticated)
	v1.POST("/api-keys", h.CreateAPIKey, auth.RequireAnyRole(models.RoleDeveloper, models.RoleDeployer, models.RoleAuditor))
	v1.DELETE("/api-keys/:id", h.RevokeAPIKey, auth.RequireAuthenticated)

	// Instance-wide branding (#158): anonymous reads, platform_admin writes.
	v1.GET("/branding", h.GetBranding)
	v1.PUT("/branding", h.PutBranding, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.DELETE("/branding", h.DeleteBranding, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.GET("/branding/logo", h.GetBrandingLogo)
	v1.POST("/branding/logo", h.PostBrandingLogo, auth.RequireAnyRole(models.RolePlatformAdmin))
	v1.DELETE("/branding/logo", h.DeleteBrandingLogo, auth.RequireAnyRole(models.RolePlatformAdmin))

	// Global audit log (platform admin only).
	v1.GET("/audit-log", h.ListAuditLog, auth.RequireAnyRole(models.RolePlatformAdmin))

	// GitHub webhook ingress (no session — verified via HMAC signature).
	v1.POST("/webhooks/github", h.GitHubWebhook)
}

// RegisterAgent wires the saidkick-agent endpoints under /api/v1/. The server
// mounts them on the dedicated agent listener, which requires a verified
// client certificate; AgentAuth then checks the certificate against the
// cluster row.
func (h *Handlers) RegisterAgent(e *echo.Echo) {
	v1 := e.Group("/api/v1")
	v1.POST("/clusters/:id/heartbeat", h.AgentHeartbeat, h.AgentAuth)
	v1.POST("/clusters/:id/deployment-states", h.AgentReportDeploymentStates, h.AgentAuth)
	v1.POST("/clusters/:id/commands/:cmd_id/ack", h.AgentAckCommand, h.AgentAuth)
	v1.POST("/clusters/:id/agent/renew", h.AgentRenewCertificate, h.AgentAuth)
}
