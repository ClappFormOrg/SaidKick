package config

import (
	"testing"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baselineEnv clears every env var that the config loader reads and then
// sets DATABASE_URL to a dummy value so db.Load() (called first inside Load)
// succeeds without a real database. Individual tests override only the vars
// they exercise. Using t.Setenv guarantees the values are restored after the
// test and never leak between tests, so these tests must NOT run in parallel.
func baselineEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"APP_ENV",
		"APP_DEV_MODE",
		"AUTH_SESSION_SECRET",
		"AUTH_BASE_URL",
		"AUTH_CORS_ALLOWED_ORIGINS",
		"AUTH_COOKIE_DOMAIN",
		"CLUSTER_JOIN_SECRET",
		"CLUSTER_HUB_ENDPOINT",
		"CLUSTER_HUB_AGENT_ENDPOINT",
		"AGENT_CHART_REPO",
		"AGENT_CHART_VERSION",
		"AGENT_CERT_TTL",
		"AGENT_TLS_ADDR",
		"AGENT_TLS_CERT_FILE",
		"AGENT_TLS_KEY_FILE",
		"AGENT_CLIENT_CA_FILE",
		"METRICS_ADDR",
		"GITHUB_APP_ID",
		"GITHUB_APP_PRIVATE_KEY",
		"GITHUB_APP_PRIVATE_KEY_FILE",
		"GITHUB_WEBHOOK_SECRET",
		"GITHUB_DISCUSSIONS_TOKEN",
		"ORCHESTRATOR_KIND",
		"ARGOCD_SERVER_URL",
		"ARGOCD_AUTH_TOKEN",
		"ARGOCD_INSECURE_SKIP_VERIFY",
		"VAULT_ADDR",
		"VAULT_TOKEN",
		"VAULT_APPROLE_MOUNT",
		"AUDIT_DATABASE_URL",
		"SARIF_GATE_MODE",
	} {
		t.Setenv(k, "")
	}
	// db.Load() requires a non-empty DSN; it does not dial the database.
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/appstore?sslmode=disable")
}

// ─── IsProdLikeEnv: must fail closed ─────────────────────────────────────────

func TestIsProdLikeEnv(t *testing.T) {
	cases := []struct {
		appEnv string
		want   bool
	}{
		// Explicit dev allow-list -> non-prod (false).
		{"dev", false},
		{"development", false},
		{"local", false},
		{"test", false},
		{"ci", false},
		// Case-insensitive allow-list.
		{"DEV", false},
		{"Development", false},
		{"LOCAL", false},
		{"Test", false},
		{"CI", false},
		// Everything else must fail closed -> prod-like (true).
		{"", true},
		{"production", true},
		{"prod", true},
		{"staging", true},
		{"preprod", true},
		{"prd", true},
		{"acceptance", true},
		{"dev ", true},    // trailing space is not in the allow-list
		{" dev", true},    // leading space is not in the allow-list
		{"develop", true}, // near-miss must not match
		{"testing", true}, // near-miss must not match
		{"garbage", true},
		{"true", true},
	}
	for _, tc := range cases {
		t.Run(tc.appEnv, func(t *testing.T) {
			t.Setenv("APP_ENV", tc.appEnv)
			assert.Equal(t, tc.want, IsProdLikeEnv())
		})
	}
}

// ─── Load: APP_DEV_MODE gate against prod-like APP_ENV ───────────────────────

func TestLoad_DevModeGate(t *testing.T) {
	const devGateErr = "APP_DEV_MODE is only permitted when APP_ENV is dev/development/local/test/ci"

	cases := []struct {
		name    string
		appEnv  string
		wantErr bool
	}{
		// Dev mode allowed under each dev-valued APP_ENV.
		{"dev", "dev", false},
		{"development", "development", false},
		{"local", "local", false},
		{"test", "test", false},
		{"ci", "ci", false},
		{"mixed-case dev", "Dev", false},
		// Dev mode refused under prod-like / unknown / empty APP_ENV.
		{"empty", "", true},
		{"production", "production", true},
		{"staging", "staging", true},
		{"prod", "prod", true},
		{"unknown", "banana", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("APP_DEV_MODE", "true")

			cfg, err := Load()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), devGateErr)
				return
			}
			require.NoError(t, err)
			assert.True(t, cfg.IsDevMode)
		})
	}
}

