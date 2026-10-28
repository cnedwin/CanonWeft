package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
)

type RecoveryResourceIDs struct {
	RecoveryIPIDs
	Resources, Releases, Imports, Skills map[string]string
}
type RecoveryResourceCounts struct{ Resources, Releases, Imports, Drafts, Plans int }

var recoveryResourceTables = []recoveryTable{
	{"resources", "id,project_id,branch_id,name,kind,head_revision,content_hash,revision,created_at", "", "v.project_id"},
	{"source_imports", "id,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,operation_id,created_at", "", "v.project_id"},
	{"artifact_releases", "id,project_id,branch_id,manifest,content_hash,status,created_by,adopted_by,adopted_hash,created_at", "", "v.project_id"},
	{"source_import_candidates", "import_id,resource_id,release_id", " JOIN source_imports i ON i.id=v.import_id", "i.project_id"},
	{"resource_drafts", "resource_id,actor_id,revision,base_head,body,updated_at", " JOIN resources r ON r.id=v.resource_id", "r.project_id"},
	{"resource_policy_drafts", "resource_id,actor_id,revision,payload", " JOIN resources r ON r.id=v.resource_id", "r.project_id"},
	{"import_drafts", "import_id,actor_id,project_id,revision,payload,content_hash,updated_at", "", "v.project_id"},
	{"patch_drafts", "branch_id,actor_id,project_id,revision,payload,content_hash,updated_at", "", "v.project_id"},
	{"change_plans", "id,project_id,branch_id,candidate_id,payload,content_hash,created_by,created_at", "", "v.project_id"},
	{"change_plan_releases", "plan_id,release_id", " JOIN change_plans p ON p.id=v.plan_id", "p.project_id"},
}

func recoveredID(ids map[string]string, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if ids[value] == "" {
		return "", domain.ErrNotReady
	}
	return ids[value], nil
}

