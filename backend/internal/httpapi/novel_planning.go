package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerNovelPlanning(g *echo.Group, workspace *service.Workspace, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	registerNovelChapters(g, workspace, actor)
	svc := &service.NovelPlanning{IP: &service.IP{Workspace: workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	const root = "/novels/:novel/plans"
	g.POST(root+"/agents", func(c *echo.Context) error {
		v, e := svc.InstallAgents(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	assist := &service.NovelPlanningAssistance{Knowledge: &service.Knowledge{IP: svc.IP, Embedder: embedder}, Generations: &service.Generations{IP: svc.IP}}
	registerNovelResearch(g, assist, actor, providers...)
	g.POST(root+"/:resource/assist", func(c *echo.Context) error {
		var in service.NovelPlanningAssistInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := assist.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:resource/assist/:task", func(c *echo.Context) error {
		v, e := assist.Suggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.Overview(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root, func(c *echo.Context) error {
		var in struct {
			Kind  string `json:"kind"`
			Scope string `json:"scope"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Open(c.Request().Context(), actor(c).ID, c.Param("novel"), in.Kind, in.Scope)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:resource/draft", func(c *echo.Context) error {
		v, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.PUT(root+"/:resource/draft", func(c *echo.Context) error {
		var in service.NovelPlanDraft
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Save(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/basis", func(c *echo.Context) error {
		var in struct {
			Revision int64    `json:"revision"`
			Sources  []string `json:"sources"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Basis(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in.Revision, in.Sources)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/prepare", func(c *echo.Context) error {
		var in struct {
			Revision    int64  `json:"revision"`
			Hash        string `json:"hash"`
			OperationID string `json:"operation_id"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in.Revision, in.Hash, in.OperationID)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.POST(root+"/:resource/confirm", func(c *echo.Context) error {
		var in struct {
			ReleaseID string `json:"release_id"`
			Hash      string `json:"hash"`
			Accept    bool   `json:"accept"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Confirm(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in.ReleaseID, in.Hash, in.Accept)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
}
