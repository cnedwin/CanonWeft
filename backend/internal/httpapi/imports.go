package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"io"
	"mime"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerImports(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, providers ...service.ResearchProvider) {
	svc := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	root := "/projects/:id/branches/:branch/imports"
	jobs := &service.IngestJobs{IP: svc}
	registerResearchSources(g, svc, actor, providers...)
	original := func(pending bool) echo.HandlerFunc {
		return func(c *echo.Context) error {
			var name string
			var data []byte
			var err error
			if pending {
				name, data, err = jobs.ReadOriginal(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("job"))
			} else {
				name, data, err = svc.ReadImportFile(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("import"))
			}
			if err != nil {
				return respondError(c, err)
			}
			c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
			c.Response().Header().Set("Cache-Control", "private, no-store")
			c.Response().Header().Set("X-Content-Type-Options", "nosniff")
			return c.Blob(200, "application/octet-stream", data)
		}
	}
	g.GET(root+"/jobs/:job/file", original(true))
	g.GET(root+"/:import/file", original(false))
	upload := func(background bool) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Authorize before parsing any supplied file, then recheck in the service.
			a := actor(c)
			p, err := svc.Workspace.Project(c.Request().Context(), a, c.Param("id"))
			if err != nil {
				return respondError(c, err)
			}
			if !domain.CanEdit(a.Role) || !domain.CanEdit(p.Role) {
				return respondError(c, domain.ErrForbidden)
			}
			c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 33*1024*1024)
			reader, err := c.Request().MultipartReader()
			if err != nil {
				return respondError(c, domain.ErrValidation)
			}
			part, err := reader.NextPart()
			if err != nil {
				return respondError(c, domain.ErrValidation)
			}
			defer part.Close()
			if part.FormName() != "file" || part.FileName() == "" {
				return respondError(c, domain.ErrValidation)
			}
			name := part.FileName()
			data, err := io.ReadAll(io.LimitReader(part, 32*1024*1024+1))
			if err != nil || len(data) > 32*1024*1024 {
				return respondError(c, domain.ErrValidation)
			}
			if _, err = reader.NextPart(); err != io.EOF {
				return respondError(c, domain.ErrValidation)
			}
			if background {
				job, err := jobs.Create(c.Request().Context(), a, c.Param("id"), c.Param("branch"), name, c.Request().Header.Get("Idempotency-Key"), data)
				if err != nil {
					return respondError(c, err)
				}
				return c.JSON(202, job)
			}
			result, err := svc.ImportSource(c.Request().Context(), a, c.Param("id"), c.Param("branch"), name, c.Request().Header.Get("Idempotency-Key"), data)
			if err != nil {
				return respondError(c, err)
			}
			return c.JSON(201, result)
		}
	}
	g.POST(root, upload(false))
	g.POST(root+"/jobs", upload(true))
	g.GET(root+"/jobs", func(c *echo.Context) error {
		items, err := jobs.List(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/jobs/:job", func(c *echo.Context) error {
		job, err := jobs.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("job"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, job)
	})
	g.POST(root+"/jobs/:job/control", func(c *echo.Context) error {
		var input struct {
			Action   string `json:"action"`
			Revision int64  `json:"revision"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		job, err := jobs.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("job"), input.Action, input.Revision)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, job)
	})
	g.GET(root+"/:import", func(c *echo.Context) error {
		result, err := svc.ReadImport(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("import"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/:import/draft", func(c *echo.Context) error {
		d, err := svc.ReadImportDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("import"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, d)
	})
	g.PUT(root+"/:import/draft", func(c *echo.Context) error {
		var input service.ImportDraft
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		d, err := svc.SaveImportDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("import"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, d)
	})
	g.POST(root+"/:import/candidate", func(c *echo.Context) error {
		var input service.ImportCandidateInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.ImportCandidate(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("import"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
}
