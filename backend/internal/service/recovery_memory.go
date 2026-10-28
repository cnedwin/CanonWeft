package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
)

type RecoveryMemoryCounts struct {
	Sessions, Items, Revisions int
}

type recoveryMemoryDates struct{ created, updated, actor string }

var recoverySessionTable = recoveryTable{"memory_sessions", "id,project_id,branch_id,actor_id,revision,document,created_at,updated_at", "", "v.project_id"}

// CopyRecoveryMemoryTx is a component of the project recovery transaction, not
// a standalone restore endpoint. The caller supplies copied branch/body pins
// and commits only after all project domains have been copied and checked.
// This function verifies live ownership and the deletion window itself. A
// failed component rolls back its own writes even if the caller retains tx.
func (s *Memory) CopyRecoveryMemoryTx(ctx context.Context, tx *sql.Tx, actorID string, ids memory.RecoveryMap) (counts RecoveryMemoryCounts, err error) {
	if tx == nil || !lowerHex32(ids.SourceProject) || !lowerHex32(ids.TargetProject) || ids.SourceProject == ids.TargetProject {
		return counts, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, err = s.IP.Workspace.lockRecoveryProjectsTx(ctx, tx, actorID, ids.SourceProject, ids.TargetProject)
	if err != nil {
		return counts, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM memory_sessions WHERE project_id=?)+(SELECT COUNT(*) FROM memory_items WHERE project_id=?)+(SELECT COUNT(*) FROM memory_revisions WHERE project_id=?)", ids.TargetProject, ids.TargetProject, ids.TargetProject).Scan(&existing); err != nil {
		return counts, err
	}
	if existing != 0 {
		return counts, domain.ErrConflict
	}
	for from, to := range ids.Branches {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM branches WHERE (id=? AND project_id=?) OR (id=? AND project_id=?)", from, ids.SourceProject, to, ids.TargetProject).Scan(&n); err != nil {
			return counts, err
		}
		if n != 2 {
			return counts, domain.ErrConflict
		}
	}
	pins := map[memory.RecoverySource]recoveryMemoryPin{}
	for from, to := range ids.Resources {
		original, e := s.verifyRecoveryMemoryPin(ctx, tx, ids.SourceProject, from)
		if e != nil {
			return counts, e
		}
		copied, e := s.verifyRecoveryMemoryPin(ctx, tx, ids.TargetProject, to)
		if e != nil {
			return counts, e
		}
		if ids.Branches[original.branch] != copied.branch || !reflect.DeepEqual(original.policy, copied.policy) {
			return counts, domain.ErrConflict
		}
		pins[from] = original
	}
	data, expiry, err := readRecoveryMemoryRows(ctx, tx, ids.SourceProject)
	if err != nil {
		return counts, err
	}
	graph, _, err := parseRecoveryMemoryRows(ids.SourceProject, data, expiry)
	if err != nil {
		return counts, err
	}
	for _, version := range graph.Revisions {
		for _, source := range version.Item.Sources {
			pin, ok := pins[memory.RecoverySource{Resource: source.Resource, Revision: source.Revision, Hash: source.Hash}]
			if !ok || pin.branch != version.Item.Branch || source.Start < 0 || source.End > pin.runes {
				return counts, domain.ErrNotReady
			}
		}
	}
	copy, err := memory.RemapRecovery(graph, ids)
	if err != nil {
		return counts, &recoveryPhaseError{"记忆关系映射", domain.ErrNotReady}
	}
	expiryMapping, expiryGraph, err := remapRecoveryMemoryRows(data, expiry, copy, ids)
	if err != nil {
		return counts, &recoveryPhaseError{"记忆清理凭据映射", err}
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT memory_recovery_copy"); err != nil {
		return counts, err
	}
	defer func() {
		if err != nil {
			// Cancellation may stop a copy after some writes. Roll back on this
			// same transaction with a short independent cleanup deadline; if the
			// savepoint cannot be recovered, invalidate the whole transaction.
			cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer finish()
			if _, rollbackErr := tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT memory_recovery_copy"); rollbackErr != nil {
				_ = tx.Rollback()
			}
			counts = RecoveryMemoryCounts{}
		}
	}()
	for _, table := range recoveryMemoryTables {
		for _, row := range data[table.name] {
			args := make([]any, len(row))
			for i, cell := range row {
				if cell.Valid {
					args[i] = cell.String
				}
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table.name+"("+table.columns+") VALUES("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...); err != nil {
				return counts, err
			}
		}
	}
	if err = expiry.plan.WriteTx(ctx, tx, expiryMapping, expiryGraph); err != nil {
		return counts, &recoveryPhaseError{"记忆清理凭据写入", err}
	}
	for _, table := range recoveryMemoryTables {
		for _, row := range data[table.name] {
			if err = s.IP.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: table.name, Primary: map[string]string{"id": table.value(row, "id")}}); err != nil {
				return counts, &recoveryPhaseError{"记忆来源登记", err}
			}
		}
	}
	if err = recoveryWindowTx(ctx, tx, ids.SourceProject); err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT memory_recovery_copy"); err != nil {
		return counts, err
	}
	return RecoveryMemoryCounts{Sessions: len(data["memory_sessions"]), Items: len(data["memory_items"]), Revisions: len(data["memory_revisions"])}, nil
}

type recoveryMemoryPin struct {
	branch string
	runes  int
	policy domain.KnowledgePolicy
}

func (s *Memory) verifyRecoveryMemoryPin(ctx context.Context, tx *sql.Tx, project string, pin memory.RecoverySource) (recoveryMemoryPin, error) {
	var raw []byte
	var branch, hash string
	info := recoveryMemoryPin{policy: domain.DefaultKnowledgePolicy()}
	if err := tx.QueryRowContext(ctx, "SELECT a.manifest,r.branch_id,a.content_hash FROM artifact_releases a JOIN resources r ON r.project_id=a.project_id AND r.branch_id=a.branch_id WHERE a.id=? AND r.id=? AND a.project_id=? AND a.status='PUBLISHED'", pin.Revision, pin.Resource, project).Scan(&raw, &branch, &hash); err != nil {
		return info, domain.ErrNotReady
	}
	var manifest domain.Manifest
	if decodeRecoveryDocument(raw, &manifest) != nil || manifest.BranchID != branch || valueHash(manifest) != hash {
		return info, domain.ErrNotReady
	}
	found := false
	for _, artifact := range manifest.Artifacts {
		if artifact.ResourceID == pin.Resource && artifact.Hash == pin.Hash {
			if found {
				return info, domain.ErrNotReady
			}
			found = true
			if artifact.Policy != nil {
				info.policy = *artifact.Policy
			}
		}
	}
	if !found {
		return info, domain.ErrNotReady
	}
	body, err := s.IP.Blobs.ReadObject(ctx, project, pin.Hash)
	if err != nil || digest(string(body)) != pin.Hash || !utf8.Valid(body) || info.policy.Validate() != nil {
		return info, domain.ErrNotReady
	}
	info.branch, info.runes = branch, utf8.RuneCount(body)
	return info, nil
}

func decodeRecoveryDocument(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return domain.ErrValidation
	}
	return nil
}
