package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ClappFormOrg/SaidKick/API/internal/apierror"
	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/ClappFormOrg/SaidKick/API/internal/testutil"
)

// newMiddlewareEcho returns an Echo instance with the given middleware and
// a single GET /ping route answering 200.
func newMiddlewareEcho(mw ...echo.MiddlewareFunc) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = ErrorHandler
	e.Use(mw...)
	e.GET("/ping", func(c echo.Context) error {
		return c.String(http.StatusOK, requestIDFromContext(c))
	})
	return e
}

func serve(e *echo.Echo, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func preflight(origin string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, "/ping", http.NoBody)
	req.Header.Set(echo.HeaderOrigin, origin)
	req.Header.Set(echo.HeaderAccessControlRequestMethod, http.MethodGet)
	req.Header.Set(echo.HeaderAccessControlRequestHeaders, "authorization,content-type")
	return req
}

func TestCORSExplicitOrigins(t *testing.T) {
	const allowed = "https://app.example.nl"
	e := newMiddlewareEcho(CORSMiddleware([]string{allowed, "http://localhost:3000"}))

	t.Run("preflight from allowed origin", func(t *testing.T) {
		rec := serve(e, preflight(allowed))
		assert.Equal(t, http.StatusNoContent, rec.Code)
		h := rec.Header()
		assert.Equal(t, allowed, h.Get(echo.HeaderAccessControlAllowOrigin))
		assert.Equal(t, "true", h.Get(echo.HeaderAccessControlAllowCredentials))
		assert.Equal(t, "3600", h.Get(echo.HeaderAccessControlMaxAge))

		methods := h.Get(echo.HeaderAccessControlAllowMethods)
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			assert.Contains(t, methods, m)
		}
		allowHeaders := h.Get(echo.HeaderAccessControlAllowHeaders)
		assert.NotContains(t, allowHeaders, "*", "wildcard headers are invalid with credentials")
		for _, hdr := range []string{echo.HeaderAuthorization, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderXRequestID} {
			assert.Contains(t, allowHeaders, hdr)
		}
	})

	t.Run("simple request from allowed origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
		req.Header.Set(echo.HeaderOrigin, allowed)
		rec := serve(e, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, allowed, rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
		assert.Equal(t, "true", rec.Header().Get(echo.HeaderAccessControlAllowCredentials))
	})

	for _, origin := range []string{
		"https://evil.example.com",
		"https://app.example.nl.evil.com",
		"http://app.example.nl",
		"null",
	} {
		t.Run("disallowed origin "+origin, func(t *testing.T) {
			rec := serve(e, preflight(origin))
			assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
			assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowCredentials))

			req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
			req.Header.Set(echo.HeaderOrigin, origin)
			rec = serve(e, req)
			assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
			assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowCredentials))
		})
	}
}

func TestCORSWildcardDisablesCredentials(t *testing.T) {
	for name, origins := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			e := newMiddlewareEcho(CORSMiddleware(origins))
			rec := serve(e, preflight("https://anything.example"))
			assert.Equal(t, "*", rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
			assert.Empty(t, rec.Header().Get(echo.HeaderAccessControlAllowCredentials),
				"credentials must never be allowed together with a wildcard origin")
		})
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	e := newMiddlewareEcho(SecurityHeadersMiddleware())

	want := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
	}

	for _, path := range []string{"/ping", "/does-not-exist"} {
		t.Run(path, func(t *testing.T) {
			rec := serve(e, httptest.NewRequest(http.MethodGet, path, http.NoBody))
			for k, v := range want {
				assert.Equal(t, v, rec.Header().Get(k), k)
			}
		})
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	e := newMiddlewareEcho(RequestIDMiddleware())

	t.Run("honours caller id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
		req.Header.Set(echo.HeaderXRequestID, "abc-123")
		rec := serve(e, req)
		assert.Equal(t, "abc-123", rec.Header().Get(echo.HeaderXRequestID))
		assert.Equal(t, "abc-123", rec.Body.String(), "id must be on the request context")
	})

	for name, supplied := range map[string]string{
		"mints when absent":    "",
		"mints when blank":     "   ",
		"replaces oversize id": strings.Repeat("a", 129),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
			if supplied != "" {
				req.Header.Set(echo.HeaderXRequestID, supplied)
			}
			rec := serve(e, req)
			rid := rec.Header().Get(echo.HeaderXRequestID)
			_, err := uuid.Parse(rid)
			require.NoError(t, err, "minted id must be a UUID, got %q", rid)
			assert.Equal(t, rid, rec.Body.String())
		})
	}

	t.Run("accepts id of exactly 128 chars", func(t *testing.T) {
		id := strings.Repeat("b", 128)
		req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
		req.Header.Set(echo.HeaderXRequestID, id)
		rec := serve(e, req)
		assert.Equal(t, id, rec.Header().Get(echo.HeaderXRequestID))
	})
}

func TestRequestIDFromContextWithoutMiddleware(t *testing.T) {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	assert.Empty(t, requestIDFromContext(c))
}

