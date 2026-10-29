package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelBookReviews(g *echo.Group, writer *service.NovelChapterWriter, actor func(*echo.Context) domain.Actor) {
	svc := &service.NovelBookReviews{Generations: writer.Generations, Knowledge: writer.Knowledge}
	const root = "/novels/:novel/book-reviews"
	g.GET(root+"/:run/dispositions", func(c *echo.Context) error {
		value, err := svc.Dispositions(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("run"))
		if err != nil {
			return novelEntryError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, value)
	})
	g.PUT(root+"/:run/dispositions", func(c *echo.Context) error {
		var input service.NovelReviewDispositionInput
		if err := decode(c, &input); err != nil {
			return novelEntryError(c, err)
		}
		value, err := svc.SaveDisposition(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("run"), input)
		if err != nil {
			return novelEntryError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, value)
	})
	g.POST(root+"/preview", func(c *echo.Context) error {
		var in service.NovelBookReviewInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Preview(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.NovelBookReviewInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.List(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, map[string]any{"items": v})
	})
	g.GET(root+"/:run", func(c *echo.Context) error {
		v, e := svc.Report(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("run"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
}
