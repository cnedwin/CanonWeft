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

func registerRecipes(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.RecipeExecutions{Generations: &service.Generations{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}}
	root := "/projects/:id/recipe-executions"
	wake := func(c *echo.Context, result service.RecipeExecution) {
		if runtime == nil {
			return
		}
		_ = runtime.WakeRecipe(c.Request().Context(), result.ID)
		for _, step := range result.Steps {
			if step.Active && step.GenerationID != "" {
				_ = runtime.WakeGeneration(c.Request().Context(), step.GenerationID)
			}
		}
	}
	g.POST(root, func(c *echo.Context) error {
		var input service.RecipeExecutionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Prepare(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch_id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/:run", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:run/start", func(c *echo.Context) error {
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if runtime == nil {
			return respondError(c, domain.ErrNotReady)
		}
		result, err := svc.Start(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.ExpectedRevision)
		if err != nil {
			return respondError(c, err)
		}
		wake(c, result)
		return c.JSON(200, result)
	})
	g.POST(root+"/:run/control", func(c *echo.Context) error {
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Action           string `json:"action"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.Action, input.ExpectedRevision)
		if err != nil {
			return respondError(c, err)
		}
		wake(c, result)
		return c.JSON(200, result)
	})
	g.POST(root+"/:run/confirm", func(c *echo.Context) error {
		var input struct {
			ExpectedRevision int64  `json:"expected_revision"`
			StepKey          string `json:"step_key"`
			OutputHash       string `json:"output_hash"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Confirm(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.StepKey, input.ExpectedRevision, input.OutputHash)
		if err != nil {
			return respondError(c, err)
		}
		wake(c, result)
		return c.JSON(200, result)
	})
	g.POST(root+"/:run/retry", func(c *echo.Context) error {
		var input service.RecipeRetryInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if runtime == nil {
			return respondError(c, domain.ErrNotReady)
		}
		result, err := svc.Retry(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input)
		if err != nil {
			return respondError(c, err)
		}
		wake(c, result)
		return c.JSON(200, result)
	})
}