// APP_DEV_MODE=false must boot fine even under a prod-like APP_ENV, provided
// the other production requirements (CORS, join secret) are satisfied.
func TestLoad_DevModeFalseUnderProdEnv(t *testing.T) {
	baselineEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_DEV_MODE", "false")
	t.Setenv("AUTH_CORS_ALLOWED_ORIGINS", "https://appstore.gov.nl")
	t.Setenv("CLUSTER_JOIN_SECRET", "this-is-a-sufficiently-long-join-secret-value")
	setAgentListenerEnv(t)

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.IsDevMode)
	assert.True(t, cfg.HTTP.Agent.Configured())
}

// setAgentListenerEnv sets the three agent listener paths. Config only checks
// they are present; the server reads the files when it starts.
func setAgentListenerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_TLS_CERT_FILE", "/etc/saidkick/agent-tls/tls.crt")
	t.Setenv("AGENT_TLS_KEY_FILE", "/etc/saidkick/agent-tls/tls.key")
	t.Setenv("AGENT_CLIENT_CA_FILE", "/etc/saidkick/agent-ca/ca.crt")
}

// ─── Load: agent mTLS listener ───────────────────────────────────────────────

func TestLoad_AgentListenerRequiredInProdLikeEnv(t *testing.T) {
	for _, appEnv := range []string{"production", "staging", ""} {
		t.Run("env="+appEnv, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", appEnv)
			t.Setenv("AUTH_CORS_ALLOWED_ORIGINS", "https://appstore.gov.nl")
			t.Setenv("CLUSTER_JOIN_SECRET", "this-is-a-sufficiently-long-join-secret-value")

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "AGENT_CLIENT_CA_FILE are required")
		})
	}
}

func TestLoadAgentListenerConfig(t *testing.T) {
	t.Run("optional in dev-like env", func(t *testing.T) {
		for _, appEnv := range []string{"dev", "ci", "test"} {
			baselineEnv(t)
			t.Setenv("APP_ENV", appEnv)
			cfg, err := loadAgentListenerConfig()
			require.NoError(t, err, appEnv)
			assert.False(t, cfg.Configured())
			assert.Equal(t, ":8443", cfg.Addr)
		}
	})

	t.Run("full set is loaded", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("AGENT_TLS_ADDR", ":9443")
		setAgentListenerEnv(t)
		cfg, err := loadAgentListenerConfig()
		require.NoError(t, err)
		assert.Equal(t, AgentListenerConfig{
			Addr:         ":9443",
			CertFile:     "/etc/saidkick/agent-tls/tls.crt",
			KeyFile:      "/etc/saidkick/agent-tls/tls.key",
			ClientCAFile: "/etc/saidkick/agent-ca/ca.crt",
		}, cfg)
	})

	partial := map[string]string{
		"cert only":   "AGENT_TLS_CERT_FILE",
		"key only":    "AGENT_TLS_KEY_FILE",
		"client only": "AGENT_CLIENT_CA_FILE",
	}
	for name, key := range partial {
		t.Run("partial set rejected in dev env: "+name, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", "dev")
			t.Setenv(key, "/some/file.pem")
			_, err := loadAgentListenerConfig()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be set together")
		})
	}
}

// ─── Load: session-secret minimum length ─────────────────────────────────────

