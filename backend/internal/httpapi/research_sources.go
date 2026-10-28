package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/research"
	"novelstudio/internal/service"
)

func researchSourceProvider(providers []service.ResearchProvider) service.ResearchProvider {
	if len(providers) > 0 && providers[0] != nil {
		return providers[0]
	}
	return research.NewCatalog(nil)
}

func registerResearchSources(g *echo.Group, ip *service.IP, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	svc := &service.ResearchSources{IP: ip, Provider: researchSourceProvider(providers)}
	const root = "/projects/:id/branches/:branch/research-sources"
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/search", func(c *echo.Context) error {
		var in struct {
			Provider string `json:"provider,omitempty"`
			Language string `json:"language"`
			Query    string `json:"query"`
		}
		if err := decode(c, &in); err != nil {
			return respondError(c, err)
		}
		items, err := svc.SearchIn(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), in.Provider, in.Language, in.Query)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/collect", func(c *echo.Context) error {
		var in service.ResearchCollectInput
		if err := decode(c, &in); err != nil {
			return respondError(c, err)
		}
		v, err := svc.Collect(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), in)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, v)
	})
}
