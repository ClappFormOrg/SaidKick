package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// GetHealthz answers a fast liveness probe. It does not touch the DB so
// kubelet probes don't cascade outages.
func (h *Handlers) GetHealthz(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok"})
}

// GetReadyz pings the DB. Returns 503 if the connection pool is broken.
func (h *Handlers) GetReadyz(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()

	sqlDB, err := h.DB.RW.DB()
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"status": "unhealthy",
			"reason": "db-handle-unavailable",
		})
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"status": "unhealthy",
			"reason": "db-ping-failed",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": "ready"})
}
