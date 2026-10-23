package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const sessionCookieName = "saidkick_session"
const stateCookieName = "saidkick_state"

// sessionClaims is the payload of the session cookie JWT (HS256).
// RegisteredClaims.ID carries the jti that logout records in
// revoked_sessions. Tokens minted before the jti existed have an empty ID and
// cannot be revoked; they age out within one session TTL.
type sessionClaims struct {
	jwt.RegisteredClaims
	OrgID uuid.UUID `json:"org"`
}

// errSessionRevoked is returned by validateSession for a token whose jti
// has been revoked.
var errSessionRevoked = errors.New("session revoked")

// errRevocationLookup marks a validateSession failure caused by the
// revocation store rather than by the token itself.
var errRevocationLookup = errors.New("session revocation lookup failed")

// stateClaims is the payload of the short-lived state cookie JWT used during
// the OIDC authorization code flow. It binds the state nonce, PKCE verifier,
// originating org, and optional post-login redirect in a single signed blob
// so no server-side state storage is required.
type stateClaims struct {
	jwt.RegisteredClaims
	State        string    `json:"st"`
	CodeVerifier string    `json:"cv"`
	OrgID        uuid.UUID `json:"org"`
	Redirect     string    `json:"rd,omitempty"`
}

func mintSession(userID, orgID uuid.UUID, secret string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := sessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.NewString(),
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		OrgID: orgID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

// validateSession checks the token's signature and expiry, then rejects it
// when its jti is in the revocation store. A token without a jti predates
// revocation and is accepted on signature and expiry alone.
func validateSession(ctx context.Context, token, secret string, revoked revocationChecker) (*sessionClaims, error) {
	claims, err := parseSession(token, secret)
	if err != nil {
		return nil, err
	}
	if claims.ID == "" {
		return claims, nil
	}
	jti, err := uuid.Parse(claims.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid jti: %w", err)
	}
	isRevoked, err := revoked.isRevoked(ctx, jti)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRevocationLookup, err)
	}
	if isRevoked {
		return nil, errSessionRevoked
	}
	return claims, nil
}

// parseSession verifies the token's HS256 signature and expiry. It does no
// revocation check; use validateSession to authenticate a request.
func parseSession(token, secret string) (*sessionClaims, error) {
	var claims sessionClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	return &claims, nil
}

func mintStateToken(orgID uuid.UUID, state, codeVerifier, redirect, secret string) (string, error) {
	now := time.Now()
	claims := stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		State:        state,
		CodeVerifier: codeVerifier,
		OrgID:        orgID,
		Redirect:     redirect,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}

func validateStateToken(token, secret string) (*stateClaims, error) {
	var claims stateClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	return &claims, nil
}

// randomBase64URL returns n random bytes encoded as a URL-safe base64 string
// (no padding). Used for state nonces.
func randomBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
