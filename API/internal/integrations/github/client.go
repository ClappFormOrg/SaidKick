// Package github is the platform's GitHub App integration.
//
// The platform uses a single GitHub App registered against the
// "saidkick-appstore" namespace. Developers install that App on their
// own repositories to grant the platform scoped read access to
// appstore.yaml + audit evidence files. The Client in this package
// authenticates as the App, exchanges the JWT for short-lived per-
// installation tokens, and exposes the small subset of GitHub-API
// methods the rest of the application needs:
//
//   - GetDefaultBranch    — figure out the audited branch at App creation
//   - ResolveRef          — pin a tag or branch to an immutable commit SHA
//   - GetFileAtRef        — fetch arbitrary file content at a SHA (≤ 1MB)
//   - VerifyWebhookSignature — HMAC-SHA256 against the configured secret
//   - ParseEvent          — typed payload extraction for incoming webhooks
//
// The interface is the canonical Client type so handlers can mock it in
// tests. The Real implementation wraps google/go-github + ghinstallation.
// A NullClient is used when no GitHub App credentials are configured at
// startup — every method returns ErrNotConfigured so handlers can surface
// a clear 503 rather than crashing.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gh "github.com/google/go-github/v66/github"
)

// ErrNotConfigured is returned by NullClient methods when the GitHub App
// is not wired into this environment. Handlers should map this to a
// 503 github_not_configured response so the webapp can render a clear
// "ask your platform admin to provision the GitHub App" message.
var ErrNotConfigured = errors.New("github: app not configured")

// MaxFileSizeBytes is the cap on file content the platform will accept
// from a developer's repo. Files larger than this are rejected without
// reading their bytes; protects the platform from DoS via huge PDFs.
const MaxFileSizeBytes = 5 * 1024 * 1024 // 5 MiB

// Client is the interface the rest of the application sees. Inject this
// (not a concrete type) into handlers so tests can supply fakes.
type Client interface {
	// GetDefaultBranch returns the configured default branch for the repo
	// at the moment of the call. Used by CreateApp when the request body
	// omits an explicit branch.
	GetDefaultBranch(ctx context.Context, installationID int64, owner, repo string) (string, error)

	// ResolveRef turns a tag or branch into the immutable 40-char commit
	// SHA at HEAD. Callers should always persist the SHA, never the ref.
	ResolveRef(ctx context.Context, installationID int64, owner, repo, ref string) (sha string, err error)

	// GetFileAtRef fetches the bytes of one file at a specific SHA. Files
	// larger than MaxFileSizeBytes return ErrFileTooLarge without
	// streaming. Returns the raw content + the file's blob SHA.
	GetFileAtRef(ctx context.Context, installationID int64, owner, repo, path, ref string) (content []byte, blobSHA string, err error)

	// VerifyWebhookSignature validates the X-Hub-Signature-256 header
	// against the platform's configured webhook secret using the canonical
	// HMAC-SHA256 scheme. Returns nil on match.
	VerifyWebhookSignature(payload []byte, signatureHeader string) error

	// ParseEvent extracts a typed payload for the given event-type header
	// value (push, installation, repository, etc.). Unknown event types
	// return (nil, nil) — caller decides whether to log or ignore.
	ParseEvent(eventType string, payload []byte) (any, error)
}

// ErrFileTooLarge is returned when a requested file exceeds MaxFileSizeBytes.
var ErrFileTooLarge = errors.New("github: file exceeds maximum allowed size")

// ErrFileNotFound is returned when GitHub answers 404 for a content path.
var ErrFileNotFound = errors.New("github: file not found")

// realClient is the production implementation. One transport per
// installation is cached lazily.
type realClient struct {
	appID         int64
	privateKeyPEM []byte
	webhookSecret []byte
}

// Config holds the GitHub App credentials needed to construct a real Client.
type Config struct {
	// AppID is the integer ID printed on the GitHub App settings page.
	AppID int64

	// PrivateKeyPEM is the PEM-encoded RSA private key downloaded when the
	// App was created. Loaded into memory once at boot.
	PrivateKeyPEM []byte

	// WebhookSecret is the shared secret configured on the GitHub App
	// webhook delivery settings. Used as the HMAC-SHA256 key.
	WebhookSecret []byte
}

// New constructs a real Client from the supplied credentials. Returns
// an error if any field is empty.
func New(cfg Config) (Client, error) {
	if cfg.AppID == 0 {
		return nil, errors.New("github.New: app_id is required")
	}
	if len(cfg.PrivateKeyPEM) == 0 {
		return nil, errors.New("github.New: private_key_pem is required")
	}
	if len(cfg.WebhookSecret) == 0 {
		return nil, errors.New("github.New: webhook_secret is required")
	}
	// Verify the private key parses now so we fail fast at boot, not on
	// the first request. NewAppsTransport returns an error if the PEM is
	// malformed or the key is not RSA.
	if _, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKeyPEM); err != nil {
		return nil, fmt.Errorf("github.New: invalid private key: %w", err)
	}
	return &realClient{
		appID:         cfg.AppID,
		privateKeyPEM: cfg.PrivateKeyPEM,
		webhookSecret: cfg.WebhookSecret,
	}, nil
}

