package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/skills"
)

// DiscoveryRemote only reads public data. It cannot invoke models or execute code.
type DiscoveryRemote interface {
	Discover(context.Context, skills.RemoteRequest, string, int) (skills.DiscoveryInventory, error)
	Fetch(context.Context, skills.RemoteRequest) (skills.Bundle, skills.RemoteSource, error)
}
type SkillDiscovery struct {
	IP        *IP
	Remote    DiscoveryRemote
	Heartbeat func(context.Context)
}
type DiscoverySourceInput struct {
	OperationID      string `json:"operation_id"`
	Repository       string `json:"repository"`
	Ref              string `json:"ref"`
	Path             string `json:"path"`
	Keyword          string `json:"keyword"`
	Enabled          bool   `json:"enabled"`
	Limit            int    `json:"limit"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type DiscoverySource struct {
	ID         string `json:"id"`
	ProjectID  string `json:"-"`
	ActorID    string `json:"-"`
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Path       string `json:"path"`
	Keyword    string `json:"keyword"`
	Enabled    bool   `json:"enabled"`
	Limit      int    `json:"limit"`
	Revision   int64  `json:"revision"`
}
type DiscoveryRun struct {
	Retention        *RuntimeRetention `json:"retention,omitempty"`
	expiredInventory []byte
	expiredSnapshot  bool
	ID               string                     `json:"id"`
	ProjectID        string                     `json:"-"`
	SourceID         string                     `json:"source_id"`
	ActorID          string                     `json:"-"`
	SourceRevision   int64                      `json:"source_revision"`
	Source           DiscoverySource            `json:"source"`
	State            string                     `json:"state"`
	Reason           string                     `json:"reason"`
	Inventory        *skills.DiscoveryInventory `json:"inventory"`
	Done             int                        `json:"done"`
	Total            int                        `json:"total"`
	CreatedAt        string                     `json:"created_at"`
}
type DiscoveryCandidate struct {
	Retention  *domain.MaterialRetention `json:"retention,omitempty"`
	ID         string                    `json:"id"`
	RunID      string                    `json:"run_id"`
	SourceID   string                    `json:"source_id"`
	Path       string                    `json:"path"`
	Commit     string                    `json:"commit"`
	PackageID  string                    `json:"package_id"`
	PreviousID string                    `json:"previous_id"`
}
type DiscoveryOverview struct {
	Sources    []DiscoverySource    `json:"sources"`
	Runs       []DiscoveryRun       `json:"runs"`
	Candidates []DiscoveryCandidate `json:"candidates"`
}

const discoverySourceColumns = "id,project_id,actor_id,repository,source_ref,source_path,keyword,enabled,candidate_limit,revision"
const discoveryRunColumns = "id,project_id,source_id,actor_id,source_revision,source_snapshot,state,reason,inventory,done_count,total_count,CAST(created_at AS CHAR)"

func scanDiscoverySource(row interface{ Scan(...any) error }) (DiscoverySource, error) {
	var s DiscoverySource
	err := row.Scan(&s.ID, &s.ProjectID, &s.ActorID, &s.Repository, &s.Ref, &s.Path, &s.Keyword, &s.Enabled, &s.Limit, &s.Revision)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	return s, err
}
func scanDiscoveryRun(row interface{ Scan(...any) error }) (DiscoveryRun, error) {
	var r DiscoveryRun
	var source, inventory []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.SourceID, &r.ActorID, &r.SourceRevision, &source, &r.State, &r.Reason, &inventory, &r.Done, &r.Total, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return r, domain.ErrForbidden
	}
	if err != nil {
		return r, err
	}
	if err := rejectExpiredRuntime(source, "skill_discovery_runs.source_snapshot"); err != nil {
		if !errors.Is(err, domain.ErrMaterialExpired) {
			return r, err
		}
		r.expiredSnapshot = true
	} else if json.Unmarshal(source, &r.Source) != nil {
		return r, domain.ErrNotReady
	}
	if inventory != nil {
		if err := rejectExpiredRuntime(inventory, "skill_discovery_runs.inventory"); err != nil {
			r.expiredInventory = inventory
			return r, err
		}
	}
	if inventory != nil && json.Unmarshal(inventory, &r.Inventory) != nil {
		return r, domain.ErrNotReady
	}
	if r.expiredSnapshot {
		return r, domain.ErrMaterialExpired
	}
	return r, nil
}
func discoverySourceTx(ctx context.Context, tx *sql.Tx, project, id string) (DiscoverySource, error) {
	return scanDiscoverySource(tx.QueryRowContext(ctx, "SELECT "+discoverySourceColumns+" FROM skill_discovery_sources WHERE project_id=? AND id=? FOR UPDATE", project, id))
}
func discoveryRunTx(ctx context.Context, tx *sql.Tx, project, id string, history ...bool) (DiscoveryRun, error) {
	run, err := scanDiscoveryRun(tx.QueryRowContext(ctx, "SELECT "+discoveryRunColumns+" FROM skill_discovery_runs WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if errors.Is(err, domain.ErrMaterialExpired) && len(history) > 0 && history[0] {
		err = discoveryHistoryTx(ctx, tx, project, &run)
	}
	return run, err
}
func (s *SkillDiscovery) SaveSource(ctx context.Context, a domain.Actor, project, id string, input DiscoverySourceInput) (DiscoverySource, error) {
	var result DiscoverySource
	if !validName(input.OperationID, 120) || !skills.ValidRemoteSource(skills.RemoteRequest{Repository: input.Repository, Ref: input.Ref, Path: input.Path}) || len(input.Keyword) > 120 || input.Limit < 1 || input.Limit > 5 {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	hash := valueHash(struct {
		Kind, Project, ID string
		Input             DiscoverySourceInput
	}{"discovery.source", project, id, input})
	var previous, storedID string
	previous, storedID, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if previous != hash {
			return result, domain.ErrConflict
		}
		return discoverySourceTx(ctx, tx, project, storedID)
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	if id == "" {
		if input.ExpectedRevision != 0 {
			return result, domain.ErrConflict
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_discovery_sources WHERE project_id=?", project).Scan(&count); err != nil {
			return result, err
		}
		if count >= 10 {
			return result, domain.ErrConflict
		}
		id = NewID()
		_, err = tx.ExecContext(ctx, "INSERT INTO skill_discovery_sources(id,project_id,actor_id,repository,source_ref,source_path,keyword,enabled,candidate_limit) VALUES(?,?,?,?,?,?,?,?,?)", id, project, a.ID, input.Repository, input.Ref, input.Path, input.Keyword, input.Enabled, input.Limit)
		if err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'discovery.schedule',?,'{}')", NewID(), project, id)
	} else {
		current, readErr := discoverySourceTx(ctx, tx, project, id)
		if readErr != nil {
			return result, readErr
		}
		if current.Revision != input.ExpectedRevision {
			return result, domain.ErrConflict
		}
		if err = captureMetadataSourceTx(ctx, tx, project, "skill_discovery_sources", id); err != nil {
			return result, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE skill_discovery_sources SET actor_id=?,repository=?,source_ref=?,source_path=?,keyword=?,enabled=?,candidate_limit=?,revision=revision+1 WHERE id=?", a.ID, input.Repository, input.Ref, input.Path, input.Keyword, input.Enabled, input.Limit, id)
		if err == nil {
			err = blockDiscoverySourceRunsTx(ctx, tx, project, id)
		}
	}
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'discovery.source',?,?)", a.ID, input.OperationID, hash, id); err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "discovery.source", id); err != nil {
		return result, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "skill_discovery_sources", id); err != nil {
		return result, err
	}
	result, err = discoverySourceTx(ctx, tx, project, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *SkillDiscovery) Overview(ctx context.Context, a domain.Actor, project string) (DiscoveryOverview, error) {
	result := DiscoveryOverview{Sources: []DiscoverySource{}, Runs: []DiscoveryRun{}, Candidates: []DiscoveryCandidate{}}
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+discoverySourceColumns+" FROM skill_discovery_sources WHERE project_id=? ORDER BY created_at,id", project)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		v, e := scanDiscoverySource(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Sources = append(result.Sources, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT "+discoveryRunColumns+" FROM skill_discovery_runs WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 30", project)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		v, e := scanDiscoveryRun(rows)
		if e != nil && !errors.Is(e, domain.ErrMaterialExpired) {
			rows.Close()
			return result, e
		}
		result.Runs = append(result.Runs, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,run_id,source_id,source_path,source_commit,package_id,previous_id FROM skill_discovery_candidates WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var v DiscoveryCandidate
		if err = rows.Scan(&v.ID, &v.RunID, &v.SourceID, &v.Path, &v.Commit, &v.PackageID, &v.PreviousID); err != nil {
			rows.Close()
			return result, err
		}
		result.Candidates = append(result.Candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		for i := range result.Candidates {
			candidate := &result.Candidates[i]
			if _, marked := retention.RetiredLabelHash(candidate.Path); marked {
				candidate.Retention, err = runtimeTransientTx(ctx, tx, project, "skill_discovery_candidates", "source_path", candidate.ID)
				if err != nil {
					return result, err
				}
				candidate.Path = ""
			}
		}
		for i := range result.Runs {
			if result.Runs[i].expiredInventory != nil || result.Runs[i].expiredSnapshot {
				err = discoveryHistoryTx(ctx, tx, project, &result.Runs[i])
				if err != nil {
					return result, err
				}
			}
		}
	}
	return result, err
}

func discoveryHistoryTx(ctx context.Context, tx *sql.Tx, project string, run *DiscoveryRun) error {
	for _, field := range []struct {
		name    string
		expired bool
	}{{"inventory", run.expiredInventory != nil}, {"source_snapshot", run.expiredSnapshot}} {
		if !field.expired {
			continue
		}
		proof, err := runtimeTransientTx(ctx, tx, project, "skill_discovery_runs", field.name, run.ID)
		if err != nil {
			return err
		}
		run.Retention = proof
	}
	return nil
}
