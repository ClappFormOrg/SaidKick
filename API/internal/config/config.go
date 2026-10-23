// Package config gathers all environment-driven configuration for the
// API process. Subsystems read from the same Config so there is one source
// of truth.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
)

// Config bundles every runtime knob. Subsystems read the relevant sub-struct.
type Config struct {
	DB           db.Config
	HTTP         HTTPConfig
	Auth         AuthConfig
	GitHub       GitHubConfig
	Orchestrator OrchestratorConfig
	Secrets      SecretsConfig
	Cluster      ClusterConfig
	Community    CommunityConfig
	IsDevMode    bool

	// SARIFGateMode selects which rule the Stage-1 SARIF security gate
	// enforces: "report" (default) blocks on the legacy rule and reports
	// what the stricter rule would add; "enforce" blocks on the stricter
	// rule. Set via SARIF_GATE_MODE; any other value fails boot.
	SARIFGateMode manifest.SARIFGateMode
}

// CommunityConfig holds the settings for the public community discussions
// proxy. Both fields are optional; when the token is absent the endpoint
// returns an empty list so the page degrades gracefully.
type CommunityConfig struct {
	// DiscussionsToken is a GitHub PAT with public_repo scope.
	// Set via GITHUB_DISCUSSIONS_TOKEN.
	DiscussionsToken string

	// Repo is the "owner/repo" of the public platform repository whose
	// discussions are surfaced. Set via GITHUB_COMMUNITY_REPO.
	// Default: "ClappformOrg/saidkick-appstore".
	Repo string
}

// DefaultAgentChartRepo is the OCI repository the charts are published to
// when the repository owner is ClappFormOrg
// (oci://ghcr.io/<lower-cased owner>/charts). Override it with
// AGENT_CHART_REPO, for example after the repository moves to another owner.
const DefaultAgentChartRepo = "oci://ghcr.io/clappformorg/charts"

// ClusterConfig holds the settings for cluster registration and agent
// bootstrapping. JoinSecret signs one-time join-token JWTs; the Vault PKI
// settings control agent mTLS cert issuance during handshake.
type ClusterConfig struct {
	// JoinSecret is the HMAC-SHA256 key used to sign and verify one-time
	// cluster join-tokens. Must be at least 32 bytes. Set via
	// CLUSTER_JOIN_SECRET. Required unless APP_DEV_MODE=true (a fixed insecure
	// dev key is used when the variable is absent in dev mode).
	JoinSecret []byte

	// HubEndpoint is the public-facing base URL of this hub, without a
	// trailing slash (e.g. "https://appstore.gov.nl"). Returned to the agent
	// in the handshake response so it knows where to connect. Set via
	// CLUSTER_HUB_ENDPOINT; falls back to AUTH_BASE_URL when absent.
	HubEndpoint string

	// HubAgentEndpoint is the base URL agents use for mTLS traffic to the
	// agent listener, without a trailing slash (e.g.
	// "https://agents.appstore.gov.nl:8443"). Printed as agent.hubAgentUrl in
	// the agent install command when set. Set via CLUSTER_HUB_AGENT_ENDPOINT.
	HubAgentEndpoint string

	// AgentChartRepo is the OCI repository the agent chart is published to,
	// without a trailing slash; the install command pulls
	// <AgentChartRepo>/saidkick-agent. Set via AGENT_CHART_REPO; defaults to
	// DefaultAgentChartRepo.
	AgentChartRepo string

	// AgentChartVersion is the agent chart version printed as --version in
	// the install command. Empty leaves --version out, so Helm picks the
	// latest. Set via AGENT_CHART_VERSION; the hub chart sets it to its
	// appVersion.
	AgentChartVersion string

	// VaultPKIMount is the Vault PKI secrets engine mount path. Default "pki".
	// Set via VAULT_PKI_MOUNT.
	VaultPKIMount string

	// VaultPKIRole is the Vault PKI role used to sign agent CSRs. The role
	// must permit CN "saidkick-agent-<cluster-id>". Default "agent".
	// Set via VAULT_PKI_ROLE.
	VaultPKIRole string

	// AgentCertTTL is the lifetime of the agent client certificates the
	// handshake and the renew route issue. Default 2160h (90 days). The
	// AGENT_CERT_TTL override exists only so end-to-end runs can force a
	// renewal; it is refused unless APP_ENV is dev/development/local/test/ci.
	AgentCertTTL time.Duration
}

