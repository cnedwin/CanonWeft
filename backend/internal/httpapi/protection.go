package httpapi

import (
	"database/sql"
	"time"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

// RegisterProtection exposes metadata only, never database archives or paths.
// It is registered by the real process which owns the backup runner. New's
// origin/host/security middleware still applies to these routes.
func RegisterProtection(e *echo.Echo, db *sql.DB, view func(time.Time) backup.ProtectionView) {
	registerDiagnostics(e, db, view)
	workspace := &service.Workspace{DB: db}
	e.GET("/api/operations/protection", func(c *echo.Context) error {
		cookie, err := c.Cookie("studio_session")
		if err != nil {
			return respondError(c, domain.ErrUnauthorized)
		}
		actor, err := workspace.Actor(c.Request().Context(), cookie.Value)
		if err != nil {
			return respondError(c, err)
		}
		if actor.Role != "owner" {
			return respondError(c, domain.ErrForbidden)
		}
		if view == nil {
			return respondError(c, domain.ErrNotReady)
		}
		return c.JSON(200, view(time.Now().UTC()))
	})
}