func TestLoad_SessionSecretMinLength(t *testing.T) {
	const secretErr = "AUTH_SESSION_SECRET must be at least 32 bytes"

	cases := []struct {
		name    string
		secret  string
		wantErr bool
	}{
		{"empty is allowed (OIDC unconfigured)", "", false},
		{"too short (1 byte)", "x", true},
		{"just under 32 bytes", "0123456789012345678901234567890", true}, // 31 chars
		{"exactly 32 bytes", "01234567890123456789012345678901", false},  // 32 chars
		{"comfortably long", "this-session-secret-is-clearly-more-than-32-bytes", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baselineEnv(t)
			// Dev APP_ENV so the CORS-in-production rule does not interfere,
			// and dev mode so the cluster join secret is not required.
			t.Setenv("APP_ENV", "dev")
			t.Setenv("APP_DEV_MODE", "true")
			t.Setenv("AUTH_SESSION_SECRET", tc.secret)

			cfg, err := Load()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), secretErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.secret, cfg.Auth.OIDC.SessionSecret)
		})
	}
}

// ─── Load: CORS required in production ────────────────────────────────────────

func TestLoad_CORSRequiredInProduction(t *testing.T) {
	baselineEnv(t)
	t.Setenv("APP_ENV", "production")
	// A valid join secret so the failure is unambiguously the CORS rule.
	t.Setenv("CLUSTER_JOIN_SECRET", "this-is-a-sufficiently-long-join-secret-value")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUTH_CORS_ALLOWED_ORIGINS is required in production")
}

// ─── Load: SARIF gate mode ───────────────────────────────────────────────────

func TestLoad_SARIFGateMode(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    manifest.SARIFGateMode
		wantErr bool
	}{
		{"unset defaults to report", "", manifest.SARIFGateReport, false},
		{"report", "report", manifest.SARIFGateReport, false},
		{"enforce", "enforce", manifest.SARIFGateEnforce, false},
		{"case and whitespace tolerated", " Enforce ", manifest.SARIFGateEnforce, false},
		{"unknown value fails boot", "block", "", true},
		{"near-miss fails boot", "enforced", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", "dev")
			t.Setenv("APP_DEV_MODE", "true")
			t.Setenv("SARIF_GATE_MODE", tc.value)

			cfg, err := Load()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "SARIF_GATE_MODE")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.SARIFGateMode)
		})
	}
}

// ─── Load: metrics listener address ──────────────────────────────────────────

func TestLoad_MetricsAddr(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"unset defaults to :9090", "", ":9090"},
		{"override", "127.0.0.1:9100", "127.0.0.1:9100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", "dev")
			t.Setenv("APP_DEV_MODE", "true")
			t.Setenv("METRICS_ADDR", tc.value)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.HTTP.MetricsAddr)
		})
	}
}

// ─── loadClusterConfig: join secret rules + dev fallback ─────────────────────

