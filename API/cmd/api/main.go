// Command api boots the sAId-Kick Appstore API.
//
// V0 entry point: loads .env, opens the Postgres connection pools, runs
// migrations, then either exits (when -migrate-only) or starts the HTTP
// server on cfg.HTTP.Addr and blocks until SIGINT/SIGTERM.
//
// Usage:
//
//	go run ./cmd/api                 # connect + migrate + serve
//	go run ./cmd/api -migrate-only   # connect + migrate then exit
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/audit"
	"github.com/ClappFormOrg/SaidKick/API/internal/auth"
	"github.com/ClappFormOrg/SaidKick/API/internal/config"
	"github.com/ClappFormOrg/SaidKick/API/internal/db"
	gh "github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator/agentbrokered"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/orchestrator/argocd"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/secrets"
	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/vault"
	"github.com/ClappFormOrg/SaidKick/API/internal/reconciler"
	"github.com/ClappFormOrg/SaidKick/API/internal/revalidation"
	"github.com/ClappFormOrg/SaidKick/API/internal/server"
	"github.com/joho/godotenv"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "run migrations and exit")
	flag.Parse()

	if err := run(*migrateOnly); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run(migrateOnly bool) error {
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	handle, err := db.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if cErr := handle.Close(); cErr != nil {
			slog.Warn("close db", "error", cErr)
		}
	}()

	migCtx, cancelMig := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelMig()

	slog.Info("running migrations")
	if err := db.Migrate(migCtx, handle); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	slog.Info("migrations applied successfully")

	if migrateOnly {
		return nil
	}

	// Construct the GitHub App client (real if creds present, else Null).
	var ghClient gh.Client
	if cfg.GitHub.Configured() {
		ghReal, err := gh.New(gh.Config{
			AppID:         cfg.GitHub.AppID,
			PrivateKeyPEM: cfg.GitHub.PrivateKeyPEM,
			WebhookSecret: cfg.GitHub.WebhookSecret,
		})
		if err != nil {
			return fmt.Errorf("github client: %w", err)
		}
		ghClient = ghReal
		slog.Info("github app client wired", "app_id", cfg.GitHub.AppID)
	} else {
		ghClient = gh.NullClient{}
		slog.Warn("github app credentials not configured — POST /apps and webhook will return 503")
	}

	// Vault mTLS config — nil when no cert files are configured (plain HTTPS).
	vaultTLS, err := vault.BuildTLSConfig(
		cfg.Secrets.VaultCACertFile,
		cfg.Secrets.VaultClientCertFile,
		cfg.Secrets.VaultClientKeyFile,
	)
	if err != nil {
		return fmt.Errorf("vault TLS config: %w", err)
	}

	// Vault KV client for OIDC client secret storage and per-cluster
	// orchestrator token lookups. Uses the static admin token; falls back
	// to NullClient so ConfigureOrganizationOIDC can use v0-inline: scheme.
	var kvClient vault.KVClient
	if cfg.Secrets.Configured() {
		kvClient = vault.New(cfg.Secrets.VaultAddr, cfg.Secrets.VaultToken, cfg.Secrets.VaultMount, vaultTLS)
		slog.Info("vault kv client wired", "addr", cfg.Secrets.VaultAddr)
	} else {
		kvClient = vault.NullClient{}
	}

	// Construct the deployment orchestrator.
	var deployer orchestrator.Deployer
	switch {
	case cfg.Orchestrator.IsAgentBrokered():
		deployer = agentbrokered.New(handle)
		slog.Info("agent-brokered orchestrator wired")
	case cfg.Orchestrator.Configured():
		httpClient := &http.Client{
			Timeout: 30 * time.Second,
		}
		if cfg.Orchestrator.InsecureSkipVerify {
			httpClient.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // gated by APP_ENV check
			}
		}
		argoCD, err := argocd.New(argocd.Config{
			ServerURL:          cfg.Orchestrator.ServerURL,
			AuthToken:          cfg.Orchestrator.AuthToken,
			TokenStore:         kvClient,
			NamespaceForApps:   cfg.Orchestrator.NamespaceForApps,
			HTTPClient:         httpClient,
			InsecureSkipVerify: cfg.Orchestrator.InsecureSkipVerify,
		})
		if err != nil {
			return fmt.Errorf("argocd client: %w", err)
		}
		deployer = argoCD
		slog.Info("argo cd orchestrator wired", "server", cfg.Orchestrator.ServerURL, "namespace", cfg.Orchestrator.NamespaceForApps)
	default:
		deployer = orchestrator.NullDeployer{}
		slog.Warn("orchestrator not configured — POST /deployments will return 503")
	}
	// Count and time every orchestrator call, whichever deployer is wired.
	deployer = orchestrator.Instrument(deployer)

	// Secret store: per-cluster AppRole if VAULT_APPROLE_MOUNT is set,
	// static-token Vault if VAULT_TOKEN is set, in-memory for dev mode,
	// NullStore otherwise (returns 503 when secrets are used).
	var secretStore secrets.Store
	switch {
	case cfg.Secrets.AppRoleMounted():
		appRoleCache := vault.NewAppRoleCache(cfg.Secrets.VaultAddr, cfg.Secrets.VaultAppRoleMount, vaultTLS)
		secretStore = secrets.NewVaultStoreWithAppRole(cfg.Secrets.VaultAddr, cfg.Secrets.VaultMount, handle, appRoleCache)
		slog.Info("vault secret store wired with per-cluster approle",
			"addr", cfg.Secrets.VaultAddr, "approle_mount", cfg.Secrets.VaultAppRoleMount)
	case cfg.Secrets.Configured():
		secretStore = secrets.NewVaultStore(cfg.Secrets.VaultAddr, cfg.Secrets.VaultToken, cfg.Secrets.VaultMount)
		slog.Info("vault secret store wired", "addr", cfg.Secrets.VaultAddr, "mount", cfg.Secrets.VaultMount)
	case cfg.IsDevMode:
		secretStore = secrets.NewInMemoryStore()
		slog.Warn("secret store is in-memory (dev mode) — vault_addr not set")
	default:
		secretStore = secrets.NullStore{}
		slog.Warn("secret store not configured — deployments with secrets will return 503")
	}

	// Vault PKI client for cluster agent certificate signing.
	var pkiClient vault.PKISigner
	if cfg.Cluster.PKIConfigured() && cfg.Secrets.Configured() {
		pkiClient = vault.NewPKIClient(cfg.Secrets.VaultAddr, cfg.Secrets.VaultToken,
			cfg.Cluster.VaultPKIMount, cfg.Cluster.VaultPKIRole, vaultTLS)
		slog.Info("vault pki client wired", "mount", cfg.Cluster.VaultPKIMount, "role", cfg.Cluster.VaultPKIRole)
	} else {
		pkiClient = vault.NullPKIClient{}
		slog.Warn("vault pki not configured — POST /clusters/:id/handshake will return 503")
	}

	auditLogger := audit.NewLogger(handle)
	srv := server.New(cfg, handle, auditLogger, ghClient, deployer, secretStore, kvClient, pkiClient)

	if cfg.IsDevMode {
		slog.Warn("APP_DEV_MODE=true — dev-mode authenticator is active")
		slog.Warn("never enable APP_DEV_MODE in production")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if cfg.Secrets.Configured() {
		renewer := vault.NewTokenRenewer(cfg.Secrets.VaultAddr, cfg.Secrets.VaultToken, vaultTLS)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("vault token-renewer panicked", "panic", r)
				}
			}()
			renewer.Run(ctx)
		}()
		slog.Info("vault token-renewer started")
	}

	rec := reconciler.New(handle, deployer, auditLogger, 0)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("reconciler panicked", "panic", r)
			}
		}()
		rec.Run(ctx)
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("session cleanup panicked", "panic", r)
			}
		}()
		auth.RunSessionCleanup(ctx, handle, auth.SessionCleanupInterval)
	}()

	if cfg.GitHub.Configured() {
		rev := revalidation.New(handle, ghClient, auditLogger, cfg.SARIFGateMode, 0)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("revalidation worker panicked", "panic", r)
				}
			}()
			rev.Run(ctx)
		}()
		slog.Info("revalidation worker started")
	}

	if err := srv.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("http server: %w", err)
	}
	slog.Info("shutdown clean")
	return nil
}