func TestRecoveryMiddleware(t *testing.T) {
	e := newMiddlewareEcho(RecoveryMiddleware())
	e.GET("/boom", func(echo.Context) error { panic("boom") })

	rec := serve(e, httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "boom", "panic value must not leak to the client")
}

func TestAccessLogAndMetricsMiddleware(t *testing.T) {
	e := newMiddlewareEcho(AccessLogMiddleware(), MetricsMiddleware())

	rec := serve(e, httptest.NewRequest(http.MethodGet, "/ping", http.NoBody))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = serve(e, httptest.NewRequest(http.MethodGet, "/unmatched", http.NoBody))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// A handler that returns an error is answered by ErrorHandler after the
// middleware returns; the status label must be the status the client got.
func TestMetricsMiddleware_StatusOfReturnedErrors(t *testing.T) {
	e := newMiddlewareEcho(AccessLogMiddleware(), MetricsMiddleware())
	e.GET("/status-test/apierror", func(echo.Context) error {
		return apierror.New(http.StatusConflict, "conflict", "state conflict")
	})
	e.GET("/status-test/httperror", func(echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden)
	})
	e.GET("/status-test/plain", func(echo.Context) error { return errors.New("boom") })

	cases := []struct {
		path string
		want int
	}{
		{"/status-test/apierror", http.StatusConflict},
		{"/status-test/httperror", http.StatusForbidden},
		{"/status-test/plain", http.StatusInternalServerError},
		{"/status-test/none", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			label := tc.path
			if tc.want == http.StatusNotFound {
				label = metrics.PathUnmatched
			}
			counter := metrics.HTTPRequestsTotal(http.MethodGet, label, tc.want)
			ok := metrics.HTTPRequestsTotal(http.MethodGet, label, http.StatusOK)
			before, okBefore := counter.Get(), ok.Get()

			rec := serve(e, httptest.NewRequest(http.MethodGet, tc.path, http.NoBody))
			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, before+1, counter.Get(), "counted with status %d", tc.want)
			assert.Equal(t, okBefore, ok.Get(), "not counted as 200")
		})
	}
}

// A panicking handler is recovered inside the metrics middleware, so the
// request is counted under its route pattern with the status the client
// gets, and the client still receives the error envelope. newEcho is the
// chain both the main and the agent mTLS listeners use.
func TestMetricsMiddleware_CountsPanics(t *testing.T) {
	e := newEcho()
	e.GET("/panic-test/:id", func(echo.Context) error { panic("boom") })
	e.GET("/panic-test/:id/forbidden", func(echo.Context) error { panic(echo.ErrForbidden) })

	cases := []struct {
		url, route string
		want       int
		code       string
	}{
		{"/panic-test/42", "/panic-test/:id", http.StatusInternalServerError, "internal_error"},
		{"/panic-test/42/forbidden", "/panic-test/:id/forbidden", http.StatusForbidden, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			counter := metrics.HTTPRequestsTotal(http.MethodGet, tc.route, tc.want)
			duration := metrics.HTTPRequestDuration(http.MethodGet, tc.route)
			before, observed := counter.Get(), testutil.HistogramCount(duration)

			req := httptest.NewRequest(http.MethodGet, tc.url, http.NoBody)
			req.Header.Set(echo.HeaderXRequestID, "panic-req")
			rec := serve(e, req)

			assert.Equal(t, tc.want, rec.Code)
			var body struct {
				Error apierror.Error `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body.Error.Code)
			assert.Equal(t, "panic-req", body.Error.RequestID)
			assert.NotContains(t, rec.Body.String(), "boom", "panic value must not leak to the client")
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

			assert.Equal(t, before+1, counter.Get(), "counted with status %d", tc.want)
			assert.Equal(t, observed+1, testutil.HistogramCount(duration), "duration recorded")
		})
	}
}

// An unmatched URL must not become a label value: it would carry IDs and
// let any caller mint new series.
func TestMetricsMiddleware_UnmatchedPathIsOneLabel(t *testing.T) {
	e := newMiddlewareEcho(MetricsMiddleware())
	unmatched := metrics.HTTPRequestDuration(http.MethodPost, metrics.PathUnmatched)
	before := testutil.HistogramCount(unmatched)

	for _, p := range []string{
		"/api/v1/clusters/0190c3a4-0000-7000-8000-000000000001/heartbeat",
		"/users/someone@example.com",
	} {
		rec := serve(e, httptest.NewRequest(http.MethodPost, p, http.NoBody))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	}

	assert.Equal(t, before+2, testutil.HistogramCount(unmatched), "both requests land on path=%q", metrics.PathUnmatched)
	var buf bytes.Buffer
	metrics.WritePrometheus(&buf, false)
	assert.NotContains(t, buf.String(), "0190c3a4-0000-7000-8000-000000000001")
	assert.NotContains(t, buf.String(), "/users/")
	testutil.RequireNoIdentifyingLabels(t)
}
