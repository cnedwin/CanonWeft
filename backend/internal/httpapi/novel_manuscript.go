package httpapi

import (
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelManuscripts(g *echo.Group, ip *service.IP, actor func(*echo.Context) domain.Actor) {
	svc := &service.NovelManuscripts{IP: ip}
	const root = "/novels/:novel/manuscripts"
	reply := func(c *echo.Context, v any, err error, status int) error {
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		if err != nil {
			return novelEntryError(c, err)
		}
		return c.JSON(status, v)
	}
	g.POST(root+"/sources", func(c *echo.Context) error {
		if err := svc.Authorize(c.Request().Context(), actor(c).ID, c.Param("novel")); err != nil {
			return reply(c, nil, err, 400)
		}
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 33<<20)
		reader, err := c.Request().MultipartReader()
		if err != nil {
			return reply(c, nil, domain.ErrValidation, 400)
		}
		part, err := reader.NextPart()
		if err != nil {
			return reply(c, nil, domain.ErrValidation, 400)
		}
		defer part.Close()
		if part.FormName() != "file" || part.FileName() == "" {
			return reply(c, nil, domain.ErrValidation, 400)
		}
		name := part.FileName()
		data, err := io.ReadAll(io.LimitReader(part, 32<<20+1))
		if err != nil || len(data) > 32<<20 {
			return reply(c, nil, domain.ErrValidation, 400)
		}
		if _, err = reader.NextPart(); err != io.EOF {
			return reply(c, nil, domain.ErrValidation, 400)
		}
		v, err := svc.Upload(c.Request().Context(), actor(c).ID, c.Param("novel"), name, c.Request().Header.Get("Idempotency-Key"), data)
		return reply(c, v, err, 201)
	})
	g.GET(root+"/sources", func(c *echo.Context) error {
		v, e := svc.Sources(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		return reply(c, map[string]any{"items": v}, e, 200)
	})
	g.GET(root+"/sources/receipt", func(c *echo.Context) error {
		v, e := svc.SourceReceipt(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("operation_id"))
		return reply(c, map[string]any{"source": v}, e, 200)
	})
	g.POST(root+"/sources/:source/reparse", func(c *echo.Context) error {
		var in struct {
			OperationID string `json:"operation_id"`
		}
		if e := decode(c, &in); e != nil {
			return reply(c, nil, e, 400)
		}
		v, e := svc.Reparse(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("source"), in.OperationID)
		return reply(c, v, e, 201)
	})
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.List(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		return reply(c, map[string]any{"items": v}, e, 200)
	})
	g.GET(root+"/by-source/:source", func(c *echo.Context) error {
		v, e := svc.BySource(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("source"))
		return reply(c, map[string]any{"batch": v}, e, 200)
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.ManuscriptCreate
		if e := decode(c, &in); e != nil {
			return reply(c, nil, e, 400)
		}
		v, e := svc.Create(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		return reply(c, v, e, 201)
	})
	g.GET(root+"/:batch", func(c *echo.Context) error {
		v, e := svc.Read(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("batch"))
		return reply(c, v, e, 200)
	})
	g.PUT(root+"/:batch", func(c *echo.Context) error {
		var in service.ManuscriptLayout
		if e := decode(c, &in); e != nil {
			return reply(c, nil, e, 400)
		}
		v, e := svc.Save(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("batch"), in)
		return reply(c, v, e, 200)
	})
	g.GET(root+"/:batch/parts/:part", func(c *echo.Context) error {
		offset := 0
		var e error
		if c.QueryParam("offset") != "" {
			offset, e = strconv.Atoi(c.QueryParam("offset"))
			if e != nil {
				return reply(c, nil, domain.ErrValidation, 400)
			}
		}
		v, e := svc.Preview(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("batch"), c.Param("part"), offset)
		return reply(c, v, e, 200)
	})
	g.POST(root+"/:batch/parts/:part/apply", func(c *echo.Context) error {
		var in service.ManuscriptApply
		if e := decode(c, &in); e != nil {
			return reply(c, nil, e, 400)
		}
		v, e := svc.Apply(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("batch"), c.Param("part"), in)
		return reply(c, v, e, 200)
	})
}