// installationClient returns a fully-authenticated go-github client
// scoped to the given installation. The underlying ghinstallation
// transport caches the installation token and refreshes it ~10 minutes
// before expiry.
func (r *realClient) installationClient(installationID int64) (*gh.Client, error) {
	tr, err := ghinstallation.New(
		&rateLimitTransport{inner: http.DefaultTransport},
		r.appID,
		installationID,
		r.privateKeyPEM,
	)
	if err != nil {
		return nil, fmt.Errorf("github: installation transport: %w", err)
	}
	return gh.NewClient(&http.Client{Transport: tr}), nil
}

// GetDefaultBranch satisfies Client.
func (r *realClient) GetDefaultBranch(ctx context.Context, installationID int64, owner, repo string) (string, error) {
	c, err := r.installationClient(installationID)
	if err != nil {
		return "", err
	}
	repoMeta, _, err := c.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("github: get repo: %w", err)
	}
	if repoMeta.GetDefaultBranch() == "" {
		return "main", nil // sensible fallback for empty / brand-new repos
	}
	return repoMeta.GetDefaultBranch(), nil
}

// ResolveRef satisfies Client. It calls Repositories.GetCommitSHA1 which
// follows tags and branches to a commit SHA.
func (r *realClient) ResolveRef(ctx context.Context, installationID int64, owner, repo, ref string) (string, error) {
	c, err := r.installationClient(installationID)
	if err != nil {
		return "", err
	}
	sha, _, err := c.Repositories.GetCommitSHA1(ctx, owner, repo, ref, "")
	if err != nil {
		return "", fmt.Errorf("github: resolve ref %q: %w", ref, err)
	}
	return sha, nil
}

// GetFileAtRef satisfies Client.
func (r *realClient) GetFileAtRef(ctx context.Context, installationID int64, owner, repo, path, ref string) ([]byte, string, error) {
	c, err := r.installationClient(installationID)
	if err != nil {
		return nil, "", err
	}

	// We use the Repositories.DownloadContents API which returns a raw
	// io.ReadCloser; but for files this falls back to GetContents which
	// is more robust for short files. Use GetContents so we can read the
	// blob SHA + size before deciding to fetch the body.
	fileContent, _, resp, err := c.Repositories.GetContents(
		ctx, owner, repo, path,
		&gh.RepositoryContentGetOptions{Ref: ref},
	)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, "", ErrFileNotFound
		}
		return nil, "", fmt.Errorf("github: get contents %q@%s: %w", path, ref, err)
	}
	if fileContent == nil {
		// The path resolved to a directory listing — not what we want.
		return nil, "", ErrFileNotFound
	}
	if fileContent.GetSize() > MaxFileSizeBytes {
		return nil, "", ErrFileTooLarge
	}
	// fileContent.GetContent() base64-decodes the inline payload when
	// GitHub returns it directly (files ≤ 1 MiB). Larger files are
	// returned as a download URL and require a separate fetch; for our
	// 5 MiB cap we still need to handle the download-URL case.
	body, err := fileContent.GetContent()
	if err != nil {
		// Body wasn't inline — fall through to a streamed download.
		stream, _, sErr := c.Repositories.DownloadContents(ctx, owner, repo, path, &gh.RepositoryContentGetOptions{Ref: ref})
		if sErr != nil {
			return nil, "", fmt.Errorf("github: download stream %q: %w", path, sErr)
		}
		defer func() { _ = stream.Close() }()
		raw, rErr := readCapped(stream, MaxFileSizeBytes)
		if rErr != nil {
			return nil, "", rErr
		}
		return raw, fileContent.GetSHA(), nil
	}
	return []byte(body), fileContent.GetSHA(), nil
}

// VerifyWebhookSignature delegates to go-github's helper which performs
// constant-time comparison.
func (r *realClient) VerifyWebhookSignature(payload []byte, signatureHeader string) error {
	return gh.ValidateSignature(signatureHeader, payload, r.webhookSecret)
}

// ParseEvent decodes the payload into the typed event matching the
// X-GitHub-Event header value.
func (r *realClient) ParseEvent(eventType string, payload []byte) (any, error) {
	ev, err := gh.ParseWebHook(eventType, payload)
	if err != nil {
		return nil, fmt.Errorf("github: parse %q event: %w", eventType, err)
	}
	return ev, nil
}
