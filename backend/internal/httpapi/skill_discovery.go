package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerSkillDiscovery(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	root := "/projects/:id/skill-discovery"
	g.GET(root, func(c *echo.Context) error {
		result, err := svc.Overview(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	save := func(c *echo.Context) error {
		var input service.DiscoverySourceInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.SaveSource(c.Request().Context(), actor(c), c.Param("id"), c.Param("source"), input)
		if err != nil {
			return respondError(c, err)
		}
		status := 200
		if c.Param("source") == "" {
			status = 201
		}
		return c.JSON(status, result)
	}
	g.POST(root+"/sources", save)
	g.PUT(root+"/sources/:source", save)
	g.POST(root+"/sources/:source/runs", func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Start(c.Request().Context(), actor(c), c.Param("id"), c.Param("source"), input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/runs/:run/control", func(c *echo.Context) error {
		var input struct {
			Action string `json:"action"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.Action)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
}
