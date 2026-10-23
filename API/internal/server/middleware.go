package server

import (
	"context"
	"strings"
	"time"

	"github.com/ClappFormOrg/SaidKick/API/internal/metrics"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type ctxKey int

const requestIDCtxKey ctxKey = 0

// RequestIDMiddleware attaches an opaque request ID to every request. If
// the caller supplied X-Request-ID it is honoured (capped at 128 chars);
// otherwise a UUIDv7 is minted. The ID is reflected in the response header
// and made available via requestIDFromContext.
func RequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			rid := strings.TrimSpace(c.Request().Header.Get(echo.HeaderXRequestID))
			if rid == "" || len(rid) > 128 {
				id, err := uuid.NewV7()
				if err != nil {
					rid = uuid.NewString()
				} else {
					rid = id.String()
				}
			}
			c.Response().Header().Set(echo.HeaderXRequestID, rid)
			ctx := context.WithValue(c.Request().Context(), requestIDCtxKey, rid)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func requestIDFromContext(c echo.Context) string {
	if v, ok := c.Request().Context().Value(requestIDCtxKey).(string); ok {
		return v
	}
	return ""
}

// AccessLogMiddleware emits a one-line structured log per request. We use
// Echo's stdlib logger for V0; swap to zerolog/zap when we wire structured
// logging globally (planned for Sprint 2).
func AccessLogMiddleware() echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:    true,
		LogURI:       true,
		LogMethod:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			c.Logger().Infof(
				"http method=%s uri=%s status=%d latency=%s request_id=%s",
				v.Method, v.URI, v.Status, v.Latency.Round(time.Microsecond), v.RequestID,
			)
			return nil
		},
	})
}

// RecoveryMiddleware turns panics into 500 responses without crashing the
// process. Echo's built-in does this: it hands the panic to ErrorHandler,
// which writes the response before the middleware returns. Middleware
// registered before it therefore sees a panicking request as an ordinary
// committed response.
func RecoveryMiddleware() echo.MiddlewareFunc {
	return middleware.Recover()
}

// CORSMiddleware returns CORS middleware configured for the given origin list.
// Pass nil or empty to default to ["*"] (dev only — production rejects this at
// config.Load() time so the wildcard can never reach a production instance).
func CORSMiddleware(allowedOrigins []string) echo.MiddlewareFunc {
	origins := allowedOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	// AllowCredentials requires explicit origins — wildcard + credentials is
	// rejected by all browsers. When the caller passes no origins (dev, wildcard
	// mode) we disable credentials support so the wildcard remains valid.
	allowCreds := len(allowedOrigins) > 0
	// AllowHeaders must be an explicit list when AllowCredentials is true.
	// The Fetch spec forbids the wildcard "*" with credentials — browsers
	// will reject the preflight if both are set.
	return middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: origins,
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{
			echo.HeaderAuthorization,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderXRequestID,
		},
		AllowCredentials: allowCreds,
		MaxAge:           int(time.Hour / time.Second),
	})
}

// apiContentSecurityPolicy is the CSP for every API response. The API never
// serves a document a browser should render: it answers JSON, redirects and
// evidence downloads. Allowing nothing means a response that does end up
// rendered (an evidence file with an HTML or SVG content type, say) cannot
// load or run anything, and frame-ancestors backs up X-Frame-Options.
const apiContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"

// SecurityHeadersMiddleware sets defensive HTTP response headers on every
// response.
func SecurityHeadersMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", apiContentSecurityPolicy)
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			return next(c)
		}
	}
}

// MetricsMiddleware records per-route request counts and latency histograms
// for VictoriaMetrics. The path label uses the Echo route pattern (e.g.
// /api/v1/apps/:slug) so high-cardinality UUIDs don't explode the series.
func MetricsMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			// A returned error has no response yet: ErrorHandler writes it
			// once the chain unwinds. Write it here so the status label is
			// the one the client gets, not the 200 the response starts at.
			// The error is handled, so the outer middleware sees nil.
			if err := next(c); err != nil {
				c.Error(err)
			}
			elapsed := time.Since(start).Seconds()

			// Use the matched route pattern. A request no route matches gets
			// one fixed label value: its raw URL would put caller-chosen
			// text (IDs included) into a label and grow the series without
			// bound.
			path := c.Path()
			if path == "" {
				path = metrics.PathUnmatched
			}
			method := c.Request().Method
			status := c.Response().Status

			metrics.HTTPRequestsTotal(method, path, status).Inc()
			metrics.HTTPRequestDuration(method, path).Update(elapsed)
			return nil
		}
	}
}