// CopyRecoveryResourcesTx copies immutable body versions, imports and private
// text/policy/import drafts after the IP graph exists in the same transaction.
// Historical generation attribution is copied as data, never as runnable jobs.
func (s *IP) CopyRecoveryResourcesTx(ctx context.Context, tx *sql.Tx, actorID string, ids RecoveryResourceIDs) (counts RecoveryResourceCounts, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for _, mapping := range []map[string]string{ids.Branches, ids.Revisions, ids.Resources, ids.Releases, ids.Imports, ids.Skills} {
		if !validRecoveryIDs(mapping) {
			return counts, domain.ErrValidation
		}
	}
	if _, err = s.Workspace.lockRecoveryProjectsTx(ctx, tx, actorID, ids.SourceProject, ids.TargetProject); err != nil {
		return counts, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM resources WHERE project_id=?)+(SELECT COUNT(*) FROM artifact_releases WHERE project_id=?)+(SELECT COUNT(*) FROM source_imports WHERE project_id=?)+(SELECT COUNT(*) FROM patch_drafts WHERE project_id=?)+(SELECT COUNT(*) FROM change_plans WHERE project_id=?)", ids.TargetProject, ids.TargetProject, ids.TargetProject, ids.TargetProject, ids.TargetProject).Scan(&existing); err != nil {
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
	data := map[string][]recoveryRow{}
	total := 0
	for _, table := range recoveryResourceTables {
		if data[table.name], err = readRecoveryTable(ctx, tx, table, ids.SourceProject, &total); err != nil {
			return counts, err
		}
	}
	expiry, err := prepareRecoveryExpiryRows(ctx, tx, ids.SourceProject, recoveryResourceTables, data)
	if err != nil {
		return counts, err
	}
	if len(ids.Resources) != len(data["resources"]) || len(ids.Releases) != len(data["artifact_releases"]) || len(ids.Imports) != len(data["source_imports"]) {
		return counts, domain.ErrConflict
	}
	objects := map[string][]byte{}
	copyHashes := map[string]bool{}
	readObject := func(hash string) error {
		if hash == "" {
			return nil
		}
		if _, ok := objects[hash]; ok {
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
	manifests := map[string]domain.Manifest{}
	releaseRows := map[string]recoveryRow{}
	releases := recoveryResourceTables[2]
	copyRetainedObject := func(row recoveryRow, table recoveryTable, field, hash string) error {
		if hash == "" || expiry.expired(table.name, row, table, field) {
			return nil
		}
		if err := readObject(hash); err != nil {
			return err
		}
		copyHashes[hash] = true
		return nil
	}
	for _, row := range data["artifact_releases"] {
		id, hash := releases.value(row, "id"), releases.value(row, "content_hash")
		if ids.Releases[id] == "" {
			return counts, domain.ErrNotReady
		}
		releaseRows[id] = append(recoveryRow{}, row...)
		if proof, expired := expiry.field(row, releases, "manifest"); expired {
			if proof.Summary.Release == nil {
				return counts, domain.ErrNotReady
			}
			// The independently verified summary is history, not a Manifest.
			// A separately retained file stays byte-for-byte; never parse it to
			// resurrect the erased publication or its missing artifact bodies.
			if err = copyRetainedObject(row, releases, "content_hash", hash); err != nil {
				return counts, err
			}
			for _, ref := range proof.Summary.Release.Artifacts {
				if ids.Resources[ref.ResourceID] == "" {
					return counts, domain.ErrNotReady
				}
				for _, original := range []bool{false, true} {
					field, e := retention.ArtifactField(ref.ResourceID, original)
					if e != nil {
						return counts, domain.ErrNotReady
					}
					bodyHash := ref.Hash
					if original {
						bodyHash = ref.OriginalHash
					}
					if err = copyRetainedObject(row, releases, field, bodyHash); err != nil {
						return counts, err
					}
				}
			}
			continue
		}
		var manifest domain.Manifest
		if ids.Releases[id] == "" || decodeRecoveryDocument([]byte(releases.value(row, "manifest")), &manifest) != nil || valueHash(manifest) != hash || (manifest.Schema != 1 && manifest.Schema != 2) || manifest.BranchID != releases.value(row, "branch_id") || (manifest.ReleaseID != "" && manifest.ReleaseID != id) {
			return counts, domain.ErrNotReady
		}
		if !expiry.expired(releases.name, row, releases, "content_hash") {
			if err = readObject(hash); err != nil {
				return counts, err
			}
			var stored domain.Manifest
			if decodeRecoveryDocument(objects[hash], &stored) != nil || valueHash(stored) != hash {
				return counts, domain.ErrNotReady
			}
		}
		for _, ref := range manifest.Artifacts {
			if ids.Resources[ref.ResourceID] == "" || (manifest.Schema == 2 && (ref.Origin == nil || ref.Origin.ValidateTarget(manifest.BranchID, ref) != nil || ref.Origin.IPHead != manifestIPHead(manifest))) {
				return counts, domain.ErrNotReady
			}
			bodyField, e := retention.ArtifactField(ref.ResourceID, false)
			if e != nil {
				return counts, domain.ErrNotReady
			}
			originalField, e := retention.ArtifactField(ref.ResourceID, true)
			if e != nil {
				return counts, domain.ErrNotReady
			}
			if err = copyRetainedObject(row, releases, bodyField, ref.Hash); err != nil {
				return counts, err
			}
			if !expiry.expired(releases.name, row, releases, bodyField) && int64(len(objects[ref.Hash])) != ref.Size {
				return counts, domain.ErrNotReady
			}
			if err = copyRetainedObject(row, releases, originalField, ref.OriginalHash); err != nil {
				return counts, err
			}
			if ref.Origin != nil && ref.Origin.Generation != nil && len(ref.Origin.Recovery) == 0 {
				g := ref.Origin.Generation
				var n int
				if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM generation_tasks g JOIN model_calls c ON c.id=g.call_id WHERE g.id=? AND g.project_id=? AND g.branch_id=? AND c.project_id=? AND c.id=? AND g.input_hash=? AND c.output_hash=?", g.GenerationID, ids.SourceProject, manifest.BranchID, ids.SourceProject, g.CallID, g.InputHash, g.OutputHash).Scan(&n); err != nil {
					return counts, err
				}
				if n != 1 {
					return counts, domain.ErrNotReady
				}
			}
		}
		manifests[id], releaseRows[id] = manifest, append(recoveryRow{}, row...)
	}
	resourceTable, imports := recoveryResourceTables[0], recoveryResourceTables[1]
	for _, row := range data["resources"] {
		if ids.Resources[resourceTable.value(row, "id")] == "" {
			return counts, domain.ErrNotReady
		}
		if err = readObject(resourceTable.value(row, "content_hash")); err != nil {
			return counts, err
		}
		if hash := resourceTable.value(row, "content_hash"); hash != "" {
			copyHashes[hash] = true
		}
		if head := resourceTable.value(row, "head_revision"); head != "" {
			found := false
			for _, ref := range manifests[head].Artifacts {
				if ref.ResourceID == resourceTable.value(row, "id") && ref.Hash == resourceTable.value(row, "content_hash") {
					found = true
				}
			}
			if !found || releases.value(releaseRows[head], "status") != "PUBLISHED" || manifests[head].BranchID != resourceTable.value(row, "branch_id") {
				return counts, domain.ErrNotReady
			}
		}
	}
	for _, row := range data["source_imports"] {
		var report retrieval.ImportReport
		if ids.Imports[imports.value(row, "id")] == "" || (!expiry.expired(imports.name, row, imports, "report") && decodeRecoveryDocument([]byte(imports.value(row, "report")), &report) != nil) {
			return counts, domain.ErrNotReady
		}
		hash := imports.value(row, "source_hash")
		if err = copyRetainedObject(row, imports, "source_hash", hash); err != nil {
			return counts, err
		}
		size, e := strconv.ParseInt(imports.value(row, "source_size"), 10, 64)
		if e != nil || size < 0 || (!expiry.expired(imports.name, row, imports, "source_hash") && size != int64(len(objects[hash]))) {
			return counts, domain.ErrNotReady
		}
	}
	remapped, err := remapRecoveryManifests(manifests, releaseRows, ids)
	if err != nil {
		return counts, err
	}
	jointGroups, err := s.recoveryJointGroupsTx(ctx, tx, ids, remapped)
	if err != nil {
		return counts, err
	}
	plans, err := remapRecoveryWork(data, manifests, remapped, ids, expiry)
	if err != nil {
		return counts, err
	}
	for _, table := range recoveryResourceTables {
		for _, row := range data[table.name] {
			if table.name == "artifact_releases" && !expiry.expired(table.name, row, table, "manifest") {
				id := table.value(row, "id")
				if adopted := table.value(row, "adopted_hash"); adopted != "" && adopted != table.value(row, "content_hash") {
					return counts, domain.ErrNotReady
				}
				raw, e := json.Marshal(remapped[id])
				if e != nil {
					return counts, e
				}
				hash := digest(string(raw))
				// Remap the genuinely retained inline document, but do not recreate
				// an erased file. Its independent expiry proof binds this new inline
				// identity while keeping the original erased object and clock intact.
				if !expiry.expired(table.name, row, table, "content_hash") {
					objects[hash] = raw
					copyHashes[hash] = true
				}
				table.set(row, "manifest", string(raw))
				table.set(row, "content_hash", hash)
				if table.value(row, "adopted_hash") != "" {
					table.set(row, "adopted_hash", hash)
				}
			}
			if table.name == "resource_drafts" && !expiry.expired(table.name, row, table, "body") && rejectExpiredRuntime([]byte(table.value(row, "body")), "resource_drafts.body") != nil {
				return counts, domain.ErrMaterialExpired
			}
			if table.name == "resource_policy_drafts" && !expiry.expired(table.name, row, table, "payload") {
				var policy *domain.KnowledgePolicy
				if decodeRecoveryDocument([]byte(table.value(row, "payload")), &policy) != nil || (policy != nil && policy.Validate() != nil) {
					return counts, domain.ErrNotReady
				}
			}
			if table.name == "import_drafts" && !expiry.expired(table.name, row, table, "payload") {
				var draft domain.ImportDraftContent
				raw := []byte(table.value(row, "payload"))
				if digest(string(raw)) != table.value(row, "content_hash") || decodeRecoveryDocument(raw, &draft) != nil || draft.Validate() != nil {
					return counts, domain.ErrNotReady
				}
				if mapped := ids.Revisions[draft.BaseIPHead]; mapped != "" {
					draft.BaseIPHead = mapped
				}
				draft.OperationID = "recovery:" + ids.TargetProject + ":" + ids.Imports[table.value(row, "import_id")]
				raw, e := json.Marshal(draft)
				if e != nil {
					return counts, e
				}
				table.set(row, "payload", string(raw))
				table.set(row, "content_hash", digest(string(raw)))
			}
			for _, column := range strings.Split(table.columns, ",") {
				var mapping map[string]string
				switch column {
				case "project_id":
					table.set(row, column, ids.TargetProject)
					continue
				case "id":
					switch table.name {
					case "resources":
						mapping = ids.Resources
					case "source_imports":
						mapping = ids.Imports
					case "artifact_releases":
						mapping = ids.Releases
					case "change_plans":
						mapping = plans
					}
				case "plan_id":
					mapping = plans
				case "candidate_id":
					if table.value(row, column) == "" {
						continue
					}
					mapping = ids.Revisions
				case "branch_id":
					mapping = ids.Branches
				case "resource_id":
					mapping = ids.Resources
				case "head_revision", "base_head", "release_id":
					mapping = ids.Releases
				case "import_id":
					mapping = ids.Imports
				}
				if mapping != nil {
					mapped, e := recoveredID(mapping, table.value(row, column))
					if e != nil {
						return counts, e
					}
					table.set(row, column, mapped)
				}
			}
			if table.name == "source_imports" {
				table.set(row, "operation_id", "recovery:"+ids.TargetProject+":"+table.value(row, "id"))
				table.set(row, "request_hash", digest(ids.TargetProject+"\x00"+table.value(row, "branch_id")+"\x00"+table.value(row, "filename")+"\x00"+table.value(row, "source_hash")))
			}
		}
	}
	expiryMapping, expiryGraph, err := expiry.rebind(ids.SourceProject, ids.TargetProject, recoveryResourceTables, data, retention.RecoveryExpiryMap{Resources: ids.Resources, Revisions: ids.Revisions, Releases: ids.Releases})
	if err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT resource_recovery_copy"); err != nil {
		return counts, err
	}
	defer func() {
		if err != nil {
			cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer finish()
			if _, e := tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT resource_recovery_copy"); e != nil {
				_ = tx.Rollback()
			}
			counts = RecoveryResourceCounts{}
		}
	}()
	for hash, body := range objects {
		if !copyHashes[hash] {
			continue
		}
		object, e := objectwriter.Write(ctx, tx, s.Blobs, ids.TargetProject, body)
		if e != nil {
			return counts, e
		}
		if object.Hash != hash {
			return counts, domain.ErrNotReady
		}
	}
	for _, table := range recoveryResourceTables {
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
		return counts, err
	}
	if err = s.writeRecoveredJointGroupsTx(ctx, tx, actorID, ids.TargetProject, jointGroups); err != nil {
		return counts, err
	}
	for _, table := range recoveryResourceTables {
		for _, row := range data[table.name] {
			primary := map[string]string{}
			switch table.name {
			case "resources", "artifact_releases", "source_imports", "change_plans":
				primary["id"] = table.value(row, "id")
			case "change_plan_releases":
				primary["plan_id"] = table.value(row, "plan_id")
			case "patch_drafts":
				primary["branch_id"], primary["actor_id"] = table.value(row, "branch_id"), table.value(row, "actor_id")
			case "source_import_candidates":
				primary["import_id"], primary["release_id"] = table.value(row, "import_id"), table.value(row, "release_id")
			case "import_drafts":
				primary["import_id"], primary["actor_id"] = table.value(row, "import_id"), table.value(row, "actor_id")
			default:
				primary["resource_id"], primary["actor_id"] = table.value(row, "resource_id"), table.value(row, "actor_id")
			}
			if err = s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: table.name, Primary: primary}); err != nil {
				return counts, err
			}
		}
	}
	if err = recoveryWindowTx(ctx, tx, ids.SourceProject); err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT resource_recovery_copy"); err != nil {
		return counts, err
	}
	return RecoveryResourceCounts{Resources: len(data["resources"]), Releases: len(data["artifact_releases"]), Imports: len(data["source_imports"]), Drafts: len(data["resource_drafts"]) + len(data["import_drafts"]) + len(data["patch_drafts"]), Plans: len(data["change_plans"])}, nil
}