// DefaultAgentCertTTL is the agent client certificate lifetime: 90 days.
const DefaultAgentCertTTL = 2160 * time.Hour

// PKIConfigured reports whether the Vault PKI settings are present. It does
// NOT verify Vault connectivity — SecretsConfig.Configured() must also be
// true before wiring a real PKI client.
func (c ClusterConfig) PKIConfigured() bool {
	return c.VaultPKIMount != "" && c.VaultPKIRole != ""
}

// OrchestratorConfig holds the connection details for the deployment
// orchestrator. Leave unset to wire the orchestrator.NullDeployer
// fallback; deployment endpoints then return 503
// orchestrator_not_configured.
type OrchestratorConfig struct {
	// Kind selects the implementation: "agent" (agent-brokered, the only
	// kind a prod-like APP_ENV accepts), "argocd" (direct Argo CD client,
	// dev-like envs only) or "" (not configured).
	Kind string

	// ServerURL is the base URL of the Argo CD API (no trailing slash).
	ServerURL string

	// AuthToken is the bearer token Argo CD verifies on every request.
	AuthToken string

	// NamespaceForApps is the Argo CD installation's namespace where
	// Application CRs live. Defaults to "argocd".
	NamespaceForApps string

	// InsecureSkipVerify allows self-signed Argo CD certs in dev. Will
	// be refused if APP_ENV=production.
	InsecureSkipVerify bool
}

// Configured reports whether the orchestrator is configured and ready.
// For agent-brokered kind, no remote credentials are required.
func (o OrchestratorConfig) Configured() bool {
	if o.Kind == "agent" {
		return true
	}
	return o.Kind != "" && o.ServerURL != "" && o.AuthToken != ""
}

// IsAgentBrokered reports whether the orchestrator kind is "agent".
func (o OrchestratorConfig) IsAgentBrokered() bool {
	return o.Kind == "agent"
}

// GitHubConfig holds the credentials for the platform's GitHub App. All
// three fields must be present for the integration to be wired; if any
// is missing the API boots with internal/integrations/github.NullClient
// and the handlers that need GitHub return 503.
type GitHubConfig struct {
	// AppID is the integer ID printed on the App's settings page.
	AppID int64

	// PrivateKeyPEM is the PEM-encoded RSA private key downloaded when
	// the GitHub App was created. Set via GITHUB_APP_PRIVATE_KEY (inline)
	// or GITHUB_APP_PRIVATE_KEY_FILE (path).
	PrivateKeyPEM []byte

	// WebhookSecret is the shared secret configured on the GitHub App's
	// webhook delivery settings.
	WebhookSecret []byte
}

// Configured reports whether the GitHub App credentials are complete.
func (g GitHubConfig) Configured() bool {
	return g.AppID != 0 && len(g.PrivateKeyPEM) > 0 && len(g.WebhookSecret) > 0
}

// SecretsConfig holds the connection details for the platform's secret store
// (HashiCorp Vault KV v2). Leave unset to fall back to InMemoryStore in dev
// mode or NullStore in production.
type SecretsConfig struct {
	// VaultAddr is the base URL of the Vault server (no trailing slash).
	// Set via VAULT_ADDR.
	VaultAddr string

	// VaultToken is the static token used to authenticate with Vault.
	// Set via VAULT_TOKEN.
	VaultToken string

	// VaultMount is the KV v2 mount path. Defaults to "kv".
	// Set via VAULT_MOUNT.
	VaultMount string

	// VaultAppRoleMount is the Vault AppRole auth mount name (e.g. "approle").
	// When set, the API uses per-cluster AppRole credentials instead of the
	// static VAULT_TOKEN for deployment secrets. Set via VAULT_APPROLE_MOUNT.
	VaultAppRoleMount string

	// VaultCACertFile is the path to a PEM-encoded CA certificate used to
	// verify Vault's server TLS certificate against a private CA. Optional
	// when the system root pool is sufficient. Set via VAULT_CA_CERT_FILE.
	VaultCACertFile string

	// VaultClientCertFile and VaultClientKeyFile are the paths to the
	// PEM-encoded client certificate and private key for mTLS authentication
	// with Vault. Both must be set together. Set via VAULT_CLIENT_CERT_FILE
	// and VAULT_CLIENT_KEY_FILE.
	VaultClientCertFile string
	VaultClientKeyFile  string
}

