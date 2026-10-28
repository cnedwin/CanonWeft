package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"strconv"
)

func registerDependencies(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	g.GET("/projects/:id/branches/:branch/patch-draft", func(c *echo.Context) error {
		draft, err := svc.ReadPatchDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
	g.PUT("/projects/:id/branches/:branch/patch-draft", func(c *echo.Context) error {
		var input service.PatchDraft
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		draft, err := svc.SavePatchDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
	g.POST("/projects/:id/compensations/preview", func(c *echo.Context) error {
		var input service.CompensationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		preview, err := svc.PreviewCompensation(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, preview)
	})
	g.POST("/projects/:id/patches/preview", func(c *echo.Context) error {
		var input service.PatchInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		preview, err := svc.PreviewPatches(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, preview)
	})
	g.POST("/projects/:id/patches/releases", func(c *echo.Context) error {
		var input service.PatchInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		release, err := svc.PreparePatchRelease(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, release)
	})
	g.GET("/projects/:id/changes", func(c *echo.Context) error {
		items, err := svc.ChangePlans(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET("/projects/:id/dependencies/catalog", func(c *echo.Context) error {
		items, err := svc.DependencyCatalog(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST("/projects/:id/changes", func(c *echo.Context) error {
		var input service.ChangePlanInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		plan, err := svc.CreateChangePlan(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, plan)
	})
	g.GET("/projects/:id/changes/:change", func(c *echo.Context) error {
		plan, err := svc.ReadChangePlan(c.Request().Context(), actor(c), c.Param("id"), c.Param("change"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, plan)
	})
	g.POST("/projects/:id/changes/:change/refresh", func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		plan, err := svc.RefreshPublicationPlan(c.Request().Context(), actor(c), c.Param("id"), c.Param("change"), input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, plan)
	})
	g.GET("/projects/:id/changes/:change/outcomes", func(c *echo.Context) error {
		limit := 100
		if value := c.QueryParam("limit"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return respondError(c, domain.ErrValidation)
			}
			limit = parsed
		}
		result, err := svc.ChangeOutcomesPage(c.Request().Context(), actor(c), c.Param("id"), c.Param("change"), c.QueryParam("after"), limit)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST("/projects/:id/changes/:change/scope", func(c *echo.Context) error {
		var input service.ChangeScopeInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.ExtendChangeScope(c.Request().Context(), actor(c), c.Param("id"), c.Param("change"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
}
