package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerReleases(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	root := "/projects/:id"
	g.GET(root+"/releases/:release/peer-sources", func(c *echo.Context) error {
		result, err := svc.PeerReleaseSources(c.Request().Context(), actor(c), c.Param("id"), c.Param("release"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/releases/composition/preview", func(c *echo.Context) error {
		var input service.ReleaseCompositionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PreviewReleaseComposition(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/releases/composition", func(c *echo.Context) error {
		var input service.ReleaseCompositionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PrepareReleaseComposition(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/releases/joint-publication", func(c *echo.Context) error {
		var input service.JointPublicationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.PublishJoint(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/joint-publications", func(c *echo.Context) error {
		result, err := svc.ListJointPublications(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/joint-publications/:group", func(c *echo.Context) error {
		result, err := svc.ReadJointPublication(c.Request().Context(), actor(c), c.Param("id"), c.Param("group"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/resources/:resource/draft", func(c *echo.Context) error {
		draft, err := svc.ReadResourceDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("resource"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
	g.PUT(root+"/resources/:resource/draft", func(c *echo.Context) error {
		var input service.ResourceDraft
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		draft, err := svc.SaveResourceDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("resource"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
	g.POST(root+"/resources", func(c *echo.Context) error {
		var input service.ResourceInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		resource, err := svc.CreateResource(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, resource)
	})
	g.GET(root+"/resources", func(c *echo.Context) error {
		items, err := svc.Resources(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/resources/:resource", func(c *echo.Context) error {
		resource, err := svc.ReadResource(c.Request().Context(), actor(c), c.Param("id"), c.Param("resource"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, resource)
	})
	g.POST(root+"/releases", func(c *echo.Context) error {
		var input domain.ReleaseInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		release, err := svc.PrepareRelease(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, release)
	})
	g.GET(root+"/releases", func(c *echo.Context) error {
		items, err := svc.Releases(c.Request().Context(), actor(c), c.Param("id"), c.QueryParam("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/releases/:release", func(c *echo.Context) error {
		release, err := svc.ReadRelease(c.Request().Context(), actor(c), c.Param("id"), c.Param("release"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, release)
	})
	g.POST(root+"/releases/:release/adopt", func(c *echo.Context) error {
		release, err := svc.AdoptRelease(c.Request().Context(), actor(c), c.Param("id"), c.Param("release"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, release)
	})
	g.POST(root+"/releases/:release/publish", func(c *echo.Context) error {
		release, err := svc.PublishRelease(c.Request().Context(), actor(c), c.Param("id"), c.Param("release"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, release)
	})
}
