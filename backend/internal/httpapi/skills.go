package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/skills"
	"novelstudio/internal/storage"
	"os"
)

func registerSkills(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.Skills{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	root := "/projects/:id/skills"
	g.GET(root+"/presets", func(c *echo.Context) error {
		items, err := svc.Presets(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/presets/:preset", func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.InstallPreset(c.Request().Context(), actor(c), c.Param("id"), c.Param("preset"), input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root, func(c *echo.Context) error {
		var input struct {
			OperationID string `json:"operation_id"`
			Markdown    string `json:"markdown"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Create(c.Request().Context(), actor(c), c.Param("id"), input.OperationID, input.Markdown)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/fetch", func(c *echo.Context) error {
		var input service.SkillFetch
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Fetch(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/import", func(c *echo.Context) error {
		request := c.Request()
		request.Body = http.MaxBytesReader(c.Response(), request.Body, skills.MaxBundleBytes+65536)
		if err := request.ParseMultipartForm(skills.MaxBundleBytes + 65536); err != nil {
			return respondError(c, domain.ErrValidation)
		}
		defer request.MultipartForm.RemoveAll()
		file, _, err := request.FormFile("file")
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, skills.MaxBundleBytes+1))
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		result, err := svc.Import(request.Context(), actor(c), c.Param("id"), request.FormValue("operation_id"), raw)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/merge-preview", func(c *echo.Context) error {
		var input service.SkillMerge
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PreviewMerge(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/merge", func(c *echo.Context) error {
		var input service.SkillMerge
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Merge(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/:skill", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("skill"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/:skill/files", func(c *echo.Context) error {
		result, err := svc.File(c.Request().Context(), actor(c), c.Param("id"), c.Param("skill"), c.QueryParam("path"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:skill/edit", func(c *echo.Context) error {
		var input service.SkillEdit
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Edit(c.Request().Context(), actor(c), c.Param("id"), c.Param("skill"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/:skill/reference", func(c *echo.Context) error {
		result, err := svc.Reference(c.Request().Context(), actor(c), c.Param("id"), c.Param("skill"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:skill/revoke", func(c *echo.Context) error {
		var input struct {
			Reason string `json:"reason"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.Revoke(c.Request().Context(), actor(c), c.Param("id"), c.Param("skill"), input.Reason); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"revoked": true})
	})
}
