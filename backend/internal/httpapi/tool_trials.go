package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
	"os"
)

func registerToolTrials(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.ToolTrials{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, RuntimeRoot: os.Getenv("STUDIO_ISOLATION_ROOT")}
	root := "/projects/:id/tool-trials"
	g.POST(root, func(c *echo.Context) error {
		var input service.ToolTrialInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Create(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(202, result)
	})
	g.GET(root, func(c *echo.Context) error {
		items, next, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "next_cursor": next, "runtime_configured": runtime != nil && svc.RuntimeRoot != ""})
	})
	g.GET(root+"/:job", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:job/cancel", func(c *echo.Context) error {
		result, err := svc.Cancel(c.Request().Context(), actor(c), c.Param("id"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
}
