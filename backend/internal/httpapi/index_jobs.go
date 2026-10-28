package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
	"os"
)

func registerIndexJobs(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	svc := &service.IndexJobs{Knowledge: &service.Knowledge{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Embedder: embedder}}
	root := "/projects/:id/branches/:branch/knowledge/jobs"
	g.POST(root, func(c *echo.Context) error {
		var input service.IndexJobInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		job, err := svc.Create(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(202, job)
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "runtime_ready": runtime != nil})
	})
	g.GET(root+"/:job", func(c *echo.Context) error {
		job, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, job)
	})
	g.POST(root+"/:job/cancel", func(c *echo.Context) error {
		job, err := svc.Cancel(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, job)
	})
}
