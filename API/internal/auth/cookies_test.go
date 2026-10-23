package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCookieDomain = "appstore.example.nl"

// setCookies runs set against a fresh Echo context and returns the cookies
// the response sets.
func setCookies(t *testing.T, set func(c echo.Context)) []*http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	set(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec))
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies
}

// TestCookieAttributes pins the attributes of every cookie the API sets.
// The session cookie is Strict because the webapp reads it only through
// same-site fetches; the state cookie must be Lax so the IdP's cross-site
// redirect back to /auth/callback carries it.
func TestCookieAttributes(t *testing.T) {
	const ttl = 8 * time.Hour

	cases := []struct {
		name     string
		set      func(c echo.Context)
		cookie   string
		path     string
		sameSite http.SameSite
		maxAge   int
		value    string
	}{
		{
			name:     "session",
			set:      func(c echo.Context) { setSessionCookie(c, "tok", testCookieDomain, ttl) },
			cookie:   sessionCookieName,
			path:     "/",
			sameSite: http.SameSiteStrictMode,
			maxAge:   int(ttl / time.Second),
			value:    "tok",
		},
		{
			name:     "session cleared",
			set:      func(c echo.Context) { clearSessionCookie(c, testCookieDomain) },
			cookie:   sessionCookieName,
			path:     "/",
			sameSite: http.SameSiteStrictMode,
			maxAge:   -1,
		},
		{
			name:     "state",
			set:      func(c echo.Context) { setStateCookie(c, "st", testCookieDomain) },
			cookie:   stateCookieName,
			path:     "/auth/callback",
			sameSite: http.SameSiteLaxMode,
			maxAge:   int(5 * time.Minute / time.Second),
			value:    "st",
		},
		{
			name:     "state cleared",
			set:      func(c echo.Context) { clearStateCookie(c, testCookieDomain) },
			cookie:   stateCookieName,
			path:     "/auth/callback",
			sameSite: http.SameSiteLaxMode,
			maxAge:   -1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setCookies(t, tc.set)[0]
			assert.Equal(t, tc.cookie, c.Name)
			assert.Equal(t, tc.value, c.Value)
			assert.Equal(t, tc.path, c.Path)
			assert.Equal(t, testCookieDomain, c.Domain)
			assert.Equal(t, tc.maxAge, c.MaxAge)
			assert.True(t, c.HttpOnly, "HttpOnly")
			assert.True(t, c.Secure, "Secure")
			assert.Equal(t, tc.sameSite, c.SameSite)
		})
	}
}

// TestCookiesAreHostOnlyWithoutDomain checks that an empty CookieDomain, the
// production setting, gives cookies without a Domain attribute, so the
// browser sends them only to the host that set them.
func TestCookiesAreHostOnlyWithoutDomain(t *testing.T) {
	cases := map[string]func(c echo.Context){
		"session":         func(c echo.Context) { setSessionCookie(c, "tok", "", time.Hour) },
		"session cleared": func(c echo.Context) { clearSessionCookie(c, "") },
		"state":           func(c echo.Context) { setStateCookie(c, "st", "") },
		"state cleared":   func(c echo.Context) { clearStateCookie(c, "") },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			set(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec))
			header := rec.Header().Get(echo.HeaderSetCookie)
			require.NotEmpty(t, header)
			assert.NotContains(t, strings.ToLower(header), "domain=")
		})
	}
}

// TestSessionCookieLifetimeMatchesToken checks that the cookie does not
// outlive the session JWT it carries, or expire before it.
func TestSessionCookieLifetimeMatchesToken(t *testing.T) {
	const ttl = 2 * time.Hour
	tok, err := mintSession(uuid.New(), uuid.New(), testSecret, ttl)
	require.NoError(t, err)
	claims, err := parseSession(tok, testSecret)
	require.NoError(t, err)

	c := setCookies(t, func(c echo.Context) { setSessionCookie(c, tok, "", ttl) })[0]
	tokenLifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time)
	assert.Equal(t, int(tokenLifetime/time.Second), c.MaxAge)
}

// TestLogoutClearsTheCookieItSet checks that logout's deleting Set-Cookie
// names the same path and domain as the session cookie, which a browser
// needs to drop the original.
func TestLogoutClearsTheCookieItSet(t *testing.T) {
	a := newUnitAuthenticator(newFakeRevocations())
	a.cfg.CookieDomain = testCookieDomain
	e := echo.New()
	a.RegisterRoutes(e)

	tok, err := mintSession(uuid.New(), uuid.New(), testSecret, time.Hour)
	require.NoError(t, err)
	set := setCookies(t, func(c echo.Context) { setSessionCookie(c, tok, a.cfg.CookieDomain, a.cfg.SessionTTL) })[0]

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", http.NoBody)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tok})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)

	cleared := rec.Result().Cookies()
	require.Len(t, cleared, 1)
	assert.Equal(t, set.Name, cleared[0].Name)
	assert.Equal(t, set.Path, cleared[0].Path)
	assert.Equal(t, set.Domain, cleared[0].Domain)
	assert.Empty(t, cleared[0].Value)
	assert.Negative(t, cleared[0].MaxAge)
}
