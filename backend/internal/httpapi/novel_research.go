package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelResearch(g *echo.Group, assist *service.NovelPlanningAssistance, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	svc := &service.NovelResearch{Generations: assist.Generations, Knowledge: assist.Knowledge}
	registerNovelResearchExecutions(g, svc, actor, providers...)
	const root = "/novels/:novel/research"
	g.GET("/novels/:novel/source-review", func(c *echo.Context) error {
		v, e := svc.SourceReview(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.List(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, map[string]any{"items": v})
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.ResearchCreate
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Create(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.POST(root+"/agents", func(c *echo.Context) error {
		v, e := svc.Install(c.Request().Context(), actor(c).ID, c.Param("novel"), "")
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/:resource", func(c *echo.Context) error {
		v, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.PUT(root+"/:resource", func(c *echo.Context) error {
		var in service.ResearchDraft
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Save(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/refresh-sources", func(c *echo.Context) error {
		var in service.ResearchReleaseInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.RefreshSources(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/assist", func(c *echo.Context) error {
		var in service.NovelPlanningAssistInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:resource/assist/:task", func(c *echo.Context) error {
		v, e := svc.Suggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/release", func(c *echo.Context) error {
		var in service.ResearchReleaseInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PrepareRelease(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
}
