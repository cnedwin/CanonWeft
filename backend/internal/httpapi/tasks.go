package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
	"os"
)

func registerTasks(group *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.Tasks{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	root := "/projects/:id/tasks"
	group.GET(root, func(c *echo.Context) error {
		tasks, err := svc.List(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": tasks, "runtime_ready": runtime != nil})
	})
	group.POST(root, func(c *echo.Context) error {
		if runtime == nil {
			return respondError(c, domain.ErrNotReady)
		}
		var input struct {
			BranchID    string `json:"branch_id"`
			CandidateID string `json:"candidate_id"`
			OperationID string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		task, err := svc.StartReview(c.Request().Context(), actor(c), c.Param("id"), input.BranchID, input.CandidateID, input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, task)
	})
	group.POST(root+"/:task/control", func(c *echo.Context) error {
		var input struct {
			Action   string `json:"action"`
			Revision int64  `json:"revision"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		task, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("task"), input.Action, input.Revision)
		if err != nil {
			return respondError(c, err)
		}
		if runtime != nil {
			_ = runtime.Wake(c.Request().Context(), task.ID)
		}
		return c.JSON(200, task)
	})
}
