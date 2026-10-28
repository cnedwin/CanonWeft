package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

type MergeInput = domain.MergeInput
type MergeView struct {
	BaseHead     string                 `json:"base_head"`
	UpstreamHead string                 `json:"upstream_head"`
	Snapshot     domain.Snapshot        `json:"snapshot"`
	Conflicts    []domain.MergeConflict `json:"conflicts"`
	Base         domain.Snapshot        `json:"base"`
	Upstream     domain.Snapshot        `json:"upstream"`
	Local        domain.Snapshot        `json:"local"`
}

func mergeViewTx(ctx context.Context, tx *sql.Tx, project string, b Branch) (MergeView, error) {
	view := MergeView{BaseHead: b.Head}
	if b.ParentID == "" {
		return view, domain.ErrValidation
	}
	parent, err := branchTx(ctx, tx, project, b.ParentID)
	if err != nil {
		return view, err
	}
	view.UpstreamHead = parent.Head
	view.Base, err = snapshotTx(ctx, tx, project, b.BaseRevision)
	if err != nil {
		return view, err
	}
	view.Upstream, err = snapshotTx(ctx, tx, project, parent.Head)
	if err != nil {
		return view, err
	}
	view.Local, err = snapshotTx(ctx, tx, project, b.Head)
	if err != nil {
		return view, err
	}
	view.Snapshot, view.Conflicts = domain.MergeSnapshots(view.Base, view.Upstream, view.Local)
	return view, nil
}
func (s *IP) ProposeMerge(ctx context.Context, a domain.Actor, project, branch string, input MergeInput) (Candidate, error) {
	if input.ChangeReasons == nil {
		return Candidate{}, domain.ErrValidation
	}
	return s.propose(ctx, a, project, branch, input.BaseHead, input.OperationID, domain.Snapshot{}, &input, "", nil, input.ChangeReasons)
}

func (s *IP) MergeDeltaPreview(ctx context.Context, a domain.Actor, project, branch string, input MergeInput) (domain.BranchDelta, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	if b.Head != input.BaseHead {
		return domain.BranchDelta{}, domain.ErrConflict
	}
	view, err := mergeViewTx(ctx, tx, project, b)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	if view.UpstreamHead != input.UpstreamHead {
		return domain.BranchDelta{}, domain.ErrConflict
	}
	next, err := domain.ResolveMerge(view.Base, view.Upstream, view.Local, input.Resolutions)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	normalizeDeltaMetadata(view.Local, &next)
	return domain.PreviewBranchDelta(view.Local, next)
}
