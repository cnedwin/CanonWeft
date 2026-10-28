package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerSetupAssistance(g *echo.Group, w *service.Workspace, actor func(*echo.Context) domain.Actor) {
	ip := &service.IP{Workspace: w, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	svc := &service.NovelSetupAssistance{Generations: &service.Generations{IP: ip}, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	root := "/novel-setups/:setup"
	g.POST(root+"/assistant", func(c *echo.Context) error {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if e := decode(c, &in); e != nil {
			return respondError(c, e)
		}
		v, e := svc.Workspace(c.Request().Context(), actor(c).ID, c.Param("setup"), in.Revision)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/assist", func(c *echo.Context) error {
		var in service.SetupAssistInput
		if e := decode(c, &in); e != nil {
			return respondError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("setup"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/assist/:task", func(c *echo.Context) error {
		v, e := svc.Suggestion(c.Request().Context(), actor(c).ID, c.Param("setup"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/assist-adoption", func(c *echo.Context) error {
		var in service.SetupAssistAdoption
		if e := decode(c, &in); e != nil {
			return respondError(c, e)
		}
		v, e := svc.Adopt(c.Request().Context(), actor(c).ID, c.Param("setup"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
}
