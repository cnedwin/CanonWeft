package retention

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"

	"novelstudio/internal/domain"
)

// ContentReference is a source-bound preservation root. It carries no private
// body and does not assert that the physical blob exists or permit deletion.
type ContentReference struct {
	ID, Table, OwnerKey, Field, ObjectHash string
}

type adoptedContentGraph struct {
	index             *SourceIndex
	project           string
	refs              map[string]ContentReference
	manifests         map[string]domain.Manifest
	visiting, visited map[string]bool
}

// AdoptedContentReferences resolves adopted/published IP and release history,
// current resource bodies, imported originals and required patch ancestors.
// It deliberately never promotes raw model inputs, outputs or review reports.
// Author drafts, capability packages and derived indexes have separate roots;
// this result alone MUST NOT set Inventory.Complete or authorize object GC.
func (index *SourceIndex) AdoptedContentReferences(project string) ([]ContentReference, error) {
	if index == nil || !id32.MatchString(project) {
		return nil, ErrSourceOwnership
	}
	g := adoptedContentGraph{index: index, project: project, refs: map[string]ContentReference{}, manifests: map[string]domain.Manifest{}, visiting: map[string]bool{}, visited: map[string]bool{}}
	if _, err := g.row("projects", project); err != nil {
		return nil, err
	}
	for _, row := range index.rows["ip_revisions"] {
		owner, err := index.Owner(row.Table, row.Values)
		if err != nil {
			return nil, err
		}
		if owner.ProjectID != project || (row.Values["status"] != "READY" && row.Values["status"] != "PUBLISHED") {
			continue
		}
		if err = g.ip(row); err != nil {
			return nil, err
		}
	}
	for _, row := range index.rows["artifact_releases"] {
		if row.Values["project_id"] != project || (row.Values["status"] != "READY" && row.Values["status"] != "PUBLISHED") {
			continue
		}
		if index.replacedColumn(project, row, "manifest") {
			if err := g.add(row, "content_hash", row.Values["content_hash"]); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := g.release(row.Values["id"], 0); err != nil {
			return nil, err
		}
	}
	for _, row := range index.rows["resources"] {
		if row.Values["project_id"] != project {
			continue
		}
		key, _ := RowOwnerKey(row.Table, row.Values)
		refID, _ := ReferenceID(project, row.Table, key, "content_hash")
		if _, expired := index.expired[refID]; expired {
			continue
		}
		if row.Values["head_revision"] == "" {
			if row.Values["content_hash"] != "" {
				return nil, ErrSourceOwnership
			}
			continue
		}
		if _, err := g.row("resources", row.Values["id"]); err != nil {
			return nil, err
		}
		release, err := g.row("artifact_releases", row.Values["head_revision"])
		if err != nil || release.Values["status"] != "PUBLISHED" || release.Values["branch_id"] != row.Values["branch_id"] {
			return nil, ErrSourceOwnership
		}
		m, err := g.release(release.Values["id"], 0)
		if err != nil {
			return nil, err
		}
		found := false
		for _, ref := range m.Artifacts {
			if ref.ResourceID == row.Values["id"] && ref.Hash == row.Values["content_hash"] {
				found = true
			}
		}
		if !found {
			return nil, ErrSourceOwnership
		}
		if err = g.add(row, "content_hash", row.Values["content_hash"]); err != nil {
			return nil, err
		}
	}
	if err := g.memory(); err != nil {
		return nil, err
	}
	refs := make([]ContentReference, 0, len(g.refs))
	for _, ref := range g.refs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

func (g *adoptedContentGraph) row(table, id string) (SourceRow, error) {
	pk := PrimaryKey(table)
	if len(pk) != 1 || !id32.MatchString(id) {
		return SourceRow{}, ErrSourceOwnership
	}
	row, err := g.index.sourceRow(table, map[string]string{pk[0].Name: id})
	if err != nil {
		return SourceRow{}, err
	}
	owner, err := g.index.Owner(table, row.Values)
	if err != nil || owner.ProjectID != g.project || owner.Shared {
		return SourceRow{}, ErrSourceOwnership
	}
	return row, nil
}

func (g *adoptedContentGraph) add(row SourceRow, field, object string) error {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	id, err := ReferenceID(g.project, row.Table, key, field)
	if _, expired := g.index.expired[id]; err == nil && expired {
		// Independent journal validation happened before this graph walk.
		// A surviving structural row must not resurrect an erased material.
		return nil
	}
	rule, ok := ConcreteRule(row.Table + "." + field)
	if err != nil || !ok || !rule.Promotable || (rule.Storage == Blob && !hash64.MatchString(object)) || (rule.Storage != Blob && object != "") {
		return ErrSourceOwnership
	}
	g.refs[id] = ContentReference{id, row.Table, key, field, object}
	return nil
}

func (g *adoptedContentGraph) ip(row SourceRow) error {
	adoption, err := g.row("adoptions", row.Values["id"])
	if err != nil || adoption.Values["content_hash"] != row.Values["content_hash"] {
		return ErrSourceOwnership
	}
	if g.index.replacedColumn(g.project, row, "payload") {
		return g.add(row, "content_hash", row.Values["content_hash"])
	}
	var snapshot domain.Snapshot
	if contentJSON([]byte(row.Values["payload"]), &snapshot, row.Values["content_hash"]) != nil || domain.ValidateSnapshot(snapshot) != nil {
		return ErrSourceOwnership
	}
	if err = g.add(row, "payload", ""); err != nil {
		return err
	}
	// Delta and promotion bodies are required to reopen an adopted candidate,
	// so they follow its content retention instead of the unadopted TTL.
	for _, table := range []string{"ip_branch_deltas", "ip_promotions"} {
		if aux, ok := g.index.optionalCandidateRow(table, row.Values["id"]); ok {
			owner, e := g.index.Owner(table, aux.Values)
			if e != nil || owner.ProjectID != g.project || owner.Shared {
				return ErrSourceOwnership
			}
			if e = g.add(aux, "payload", ""); e != nil {
				return e
			}
		}
	}
	return g.add(row, "content_hash", row.Values["content_hash"])
}

func (g *adoptedContentGraph) release(id string, depth int) (domain.Manifest, error) {
	var m domain.Manifest
	if depth > 10000 || g.visiting[id] {
		return m, ErrSourceOwnership
	}
	if g.visited[id] {
		return g.manifests[id], nil
	}
	row, err := g.row("artifact_releases", id)
	if err != nil {
		return m, err
	}
	if contentJSON([]byte(row.Values["manifest"]), &m, row.Values["content_hash"]) != nil || (m.Schema != 1 && m.Schema != 2) || (m.ReleaseID != "" && m.ReleaseID != id) || m.BranchID != row.Values["branch_id"] || len(m.Artifacts) == 0 || len(m.Artifacts) > 32 {
		return m, ErrSourceOwnership
	}
	switch row.Values["status"] {
	case "READY", "PUBLISHED":
		if row.Values["adopted_hash"] != row.Values["content_hash"] || !id32.MatchString(row.Values["adopted_by"]) {
			return m, ErrSourceOwnership
		}
	case "PREPARED":
		// Only an explicitly referenced content ancestor can preserve a not-yet-
		// adopted candidate. It does not itself receive approval or execute.
		if depth == 0 {
			return m, ErrSourceOwnership
		}
	default:
		return m, ErrSourceOwnership
	}
	g.visiting[id] = true
	defer delete(g.visiting, id)
	if m.ExpectedIPHead != "" {
		base, e := g.row("ip_revisions", m.ExpectedIPHead)
		lineage, e2 := g.index.branchLineage(m.BranchID, g.project)
		if e != nil || e2 != nil || base.Values["status"] != "PUBLISHED" || !lineage[base.Values["branch_id"]] {
			return m, ErrSourceOwnership
		}
	}
	if m.IPCandidateID != "" {
		candidate, e := g.row("ip_revisions", m.IPCandidateID)
		if e != nil || candidate.Values["branch_id"] != m.BranchID || candidate.Values["base_head"] != m.ExpectedIPHead || (candidate.Values["status"] != "READY" && candidate.Values["status"] != "PUBLISHED") {
			return m, ErrSourceOwnership
		}
		if e = g.ip(candidate); e != nil {
			return m, e
		}
	}
	seen := map[string]bool{}
	for _, ref := range m.Artifacts {
		if seen[ref.ResourceID] || !hash64.MatchString(ref.Hash) || ref.Size < 0 {
			return m, ErrSourceOwnership
		}
		seen[ref.ResourceID] = true
		resource, e := g.row("resources", ref.ResourceID)
		if e != nil || resource.Values["branch_id"] != m.BranchID {
			return m, ErrSourceOwnership
		}
		if m.Schema == 2 || ref.Origin != nil {
			if e = g.origin(m, ref, depth); e != nil {
				return m, e
			}
		}
		if ref.ExpectedHead != "" {
			parent, e := g.row("artifact_releases", ref.ExpectedHead)
			if e != nil || parent.Values["branch_id"] != m.BranchID || parent.Values["status"] != "PUBLISHED" {
				return m, ErrSourceOwnership
			}
			previous, e := g.release(ref.ExpectedHead, depth+1)
			if e != nil {
				return m, e
			}
			found := false
			for _, prior := range previous.Artifacts {
				if prior.ResourceID == ref.ResourceID {
					found = true
				}
			}
			if !found {
				return m, ErrSourceOwnership
			}
		}
		field, _ := ArtifactField(ref.ResourceID, false)
		if e = g.add(row, field, ref.Hash); e != nil {
			return m, e
		}
		if ref.SourceImportID != "" {
			source, e := g.row("source_imports", ref.SourceImportID)
			if e != nil || source.Values["branch_id"] != m.BranchID || source.Values["source_hash"] != ref.OriginalHash {
				return m, ErrSourceOwnership
			}
			field, _ = ArtifactField(ref.ResourceID, true)
			if e = g.add(row, field, ref.OriginalHash); e != nil {
				return m, e
			}
			if e = g.add(source, "source_hash", ref.OriginalHash); e != nil {
				return m, e
			}
			if e = g.add(source, "report", ""); e != nil {
				return m, e
			}
		} else if ref.OriginalHash != "" {
			return m, ErrSourceOwnership
		}
	}
	parents := map[string]bool{}
	for _, parentID := range m.PatchParents {
		if parents[parentID] {
			return m, ErrSourceOwnership
		}
		parents[parentID] = true
		parent, e := g.release(parentID, depth+1)
		if e != nil || parent.BranchID != m.BranchID || (parent.PatchGroup == nil && len(parent.CompositionParents) == 0 && len(parent.PeerReleases) == 0) {
			return m, ErrSourceOwnership
		}
	}
	compositions := map[string]bool{}
	for _, parentID := range m.CompositionParents {
		if compositions[parentID] {
			return m, ErrSourceOwnership
		}
		compositions[parentID] = true
		parent, e := g.release(parentID, depth+1)
		if e != nil || parent.BranchID != m.BranchID {
			return m, ErrSourceOwnership
		}
	}
	peers := map[string]bool{}
	for _, peerID := range m.PeerReleases {
		if peers[peerID] {
			return m, ErrSourceOwnership
		}
		peers[peerID] = true
		peer, e := g.release(peerID, depth+1)
		if e != nil || peer.BranchID == m.BranchID {
			return m, ErrSourceOwnership
		}
	}
	if err = g.add(row, "manifest", ""); err != nil {
		return m, err
	}
	if err = g.add(row, "content_hash", row.Values["content_hash"]); err != nil {
		return m, err
	}
	g.visited[id], g.manifests[id] = true, m
	return m, nil
}

func (g *adoptedContentGraph) origin(m domain.Manifest, ref domain.ArtifactRef, depth int) error {
	o := ref.Origin
	if o == nil || o.ValidateTarget(m.BranchID, ref) != nil {
		return ErrSourceOwnership
	}
	ipHead := m.ExpectedIPHead
	if m.IPCandidateID != "" {
		ipHead = m.IPCandidateID
	}
	if o.IPHead != ipHead {
		return ErrSourceOwnership
	}
	actor, err := g.index.Owner("users", map[string]string{"id": o.ActorID})
	project, err2 := g.index.Owner("projects", map[string]string{"id": g.project})
	if err != nil || err2 != nil || actor.WorkspaceID != project.WorkspaceID {
		return ErrSourceOwnership
	}
	if o.ParentReleaseID != "" {
		parent, err := g.release(o.ParentReleaseID, depth+1)
		if err != nil || parent.BranchID != m.BranchID {
			return ErrSourceOwnership
		}
		found := false
		for _, prior := range parent.Artifacts {
			if prior.ResourceID == ref.ResourceID && prior.Hash == o.ParentHash {
				found = true
			}
		}
		if !found {
			return ErrSourceOwnership
		}
	}
	if o.ImportID != "" {
		source, err := g.row("source_imports", o.ImportID)
		if err != nil || source.Values["branch_id"] != m.BranchID || source.Values["source_hash"] != o.ImportHash {
			return ErrSourceOwnership
		}
		if err = g.add(source, "source_hash", o.ImportHash); err != nil {
			return err
		}
		if err = g.add(source, "report", ""); err != nil {
			return err
		}
	}
	if ref.SourceImportID != "" && (o.ImportID != ref.SourceImportID || o.ImportHash != ref.OriginalHash) {
		return ErrSourceOwnership
	}
	if len(o.Recovery) > 0 {
		latest := o.Recovery[len(o.Recovery)-1]
		if latest.ProjectID == g.project || latest.BranchID == m.BranchID {
			return ErrSourceOwnership
		}
	}
	if evidence := o.Generation; evidence != nil && len(o.Recovery) == 0 {
		generation, err := g.row("generation_tasks", evidence.GenerationID)
		call, err2 := g.row("model_calls", evidence.CallID)
		if err != nil || err2 != nil || generation.Values["branch_id"] != m.BranchID || generation.Values["call_id"] != evidence.CallID || generation.Values["input_hash"] != evidence.InputHash || call.Values["output_hash"] != evidence.OutputHash {
			return ErrSourceOwnership
		}
		// Matching historical identities does not preserve the frozen request or
		// provider response, and does not award current execution qualification.
	}
	return nil
}

// Match the original typed writer hash while allowing MySQL JSON whitespace
// and key order. Duplicate/unknown/case-aliased fields and omitted/null variants
// cannot hide additional object references. No arbitrary JSON path traversal.
func contentJSON(raw []byte, target any, hash string) error {
	if len(raw) == 0 || len(raw) > 16<<20 || !hash64.MatchString(hash) {
		return ErrSourceOwnership
	}
	shape := json.NewDecoder(bytes.NewReader(raw))
	shape.UseNumber()
	if financialJSONValue(shape, 0) != nil {
		return ErrSourceOwnership
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrSourceOwnership
	}
	canonical, err := json.Marshal(target)
	if err != nil || financialDigest(canonical) != hash {
		return ErrSourceOwnership
	}
	normalize := func(value []byte) ([]byte, error) {
		var v any
		d := json.NewDecoder(bytes.NewReader(value))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		return json.Marshal(v)
	}
	a, err := normalize(raw)
	if err != nil {
		return ErrSourceOwnership
	}
	b, err := normalize(canonical)
	if err != nil || !bytes.Equal(a, b) {
		return ErrSourceOwnership
	}
	return nil
}
