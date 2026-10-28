package httpapi

import (
	"database/sql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"strconv"
)

func registerIP(g *echo.Group, db *sql.DB, actor func(*echo.Context) domain.Actor) {
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	root := "/projects/:id/branches"
	g.POST(root+"/:branch/delta-preview", func(c *echo.Context) error {
		var input struct {
			BaseHead string          `json:"base_head"`
			Snapshot domain.Snapshot `json:"snapshot"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := ip.DeltaPreview(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input.BaseHead, input.Snapshot)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:branch/merge-delta-preview", func(c *echo.Context) error {
		var input service.MergeInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := ip.MergeDeltaPreview(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:branch/promotion-delta-preview", func(c *echo.Context) error {
		var input service.PromotionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		result, err := ip.PromotionDeltaPreview(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, result)
	})
	g.POST(root+"/:branch/promotions", func(c *echo.Context) error {
		var input service.PromotionInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		candidate, err := ip.ProposePromotion(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, candidate)
	})
	g.GET(root+"/:branch/capabilities/:entity/check", func(c *echo.Context) error {
		at, err := strconv.ParseInt(c.QueryParam("time"), 10, 64)
		if err != nil {
			return respondError(c, domain.ErrValidation)
		}
		branch, err := ip.ReadBranch(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, domain.EvaluateCapability(branch.Snapshot, c.Param("entity"), at))
	})
	g.POST(root+"/:branch/amendments", func(c *echo.Context) error {
		var input struct {
			ChangeReasons map[string]string `json:"change_reasons"`
			BaseHead      string            `json:"base_head"`
			OperationID   string            `json:"operation_id"`
			Reason        string            `json:"reason"`
			Snapshot      domain.Snapshot   `json:"snapshot"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		candidate, err := ip.ProposeAmendmentChanges(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input.BaseHead, input.OperationID, input.Reason, input.Snapshot, input.ChangeReasons)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, candidate)
	})
	g.POST(root+"/:branch/candidates/:candidate/approve-amendment", func(c *echo.Context) error {
		candidate, err := ip.ApproveAmendment(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("candidate"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, candidate)
	})
	g.GET(root, func(c *echo.Context) error {
		items, err := ip.Branches(c.Request().Context(), actor(c), c.Param("id"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root, func(c *echo.Context) error {
		var input struct {
			Name         string `json:"name"`
			ParentID     string `json:"parent_id"`
			BaseRevision string `json:"base_revision"`
			OperationID  string `json:"operation_id"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		b, err := ip.CreateBranch(c.Request().Context(), actor(c), c.Param("id"), input.Name, input.ParentID, input.BaseRevision, input.OperationID)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, b)
	})
	g.GET(root+"/:branch", func(c *echo.Context) error {
		b, err := ip.ReadBranch(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, b)
	})
	g.GET(root+"/:branch/candidates", func(c *echo.Context) error {
		items, err := ip.Candidates(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, map[string]any{"items": items})
	})
	g.POST(root+"/:branch/candidates", func(c *echo.Context) error {
		var input struct {
			ChangeReasons map[string]string `json:"change_reasons"`
			BaseHead      string            `json:"base_head"`
			OperationID   string            `json:"operation_id"`
			Snapshot      domain.Snapshot   `json:"snapshot"`
		}
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		candidate, err := ip.ProposeChanges(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input.BaseHead, input.OperationID, input.Snapshot, input.ChangeReasons)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, candidate)
	})
	g.POST(root+"/:branch/candidates/:candidate/adopt", func(c *echo.Context) error {
		candidate, err := ip.Adopt(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("candidate"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, candidate)
	})
	g.POST(root+"/:branch/candidates/:candidate/publish", func(c *echo.Context) error {
		candidate, err := ip.Publish(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), c.Param("candidate"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, candidate)
	})
	g.GET(root+"/:branch/merge-preview", func(c *echo.Context) error {
		view, err := ip.MergePreview(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, view)
	})
	g.POST(root+"/:branch/merge", func(c *echo.Context) error {
		var input service.MergeInput
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		candidate, err := ip.ProposeMerge(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(201, candidate)
	})
	g.GET(root+"/:branch/draft", func(c *echo.Context) error {
		draft, err := ip.ReadDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"))
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
	g.PUT(root+"/:branch/draft", func(c *echo.Context) error {
		var input service.IPDraft
		if err := decode(c, &input); err != nil {
			return respondError(c, err)
		}
		draft, err := ip.SaveDraft(c.Request().Context(), actor(c), c.Param("id"), c.Param("branch"), input)
		if err != nil {
			return respondError(c, err)
		}
		return c.JSON(200, draft)
	})
}
