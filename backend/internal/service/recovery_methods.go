package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
	"novelstudio/internal/skills"
)

type RecoveryMethodIDs struct {
	SourceProject, TargetProject                  string
	Packages, Versions, Sources, Runs, Candidates map[string]string
}

type RecoveryMethodCounts struct{ Packages, Versions, Sources, Runs, Candidates int }

var recoveryMethodTables = []recoveryTable{
	{"skill_packages", "id,project_id,parent_id,upstream_id,bundle_hash,report,source,state,created_by,created_at", "", "v.project_id"},
	{"skill_revocations", "package_id,project_id,actor_id,reason,created_at", "", "v.project_id"},
	{"capability_versions", "id,project_id,capability_key,kind,parent_id,content_hash,definition,created_by,created_at", "", "v.project_id"},
	{"capability_heads", "project_id,capability_key,version_id", "", "v.project_id"},
	{"capability_revocations", "version_id,project_id,actor_id,reason,created_at", "", "v.project_id"},
	{"capability_preset_steps", "project_id,step_key,kind,request_hash,result_id,created_by,created_at", "", "v.project_id"},
	{"skill_discovery_sources", "id,project_id,actor_id,repository,source_ref,source_path,keyword,enabled,candidate_limit,revision,created_at", "", "v.project_id"},
	{"skill_discovery_runs", "id,project_id,source_id,actor_id,source_revision,source_snapshot,state,reason,inventory,done_count,total_count,created_at", "", "v.project_id"},
	{"skill_discovery_candidates", "id,project_id,source_id,run_id,source_path,source_commit,package_id,previous_id,created_at", "", "v.project_id"},
}

