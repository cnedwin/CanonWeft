package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelResearchExecutions(g *echo.Group, notebook *service.NovelResearch, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	svc := &service.NovelResearchExecutions{Research: notebook, Sources: &service.ResearchSources{IP: notebook.Generations.IP, Provider: researchSourceProvider(providers)}}
	const root = "/novels/:novel/research/:resource/runs"
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.List(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		return c.JSON(200, map[string]any{"items": v})
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.ResearchExecutionCreate
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Create(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:run", func(c *echo.Context) error {
		v, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("run"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		return c.JSON(200, v)
	})
	g.POST(root+"/:run/step", func(c *echo.Context) error {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Step(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("run"), in.Revision)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:run/background", func(c *echo.Context) error {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.StartBackground(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("run"), in.Revision)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:run/control", func(c *echo.Context) error {
		var in struct {
			Action string `json:"action"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Control(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("run"), in.Action)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.PUT(root+"/:run/selection", func(c *echo.Context) error {
		var in service.ResearchExecutionSelection
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Select(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("run"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
}
