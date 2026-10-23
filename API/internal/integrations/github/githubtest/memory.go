// Package githubtest provides an in-memory GitHub client and repository
// fixtures for tests.
package githubtest

import (
	"context"

	"github.com/ClappFormOrg/SaidKick/API/internal/integrations/github"
)

// SHA is the commit SHA MemoryClient resolves every ref to.
const SHA = "abcdef1234567890abcdef1234567890abcdef12"

// MemoryClient is a github.Client that serves Files from memory. Every
// repository has default branch "main" and every ref resolves to SHA.
// Webhook methods are not supported and return github.ErrNotConfigured.
type MemoryClient struct {
	// Files maps a repository path (no leading "./") to its content.
	Files map[string][]byte
}

var _ github.Client = MemoryClient{}

// GetDefaultBranch returns "main".
func (MemoryClient) GetDefaultBranch(context.Context, int64, string, string) (string, error) {
	return "main", nil
}

// ResolveRef returns SHA.
func (MemoryClient) ResolveRef(context.Context, int64, string, string, string) (string, error) {
	return SHA, nil
}

// GetFileAtRef returns the content of path with blob SHA "blob-<path>", or
// github.ErrFileNotFound when Files has no such path.
func (c MemoryClient) GetFileAtRef(_ context.Context, _ int64, _, _, path, _ string) ([]byte, string, error) {
	if b, ok := c.Files[path]; ok {
		return b, "blob-" + path, nil
	}
	return nil, "", github.ErrFileNotFound
}

// VerifyWebhookSignature returns github.ErrNotConfigured.
func (MemoryClient) VerifyWebhookSignature([]byte, string) error {
	return github.ErrNotConfigured
}

// ParseEvent returns github.ErrNotConfigured.
func (MemoryClient) ParseEvent(string, []byte) (any, error) {
	return nil, github.ErrNotConfigured
}

// EvidenceFiles returns a fresh map with the demo and audit evidence a valid
// appstore.yaml declares: a description, DPIA, clean SARIF scan, AI risk and
// WCAG reports, and four screenshots. Callers may add or replace entries.
func EvidenceFiles() map[string][]byte {
	return map[string][]byte{
		"demo/description.md": []byte("# GovChat\nDemo."),
		"audit/dpia.pdf":      []byte("%PDF-1.4 dpia"),
		"audit/scan.sarif":    []byte(`{"version":"2.1.0","runs":[{"results":[]}]}`),
		"audit/ai-risk.pdf":   []byte("%PDF-1.4 ai"),
		"audit/wcag.pdf":      []byte("%PDF-1.4 wcag"),
		"demo/01.png":         []byte("\x89PNG\r\n1"),
		"demo/02.png":         []byte("\x89PNG\r\n2"),
		"demo/03.png":         []byte("\x89PNG\r\n3"),
		"demo/04.png":         []byte("\x89PNG\r\n4"),
	}
}
