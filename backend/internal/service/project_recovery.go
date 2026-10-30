package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

var ErrRecoveryIncomplete = errors.New("恢复完整性检查未通过，可能存在已清理或缺失的材料；未创建副本")

type recoveryPhaseError struct {
	phase string
	err   error
}

func (e *recoveryPhaseError) Error() string { return e.phase + ": " + e.err.Error() }
func (e *recoveryPhaseError) Unwrap() error { return e.err }

type ProjectRecoveryInput struct {
	Name         string `json:"name"`
	Confirmation string `json:"confirm_name"`
	Revision     int64  `json:"revision"`
	OperationID  string `json:"operation_id"`
}

// RecoverProject commits the project, its author material and a retry receipt
// together. Copied history cannot replay model calls, approvals or old jobs.
func (s *IP) RecoverProject(ctx context.Context, actorID, source string, input ProjectRecoveryInput) (project domain.Project, err error) {
	input.Name = strings.TrimSpace(input.Name)
	if !lowerHex32(source) || !validName(input.Name, 120) || !validName(input.Confirmation, 120) || input.Revision < 1 || !validName(input.OperationID, 120) {
		return project, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return project, err
	}
	defer tx.Rollback()
	a, err := s.Workspace.actorTx(ctx, tx, actorID)
	if err != nil {
		return project, err
	}
	if a.Role != "owner" {
		return project, domain.ErrForbidden
	}
	fingerprint := valueHash([]any{"project.recover.v1", source, input.Name, input.Confirmation, input.Revision})
	stored, previous, err := operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if stored != fingerprint {
			return project, domain.ErrConflict
		}
		// A lost response can be retried after the source window closes. The
		// completed copy must still be accessible; never resurrect a deleted copy.
		return projectTx(ctx, tx, a, previous)
	}
	if err != sql.ErrNoRows {
		return project, err
	}
	var name, role string
	var revision int64
	var deleted bool
	err = tx.QueryRowContext(ctx, "SELECT p.name,p.revision,p.deleted_at IS NOT NULL,m.role FROM projects p JOIN project_members m ON m.project_id=p.id AND m.user_id=? WHERE p.id=? AND p.workspace_id=? FOR UPDATE", actorID, source, a.WorkspaceID).Scan(&name, &revision, &deleted, &role)
	if err == sql.ErrNoRows || (err == nil && role != "owner") {
		return project, domain.ErrForbidden
	}
	if err != nil {
		return project, err
	}
	if !deleted || revision != input.Revision {
		return project, domain.ErrConflict
	}
	if name != input.Confirmation {
		return project, domain.ErrValidation
	}
	if err = recoveryWindowTx(ctx, tx, source); err != nil {
		return project, err
	}
	project = domain.Project{ID: NewID(), Name: input.Name, Revision: 1, Role: "owner"}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,?)", project.ID, a.WorkspaceID, project.Name); err != nil {
		return domain.Project{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,'owner')", project.ID, a.ID); err != nil {
		return domain.Project{}, err
	}
	if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectCreated, ProjectID: project.ID, ResourceID: project.ID, ActorID: a.ID}); err != nil {
		return domain.Project{}, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project.ID, "projects", project.ID); err != nil {
		return domain.Project{}, err
	}
	objects, err := storage.NewRecoveryObjects(ctx, s.Blobs, project.ID)
	if err != nil {
		return domain.Project{}, err
	}
	defer objects.Close()
	target := project.ID
	durable := false
	namespace, err := objects.Journal(func(writeContext context.Context, object storage.BlobRef) error {
		if !durable {
			return nil
		}
		return retention.RecordRecoveryObject(writeContext, s.Workspace.DB, target, retention.PhysicalObject{Hash: object.Hash, Size: object.Size})
	})
	if err != nil {
		return domain.Project{}, err
	}
	durable, err = retention.BeginRecoveryAttempt(ctx, s.Workspace.DB, source, target, a.ID, namespace)
	if err != nil {
		return domain.Project{}, err
	}
	commitStarted := false
	defer func() {
		if err == nil || commitStarted {
			return
		}
		cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer finish()
		// The original transaction must still own this uncommitted row. A
		// cancelled/ended transaction is not a safe basis for physical deletion.
		var locked string
		if e := tx.QueryRowContext(cleanup, "SELECT id FROM projects WHERE id=? FOR UPDATE", target).Scan(&locked); e != nil || locked != target {
			return
		}
		if e := objects.Discard(cleanup); e != nil {
			err = errors.Join(err, e)
		}
	}()
	copyService := *s
	copyService.Blobs = objects
	if err = copyService.copyRecoveredAuthorMaterialTx(ctx, tx, a, source, project.ID); err != nil {
		if errors.Is(err, domain.ErrMaterialExpired) || errors.Is(err, domain.ErrNotReady) {
			var phase *recoveryPhaseError
			if errors.As(err, &phase) {
				for nested := errors.Unwrap(phase); nested != nil; nested = errors.Unwrap(nested) {
					if detail, ok := nested.(*recoveryPhaseError); ok {
						phase = detail
					}
				}
				return domain.Project{}, fmt.Errorf("%w（%s）", ErrRecoveryIncomplete, phase.phase)
			}
			return domain.Project{}, ErrRecoveryIncomplete
		}
		return domain.Project{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'project.recover',?,?)", a.ID, input.OperationID, fingerprint, project.ID); err != nil {
		return domain.Project{}, err
	}
	if err = audit(ctx, tx, a, "project.recover", project.ID); err != nil {
		return domain.Project{}, err
	}
	if err = recoveryWindowTx(ctx, tx, source); err != nil {
		return domain.Project{}, err
	}
	if durable {
		if err = retention.CommitRecoveryAttemptTx(ctx, tx, target); err != nil {
			return domain.Project{}, err
		}
	}
	commitStarted = true
	objects.BeginCommit()
	if err = tx.Commit(); err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (s *IP) copyRecoveredAuthorMaterialTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, source, target string) error {
	identities := map[string]map[string]string{}
	// Fixed descriptors only. The source project is locked for this entire
	// transaction, so no identifier map can race an author edit or deletion.
	tables := []recoveryTable{
		{"branches", "id", "", "v.project_id"},
		{"ip_revisions", "id", " JOIN branches b ON b.id=v.branch_id", "b.project_id"},
	}
	for _, table := range []string{"resources", "artifact_releases", "source_imports", "skill_packages", "capability_versions", "skill_discovery_sources", "skill_discovery_runs", "skill_discovery_candidates"} {
		tables = append(tables, recoveryTable{table, "id", "", "v.project_id"})
	}
	total := 0
	for _, table := range tables {
		rows, err := readRecoveryTable(ctx, tx, table, source, &total)
		if err != nil {
			return err
		}
		identities[table.name] = map[string]string{}
		for _, row := range rows {
			identities[table.name][table.value(row, "id")] = NewID()
		}
	}
	ipIDs := RecoveryIPIDs{SourceProject: source, TargetProject: target, Branches: identities["branches"], Revisions: identities["ip_revisions"]}
	methods := RecoveryMethodIDs{SourceProject: source, TargetProject: target, Packages: identities["skill_packages"], Versions: identities["capability_versions"], Sources: identities["skill_discovery_sources"], Runs: identities["skill_discovery_runs"], Candidates: identities["skill_discovery_candidates"]}
	if _, err := s.CopyRecoveryMethodsTx(ctx, tx, actor.ID, methods); err != nil {
		return &recoveryPhaseError{"创作方法", err}
	}
	if _, err := s.CopyRecoveryIPTx(ctx, tx, actor.ID, ipIDs); err != nil {
		return &recoveryPhaseError{"人物与世界设定", err}
	}
	novelIDs := map[string]string{}
	if err := s.copyRecoveryNovelsTx(ctx, tx, actor, ipIDs, novelIDs); err != nil {
		return &recoveryPhaseError{"小说与创作设置", err}
	}
	resources := RecoveryResourceIDs{RecoveryIPIDs: ipIDs, Resources: identities["resources"], Releases: identities["artifact_releases"], Imports: identities["source_imports"], Skills: methods.Packages}
	if _, err := s.CopyRecoveryResourcesTx(ctx, tx, actor.ID, resources); err != nil {
		return &recoveryPhaseError{"正文与资料", err}
	}
	if err := s.copyRecoveryManuscriptsTx(ctx, tx, actor, resources, novelIDs); err != nil {
		return &recoveryPhaseError{"长稿导入记录", err}
	}
	graph, _, err := readRecoveryMemoryTx(ctx, tx, source)
	if err != nil {
		return &recoveryPhaseError{"记忆来源", err}
	}
	memoryIDs := memory.RecoveryMap{SourceProject: source, TargetProject: target, Branches: ipIDs.Branches, Sessions: map[string]string{}, Items: map[string]string{}, Revisions: map[string]string{}, Resources: map[memory.RecoverySource]memory.RecoverySource{}}
	for _, session := range graph.Sessions {
		memoryIDs.Sessions[session.ID] = NewID()
	}
	for _, item := range graph.Heads {
		memoryIDs.Items[item.ID] = NewID()
	}
	for _, item := range graph.ExpiredHeads {
		memoryIDs.Items[item.ID] = NewID()
	}
	for _, revision := range graph.ExpiredRevisions {
		memoryIDs.Revisions[revision.ID] = NewID()
	}
	for _, revision := range graph.Revisions {
		memoryIDs.Revisions[revision.Item.Revision] = NewID()
		for _, pin := range revision.Item.Sources {
			resource, err := recoveredID(resources.Resources, pin.Resource)
			if err != nil {
				return err
			}
			version, err := recoveredID(resources.Releases, pin.Revision)
			if err != nil {
				return err
			}
			memoryIDs.Resources[memory.RecoverySource{Resource: pin.Resource, Revision: pin.Revision, Hash: pin.Hash}] = memory.RecoverySource{Resource: resource, Revision: version, Hash: pin.Hash}
		}
	}
	if _, err := (&Memory{IP: s}).CopyRecoveryMemoryTx(ctx, tx, actor.ID, memoryIDs); err != nil {
		return &recoveryPhaseError{"创作记忆", err}
	}
	jobs := &IndexJobs{Knowledge: &Knowledge{IP: s}}
	for _, branch := range ipIDs.Branches {
		for _, kind := range []string{"published", "working"} {
			job := IndexJob{ID: NewID(), ProjectID: target, BranchID: branch, ActorID: actor.ID, Kind: kind}
			info, err := jobs.resolve(ctx, tx, job)
			if err != nil {
				return &recoveryPhaseError{"知识检索", err}
			}
			if len(info.Manifest.Sources) == 0 {
				continue
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO index_jobs(id,project_id,branch_id,actor_id,index_id,total_sources,kind) VALUES(?,?,?,?,?,?,?)", job.ID, target, branch, actor.ID, info.ID, len(info.Manifest.Sources), kind); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'index.start',?,'{}')", NewID(), target, job.ID); err != nil {
				return err
			}
			if err = captureMetadataSourceTx(ctx, tx, target, "index_jobs", job.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
