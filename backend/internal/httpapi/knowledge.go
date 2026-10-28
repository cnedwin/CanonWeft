package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerKnowledge(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	svc := &service.Knowledge{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Embedder: embedder, MemoryCache: memory.NewLocalCache(128, 16<<20)}
	root := "/projects/:id/branches/:branch/knowledge"
	g.GET(root+"/private-index", func(c *echo.Context) error {
		result, err := svc.PrivateStatus(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/contexts/:context/private-sources/:chunk", func(c *echo.Context) error {
		result, err := svc.PrivateSource(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("context"), c.Param("chunk"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/working-inputs", func(c *echo.Context) error {
		result, err := svc.WorkingChoices(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root, func(c *echo.Context) error {
		result, err := svc.Status(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/index", func(c *echo.Context) error {
		result, err := svc.Build(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/search", func(c *echo.Context) error {
		var input service.SearchInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Search(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/contexts/:context", func(c *echo.Context) error {
		result, err := svc.ReadContext(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("context"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
}
