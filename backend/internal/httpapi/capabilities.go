package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerCapabilities(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.Capabilities{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	root := "/projects/:id/capabilities"
	for _, preset := range []struct{ path, name string }{
		{"memory", "memory-consolidation"},
		{"adaptation", "adaptation-fidelity-review"},
		{"continuity", "continuity-repair"},
		{"change-inspection", "change-impact-review"},
		{"character-visual", "character-visual-director"},
	} {
		g.POST(root+"/presets/"+preset.path, func(c *echo.Context) error {
			var input struct{}
			if err := decode(c, &input); err != nil {
				return respondError(c, err)
			}
			result, err := svc.InstallNarrative(c.Request().Context(), actor(c), c.Param("id"), preset.name)
			if err != nil {
				return respondError(c, err)
			}
			return c.JSON(201, result)
		})
	}
	g.POST(root+"/presets/research", func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.InstallResearch(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root, func(c *echo.Context) error {
		items, next, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "next_cursor": next})
	})
	g.POST(root, func(c *echo.Context) error {
		var input service.CapabilitySave
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Save(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/:version", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("version"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/:version/manifest", func(c *echo.Context) error {
		result, err := svc.Manifest(c.Request().Context(), actor(c), c.Param("id"), c.Param("version"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:version/revoke", func(c *echo.Context) error {
		var input struct {
			Reason string `json:"reason"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.Revoke(c.Request().Context(), actor(c), c.Param("id"), c.Param("version"), input.Reason); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"revoked": true})
	})
}
