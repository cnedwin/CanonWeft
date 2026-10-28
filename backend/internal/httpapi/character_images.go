package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"strings"

	"github.com/labstack/echo/v5"
)

func registerCharacterImages(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	root := "/projects/:id/branches/:branch/character-images"
	g.POST(root+"/assistant", func(c *echo.Context) error {
		var input struct{}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := (&service.Generations{IP: svc}).VisualAssistantWorkspace(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/assist", func(c *echo.Context) error {
		var input service.VisualAssistInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := (&service.Generations{IP: svc}).PrepareVisualAssistant(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/assist/:task", func(c *echo.Context) error {
		result, err := (&service.Generations{IP: svc}).VisualSuggestion(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("task"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/generate", func(c *echo.Context) error {
		var input service.CharacterImageGenerationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := (&service.Generations{IP: svc}).PrepareCharacterImageGeneration(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/generation-options", func(c *echo.Context) error {
		items, err := (&service.Generations{IP: svc}).Options(c.Request().Context(), actor(c), c.Param("id"), true)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items, "requested_model": "GPT Image 2.1", "availability": "unverified"})
	})
	g.GET(root+"/portrait/:character", func(c *echo.Context) error {
		id, err := svc.CharacterPortrait(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("character"))
		if err != nil {
			return respondError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		if id == "" {
			return c.NoContent(204)
		}
		item, data, err := svc.ReadCharacterImage(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), id)
		if err != nil {
			return respondError(c, err)
		}
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		return c.Blob(200, item.Report.CharacterImage.MIME, data)
	})
	g.GET(root, func(c *echo.Context) error {
		result, err := svc.CharacterImages(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.QueryParam("character"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root, func(c *echo.Context) error {
		a := actor(c)
		p, err := svc.Workspace.Project(c.Request().Context(), a, c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		if !domain.CanEdit(a.Role) || !domain.CanEdit(p.Role) {
			return respondError(c, domain.ErrForbidden)
		}
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, retrieval.MaxCharacterImageBytes+32*1024)
		reader, err := c.Request().MultipartReader()
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		part, err := reader.NextPart()
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		if part.FormName() != "metadata" {
			part.Close()
			return respondError(c, domain.ErrValidation)
		}
		raw, readErr := io.ReadAll(io.LimitReader(part, 16*1024+1))
		part.Close()
		if readErr != nil || len(raw) > 16*1024 {
			return respondError(c, domain.ErrValidation)
		}
		var input retrieval.CharacterImage
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			return respondError(c, domain.ErrValidation)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return respondError(c, domain.ErrValidation)
		}
		part, err = reader.NextPart()
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			return respondError(c, domain.ErrValidation)
		}
		name := part.FileName()
		data, readErr := io.ReadAll(io.LimitReader(part, retrieval.MaxCharacterImageBytes+1))
		part.Close()
		if readErr != nil || len(data) > retrieval.MaxCharacterImageBytes {
			return respondError(c, domain.ErrValidation)
		}
		if _, err = reader.NextPart(); err != io.EOF {
			return respondError(c, domain.ErrValidation)
		}
		result, err := svc.ImportCharacterImage(c.Request().Context(), a, c.Param("id"), c.Param("branch"), name, c.Request().Header.Get("Idempotency-Key"), input, data)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.GET(root+"/:image/file", func(c *echo.Context) error {
		item, data, err := svc.ReadCharacterImage(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("image"))
		if err != nil {
			return respondError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		c.Response().Header().Set("Content-Disposition", "inline")
		return c.Blob(200, item.Report.CharacterImage.MIME, data)
	})
	g.POST(root+"/:image/candidate", func(c *echo.Context) error {
		var input service.CharacterImageCandidateInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PrepareCharacterImage(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("image"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
}