// Configured reports whether the Vault credentials are complete enough to
// wire a real VaultStore.
func (s SecretsConfig) Configured() bool {
	return s.VaultAddr != "" && s.VaultToken != ""
}

// AppRoleMounted reports whether per-cluster AppRole auth is enabled.
// Requires Vault to be configured (VaultAddr set) and VAULT_APPROLE_MOUNT set.
func (s SecretsConfig) AppRoleMounted() bool {
	return s.VaultAddr != "" && s.VaultAppRoleMount != ""
}

// loadSecretsConfig reads VAULT_ADDR, VAULT_TOKEN, VAULT_MOUNT, and
// VAULT_APPROLE_MOUNT. A partial or absent set is fine — callers check
// Configured() / AppRoleMounted() at boot.
func loadSecretsConfig() SecretsConfig {
	return SecretsConfig{
		VaultAddr:           os.Getenv("VAULT_ADDR"),
		VaultToken:          os.Getenv("VAULT_TOKEN"),
		VaultMount:          defaultStr("VAULT_MOUNT", "kv"),
		VaultAppRoleMount:   os.Getenv("VAULT_APPROLE_MOUNT"),
		VaultCACertFile:     os.Getenv("VAULT_CA_CERT_FILE"),
		VaultClientCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"),
		VaultClientKeyFile:  os.Getenv("VAULT_CLIENT_KEY_FILE"),
	}
}

// HTTPConfig governs the Echo HTTP server.
type HTTPConfig struct {
	Addr            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	TrustedProxies  []string

	// CORSAllowedOrigins is the list of origins the CORS middleware accepts.
	// Required when APP_ENV=production; defaults to ["*"] in dev mode.
	// Set via AUTH_CORS_ALLOWED_ORIGINS (comma-separated).
	CORSAllowedOrigins []string

	// Agent configures the dedicated mTLS listener that serves the
	// saidkick-agent endpoints.
	Agent AgentListenerConfig

	// MetricsAddr is the listen address of the plain-HTTP listener that
	// serves GET /metrics and nothing else. It is a separate port so the
	// scrape endpoint stays reachable to in-cluster scrapers while the
	// ingress, which only routes the main listener, never exposes it.
	// Set via METRICS_ADDR. Default ":9090".
	MetricsAddr string
}

// AgentListenerConfig configures the dedicated agent listener. The listener
// terminates TLS itself and requires a client certificate signed by the CA
// in ClientCAFile, which must be the CA of the Vault PKI mount that signs
// agent CSRs during the cluster handshake.
type AgentListenerConfig struct {
	// Addr is the listen address. Set via AGENT_TLS_ADDR. Default ":8443".
	Addr string

	// CertFile and KeyFile are the PEM serving certificate and key presented
	// to agents. Set via AGENT_TLS_CERT_FILE and AGENT_TLS_KEY_FILE.
	CertFile string
	KeyFile  string

	// ClientCAFile is the PEM bundle used to verify agent client
	// certificates. Set via AGENT_CLIENT_CA_FILE.
	ClientCAFile string
}

// Configured reports whether the serving certificate, key and client CA
// are all set, so the agent listener can be started.
func (a AgentListenerConfig) Configured() bool {
	return a.CertFile != "" && a.KeyFile != "" && a.ClientCAFile != ""
}

