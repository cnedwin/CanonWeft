package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelExport(g *echo.Group, ip *service.IP, actor func(*echo.Context) domain.Actor) {
	svc := &service.NovelExport{IP: ip}
	g.GET("/novels/:novel/export/editions", func(c *echo.Context) error {
		v, e := svc.Editions(c.Request().Context(), actor(c).ID, c.Param("novel"), c.QueryParam("after"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.POST("/novels/:novel/export/editions", func(c *echo.Context) error {
		var in service.NovelEditionInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.SaveEdition(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(201, v)
	})
	g.POST("/novels/:novel/export/editions/:edition/confirm", func(c *echo.Context) error {
		var in struct {
			Hash   string `json:"hash"`
			Accept bool   `json:"accept"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.ConfirmEdition(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("edition"), in.Hash, in.Accept)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET("/novels/:novel/export/editions/:edition/cover", func(c *echo.Context) error {
		raw, mime, e := svc.EditionCover(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("edition"))
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		return c.Blob(200, mime, raw)
	})
	g.POST("/novels/:novel/export/preview", func(c *echo.Context) error {
		var in service.NovelExportInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.Preview(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(200, v)
	})
	g.POST("/novels/:novel/export/download", func(c *echo.Context) error {
		var in service.NovelExportInput
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		raw, mime, e := svc.Download(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		c.Response().Header().Set("Content-Disposition", `attachment; filename="novel.`+in.Format+`"`)
		return c.Blob(200, mime, raw)
	})
}