func TestLoadClusterConfig(t *testing.T) {
	const validSecret = "this-is-a-sufficiently-long-join-secret-value" // >= 32 bytes

	t.Run("required when not dev mode", func(t *testing.T) {
		baselineEnv(t)
		_, err := loadClusterConfig(false, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CLUSTER_JOIN_SECRET is required")
	})

	t.Run("dev fallback key when absent in dev mode", func(t *testing.T) {
		baselineEnv(t)
		cfg, err := loadClusterConfig(true, "")
		require.NoError(t, err)
		assert.Equal(t, []byte(clusterJoinDevKey), cfg.JoinSecret)
	})

	t.Run("too short is rejected even in dev mode", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", "short")
		_, err := loadClusterConfig(true, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CLUSTER_JOIN_SECRET must be at least 32 bytes")
	})

	t.Run("too short is rejected when not dev mode", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", "short")
		_, err := loadClusterConfig(false, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CLUSTER_JOIN_SECRET must be at least 32 bytes")
	})

	t.Run("valid secret is used verbatim", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, []byte(validSecret), cfg.JoinSecret)
		assert.NotEqual(t, []byte(clusterJoinDevKey), cfg.JoinSecret)
	})

	t.Run("hub endpoint falls back to auth base URL", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, "https://appstore.gov.nl", cfg.HubEndpoint)
	})

	t.Run("explicit hub endpoint wins and trailing slash trimmed", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		t.Setenv("CLUSTER_HUB_ENDPOINT", "https://hub.gov.nl/")
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, "https://hub.gov.nl", cfg.HubEndpoint)
	})

	t.Run("hub agent endpoint read and trailing slash trimmed", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		t.Setenv("CLUSTER_HUB_AGENT_ENDPOINT", "https://agents.gov.nl:8443/")
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, "https://agents.gov.nl:8443", cfg.HubAgentEndpoint)
	})

	t.Run("agent chart repo defaults and version is empty", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, "oci://ghcr.io/clappformorg/charts", cfg.AgentChartRepo)
		assert.Empty(t, cfg.AgentChartVersion)
	})

	t.Run("agent chart repo and version read, trailing slash trimmed", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		t.Setenv("AGENT_CHART_REPO", "oci://ghcr.io/example/charts/")
		t.Setenv("AGENT_CHART_VERSION", " 0.3.0 ")
		cfg, err := loadClusterConfig(false, "https://appstore.gov.nl")
		require.NoError(t, err)
		assert.Equal(t, "oci://ghcr.io/example/charts", cfg.AgentChartRepo)
		assert.Equal(t, "0.3.0", cfg.AgentChartVersion)
	})
}

func TestLoadAgentCertTTL(t *testing.T) {
	const validSecret = "this-is-a-sufficiently-long-join-secret-value"

	t.Run("defaults to 90 days", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		t.Setenv("APP_ENV", "production")
		cfg, err := loadClusterConfig(false, "")
		require.NoError(t, err)
		assert.Equal(t, 2160*time.Hour, cfg.AgentCertTTL)
	})

	t.Run("override accepted in a dev-like env", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
		t.Setenv("APP_ENV", "ci")
		t.Setenv("AGENT_CERT_TTL", "48h")
		cfg, err := loadClusterConfig(false, "")
		require.NoError(t, err)
		assert.Equal(t, 48*time.Hour, cfg.AgentCertTTL)
	})

	for _, env := range []string{"production", "staging", ""} {
		t.Run("override refused with APP_ENV="+env, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
			t.Setenv("APP_ENV", env)
			t.Setenv("AGENT_CERT_TTL", "48h")
			_, err := loadClusterConfig(false, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "AGENT_CERT_TTL is a test-only override")
		})
	}

	for _, v := range []string{"soon", "0s", "-1h"} {
		t.Run("invalid value "+v, func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("CLUSTER_JOIN_SECRET", validSecret)
			t.Setenv("APP_ENV", "test")
			t.Setenv("AGENT_CERT_TTL", v)
			_, err := loadClusterConfig(false, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "AGENT_CERT_TTL must be a positive duration")
		})
	}
}

// End-to-end confirmation via Load: dev mode + no join secret yields the
// insecure dev fallback key on the assembled Config.
func TestLoad_ClusterJoinDevFallback(t *testing.T) {
	baselineEnv(t)
	t.Setenv("APP_ENV", "dev")
	t.Setenv("APP_DEV_MODE", "true")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []byte(clusterJoinDevKey), cfg.Cluster.JoinSecret)
}

// ─── Load: orchestrator kind (ADR-4) ─────────────────────────────────────────

// setProdLikeEnv sets APP_ENV and the other settings a prod-like Load needs,
// so the orchestrator check is the only one that can fail.
func setProdLikeEnv(t *testing.T, appEnv string) {
	t.Helper()
	t.Setenv("APP_ENV", appEnv)
	t.Setenv("AUTH_CORS_ALLOWED_ORIGINS", "https://appstore.gov.nl")
	t.Setenv("CLUSTER_JOIN_SECRET", "this-is-a-sufficiently-long-join-secret-value")
	setAgentListenerEnv(t)
}

