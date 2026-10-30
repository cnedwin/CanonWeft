package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelChapterHistory(g *echo.Group, svc *service.NovelChapters, actor func(*echo.Context) domain.Actor) {
	const root = "/novels/:novel/chapters/:resource/history"
	reply := func(c *echo.Context, v any, e error, status int) error {
		c.Response().Header().Set("Cache-Control", "private, no-store")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(status, v)
	}
	g.GET(root, func(c *echo.Context) error {
		v, e := svc.History(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.QueryParam("after"), c.QueryParam("operation_id"))
		return reply(c, v, e, 200)
	})
	g.GET(root+"/:history", func(c *echo.Context) error {
		v, e := svc.HistoryText(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), c.Param("history"))
		return reply(c, v, e, 200)
	})
	g.POST(root, func(c *echo.Context) error {
		var in service.ChapterHistoryInput
		if e := decode(c, &in); e != nil {
			return reply(c, nil, e, 400)
		}
		v, e := svc.Checkpoint(c.Request().Context(), actor(c).ID, c.Param("novel"), c.Param("resource"), in)
		return reply(c, v, e, 201)
	})
}
