package retention

import (
	"context"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

func (b *contextSourceBuilder) changePrompt(ctx context.Context, id, role, baseHead string, request models.Request, read ReadSourceObject) error {
	if id == "" {
		return nil
	}
	if role != "critic" && role != "continuity_editor" {
		return ErrSourceOwnership
	}
	row, err := b.index.sourceRow("change_plans", map[string]string{"id": id})
	var plan domain.ChangePlan
	if err != nil || row.Values["project_id"] != b.project || contentJSON([]byte(row.Values["payload"]), &plan, row.Values["content_hash"]) != nil || plan.ID != id || plan.BranchID != row.Values["branch_id"] || plan.Stale || plan.SourceState == "SUPERSEDED" {
		return ErrSourceOwnership
	}
	const open = "\n\n<proposed-change-data>\n"
	const close = "\n</proposed-change-data>"
	start := strings.LastIndex(request.Prompt, open)
	if start < 0 {
		return ErrSourceOwnership
	}
	start += len(open)
	end := strings.Index(request.Prompt[start:], close)
	if end < 0 {
		return ErrSourceOwnership
	}
	segment := []byte(request.Prompt[start : start+end])
	var copied domain.ChangeReviewContext
	if contentJSON(segment, &copied, financialDigest(segment)) != nil || stableHash(copied.Plan) != row.Values["content_hash"] {
		return ErrSourceOwnership
	}
	if plan.BranchID == b.branch {
		if plan.BaseHead != baseHead || copied.FocusBranchID != "" || copied.FocusBaseHead != "" {
			return ErrSourceOwnership
		}
	} else {
		allowed := false
		for _, item := range plan.Items {
			if item.Node.Kind == "resource" && item.Node.BranchID == b.branch {
				allowed = true
			}
		}
		if !allowed || plan.SourceEventID == "" || copied.FocusBranchID != b.branch || copied.FocusBaseHead != baseHead {
			return ErrSourceOwnership
		}
	}
	items := []domain.ImpactItem{}
	if len(copied.FocusTargets) > 0 {
		if role != "critic" {
			return ErrSourceOwnership
		}
		refs, focusErr := domain.ChangeInspectionFocus(copied)
		if focusErr != nil {
			return ErrSourceOwnership
		}
		for _, ref := range refs {
			items = append(items, domain.ImpactItem{Node: ref})
		}
		snapshot, snapshotErr := b.ip(baseHead, b.branch)
		if snapshotErr != nil {
			return snapshotErr
		}
		if baseHead == "" {
			snapshot = domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
		}
		doc, docErr := domain.BuildChangeInspectionDocument(copied, snapshot)
		_, actual, parseErr := domain.ParseChangeInspectionInput(request.Prompt)
		if docErr != nil || parseErr != nil || stableHash(doc) != stableHash(actual) {
			return ErrSourceOwnership
		}
	} else {
		for _, item := range plan.Items {
			if item.Node.Kind == "resource" {
				items = append(items, item)
			}
		}
	}
	if len(items) != len(copied.Targets) {
		return ErrSourceOwnership
	}
	for i, target := range copied.Targets {
		// Older requests omit this redundant body anchor; newly frozen anchors
		// must agree with the immutable text already checked below.
		if target.BodyHash != "" && target.BodyHash != financialDigest([]byte(target.Text)) {
			return ErrSourceOwnership
		}
		if target.Ref != items[i].Node {
			return ErrSourceOwnership
		}
		node := struct {
			Hash         string
			Policy       *domain.KnowledgePolicy
			Dependencies *domain.DependencyPolicy
		}{financialDigest([]byte(target.Text)), target.Policy, target.Dependencies}
		if stableHash(node) != target.Ref.Hash {
			return ErrSourceOwnership
		}
		space := "evidence"
		if target.Policy != nil {
			space = target.Policy.Space
		}
		if err := b.required(ctx, domain.RequiredInput{Dependency: domain.Dependency{Source: target.Ref}, Name: target.Name, Text: target.Text, Space: space}, read); err != nil {
			return err
		}
	}
	return b.link(row, "payload", "")
}
