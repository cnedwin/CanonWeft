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

func registerQualification(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor, runtime *worker.Runtime) {
	svc := &service.Qualifications{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, RuntimeRoot: os.Getenv("STUDIO_ISOLATION_ROOT")}
	root := "/projects/:id/qualification"
	g.GET(root+"/schedule", func(c *echo.Context) error {
		result, err := svc.ReadSchedule(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.PUT(root+"/schedule", func(c *echo.Context) error {
		var input service.QualificationScheduleInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.SaveSchedule(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/suites", func(c *echo.Context) error {
		items, err := svc.Suites(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/baseline-presets", func(c *echo.Context) error {
		var input service.QualificationPresetInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.InstallBaselines(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/quote", func(c *echo.Context) error {
		var input service.QualificationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Quote(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/runs", func(c *echo.Context) error {
		var input service.QualificationInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Create(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(202, result)
	})
	g.GET(root+"/runs", func(c *echo.Context) error {
		items, err := svc.List(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.GET(root+"/runs/:run", func(c *echo.Context) error {
		result, err := svc.Read(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/runs/:run/report", func(c *echo.Context) error {
		result, err := svc.Report(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"))
		if err != nil {
			return respondError(c, err)
		}
		return c.Blob(200, "application/json; charset=utf-8", result)
	})
	g.POST(root+"/runs/:run/control", func(c *echo.Context) error {
		var input struct {
			Action string `json:"action"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Control(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.Action)
		if err != nil {
			return respondError(c, err)
		}
		if runtime != nil {
			_ = runtime.WakeQualification(c.Request().Context(), result.ID)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/runs/:run/approve-executable", func(c *echo.Context) error {
		var input struct {
			ReportHash string `json:"report_hash"`
			Note       string `json:"note"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.ApproveExecutable(c.Request().Context(), actor(c), c.Param("id"), c.Param("run"), input.ReportHash, input.Note)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/bindings", func(c *echo.Context) error {
		items, err := svc.Bindings(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/bindings", func(c *echo.Context) error {
		var input service.BindingInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Activate(c.Request().Context(), actor(c), c.Param("id"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, result)
	})
	g.POST(root+"/bindings/:binding/disable", func(c *echo.Context) error {
		if err := svc.DisableBinding(c.Request().Context(), actor(c), c.Param("id"), c.Param("binding")); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"disabled": true})
	})
	g.POST(root+"/records/:record/revoke-version", func(c *echo.Context) error {
		var input struct {
			Reason string `json:"reason"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		if err := svc.RevokeVersion(c.Request().Context(), actor(c), c.Param("id"), c.Param("record"), input.Reason); err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]bool{"entire_capability_version_revoked": true})
	})
}