// CopyRecoveryMethodsTx restores editable methods and their history. It does
// not restore bindings, qualifications, trial leases or runnable outbox jobs.
func (s *IP) CopyRecoveryMethodsTx(ctx context.Context, tx *sql.Tx, actorID string, ids RecoveryMethodIDs) (counts RecoveryMethodCounts, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for _, mapping := range []map[string]string{ids.Packages, ids.Versions, ids.Sources, ids.Runs, ids.Candidates} {
		if !validRecoveryIDs(mapping) {
			return counts, domain.ErrValidation
		}
	}
	if _, err = s.Workspace.lockRecoveryProjectsTx(ctx, tx, actorID, ids.SourceProject, ids.TargetProject); err != nil {
		return counts, err
	}
	data := map[string][]recoveryRow{}
	total := 0
	for _, table := range recoveryMethodTables {
		var existing int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table.name+" WHERE project_id=?", ids.TargetProject).Scan(&existing); err != nil {
			return counts, err
		}
		if existing != 0 {
			return counts, domain.ErrConflict
		}
		if data[table.name], err = readRecoveryTable(ctx, tx, table, ids.SourceProject, &total); err != nil {
			return counts, err
		}
	}
	for table, mapping := range map[string]map[string]string{"skill_packages": ids.Packages, "capability_versions": ids.Versions, "skill_discovery_sources": ids.Sources, "skill_discovery_runs": ids.Runs, "skill_discovery_candidates": ids.Candidates} {
		if len(data[table]) != len(mapping) {
			return counts, domain.ErrConflict
		}
	}
	expiry, err := prepareRecoveryExpiryRows(ctx, tx, ids.SourceProject, recoveryMethodTables, data)
	if err != nil {
		return counts, err
	}
	objects := map[string][]byte{}
	read := func(hash string) error {
		if hash == "" || objects[hash] != nil {
			return nil
		}
		body, e := s.Blobs.ReadObject(ctx, ids.SourceProject, hash)
		if e != nil || digest(string(body)) != hash {
			return domain.ErrNotReady
		}
		total += len(body)
		if total > 128<<20 {
			return domain.ErrNotReady
		}
		objects[hash] = body
		return nil
	}
	packages := map[string]SkillVersion{}
	packageEdges := map[string][]string{}
	for _, row := range data["skill_packages"] {
		table := recoveryMethodTables[0]
		id := table.value(row, "id")
		v, e := skillRecordTx(ctx, tx, ids.SourceProject, id, true)
		if e != nil {
			return counts, e
		}
		if ids.Packages[id] == "" || !slices.Contains([]string{"CANDIDATE", "REFERENCE", "REVOKED"}, v.State) {
			return counts, domain.ErrNotReady
		}
		reportExpired := expiry.expired(table.name, row, table, "report")
		bundleExpired := expiry.expired(table.name, row, table, "bundle_hash")
		hash := table.value(row, "bundle_hash")
		if !reportExpired && (decodeRecoveryDocument([]byte(table.value(row, "report")), &v.Report) != nil || v.Report.Hash != hash) {
			return counts, domain.ErrNotReady
		}
		if !bundleExpired {
			if err = read(hash); err != nil {
				return counts, err
			}
			var bundle skills.Bundle
			if decodeRecoveryDocument(objects[hash], &bundle) != nil || bundle.Validate() != nil {
				return counts, domain.ErrNotReady
			}
			if !reportExpired {
				report, e := skills.Analyze(bundle)
				if e != nil || valueHash(report) != valueHash(v.Report) {
					return counts, domain.ErrNotReady
				}
			}
		}
		archive := ""
		if proof, expired := expiry.field(row, table, "source"); expired {
			archive = proof.Summary.InputHash
		} else {
			if decodeRecoveryDocument([]byte(table.value(row, "source")), &v.Source) != nil {
				return counts, domain.ErrNotReady
			}
			archive = v.Source.ArchiveHash
		}
		if !expiry.expired(table.name, row, table, "source.archive_hash") {
			if err = read(archive); err != nil {
				return counts, err
			}
		}
		packages[id], packageEdges[id] = v, []string{v.ParentID, v.UpstreamID}
	}
	if err = recoveryMethodGraph(packageEdges); err != nil {
		return counts, err
	}
	original := map[string]CapabilityVersion{}
	for _, row := range data["capability_versions"] {
		table := recoveryMethodTables[2]
		id := table.value(row, "id")
		v, e := capabilityTx(ctx, tx, ids.SourceProject, id)
		if e != nil {
			return counts, e
		}
		if ids.Versions[id] == "" || v.Definition.Key != table.value(row, "capability_key") || v.Definition.Kind != table.value(row, "kind") {
			return counts, domain.ErrNotReady
		}
		original[id] = v
	}
	versions, err := remapRecoveryMethods(original, packages, ids)
	if err != nil {
		return counts, err
	}
	// Current protected revocations survive even if an old business projection
	// was restored without its revocation row. Recreate restrictions in the copy.
	restrictions := []authority.Event{}
	for _, group := range []struct {
		mapping map[string]string
		table   recoveryTable
		column  string
		kind    authority.Kind
	}{
		{ids.Packages, recoveryMethodTables[1], "package_id", authority.SkillRevoked},
		{ids.Versions, recoveryMethodTables[4], "version_id", authority.CapabilityRevoked},
	} {
		present := map[string]bool{}
		for _, row := range data[group.table.name] {
			present[group.table.value(row, group.column)] = true
		}
		for oldID, newID := range group.mapping {
			revoked, e := protectedLedger().RevokedTx(ctx, tx, ids.SourceProject, group.kind, []string{oldID})
			if e != nil {
				return counts, e
			}
			if group.kind == authority.SkillRevoked && packages[oldID].State == "REVOKED" {
				revoked = true
			}
			if !revoked && !present[oldID] {
				continue
			}
			if !present[oldID] {
				row := make(recoveryRow, len(strings.Split(group.table.columns, ",")))
				for key, value := range map[string]string{group.column: oldID, "project_id": ids.SourceProject, "actor_id": actorID, "reason": "恢复时保留原项目已生效的撤销限制", "created_at": time.Now().UTC().Format("2006-01-02 15:04:05.000000")} {
					group.table.set(row, key, value)
				}
				data[group.table.name] = append(data[group.table.name], row)
				owner, e := retention.RowOwnerKey(group.table.name, recoveryRowSource(group.table, row).Values)
				if e != nil {
					return counts, domain.ErrNotReady
				}
				expiry.owners[group.table.name] = append(expiry.owners[group.table.name], owner)
			}
			restrictions = append(restrictions, authority.Event{Kind: group.kind, ProjectID: ids.TargetProject, ResourceID: newID, ActorID: actorID, Reason: "恢复时继承原方法的撤销限制"})
		}
	}
	for _, table := range recoveryMethodTables {
		for _, row := range data[table.name] {
			if table.name == "capability_heads" {
				v, exists := original[table.value(row, "version_id")]
				if !exists || v.Definition.Key != table.value(row, "capability_key") {
					return counts, domain.ErrNotReady
				}
			}
			if table.name == "capability_versions" {
				v := versions[table.value(row, "id")]
				raw, _ := json.Marshal(v.Definition)
				table.set(row, "definition", string(raw))
				table.set(row, "content_hash", v.Hash)
			}
			if table.name == "capability_preset_steps" {
				if err = remapRecoveryPreset(table, row, original, versions, packages, ids); err != nil {
					return counts, err
				}
			}
			if table.name == "skill_discovery_sources" {
				table.set(row, "enabled", "0")
			}
			if table.name == "skill_discovery_runs" {
				run, e := discoveryRunTx(ctx, tx, ids.SourceProject, table.value(row, "id"), true)
				if e != nil {
					return counts, e
				}
				if !expiry.expired(table.name, row, table, "source_snapshot") {
					if run.Source.ID != run.SourceID || run.Source.Revision != run.SourceRevision {
						return counts, domain.ErrNotReady
					}
					if err = remapRecoveryField(&run.Source.ID, ids.Sources); err != nil {
						return counts, err
					}
					raw, _ := json.Marshal(run.Source)
					table.set(row, "source_snapshot", string(raw))
				}
				if slices.Contains([]string{"QUEUED", "RUNNING", "PAUSED"}, run.State) {
					table.set(row, "state", "CANCELLED")
					table.set(row, "reason", "原任务未完成；恢复后保留已采集结果，需要时重新发起扫描")
				}
			}
			for _, column := range strings.Split(table.columns, ",") {
				var mapping map[string]string
				switch column {
				case "project_id":
					table.set(row, column, ids.TargetProject)
					continue
				case "id":
					mapping = map[string]map[string]string{"skill_packages": ids.Packages, "capability_versions": ids.Versions, "skill_discovery_sources": ids.Sources, "skill_discovery_runs": ids.Runs, "skill_discovery_candidates": ids.Candidates}[table.name]
				case "parent_id":
					if table.name == "skill_packages" {
						mapping = ids.Packages
					} else {
						mapping = ids.Versions
					}
				case "upstream_id", "package_id":
					mapping = ids.Packages
				case "version_id":
					mapping = ids.Versions
				case "source_id":
					mapping = ids.Sources
				case "run_id":
					mapping = ids.Runs
				case "previous_id":
					mapping = ids.Candidates
				}
				if mapping != nil {
					value, e := recoveredID(mapping, table.value(row, column))
					if e != nil {
						return counts, e
					}
					table.set(row, column, value)
				}
			}
		}
	}
	expiryMapping, expiryGraph, err := expiry.rebind(ids.SourceProject, ids.TargetProject, recoveryMethodTables, data, retention.RecoveryExpiryMap{})
	if err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT method_recovery_copy"); err != nil {
		return counts, err
	}
	defer func() {
		if err != nil {
			cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer finish()
			if _, e := tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT method_recovery_copy"); e != nil {
				_ = tx.Rollback()
			}
			counts = RecoveryMethodCounts{}
		}
	}()
	for hash, body := range objects {
		object, e := objectwriter.Write(ctx, tx, s.Blobs, ids.TargetProject, body)
		if e != nil {
			return counts, e
		}
		if object.Hash != hash {
			return counts, domain.ErrNotReady
		}
	}
	for _, table := range recoveryMethodTables {
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
	for _, restriction := range restrictions {
		if err = protectedLedger().AppendTx(ctx, tx, restriction); err != nil {
			return counts, err
		}
	}
	if err = expiry.plan.WriteTx(ctx, tx, expiryMapping, expiryGraph); err != nil {
		return counts, &recoveryPhaseError{"方法过期凭据", err}
	}
	for _, table := range recoveryMethodTables {
		for _, row := range data[table.name] {
			primary := map[string]string{}
			switch table.name {
			case "skill_revocations":
				primary["package_id"] = table.value(row, "package_id")
			case "capability_revocations":
				primary["version_id"] = table.value(row, "version_id")
			case "capability_heads":
				primary["project_id"], primary["capability_key"] = ids.TargetProject, table.value(row, "capability_key")
			case "capability_preset_steps":
				primary["project_id"], primary["step_key"] = ids.TargetProject, table.value(row, "step_key")
			default:
				primary["id"] = table.value(row, "id")
			}
			if err = s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: table.name, Primary: primary}); err != nil {
				return counts, &recoveryPhaseError{"方法来源登记", err}
			}
		}
	}
	if err = recoveryWindowTx(ctx, tx, ids.SourceProject); err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT method_recovery_copy"); err != nil {
		return counts, err
	}
	return RecoveryMethodCounts{len(ids.Packages), len(ids.Versions), len(ids.Sources), len(ids.Runs), len(ids.Candidates)}, nil
}