// loadAgentListenerConfig reads the AGENT_TLS_* and AGENT_CLIENT_CA_FILE
// variables. A partial set is always an error. In a prod-like environment
// agent authentication is always on (dev mode is refused there), so an
// absent set is an error too: booting without the agent listener would
// leave every agent unable to authenticate.
func loadAgentListenerConfig() (AgentListenerConfig, error) {
	a := AgentListenerConfig{
		Addr:         defaultStr("AGENT_TLS_ADDR", ":8443"),
		CertFile:     os.Getenv("AGENT_TLS_CERT_FILE"),
		KeyFile:      os.Getenv("AGENT_TLS_KEY_FILE"),
		ClientCAFile: os.Getenv("AGENT_CLIENT_CA_FILE"),
	}
	if a.Configured() {
		return a, nil
	}
	if a.CertFile != "" || a.KeyFile != "" || a.ClientCAFile != "" {
		return AgentListenerConfig{}, errors.New(
			"AGENT_TLS_CERT_FILE, AGENT_TLS_KEY_FILE and AGENT_CLIENT_CA_FILE must be set together")
	}
	if IsProdLikeEnv() {
		return AgentListenerConfig{}, errors.New(
			"AGENT_TLS_CERT_FILE, AGENT_TLS_KEY_FILE and AGENT_CLIENT_CA_FILE are required unless APP_ENV is dev/development/local/test/ci")
	}
	return a, nil
}

// AuthConfig governs authentication wiring. V0 ships with a dev-mode
// header-based user (gated by IsDevMode); Sprint 3 adds OIDC federation.
type AuthConfig struct {
	// DevUserHeader is the HTTP header inspected in dev mode to identify
	// the caller. Default "X-Dev-User-ID". Only honoured when IsDevMode is
	// true at startup.
	DevUserHeader string

	// OIDC holds the session and base-URL configuration for the production
	// OIDC RP middleware. Leave unset to boot without OIDC (dev-mode only).
	OIDC OIDCConfig
}

// OIDCConfig holds the server-level settings for the OIDC RP middleware.
// Per-org issuer / client credentials live on the Organisation DB row.
type OIDCConfig struct {
	// SessionSecret is the HMAC-SHA256 key used to sign session and state
	// cookies. Must be at least 32 bytes of random data. Set via
	// AUTH_SESSION_SECRET.
	SessionSecret string

	// CookieDomain sets the Domain attribute of the session and state
	// cookies. Empty (the default, and the production setting) gives a
	// host-only cookie that the browser sends only to the host that set it.
	// Set a domain only if the API and webapp are served from different
	// hosts. Set via AUTH_COOKIE_DOMAIN.
	CookieDomain string

	// BaseURL is the public-facing base URL of this API, without a trailing
	// slash (e.g. "https://appstore.gov.nl"). Used to construct the OIDC
	// callback redirect URI. Set via AUTH_BASE_URL.
	BaseURL string

	// SessionTTL is the lifetime of the session JWT cookie. Default: 8h.
	// Set via AUTH_SESSION_TTL (accepts Go duration strings).
	SessionTTL time.Duration

	// SecretEncryptionKey is a base64-encoded 32-byte AES-256 key used to
	// encrypt OIDC client secrets stored in the database. Only used when
	// Vault is not configured. Generate with: openssl rand -base64 32
	// Set via AUTH_SECRET_ENCRYPTION_KEY.
	SecretEncryptionKey string

	// ProviderCacheTTL is how long a resolved OIDC provider+oauth2 config
	// pair is cached before re-fetching the discovery document. Default: 10m.
	// Set via AUTH_OIDC_PROVIDER_CACHE_TTL (accepts Go duration strings).
	ProviderCacheTTL time.Duration

	// AllowedRedirectOrigins is the set of absolute origins (scheme+host+port,
	// no trailing slash) that the post-login redirect param may target. Derived
	// from AUTH_CORS_ALLOWED_ORIGINS so no extra env var is needed.
	// Relative paths (starting with "/") are always allowed regardless.
	AllowedRedirectOrigins []string
}

// Configured reports whether the minimum OIDC settings are present.
// Both SessionSecret and BaseURL must be non-empty to wire the authenticator.
func (o OIDCConfig) Configured() bool {
	return o.SessionSecret != "" && o.BaseURL != ""
}