func TestLoad_OrchestratorRefusedInProdLikeEnv(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		server string
		token  string
	}{
		{"explicit argocd kind", "argocd", "https://argocd.example.gov.nl", "argocd-token"},
		{"explicit argocd kind, mixed case", "ArgoCD", "https://argocd.example.gov.nl", "argocd-token"},
		{"explicit argocd kind without credentials", "argocd", "", ""},
		{"unset kind with server URL", "", "https://argocd.example.gov.nl", ""},
		{"unset kind with server URL and token", "", "https://argocd.example.gov.nl", "argocd-token"},
		{"unset kind with token only", "", "", "argocd-token"},
	}
	for _, appEnv := range []string{"production", "staging", ""} {
		for _, tc := range cases {
			t.Run("env="+appEnv+"/"+tc.name, func(t *testing.T) {
				baselineEnv(t)
				setProdLikeEnv(t, appEnv)
				t.Setenv("ORCHESTRATOR_KIND", tc.kind)
				t.Setenv("ARGOCD_SERVER_URL", tc.server)
				t.Setenv("ARGOCD_AUTH_TOKEN", tc.token)

				_, err := Load()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "ADR-4")
				assert.Contains(t, err.Error(), "ORCHESTRATOR_KIND=agent")
			})
		}
	}
}

func TestLoad_OrchestratorAgentInProdLikeEnv(t *testing.T) {
	t.Run("agent kind", func(t *testing.T) {
		baselineEnv(t)
		setProdLikeEnv(t, "production")
		t.Setenv("ORCHESTRATOR_KIND", "agent")

		cfg, err := Load()
		require.NoError(t, err)
		assert.True(t, cfg.Orchestrator.IsAgentBrokered())
		assert.True(t, cfg.Orchestrator.Configured())
	})

	t.Run("nothing set boots with no orchestrator", func(t *testing.T) {
		baselineEnv(t)
		setProdLikeEnv(t, "production")

		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.Orchestrator.Configured())
	})
}

func TestLoad_OrchestratorArgoCDInDevLikeEnv(t *testing.T) {
	const joinSecret = "this-is-a-sufficiently-long-join-secret-value"
	for _, appEnv := range []string{"dev", "development", "local", "test", "ci"} {
		t.Run("env="+appEnv+"/explicit argocd kind", func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", appEnv)
			t.Setenv("CLUSTER_JOIN_SECRET", joinSecret)
			t.Setenv("ORCHESTRATOR_KIND", "argocd")
			t.Setenv("ARGOCD_SERVER_URL", "https://argocd.localhost")
			t.Setenv("ARGOCD_AUTH_TOKEN", "argocd-token")
			t.Setenv("ARGOCD_INSECURE_SKIP_VERIFY", "true")

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, OrchestratorConfig{
				Kind:               "argocd",
				ServerURL:          "https://argocd.localhost",
				AuthToken:          "argocd-token",
				NamespaceForApps:   "argocd",
				InsecureSkipVerify: true,
			}, cfg.Orchestrator)
		})

		t.Run("env="+appEnv+"/unset kind falls back to argocd", func(t *testing.T) {
			baselineEnv(t)
			t.Setenv("APP_ENV", appEnv)
			t.Setenv("CLUSTER_JOIN_SECRET", joinSecret)
			t.Setenv("ARGOCD_SERVER_URL", "https://argocd.localhost")
			t.Setenv("ARGOCD_AUTH_TOKEN", "argocd-token")

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, "argocd", cfg.Orchestrator.Kind)
			assert.True(t, cfg.Orchestrator.Configured())
		})
	}

	t.Run("argocd kind still needs its credentials", func(t *testing.T) {
		baselineEnv(t)
		t.Setenv("APP_ENV", "dev")
		t.Setenv("CLUSTER_JOIN_SECRET", joinSecret)
		t.Setenv("ORCHESTRATOR_KIND", "argocd")

		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ARGOCD_SERVER_URL is required")
	})
}