func recoveryMethodGraph(edges map[string][]string) error {
	state := map[string]int{}
	var visit func(string, int) error
	visit = func(id string, depth int) error {
		if id == "" || state[id] == 2 {
			return nil
		}
		children, exists := edges[id]
		if !exists || state[id] == 1 || depth > 128 {
			return domain.ErrNotReady
		}
		state[id] = 1
		for _, child := range children {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range edges {
		if err := visit(id, 0); err != nil {
			return err
		}
	}
	return nil
}

func remapRecoveryMethods(original map[string]CapabilityVersion, packages map[string]SkillVersion, ids RecoveryMethodIDs) (map[string]CapabilityVersion, error) {
	edges := map[string][]string{}
	for id, v := range original {
		edges[id] = []string{v.ParentID}
		if v.ParentID != "" {
			parent, exists := original[v.ParentID]
			if !exists || parent.Definition.Key != v.Definition.Key || parent.Definition.Kind != v.Definition.Kind {
				return nil, domain.ErrNotReady
			}
		}
		for _, dependency := range v.Definition.Dependencies {
			other, ok := original[dependency.ID]
			if !ok || other.Hash != dependency.Hash || other.Definition.Kind != dependency.Kind {
				return nil, domain.ErrNotReady
			}
			edges[id] = append(edges[id], dependency.ID)
		}
	}
	if err := recoveryMethodGraph(edges); err != nil {
		return nil, err
	}
	result := map[string]CapabilityVersion{}
	var copyVersion func(string) (CapabilityVersion, error)
	copyVersion = func(id string) (CapabilityVersion, error) {
		if v, exists := result[id]; exists {
			return v, nil
		}
		raw, _ := json.Marshal(original[id])
		var v CapabilityVersion
		if json.Unmarshal(raw, &v) != nil || ids.Versions[id] == "" {
			return v, domain.ErrNotReady
		}
		v.ID = ids.Versions[id]
		if err := remapRecoveryField(&v.ParentID, ids.Versions); err != nil {
			return v, err
		}
		for i := range v.Definition.Dependencies {
			ref := &v.Definition.Dependencies[i]
			dependency, err := copyVersion(ref.ID)
			if err != nil {
				return v, err
			}
			ref.ID, ref.Hash = dependency.ID, dependency.Hash
		}
		if v.Definition.PackageID != "" {
			pkg, exists := packages[v.Definition.PackageID]
			if !exists || pkg.Retention != nil || pkg.Report.Hash != v.Definition.PackageHash {
				return v, domain.ErrNotReady
			}
			if err := remapRecoveryField(&v.Definition.PackageID, ids.Packages); err != nil {
				return v, err
			}
		}
		for i := range v.Definition.Steps {
			if err := remapRecoveryField(&v.Definition.Steps[i].AgentID, ids.Versions); err != nil {
				return v, err
			}
		}
		if v.Definition.Validate() != nil {
			return v, domain.ErrNotReady
		}
		v.Hash = capabilityVersionHash(v)
		result[id] = v
		return v, nil
	}
	for id := range original {
		if _, err := copyVersion(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func remapRecoveryPreset(table recoveryTable, row recoveryRow, old, copied map[string]CapabilityVersion, packages map[string]SkillVersion, ids RecoveryMethodIDs) error {
	key, id := table.value(row, "step_key"), table.value(row, "result_id")
	needle := "." + ids.SourceProject + "."
	if strings.Count(key, needle) != 1 {
		return domain.ErrNotReady
	}
	var before, after, resultID string
	switch table.value(row, "kind") {
	case "capability":
		v, ok := old[id]
		if !ok {
			return domain.ErrNotReady
		}
		newer := copied[id]
		before = capabilityDigest([]any{"capability.save", ids.SourceProject, v.ParentID, v.Definition})
		after = capabilityDigest([]any{"capability.save", ids.TargetProject, newer.ParentID, newer.Definition})
		resultID = newer.ID
	case "skill-package":
		v, ok := packages[id]
		if !ok || v.Retention != nil {
			return domain.ErrNotReady
		}
		raw, _ := json.Marshal([]any{ids.SourceProject, v.ParentID, v.UpstreamID, v.Report.Hash, v.Source})
		before = digest("skill.save\x00" + string(raw))
		if err := remapRecoveryField(&v.ParentID, ids.Packages); err != nil {
			return err
		}
		if err := remapRecoveryField(&v.UpstreamID, ids.Packages); err != nil {
			return err
		}
		raw, _ = json.Marshal([]any{ids.TargetProject, v.ParentID, v.UpstreamID, v.Report.Hash, v.Source})
		after, resultID = digest("skill.save\x00"+string(raw)), ids.Packages[id]
	default:
		return domain.ErrNotReady
	}
	if before != table.value(row, "request_hash") || resultID == "" {
		return domain.ErrNotReady
	}
	table.set(row, "step_key", strings.Replace(key, needle, "."+ids.TargetProject+".", 1))
	table.set(row, "result_id", resultID)
	table.set(row, "request_hash", after)
	return nil
}
