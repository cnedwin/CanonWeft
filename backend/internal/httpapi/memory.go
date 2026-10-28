package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
)

func registerMemory(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	svc := &service.Memory{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Cache: memory.NewLocalCache(128, 16<<20)}
	root := "/projects/:id/branches/:branch/memory"
	g.GET(root+"/items/:item/revisions/:revision", func(c *echo.Context) error {
		result, err := svc.ReadRevision(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("item"), c.Param("revision"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/import-extraction", func(c *echo.Context) error {
		var input service.MemoryImportInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.ImportExtraction(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": result})
	})
	g.POST(root+"/extraction-input", func(c *echo.Context) error {
		var input service.ExtractionSourceInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.ExtractionInput(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root, func(c *echo.Context) error {
		result, err := svc.List(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/sessions", func(c *echo.Context) error {
		var input service.MemorySessionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.SaveSession(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.GET(root+"/sessions/:session", func(c *echo.Context) error {
		result, err := svc.ReadSession(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("session"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/items", func(c *echo.Context) error {
		var input service.MemoryInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Save(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	for _, action := range []string{"adopt", "revoke"} {
		g.POST(root+"/items/:item/"+action, func(c *echo.Context) error {
			var input service.MemoryAction
			if err := decode(c, &input); err != nil {
				return respondError(c, err)
			}
			result, err := svc.Transition(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("item"), action, input)
			if err != nil {
				return respondError(c, err)
			}
			return c.JSON(200, result)
		})
	}
	g.POST(root+"/compile", func(c *echo.Context) error {
		var input service.MemoryCompileInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := svc.Compile(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
}
