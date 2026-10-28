package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerNovelChapters(g *echo.Group, workspace *service.Workspace, actor func(*echo.Context) domain.Actor) {
	ip := &service.IP{Workspace: workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	registerNovelExport(g, ip, actor)
	svc := &service.NovelChapters{IP: ip}
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	writer := &service.NovelChapterWriter{Generations: &service.Generations{IP: ip}, Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	registerNovelBookReviews(g, writer, actor)
	registerNovelCharacters(g, writer, actor)
	const root = "/novels/:novel/chapters"
	g.GET(root+"/:resource/writing-card", func(c *echo.Context) error {
		result, err := svc.WritingCard(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(200, result)
	})
	research := &service.NovelChapterResearch{Writer: writer}
	g.POST(root+"/:resource/research/search", func(c *echo.Context) error {
		var in service.ChapterResearchSearchInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := research.Search(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	batch := &service.NovelBatch{Writer: writer}
	g.POST(root+"/:resource/polish-agent", func(c *echo.Context) error {
		v, e := svc.InstallPolish(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET("/novels/:novel/writing-batches/chapters", func(c *echo.Context) error {
		v, e := batch.Chapters(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, map[string]any{"items": v})
	})
	g.POST("/novels/:novel/writing-batches", func(c *echo.Context) error {
		var in service.NovelBatchInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := batch.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.POST(root+"/:resource/agents", func(c *echo.Context) error {
		v, e := svc.InstallAgents(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/release/:release/confirm", func(c *echo.Context) error {
		var in struct {
			Hash   string `json:"hash"`
			Accept bool   `json:"accept"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.ConfirmRelease(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("release"), in.Hash, in.Accept)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/:resource/release", func(c *echo.Context) error {
		v, e := svc.LatestRelease(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/release", func(c *echo.Context) error {
		var in service.NovelChapterReleaseInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PrepareRelease(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.POST(root+"/:resource/ledger", func(c *echo.Context) error {
		v, e := svc.OpenLedger(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/:resource/ledger", func(c *echo.Context) error {
		v, e := svc.ReadLedger(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.PUT(root+"/:resource/ledger", func(c *echo.Context) error {
		var in service.NovelLedgerDraft
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.SaveLedger(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/bookkeeper", func(c *echo.Context) error {
		var in service.NovelChapterWritingInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := writer.PrepareBookkeeper(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:resource/bookkeeper/:task", func(c *echo.Context) error {
		v, e := writer.LedgerSuggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root, func(c *echo.Context) error {
		var in struct {
			ChapterID string `json:"chapter_id"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Open(c.Request().Context(), actor(c).ID, c.Param("novel"), in.ChapterID)
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
		var in service.NovelChapterDraft
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
			Revision int64 `json:"revision"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Basis(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in.Revision)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/:resource/writer", func(c *echo.Context) error {
		var in service.NovelChapterWritingInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := writer.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.GET(root+"/:resource/writer/:task", func(c *echo.Context) error {
		v, e := writer.Suggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("task"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
}
