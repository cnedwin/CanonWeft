package httpapi

import (
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerProjectDeletion(group *echo.Group, svc *service.Workspace, actor func(*echo.Context) domain.Actor) {
	group.POST("/projects/:id/recover", func(c *echo.Context) error {
		var input service.ProjectRecoveryInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		ip := &service.IP{Workspace: svc, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
		project, err := ip.RecoverProject(c.Request().Context(), actor(c).ID, c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, project)
	})
	group.GET("/operations/recycle-bin", func(c *echo.Context) error {
		page, err := svc.RecycleProjects(c.Request().Context(), actor(c).ID, c.QueryParam("after"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, page)
	})
	group.DELETE("/projects/:id", func(c *echo.Context) error {
		var input struct {
			Confirmation string `json:"confirm_name"`
			Revision     int64  `json:"revision"`
			OperationID  string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.DeleteProject(c.Request().Context(), actor(c).ID, c.Param("id"), input.Confirmation, input.Revision, input.OperationID); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"access_revoked": true, "physical_purge_completed": false})
	})
}
