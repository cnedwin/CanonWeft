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

func registerGeneration(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.Generations{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	g.GET("/model-prices", func(c *echo.Context) error {
		items, err := svc.Prices(c.Request().Context(), actor(c).ID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST("/model-prices", func(c *echo.Context) error {
		var input service.PriceCard
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		price, err := svc.SavePrice(c.Request().Context(), actor(c).ID, input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, price)
	})
	root := "/projects/:id/generations"
	g.GET("/projects/:id/changes/:plan/inspection-coverage", func(c *echo.Context) error {
		result, err := svc.ChangeInspectionCoverage(c.Request().Context(), actor(c), c.Param("id"), c.Param("plan"), c.QueryParam("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET("/projects/:id/changes/:plan/inspections", func(c *echo.Context) error {
		result, err := svc.ChangeInspections(c.Request().Context(), actor(c), c.Param("id"), c.Param("plan"), c.QueryParam("branch"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": result.Items, "next": result.Next, "runtime_ready": runtime != nil})
	})
	g.POST("/projects/:id/changes/:plan/inspections", func(c *echo.Context) error {
		var input service.ChangeInspectionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PrepareChangeInspection(c.Request().Context(), actor(c), c.Param("id"), c.Param("plan"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/:task/change-inspection", func(c *echo.Context) error {
		result, err := svc.ReadChangeInspection(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/:task/patches", func(c *echo.Context) error {
		result, err := svc.PreviewRepair(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:task/patches", func(c *echo.Context) error {
		var input service.RepairInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PrepareRepairDecisions(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/:task/patches/preview", func(c *echo.Context) error {
		var input service.RepairInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PreviewRepairDecisions(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET("/projects/:id/generation-options", func(c *echo.Context) error {
		items, err := svc.Options(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch"), c.QueryParam("character"), c.QueryParam("creative"), c.QueryParam("creative_target"), c.QueryParam("image_novel"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "runtime_ready": runtime != nil})
	})
	g.POST(root, func(c *echo.Context) error {
		var input service.GenerationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		task, err := svc.Prepare(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, task)
	})
	g.GET(root+"/:task", func(c *echo.Context) error {
		task, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, task)
	})
	g.POST(root+"/:task/start", func(c *echo.Context) error {
		if runtime == nil {
			return respondError(c, domain.ErrNotReady)
		}
		task, err := svc.Start(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, task)
	})
	g.POST(root+"/:task/control", func(c *echo.Context) error {
		var input struct {
			Action string `json:"action"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		task, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"), input.Action)
		if err != nil {
			return respondError(c, err)
		}
		if runtime != nil {
			_ = runtime.WakeGeneration(c.Request().Context(), task.ID)
		}
		return c.JSON(200, task)
	})
}