func remapRecoveryManifests(original map[string]domain.Manifest, rows map[string]recoveryRow, ids RecoveryResourceIDs) (map[string]domain.Manifest, error) {
	result := map[string]domain.Manifest{}
	positions := map[string]map[string]int{}
	for id, manifest := range original {
		raw, _ := json.Marshal(manifest)
		var next domain.Manifest
		if json.Unmarshal(raw, &next) != nil {
			return nil, domain.ErrNotReady
		}
		result[id] = next
		positions[id] = map[string]int{}
		for i, ref := range manifest.Artifacts {
			if _, exists := positions[id][ref.ResourceID]; exists {
				return nil, domain.ErrNotReady
			}
			positions[id][ref.ResourceID] = i
		}
	}
	state := map[string]int{}
	var rewrite func(string, string, int) error
	rewrite = func(release, resource string, depth int) error {
		key := release + ":" + resource
		if state[key] == 2 {
			return nil
		}
		if state[key] == 1 || depth > 256 {
			return domain.ErrNotReady
		}
		i, ok := positions[release][resource]
		if !ok {
			return domain.ErrNotReady
		}
		state[key] = 1
		source := original[release].Artifacts[i]
		m := result[release]
		ref := &m.Artifacts[i]
		if ref.Dependencies != nil {
			if ref.Dependencies.Validate() != nil {
				return domain.ErrNotReady
			}
			for j := range ref.Dependencies.Inputs {
				d := &ref.Dependencies.Inputs[j]
				var e error
				switch d.Source.Kind {
				case "resource":
					index, exists := positions[d.Source.Revision][d.Source.ID]
					if !exists {
						return domain.ErrNotReady
					}
					old := original[d.Source.Revision].Artifacts[index]
					if original[d.Source.Revision].BranchID != d.Source.BranchID || resourceNodeHash(old.Hash, old.Policy, old.Dependencies) != d.Source.Hash {
						return domain.ErrNotReady
					}
					if e = rewrite(d.Source.Revision, d.Source.ID, depth+1); e != nil {
						return e
					}
					copied := result[d.Source.Revision].Artifacts[index]
					d.Source.Hash = resourceNodeHash(copied.Hash, copied.Policy, copied.Dependencies)
					d.Source.ID, e = recoveredID(ids.Resources, d.Source.ID)
					if e != nil {
						return e
					}
					d.Source.Revision, e = recoveredID(ids.Releases, d.Source.Revision)
				case "skill":
					d.Source.ID, e = recoveredID(ids.Skills, d.Source.ID)
					if e != nil {
						return e
					}
					d.Source.Revision, e = recoveredID(ids.Skills, d.Source.Revision)
				default:
					d.Source.Revision, e = recoveredID(ids.Revisions, d.Source.Revision)
				}
				if e != nil {
					return e
				}
				d.Source.BranchID, e = recoveredID(ids.Branches, d.Source.BranchID)
				if e != nil {
					return e
				}
			}
		}
		var e error
		ref.ResourceID, e = recoveredID(ids.Resources, resource)
		if e != nil {
			return e
		}
		ref.ExpectedHead, e = recoveredID(ids.Releases, source.ExpectedHead)
		if e != nil {
			return e
		}
		ref.SourceImportID, e = recoveredID(ids.Imports, source.SourceImportID)
		if e != nil {
			return e
		}
		origin := ref.Origin
		known := origin != nil && !(len(origin.Recovery) > 0 && !origin.Recovery[len(origin.Recovery)-1].OriginKnown)
		hop := domain.ArtifactRecoveryEvidence{ProjectID: ids.SourceProject, BranchID: original[release].BranchID, ReleaseID: release, ResourceID: resource, ManifestHash: recoveryResourceTables[2].value(rows[release], "content_hash"), ContentHash: source.Hash, OriginHash: domain.ArtifactEvidenceHash(source.Origin), OriginKnown: known}
		if origin == nil {
			origin = &domain.ArtifactOrigin{Kind: "recovered", ActorID: recoveryResourceTables[2].value(rows[release], "created_by")}
			ref.Origin = origin
		}
		if origin.Generation != nil {
			hop.GenerationHash = domain.ArtifactEvidenceHash(origin.Generation)
		}
		origin.Recovery = append(origin.Recovery, hop)
		origin.ResourceID, origin.BaseHead, origin.ContentHash = ref.ResourceID, ref.ExpectedHead, ref.Hash
		origin.BranchID, e = recoveredID(ids.Branches, original[release].BranchID)
		if e != nil {
			return e
		}
		origin.IPHead, e = recoveredID(ids.Revisions, manifestIPHead(original[release]))
		if e != nil {
			return e
		}
		origin.ParentReleaseID, e = recoveredID(ids.Releases, origin.ParentReleaseID)
		if e != nil {
			return e
		}
		origin.ImportID, e = recoveredID(ids.Imports, origin.ImportID)
		if e != nil {
			return e
		}
		if source.Origin == nil && source.SourceImportID != "" {
			origin.ImportID, origin.ImportHash = ref.SourceImportID, ref.OriginalHash
		}
		origin.ReviewRequired = origin.NeedsIndependentReview()
		if origin.ValidateTarget(origin.BranchID, *ref) != nil {
			return domain.ErrNotReady
		}
		result[release] = m
		state[key] = 2
		return nil
	}
	for id, m := range original {
		for _, ref := range m.Artifacts {
			if e := rewrite(id, ref.ResourceID, 0); e != nil {
				return nil, e
			}
		}
	}
	for id, m := range result {
		var e error
		m.Schema = 2
		m.ReleaseID = ids.Releases[id]
		m.BranchID, e = recoveredID(ids.Branches, m.BranchID)
		if e != nil {
			return nil, e
		}
		m.ExpectedIPHead, e = recoveredID(ids.Revisions, m.ExpectedIPHead)
		if e != nil {
			return nil, e
		}
		m.IPCandidateID, e = recoveredID(ids.Revisions, m.IPCandidateID)
		if e != nil {
			return nil, e
		}
		for i, parent := range m.PatchParents {
			m.PatchParents[i], e = recoveredID(ids.Releases, parent)
			if e != nil {
				return nil, e
			}
		}
		for i, parent := range m.CompositionParents {
			m.CompositionParents[i], e = recoveredID(ids.Releases, parent)
			if e != nil {
				return nil, e
			}
		}
		for i, peer := range m.PeerReleases {
			m.PeerReleases[i], e = recoveredID(ids.Releases, peer)
			if e != nil {
				return nil, e
			}
		}
		if m.PatchGroup != nil {
			if e = remapRecoveryPatchGroup(m.PatchGroup, ids); e != nil {
				return nil, e
			}
		}
		result[id] = m
	}
	return result, nil
}
