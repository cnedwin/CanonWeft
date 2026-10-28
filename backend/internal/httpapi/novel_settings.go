package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func registerNovelSettings(g *echo.Group, svc *service.NovelEntry, actor func(*echo.Context) domain.Actor) {
	const root = "/novels/:novel/settings"
	g.GET(root+"/draft", func(c *echo.Context) error {
		v, e := svc.ReadSettingsDraft(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.PUT(root+"/draft", func(c *echo.Context) error {
		var in service.NovelSettingsDraft
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.SaveSettingsDraft(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/preview", func(c *echo.Context) error {
		var in struct {
			Revision int64 `json:"draft_revision"`
		}
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.PreviewSettings(c.Request().Context(), actor(c).ID, c.Param("novel"), in.Revision)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.POST(root+"/confirm", func(c *echo.Context) error {
		var in service.NovelSettingsConfirmation
		if e := decode(c, &in); e != nil {
			return novelEntryError(c, e)
		}
		v, e := svc.ConfirmSettings(c.Request().Context(), actor(c).ID, c.Param("novel"), in)
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, v)
	})
	g.GET(root+"/versions", func(c *echo.Context) error {
		v, e := svc.SettingsVersions(c.Request().Context(), actor(c).ID, c.Param("novel"))
		if e != nil {
			return novelEntryError(c, e)
		}
		return c.JSON(200, map[string]any{"items": v, "limit": 100})
	})
}
