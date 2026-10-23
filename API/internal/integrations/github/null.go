package github

import (
	"context"
	"io"
)

// NullClient is wired when the API process boots without GitHub App
// credentials. Every method returns ErrNotConfigured; handlers that
// depend on the integration translate that into a clear 503 so the
// webapp can render an actionable "ask your platform admin" message.
//
// This pattern lets the API still serve the read-side of the catalogue
// + cluster + audit endpoints during local development before anyone
// has provisioned a GitHub App.
type NullClient struct{}

// Compile-time check that NullClient satisfies Client.
var _ Client = (*NullClient)(nil)

func (NullClient) GetDefaultBranch(_ context.Context, _ int64, _, _ string) (string, error) {
	return "", ErrNotConfigured
}

func (NullClient) ResolveRef(_ context.Context, _ int64, _, _, _ string) (string, error) {
	return "", ErrNotConfigured
}

func (NullClient) GetFileAtRef(_ context.Context, _ int64, _, _, _, _ string) ([]byte, string, error) {
	return nil, "", ErrNotConfigured
}

func (NullClient) VerifyWebhookSignature(_ []byte, _ string) error { return ErrNotConfigured }

func (NullClient) ParseEvent(_ string, _ []byte) (any, error) { return nil, ErrNotConfigured }

// readCapped reads up to limit bytes from r, returning ErrFileTooLarge if
// the stream produces more. Used by realClient when go-github returns a
// download URL rather than inline content.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(r, limit+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, ErrFileTooLarge
	}
	return buf, nil
}
