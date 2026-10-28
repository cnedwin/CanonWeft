package httpapi

import (
	"bytes"
	"errors"
	"github.com/labstack/echo/v5"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
	"unicode/utf8"
)

func novelEntryError(c *echo.Context, err error) error {
	var invalid *novel.Validation
	if errors.As(err, &invalid) {
		return c.JSON(400, map[string]any{"error": invalid.Error(), "fields": invalid.Fields})
	}
	return respondError(c, err)
}

func registerNovelEntry(g *echo.Group, workspace *service.Workspace, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	svc := &service.NovelEntry{Workspace: workspace}
	registerSetupAssistance(g, workspace, actor)
	registerNovelSettings(g, svc, actor)
	registerNovelImages(g, workspace, actor)
	registerNovelPlanning(g, workspace, actor, providers...)
	g.GET("/novel-setups/presets", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{"items": novel.SetupPresets()})
	})
	g.POST("/novel-setups/preset-preview", func(c *echo.Context) error {
		// encoding/json normalizes invalid UTF-8. Reject it before decoding so
		// the author's supplied idea can always be preserved exactly.
		raw, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 1024*1024))
		if err != nil || !utf8.Valid(raw) {
			return novelEntryError(c, domain.ErrValidation)
		}
		c.Request().Body = io.NopCloser(bytes.NewReader(raw))
		var input struct {
			PresetID string `json:"preset_id"`
			Idea     string `json:"idea"`
		}
		if err := decode(c, &input); err != nil {
			return novelEntryError(c, err)
		}
		preview, err := novel.PreviewSetupPreset(input.PresetID, input.Idea)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(http.StatusOK, preview)
	})
	g.GET("/novel-setups/model-choices", func(c *echo.Context) error {
		items, err := svc.ModelChoices(c.Request().Context(), actor(c).ID)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET("/novel-setups/defaults", func(c *echo.Context) error { return c.JSON(200, novel.Defaults()) })
	g.GET("/novel-setups", func(c *echo.Context) error {
		items, err := svc.Drafts(c.Request().Context(), actor(c).ID)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "limit": 100})
	})
	g.GET("/novel-setups/:setup", func(c *echo.Context) error {
		result, err := svc.ReadDraft(c.Request().Context(), actor(c).ID, c.Param("setup"))
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, result)
	})
	g.PUT("/novel-setups/:setup", func(c *echo.Context) error {
		var input service.NovelSetupDraft
		if err := decode(c, &input); err != nil {
			return novelEntryError(c, err)
		}
		if input.ID != c.Param("setup") {
			return respondError(c, domain.ErrValidation)
		}
		result, err := svc.SaveDraft(c.Request().Context(), actor(c).ID, input)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST("/novel-setups/:setup/confirm", func(c *echo.Context) error {
		var input service.NovelConfirmation
		if err := decode(c, &input); err != nil {
			return novelEntryError(c, err)
		}
		result, err := svc.Confirm(c.Request().Context(), actor(c).ID, c.Param("setup"), input)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET("/novels", func(c *echo.Context) error {
		items, err := svc.Books(c.Request().Context(), actor(c).ID)
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "limit": 100})
	})
	g.GET("/novels/:novel", func(c *echo.Context) error {
		result, err := svc.Book(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, result)
	})
}
