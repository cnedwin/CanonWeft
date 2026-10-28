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

func registerSemanticReviews(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}}
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	svc.Knowledge = &service.Knowledge{IP: svc.Generations.IP, Embedder: embedder}
	root := "/projects/:id/semantic-reviews"
	g.POST(root, func(c *echo.Context) error {
		var input service.SemanticReviewInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Prepare(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch_id"), c.QueryParam("target_kind"), c.QueryParam("target_id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/:review", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("review"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:review/start", func(c *echo.Context) error {
		if runtime == nil {
			return respondError(c, domain.ErrNotReady)
		}
		result, err := svc.Start(c.Request().Context(), actor(c), c.Param("id"), c.Param("review"))
		if err != nil {
			return respondError(c, err)
		}
		_ = runtime.WakeSemanticReview(c.Request().Context(), result.ID)
		return c.JSON(200, result)
	})
	g.POST(root+"/:review/select", func(c *echo.Context) error {
		var input struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Select(c.Request().Context(), actor(c), c.Param("id"), c.Param("review"), input.ExpectedRevision)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:review/control", func(c *echo.Context) error {
		var input struct {
			Action string `json:"action"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("review"), input.Action)
		if err != nil {
			return respondError(c, err)
		}
		if runtime != nil {
			_ = runtime.WakeSemanticReview(c.Request().Context(), result.ID)
			if result.GenerationID != "" {
				_ = runtime.WakeGeneration(c.Request().Context(), result.GenerationID)
			}
		}
		return c.JSON(200, result)
	})
}
