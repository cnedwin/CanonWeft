package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/labstack/echo/v5"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerNovelImages(g *echo.Group, workspace *service.Workspace, actor func(*echo.Context) domain.Actor) {
	ip := &service.IP{Workspace: workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	gen := &service.Generations{IP: ip}
	svc := &service.NovelImages{Generations: gen}
	const root = "/novels/:novel/images"
	respond := func(c *echo.Context, status int, v any, e error) error {
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(status, v)
	}
	g.GET(root+"/workspace", func(c *echo.Context) error {
		v, e := svc.Workspace(c.Request().Context(), actor(c).ID, c.Param("novel"))
		return respond(c, 200, v, e)
	})
	g.POST(root+"/assistant", func(c *echo.Context) error {
		var in struct{}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.AssistantWorkspace(c.Request().Context(), actor(c).ID, c.Param("novel"))
		return respond(c, 200, v, e)
	})
	g.POST(root+"/assist", func(c *echo.Context) error {
		var in service.VisualAssistInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Assist(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		return respond(c, 201, v, e)
	})
	g.GET(root+"/assist/:task", func(c *echo.Context) error {
		v, e := svc.Suggestion(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("task"))
		return respond(c, 200, v, e)
	})
	g.GET(root+"/generation-options", func(c *echo.Context) error {
		w, e := svc.Workspace(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		v, e := gen.Options(c.Request().Context(), w.Actor, w.Project.ID, true)
		return respond(c, 200, map[string]any{"items": v}, e)
	})
	g.POST(root+"/generate", func(c *echo.Context) error {
		var in service.CharacterImageGenerationInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Prepare(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		return respond(c, 201, v, e)
	})
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.List(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		return respond(c, 200, v, e)
	})
	g.GET(root+"/:image/file", func(c *echo.Context) error {
		v, data, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("image"))
		if e != nil {
			return novelEntryError(c, e)
		}
		mime := ""
		if v.Report.NovelImage != nil {
			mime = v.Report.NovelImage.MIME
		} else if v.Report.CharacterImage != nil {
			mime = v.Report.CharacterImage.MIME
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		return c.Blob(200, mime, data)
	})
	g.POST(root+"/:image/candidate", func(c *echo.Context) error {
		var in struct {
			Head        string `json:"expected_ip_head"`
			OperationID string `json:"operation_id"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Candidate(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("image"), in.Head, in.OperationID)
		return respond(c, 201, v, e)
	})
	g.POST(root, func(c *echo.Context) error {
		if _, e := svc.Workspace(c.Request().Context(), actor(c).ID, c.Param("novel")); e != nil {
			return novelEntryError(c, e)
		}
		var in retrieval.NovelImage
		name, data, e := readVisualUpload(c, &in)
		if e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Import(c.Request().Context(), actor(c).ID, c.Param("novel"), name, c.Request().Header.Get("Idempotency-Key"), in, data)
		return respond(c, 201, v, e)
	})
}

func readVisualUpload(c *echo.Context, metadata any) (string, []byte, error) {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, retrieval.MaxCharacterImageBytes+32*1024)
	reader, err := c.Request().MultipartReader()
	if err != nil {
		return "", nil, domain.ErrValidation
	}
	part, err := reader.NextPart()
	if err != nil {
		return "", nil, domain.ErrValidation
	}
	if part.FormName() != "metadata" {
		part.Close()
		return "", nil, domain.ErrValidation
	}
	raw, err := io.ReadAll(io.LimitReader(part, 16*1024+1))
	part.Close()
	if err != nil || len(raw) > 16*1024 {
		return "", nil, domain.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(metadata) != nil || decoder.Decode(&trailing) != io.EOF {
		return "", nil, domain.ErrValidation
	}
	part, err = reader.NextPart()
	if err != nil {
		return "", nil, domain.ErrValidation
	}
	name := part.FileName()
	if part.FormName() != "file" || name == "" {
		part.Close()
		return "", nil, domain.ErrValidation
	}
	data, err := io.ReadAll(io.LimitReader(part, retrieval.MaxCharacterImageBytes+1))
	part.Close()
	if err != nil || len(data) > retrieval.MaxCharacterImageBytes {
		return "", nil, domain.ErrValidation
	}
	if _, err = reader.NextPart(); err != io.EOF {
		return "", nil, domain.ErrValidation
	}
	return name, data, nil
}
