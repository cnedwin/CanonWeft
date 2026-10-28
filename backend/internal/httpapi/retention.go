package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerRetentionInventory(group *echo.Group, workspace *service.Workspace, actor func(*echo.Context) domain.Actor) {
	scanner := service.RetentionScanner{Workspace: workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	group.GET("/operations/retention-execution", func(c *echo.Context) error {
		automatic, err := workspace.PeriodicCleanupAvailable(c.Request().Context(), actor(c).WorkspaceID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{
			"enabled":              os.Getenv("STUDIO_RETENTION_EXECUTION_ENABLED") == "1",
			"automatic_configured": os.Getenv("STUDIO_RETENTION_AUTO_ENABLED") == "1",
			"automatic_enabled":    automatic,
		})
	})
	group.POST("/projects/:project/retention-jobs/:job/start", func(c *echo.Context) error {
		var input struct {
			PlanHash string `json:"plan_hash"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, domain.ErrValidation)
		}
		if os.Getenv("STUDIO_RETENTION_EXECUTION_ENABLED") != "1" {
			return respondError(c, domain.ErrNotReady)
		}
		report, err := scanner.StartJob(c.Request().Context(), actor(c).ID, c.Param("project"), c.Param("job"), input.PlanHash)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(202, report)
	})
	group.GET("/projects/:project/retention-jobs", func(c *echo.Context) error {
		page, err := scanner.ListJobs(c.Request().Context(), actor(c).ID, c.Param("project"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, page)
	})
	group.POST("/projects/:project/retention-jobs", func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, domain.ErrValidation)
		}
		report, err := scanner.PlanJob(c.Request().Context(), actor(c).ID, c.Param("project"), input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
	group.GET("/projects/:project/retention-jobs/:job", func(c *echo.Context) error {
		report, err := scanner.ReadJob(c.Request().Context(), actor(c).ID, c.Param("project"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
	group.POST("/projects/:project/retention-jobs/:job/cancel", func(c *echo.Context) error {
		var input struct {
			PlanHash string `json:"plan_hash"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, domain.ErrValidation)
		}
		report, err := scanner.CancelPlannedJob(c.Request().Context(), actor(c).ID, c.Param("project"), c.Param("job"), input.PlanHash)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
	group.POST("/projects/:project/retention-inventory/scan", func(c *echo.Context) error {
		report, err := scanner.Scan(c.Request().Context(), actor(c).ID, c.Param("project"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
	group.POST("/projects/:project/retention-sources/register", func(c *echo.Context) error {
		report, err := workspace.RegisterRetentionSources(c.Request().Context(), actor(c).ID, c.Param("project"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
	group.GET("/operations/retention-inventory", func(c *echo.Context) error {
		report, err := workspace.RetentionInventory(c.Request().Context(), actor(c).ID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, report)
	})
}
