package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerModels(group *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.Models{Workspace: &service.Workspace{DB: db}}
	budgets := &service.Budgets{Workspace: svc.Workspace}
	group.GET("/model-calls", func(c *echo.Context) error {
		items, err := budgets.Calls(c.Request().Context(), actor(c).ID, c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		next := ""
		if len(items) == 50 {
			next = items[len(items)-1].ID
		}
		return c.JSON(200, map[string]any{"items": items, "next_cursor": next})
	})
	group.GET("/model-calls/:id/history", func(c *echo.Context) error {
		result, err := budgets.History(c.Request().Context(), actor(c).ID, c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	group.POST("/model-calls/:id/reconcile", func(c *echo.Context) error {
		var input service.ReconciliationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := budgets.Reconcile(c.Request().Context(), actor(c).ID, c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	group.GET("/model-budget", func(c *echo.Context) error {
		value, err := budgets.Read(c.Request().Context(), actor(c).ID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, value)
	})
	group.PUT("/model-budget", func(c *echo.Context) error {
		var input service.Budget
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		value, err := budgets.Configure(c.Request().Context(), actor(c).ID, input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, value)
	})
	group.GET("/models", func(c *echo.Context) error {
		settings, err := svc.Settings(c.Request().Context(), actor(c).ID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, settings)
	})
	save := func(c *echo.Context) error {
		var input service.ModelInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		config, err := svc.Save(c.Request().Context(), actor(c).ID, c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		status := 200
		if c.Param("id") == "" {
			status = 201
		}
		return c.JSON(status, config)
	}
	group.POST("/models", save)
	group.PUT("/models/:id", save)
	group.PUT("/model-bindings/:role", func(c *echo.Context) error {
		var input domain.ModelBinding
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		input.Role = c.Param("role")
		result, err := svc.Bind(c.Request().Context(), actor(c).ID, input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
}