// Load reads the environment, applies defaults, and returns a Config. Any
// missing required value is reported as an error.
func Load() (Config, error) {
	dbCfg, err := db.Load()
	if err != nil {
		return Config{}, fmt.Errorf("db config: %w", err)
	}

	gh, err := loadGitHubConfig()
	if err != nil {
		return Config{}, fmt.Errorf("github config: %w", err)
	}

	orch, err := loadOrchestratorConfig()
	if err != nil {
		return Config{}, fmt.Errorf("orchestrator config: %w", err)
	}

	sarifMode, err := manifest.ParseSARIFGateMode(defaultStr("SARIF_GATE_MODE", string(manifest.SARIFGateReport)))
	if err != nil {
		return Config{}, fmt.Errorf("SARIF_GATE_MODE: %w", err)
	}

	corsOrigins := splitCSV(os.Getenv("AUTH_CORS_ALLOWED_ORIGINS"))

	cfg := Config{
		DB:        dbCfg,
		IsDevMode: parseBool("APP_DEV_MODE", false),
		HTTP: HTTPConfig{
			Addr:               defaultStr("HTTP_ADDR", ":8080"),
			ReadTimeout:        parseDur("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:       parseDur("HTTP_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:        parseDur("HTTP_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout:    parseDur("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			TrustedProxies:     splitCSV(os.Getenv("HTTP_TRUSTED_PROXIES")),
			CORSAllowedOrigins: corsOrigins,
			MetricsAddr:        defaultStr("METRICS_ADDR", ":9090"),
		},
		Auth: AuthConfig{
			DevUserHeader: defaultStr("AUTH_DEV_USER_HEADER", "X-Dev-User-ID"),
			OIDC: OIDCConfig{
				SessionSecret:          os.Getenv("AUTH_SESSION_SECRET"),
				CookieDomain:           os.Getenv("AUTH_COOKIE_DOMAIN"),
				BaseURL:                strings.TrimRight(os.Getenv("AUTH_BASE_URL"), "/"),
				SessionTTL:             parseDur("AUTH_SESSION_TTL", 8*time.Hour),
				SecretEncryptionKey:    os.Getenv("AUTH_SECRET_ENCRYPTION_KEY"),
				ProviderCacheTTL:       parseDur("AUTH_OIDC_PROVIDER_CACHE_TTL", 10*time.Minute),
				AllowedRedirectOrigins: corsOrigins,
			},
		},
		GitHub:        gh,
		Orchestrator:  orch,
		Secrets:       loadSecretsConfig(),
		SARIFGateMode: sarifMode,
		Community: CommunityConfig{
			DiscussionsToken: os.Getenv("GITHUB_DISCUSSIONS_TOKEN"),
			Repo:             defaultStr("GITHUB_COMMUNITY_REPO", "ClappFormOrg/SaidKick"),
		},
	}

	if cfg.IsDevMode && IsProdLikeEnv() {
		return Config{}, errors.New("APP_DEV_MODE is only permitted when APP_ENV is dev/development/local/test/ci")
	}

	if os.Getenv("APP_ENV") == "production" && len(corsOrigins) == 0 {
		return Config{}, errors.New("AUTH_CORS_ALLOWED_ORIGINS is required in production; set to a comma-separated list of allowed origins")
	}

	if s := cfg.Auth.OIDC.SessionSecret; s != "" && len(s) < 32 {
		return Config{}, errors.New("AUTH_SESSION_SECRET must be at least 32 bytes; generate with: openssl rand -base64 32")
	}

	cluster, err := loadClusterConfig(cfg.IsDevMode, cfg.Auth.OIDC.BaseURL)
	if err != nil {
		return Config{}, fmt.Errorf("cluster config: %w", err)
	}
	cfg.Cluster = cluster

	agent, err := loadAgentListenerConfig()
	if err != nil {
		return Config{}, fmt.Errorf("agent listener config: %w", err)
	}
	cfg.HTTP.Agent = agent

	return cfg, nil
}

// clusterJoinDevKey is the fallback HMAC key used when APP_DEV_MODE=true and
// CLUSTER_JOIN_SECRET is not set. Must never be used in production.
const clusterJoinDevKey = "saidkick-dev-cluster-join-secret-do-not-use-in-production"

func loadClusterConfig(devMode bool, authBaseURL string) (ClusterConfig, error) {
	secret := os.Getenv("CLUSTER_JOIN_SECRET")
	switch {
	case secret == "" && !devMode:
		return ClusterConfig{}, errors.New(
			"CLUSTER_JOIN_SECRET is required; generate with: openssl rand -base64 32")
	case secret == "":
		secret = clusterJoinDevKey
	case len(secret) < 32:
		return ClusterConfig{}, errors.New("CLUSTER_JOIN_SECRET must be at least 32 bytes")
	}

	hubEndpoint := strings.TrimRight(os.Getenv("CLUSTER_HUB_ENDPOINT"), "/")
	if hubEndpoint == "" {
		hubEndpoint = authBaseURL
	}

	ttl, err := loadAgentCertTTL()
	if err != nil {
		return ClusterConfig{}, err
	}

	return ClusterConfig{
		JoinSecret:       []byte(secret),
		HubEndpoint:      hubEndpoint,
		HubAgentEndpoint:  strings.TrimRight(os.Getenv("CLUSTER_HUB_AGENT_ENDPOINT"), "/"),
		AgentChartRepo:    strings.TrimRight(defaultStr("AGENT_CHART_REPO", DefaultAgentChartRepo), "/"),
		AgentChartVersion: strings.TrimSpace(os.Getenv("AGENT_CHART_VERSION")),
		VaultPKIMount:     defaultStr("VAULT_PKI_MOUNT", "pki"),
		VaultPKIRole:      defaultStr("VAULT_PKI_ROLE", "agent"),
		AgentCertTTL:      ttl,
	}, nil
}

// loadAgentCertTTL reads the test-only AGENT_CERT_TTL override. Unset gives
// DefaultAgentCertTTL. A set value must be a positive duration and is
// refused in a prod-like APP_ENV, so production always issues 90-day
// certificates.
func loadAgentCertTTL() (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv("AGENT_CERT_TTL"))
	if v == "" {
		return DefaultAgentCertTTL, nil
	}
	if IsProdLikeEnv() {
		return 0, errors.New("AGENT_CERT_TTL is a test-only override and is refused unless APP_ENV is dev/development/local/test/ci")
	}
	ttl, err := time.ParseDuration(v)
	if err != nil || ttl <= 0 {
		return 0, fmt.Errorf("AGENT_CERT_TTL must be a positive duration such as 48h, got %q", v)
	}
	return ttl, nil
}

// loadGitHubConfig reads GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY (or
// GITHUB_APP_PRIVATE_KEY_FILE), and GITHUB_WEBHOOK_SECRET. If none of
// these are set, returns a zero-value GitHubConfig (which Configured()
// reports false for) so the API can still boot.
//
// If a partial set is supplied, returns an error so misconfigurations
// surface at startup.
func loadGitHubConfig() (GitHubConfig, error) {
	idStr := os.Getenv("GITHUB_APP_ID")
	pemInline := os.Getenv("GITHUB_APP_PRIVATE_KEY")
	pemFile := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE")
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")

	if idStr == "" && pemInline == "" && pemFile == "" && secret == "" {
		return GitHubConfig{}, nil // not configured — that is OK in dev
	}

	cfg := GitHubConfig{WebhookSecret: []byte(secret)}

	if idStr == "" {
		return cfg, errors.New("GITHUB_APP_ID is required when any other GITHUB_* var is set")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return cfg, fmt.Errorf("GITHUB_APP_ID %q is not a number: %w", idStr, err)
	}
	cfg.AppID = id

	switch {
	case pemInline != "":
		cfg.PrivateKeyPEM = []byte(pemInline)
	case pemFile != "":
		data, err := os.ReadFile(pemFile)
		if err != nil {
			return cfg, fmt.Errorf("read GITHUB_APP_PRIVATE_KEY_FILE %q: %w", pemFile, err)
		}
		cfg.PrivateKeyPEM = data
	default:
		return cfg, errors.New("GITHUB_APP_PRIVATE_KEY or GITHUB_APP_PRIVATE_KEY_FILE is required")
	}

	if len(cfg.WebhookSecret) == 0 {
		return cfg, errors.New("GITHUB_WEBHOOK_SECRET is required when GITHUB_APP_ID is set")
	}
	return cfg, nil
}

// loadOrchestratorConfig reads ORCHESTRATOR_KIND + ARGOCD_* envs.
// Returns a zero value (Configured() == false) when nothing is set so
// the API can still boot with the NullDeployer wired.
//
// ADR-4 makes the agent-brokered path the only production orchestrator:
// the hub holds no spoke credentials, and each spoke's agent drives its own
// Argo CD. In a prod-like APP_ENV, ORCHESTRATOR_KIND=argocd and the
// fallback to argocd for an unset kind with ARGOCD_* set both refuse to
// boot. Dev-like envs keep the direct Argo CD client.
func loadOrchestratorConfig() (OrchestratorConfig, error) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("ORCHESTRATOR_KIND")))
	server := os.Getenv("ARGOCD_SERVER_URL")
	token := os.Getenv("ARGOCD_AUTH_TOKEN")
	ns := defaultStr("ARGOCD_NAMESPACE", "argocd")
	insecure := parseBool("ARGOCD_INSECURE_SKIP_VERIFY", false)

	if kind == "" && server == "" && token == "" {
		return OrchestratorConfig{}, nil // not configured — that is OK in dev
	}
	if kind == "" {
		if IsProdLikeEnv() {
			return OrchestratorConfig{}, errors.New("ORCHESTRATOR_KIND is unset but ARGOCD_SERVER_URL or ARGOCD_AUTH_TOKEN is set; " +
				"the fallback to the direct Argo CD client is refused when APP_ENV is not dev/development/local/test/ci " +
				"(ADR-4: agent-pull, no spoke credentials on the hub). Set ORCHESTRATOR_KIND=agent and remove ARGOCD_*")
		}
		kind = "argocd"
	}
	switch kind {
	case "agent":
		// Agent-brokered: no Argo CD credentials required on the hub.
		return OrchestratorConfig{Kind: "agent"}, nil
	case "argocd":
		// Direct Argo CD REST API, for local development only.
		if IsProdLikeEnv() {
			return OrchestratorConfig{}, errors.New("ORCHESTRATOR_KIND=argocd is refused when APP_ENV is not dev/development/local/test/ci " +
				"(ADR-4: agent-pull, no spoke credentials on the hub). Set ORCHESTRATOR_KIND=agent")
		}
	default:
		return OrchestratorConfig{}, fmt.Errorf("ORCHESTRATOR_KIND %q is not supported (valid: 'argocd', 'agent')", kind)
	}
	if server == "" {
		return OrchestratorConfig{}, errors.New("ARGOCD_SERVER_URL is required when ORCHESTRATOR_KIND=argocd")
	}
	if token == "" {
		return OrchestratorConfig{}, errors.New("ARGOCD_AUTH_TOKEN is required when ORCHESTRATOR_KIND=argocd")
	}
	return OrchestratorConfig{
		Kind:               kind,
		ServerURL:          server,
		AuthToken:          token,
		NamespaceForApps:   ns,
		InsecureSkipVerify: insecure,
	}, nil
}

// IsProdLikeEnv reports whether APP_ENV is set to a value that should be
// treated as production-equivalent (staging, preprod, prod, production, etc.).
// Only explicitly development-safe values are considered non-production so that
// a missing or unexpected APP_ENV is treated conservatively rather than
// permissively. This guards APP_DEV_MODE, the direct Argo CD orchestrator and
// cmd/seed.
func IsProdLikeEnv() bool {
	switch strings.ToLower(os.Getenv("APP_ENV")) {
	case "dev", "development", "local", "test", "ci":
		return false
	default:
		return true
	}
}

func defaultStr(key, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}

func parseDur(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func parseBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
