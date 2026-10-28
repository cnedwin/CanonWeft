package httpapi

import (
	"database/sql"
	"time"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerDiagnostics(e *echo.Echo, db *sql.DB, view func(time.Time) backup.ProtectionView) {
	workspace := &service.Workspace{DB: db}
	e.GET("/api/operations/diagnostics", func(c *echo.Context) error {
		cookie, err := c.Cookie("studio_session")
		if err != nil {
			return respondError(c, domain.ErrUnauthorized)
		}
		actor, err := workspace.Actor(c.Request().Context(), cookie.Value)
		if err != nil {
			return respondError(c, err)
		}
		report, err := workspace.OperationsDiagnostics(c.Request().Context(), actor.ID)
		if err != nil {
			return respondError(c, err)
		}
		var protection any
		if view != nil {
			v := view(time.Now().UTC())
			// Do not export path, source identity, archive IDs or arbitrary errors.
			summary := map[string]any{"checkpoint_state": v.CheckpointState, "attempt_state": v.AttemptState, "checked_at": v.CheckedAt, "running": v.Running, "snapshot_at": v.Latest.SnapshotAt, "independent_media": v.IndependentMedia, "log_recovery": v.LogRecovery}
			if v.LogCoverage != nil {
				summary["log_state"] = v.LogCoverage.State
				summary["log_observed_at"] = v.LogCoverage.Latest.ObservedAt
				summary["history_gaps"] = v.LogCoverage.HistoryGaps
				summary["cleanup_state"] = v.LogCoverage.CleanupState
			}
			protection = summary
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, map[string]any{"operations": report, "protection": protection})
	})
}
