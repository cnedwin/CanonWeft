package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/skills"
)

func (s *SkillDiscovery) Execute(ctx context.Context, id string) (string, error) {
	r, err := s.readRawRun(ctx, id)
	if err != nil {
		return "", err
	}
	if r.State == "PAUSED" {
		// Pausing execution must not pause revocation checks. No downloaded
		// content is used while paused, but the durable state remains truthful.
		permission, _, _, permissionErr := s.IP.begin(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, true)
		if permissionErr == nil {
			permission.Rollback()
			return "PAUSED", nil
		}
		if !errors.Is(permissionErr, domain.ErrForbidden) && !errors.Is(permissionErr, domain.ErrUnauthorized) {
			return "PAUSED", permissionErr
		}
		return s.finishStates(ctx, id, "BLOCKED", "暂停期间权限已撤销，请恢复权限后重新发现", "PAUSED")
	}
	if r.State != "QUEUED" && r.State != "RUNNING" {
		return r.State, nil
	}
	tx, r, err := s.guard(ctx, r)
	if err != nil {
		return s.terminalError(ctx, r, err)
	}
	err = updateDiscoveryStateTx(ctx, tx, r.ProjectID, id, "RUNNING", "")
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	remote := s.Remote
	if remote == nil {
		remote = skills.NewFetcher(nil)
	}
	if r.Inventory == nil {
		inventory, fetchErr := remote.Discover(ctx, skills.RemoteRequest{Repository: r.Source.Repository, Ref: r.Source.Ref, Path: r.Source.Path}, r.Source.Keyword, 1000)
		if fetchErr != nil {
			return "RUNNING", fetchErr
		}
		tx, r, err = s.guard(ctx, r)
		if err != nil {
			return s.terminalError(ctx, r, err)
		}
		if r.Inventory != nil {
			// A replacement pinned its inventory while this activity was reading
			// the remote tree. Never replace that immutable commit or progress.
			tx.Rollback()
			return "RUNNING", ErrDiscoverySuperseded
		}
		// Skip complete receipts from earlier runs so a limited batch progresses
		// through the allowed directory rather than fetching the first entries forever.
		selected := []string{}
		for _, path := range inventory.Paths {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_discovery_candidates WHERE source_id=? AND source_path IN (?,?) AND source_commit=?", r.SourceID, path, "~erased:"+digest(path), inventory.Commit).Scan(&count); err != nil {
				tx.Rollback()
				return "", err
			}
			if count == 0 {
				selected = append(selected, path)
			}
			if len(selected) == r.Source.Limit {
				break
			}
		}
		inventory.Paths = selected
		raw, _ := json.Marshal(inventory)
		if _, err = tx.ExecContext(ctx, "UPDATE skill_discovery_runs SET inventory=?,total_count=? WHERE id=?", raw, len(selected), id); err != nil {
			tx.Rollback()
			return "", err
		}
		if err = s.IP.captureDiscoverySourceTx(ctx, tx, r.ProjectID, "skill_discovery_runs", id); err != nil {
			tx.Rollback()
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		r.Inventory = &inventory
		r.Total = len(selected)
	}
	for r.Done < len(r.Inventory.Paths) {
		// Recheck before each network fetch; no transaction spans public GETs.
		tx, current, e := s.guard(ctx, r)
		if e != nil {
			return s.terminalError(ctx, r, e)
		}
		tx.Rollback()
		r = current
		if r.Done >= len(r.Inventory.Paths) {
			break
		}
		path := r.Inventory.Paths[r.Done]
		bundle, source, e := remote.Fetch(ctx, skills.RemoteRequest{Repository: r.Source.Repository, Ref: r.Inventory.Commit, Path: path})
		if e != nil {
			return "RUNNING", e
		}
		if source.Commit != r.Inventory.Commit || source.Repository != r.Source.Repository || source.Path != path {
			return "RUNNING", skills.ErrFetch
		}
		if e = s.saveCandidate(ctx, r, bundle, source); e != nil {
			return s.terminalError(ctx, r, e)
		}
		r.Done++
		if s.Heartbeat != nil {
			s.Heartbeat(ctx)
		}
	}
	tx, r, err = s.guard(ctx, r)
	if err != nil {
		return s.terminalError(ctx, r, err)
	}
	err = updateDiscoveryStateTx(ctx, tx, r.ProjectID, id, "COMPLETED", "完整候选及文件哈希已入库；仅静态分析，模型、保留集和执行资格未评测")
	if err != nil {
		tx.Rollback()
		return "", err
	}
	return "COMPLETED", tx.Commit()
}

func (s *SkillDiscovery) saveCandidate(ctx context.Context, r DiscoveryRun, b skills.Bundle, source skills.RemoteSource) error {
	report, err := skills.Analyze(b)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(b)
	tx, current, err := s.guard(ctx, r)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A timed-out activity may finish after a replacement. Both share this
	// progress fence, and the unique source/commit/path receipt is a second fence.
	if current.Done > r.Done {
		return ErrDiscoverySuperseded
	}
	if current.Done != r.Done {
		return domain.ErrConflict
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT package_id FROM skill_discovery_candidates WHERE source_id=? AND source_path IN (?,?) AND source_commit=?", r.SourceID, source.Path, "~erased:"+digest(source.Path), source.Commit).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		object, err := objectwriter.Write(ctx, tx, s.IP.Blobs, r.ProjectID, raw)
		if err != nil {
			return err
		}
		if object.Hash != report.Hash {
			return domain.ErrNotReady
		}
		previous := ""
		err = tx.QueryRowContext(ctx, "SELECT package_id FROM skill_discovery_candidates WHERE source_id=? AND source_path IN (?,?) ORDER BY created_at DESC,id DESC LIMIT 1", r.SourceID, source.Path, "~erased:"+digest(source.Path)).Scan(&previous)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		packageID := NewID()
		reportRaw, _ := json.Marshal(report)
		sourceRaw, _ := json.Marshal(SkillSource{Kind: "github", Repository: source.Repository, Commit: source.Commit, Path: source.Path, URL: source.URL})
		// Upstream versions are separate candidates; old local derivations remain immutable.
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_packages(id,project_id,parent_id,upstream_id,bundle_hash,report,source,created_by) VALUES(?,?,'','',?,?,?,?)", packageID, r.ProjectID, report.Hash, reportRaw, sourceRaw, r.ActorID); err != nil {
			return err
		}
		candidateID := NewID()
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_discovery_candidates(id,project_id,source_id,run_id,source_path,source_commit,package_id,previous_id) VALUES(?,?,?,?,?,?,?,?)", candidateID, r.ProjectID, r.SourceID, r.ID, source.Path, source.Commit, packageID, previous); err != nil {
			return err
		}
		if err = s.IP.captureDiscoverySourceTx(ctx, tx, r.ProjectID, "skill_discovery_candidates", candidateID); err != nil {
			return err
		}
		actor, actorErr := s.IP.Workspace.actorTx(ctx, tx, r.ActorID)
		if actorErr != nil {
			return actorErr
		}
		if err = audit(ctx, tx, actor, "discovery.candidate", packageID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE skill_discovery_runs SET done_count=done_count+1 WHERE id=?", r.ID); err != nil {
		return err
	}
	return tx.Commit()
}
