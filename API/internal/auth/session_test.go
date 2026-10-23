package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otherSecret = "a-different-secret-32bytes!!!!!!"

// signHS is a helper that signs arbitrary claims with the given HMAC method.
func signHS(t *testing.T, method jwt.SigningMethod, claims jwt.Claims, secret string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return tok
}

// unsignedToken builds an "alg: none" token carrying the given JSON payload.
func unsignedToken(payload string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(payload)) + "."
}

func TestSessionRoundTrip(t *testing.T) {
	userID, orgID := uuid.New(), uuid.New()

	tok, err := mintSession(userID, orgID, testSecret, time.Hour)
	require.NoError(t, err)

	claims, err := parseSession(tok, testSecret)
	require.NoError(t, err)
	assert.Equal(t, userID.String(), claims.Subject)
	assert.Equal(t, orgID, claims.OrgID)
	require.NotNil(t, claims.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(time.Hour), claims.ExpiresAt.Time, 5*time.Second)
	_, err = uuid.Parse(claims.ID)
	require.NoError(t, err, "jti must be a UUID")

	other, err := mintSession(userID, orgID, testSecret, time.Hour)
	require.NoError(t, err)
	otherClaims, err := parseSession(other, testSecret)
	require.NoError(t, err)
	assert.NotEqual(t, claims.ID, otherClaims.ID, "every session gets its own jti")
}

func TestParseSessionRejects(t *testing.T) {
	userID, orgID := uuid.New(), uuid.New()

	expired, err := mintSession(userID, orgID, testSecret, -time.Minute)
	require.NoError(t, err)

	forged, err := mintSession(userID, orgID, otherSecret, time.Hour)
	require.NoError(t, err)

	valid, err := mintSession(userID, orgID, testSecret, time.Hour)
	require.NoError(t, err)
	parts := strings.Split(valid, ".")
	require.Len(t, parts, 3)

	// Swap the payload for one naming a different org while keeping the
	// original signature: the signature no longer matches.
	evilPayload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"sub":"` + userID.String() + `","org":"` + uuid.NewString() + `","exp":9999999999}`))
	payloadSwap := parts[0] + "." + evilPayload + "." + parts[2]

	hs512 := signHS(t, jwt.SigningMethodHS512, sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		OrgID: orgID,
	}, testSecret)

	algNone := unsignedToken(`{"sub":"` + userID.String() + `","org":"` + orgID.String() + `","exp":9999999999}`)

	cases := []struct {
		name  string
		token string
	}{
		{"expired", expired},
		{"signed with another secret", forged},
		{"payload swapped under original signature", payloadSwap},
		{"signature stripped", parts[0] + "." + parts[1] + "."},
		{"HS512 instead of HS256", hs512},
		{"alg none", algNone},
		{"empty", ""},
		{"garbage", "not-a-jwt"},
		{"two segments", parts[0] + "." + parts[1]},
		{"bad base64 payload", parts[0] + ".!!!." + parts[2]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := parseSession(tc.token, testSecret)
			require.Error(t, err)
			assert.Nil(t, claims)
		})
	}
}

func TestStateTokenRoundTrip(t *testing.T) {
	orgID := uuid.New()

	tok, err := mintStateToken(orgID, "nonce-123", "verifier-abc", "/dashboard", testSecret)
	require.NoError(t, err)

	sc, err := validateStateToken(tok, testSecret)
	require.NoError(t, err)
	assert.Equal(t, "nonce-123", sc.State)
	assert.Equal(t, "verifier-abc", sc.CodeVerifier)
	assert.Equal(t, orgID, sc.OrgID)
	assert.Equal(t, "/dashboard", sc.Redirect)
	require.NotNil(t, sc.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), sc.ExpiresAt.Time, 5*time.Second)
}

func TestValidateStateTokenRejects(t *testing.T) {
	orgID := uuid.New()

	expired := signHS(t, jwt.SigningMethodHS256, stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-10 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-5 * time.Minute)),
		},
		State: "nonce", CodeVerifier: "cv", OrgID: orgID,
	}, testSecret)

	forged, err := mintStateToken(orgID, "nonce", "cv", "", otherSecret)
	require.NoError(t, err)

	hs384 := signHS(t, jwt.SigningMethodHS384, stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
		State: "nonce", CodeVerifier: "cv", OrgID: orgID,
	}, testSecret)

	algNone := unsignedToken(`{"st":"nonce","cv":"cv","org":"` + orgID.String() + `","exp":9999999999}`)

	cases := []struct {
		name  string
		token string
	}{
		{"expired", expired},
		{"signed with another secret", forged},
		{"HS384 instead of HS256", hs384},
		{"alg none", algNone},
		{"empty", ""},
		{"garbage", "a.b.c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := validateStateToken(tc.token, testSecret)
			require.Error(t, err)
			assert.Nil(t, sc)
		})
	}
}

func TestRandomBase64URL(t *testing.T) {
	a, err := randomBase64URL(32)
	require.NoError(t, err)
	b, err := randomBase64URL(32)
	require.NoError(t, err)

	assert.Len(t, a, base64.RawURLEncoding.EncodedLen(32))
	assert.NotEqual(t, a, b)
	assert.NotContains(t, a, "=")
	assert.NotContains(t, a, "+")
	assert.NotContains(t, a, "/")
}
