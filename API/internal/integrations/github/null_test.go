package github

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errReader fails every Read with err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadCapped(t *testing.T) {
	cases := []struct {
		name    string
		size    int
		limit   int64
		wantErr error
	}{
		{"empty", 0, 10, nil},
		{"under limit", 9, 10, nil},
		{"exactly at limit", 10, 10, nil},
		{"one byte over", 11, 10, ErrFileTooLarge},
		{"far over", 1 << 20, 10, ErrFileTooLarge},
		{"zero limit, empty stream", 0, 0, nil},
		{"zero limit, one byte", 1, 0, ErrFileTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := bytes.Repeat([]byte("a"), tc.size)
			got, err := readCapped(bytes.NewReader(data), tc.limit)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, data, got)
		})
	}
}

func TestReadCappedAtMaxFileSize(t *testing.T) {
	got, err := readCapped(strings.NewReader(strings.Repeat("x", MaxFileSizeBytes)), MaxFileSizeBytes)
	require.NoError(t, err)
	assert.Len(t, got, MaxFileSizeBytes)

	_, err = readCapped(strings.NewReader(strings.Repeat("x", MaxFileSizeBytes+1)), MaxFileSizeBytes)
	require.ErrorIs(t, err, ErrFileTooLarge)
}

func TestReadCappedPropagatesReadError(t *testing.T) {
	boom := errors.New("stream reset")
	_, err := readCapped(errReader{err: boom}, 10)
	require.ErrorIs(t, err, boom)
}

func TestNullClientReturnsNotConfigured(t *testing.T) {
	var c Client = NullClient{}
	ctx := t.Context()

	_, err := c.GetDefaultBranch(ctx, 1, "o", "r")
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = c.ResolveRef(ctx, 1, "o", "r", "main")
	require.ErrorIs(t, err, ErrNotConfigured)
	_, _, err = c.GetFileAtRef(ctx, 1, "o", "r", "appstore.yaml", "sha")
	require.ErrorIs(t, err, ErrNotConfigured)
	require.ErrorIs(t, c.VerifyWebhookSignature([]byte("{}"), "sha256=00"), ErrNotConfigured)
	_, err = c.ParseEvent("push", []byte("{}"))
	require.ErrorIs(t, err, ErrNotConfigured)
}
