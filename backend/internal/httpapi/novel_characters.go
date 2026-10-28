package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelCharacters(g *echo.Group, writer *service.NovelChapterWriter, actor func(*echo.Context) domain.Actor) {
	svc := &service.NovelCharacters{Generations: writer.Generations, Knowledge: writer.Knowledge}
	const root = "/novels/:novel/character-designs"
	g.GET(root+"/name-catalog/sources", func(c *echo.Context) error {
		v, e := svc.NameSources(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/name-catalog/sources-preview", func(c *echo.Context) error {
		var in struct {
			ImportIDs []string `json:"import_ids"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PreviewNameSources(c.Request().Context(), actor(c).ID, c.Param("novel"), in.ImportIDs)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/name-catalog/source-preview", func(c *echo.Context) error {
		var in struct {
			ImportID string `json:"import_id"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PreviewNameSource(c.Request().Context(), actor(c).ID, c.Param("novel"), in.ImportID)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/name-catalog/open", func(c *echo.Context) error {
		v, e := svc.OpenNameCatalog(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/name-catalog", func(c *echo.Context) error {
		v, e := svc.NameCatalog(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.PUT(root+"/name-catalog", func(c *echo.Context) error {
		var in service.NameCatalog
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.SaveNameCatalog(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/name-catalog/import-preview", func(c *echo.Context) error {
		var in struct {
			Text string `json:"text"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PreviewNameImport(c.Request().Context(), actor(c).ID, c.Param("novel"), in.Text)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/name-materials", func(c *echo.Context) error {
		v, e := svc.NameMaterials(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("language"), c.QueryParam("q"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.POST(root+"/naming-agents", func(c *echo.Context) error {
		v, e := svc.InstallNaming(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:design/naming/assist", func(c *echo.Context) error {
		var in service.NovelPlanningAssistInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PrepareNames(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:design/naming/assist/:task", func(c *echo.Context) error {
		v, e := svc.NamingSuggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.POST(root+"/:design/batch-agents", func(c *echo.Context) error {
		v, e := svc.InstallBatchAgent(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:design/batches", func(c *echo.Context) error {
		var in service.CharacterBatchInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PrepareBatch(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:design/batches", func(c *echo.Context) error {
		v, e := svc.BatchList(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, map[string]any{"items": v})
	})
	g.GET(root+"/:design/batches/:run", func(c *echo.Context) error {
		v, e := svc.BatchReport(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), c.Param("run"))
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
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, map[string]any{"items": v})
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.CharacterDesignCreate
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Open(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
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
	g.GET(root+"/:design", func(c *echo.Context) error {
		v, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.PUT(root+"/:design", func(c *echo.Context) error {
		var in service.CharacterDesignDraft
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Save(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:design/apply", func(c *echo.Context) error {
		var in service.CharacterDesignApply
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Apply(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:design/assist", func(c *echo.Context) error {
		var in service.NovelPlanningAssistInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:design/assist/:task", func(c *echo.Context) error {
		v, e := svc.Suggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("design"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
}
