package httpapi

import (
	"errors"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelChapterReferences(g *echo.Group, svc *service.NovelChapters, actor func(*echo.Context) domain.Actor) {
	g.GET("/novels/:novel/chapter-references", func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		value, err := svc.ReferenceDirectory(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, value)
	})
	g.GET("/novels/:novel/chapter-references/:chapter", func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		value, err := svc.ReadReference(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("chapter"), c.QueryParam("source"))
		if errors.Is(err, service.ErrChapterReferenceMissing) {
			return c.JSON(404, map[string]string{"error": err.Error()})
		}
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, value)
	})
}
