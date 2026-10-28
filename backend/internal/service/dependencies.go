package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"reflect"
	"slices"
	"strings"
)

type DependencyEntry = domain.DependencyEntry

func valueHash(v any) string { b, _ := json.Marshal(v); return digest(string(b)) }
func resourceNodeHash(hash string, p *domain.KnowledgePolicy, d *domain.DependencyPolicy) string {
	return valueHash(struct {
		Hash         string
		Policy       *domain.KnowledgePolicy
		Dependencies *domain.DependencyPolicy
	}{hash, p, d})
}
func resourceDependenciesTx(ctx context.Context, tx *sql.Tx, project, id, revision string) (*domain.DependencyPolicy, error) {
	if revision == "" {
		return nil, nil
	}
	r, err := releaseTx(ctx, tx, project, revision)
	if err != nil {
		return nil, err
	}
	if r.Status != "PUBLISHED" {
		return nil, domain.ErrNotReady
	}
	for _, ref := range r.Manifest.Artifacts {
		if ref.ResourceID == id {
			return ref.Dependencies, nil
		}
	}
	return nil, domain.ErrNotReady
}
func snapshotEntries(branch, revision, name string, s domain.Snapshot) []DependencyEntry {
	items := []DependencyEntry{}
	add := func(kind, id, label string, v any) {
		items = append(items, DependencyEntry{Ref: domain.DependencyRef{Kind: kind, BranchID: branch, ID: id, Revision: revision, Hash: valueHash(v)}, Name: label, BranchName: name})
	}
	for _, e := range s.Entities {
		add("entity", e.ID, e.Name, e)
	}
	for _, a := range s.Assertions {
		add("assertion", a.ID, a.Subject+" · "+a.Predicate, a)
	}
	for _, r := range s.Rules {
		add("rule", r.ID, r.Name, r)
	}
	for _, e := range s.Events {
		add("event", e.ID, e.Name, e)
	}
	for _, m := range s.Mappings {
		add("mapping", m.ID, "实体映射 · "+m.ID, m)
	}
	return items
}
func queryIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func dependencyCatalogTx(ctx context.Context, tx *sql.Tx, project string) ([]DependencyEntry, []domain.Resource, error) {
	entries := []DependencyEntry{}
	resources := []domain.Resource{}
	branches, err := queryIDs(ctx, tx, "SELECT id FROM branches WHERE project_id=? ORDER BY id", project)
	if err != nil {
		return nil, nil, err
	}
	names := map[string]string{}
	for _, id := range branches {
		b, err := branchTx(ctx, tx, project, id)
		if err != nil {
			return nil, nil, err
		}
		names[id] = b.Name
		s, err := snapshotTx(ctx, tx, project, b.Head)
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, snapshotEntries(id, b.Head, b.Name, s)...)
	}
	ids, err := queryIDs(ctx, tx, "SELECT id FROM resources WHERE project_id=? ORDER BY id", project)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		r, err := resourceTx(ctx, tx, project, id)
		if err != nil {
			return nil, nil, err
		}
		resources = append(resources, r)
		if r.Head != "" {
			entries = append(entries, DependencyEntry{Ref: domain.DependencyRef{Kind: "resource", BranchID: r.BranchID, ID: id, Revision: r.Head, Hash: resourceNodeHash(r.Hash, r.Policy, r.Dependencies)}, Name: r.Name, BranchName: names[r.BranchID]})
		}
	}
	ids, err = queryIDs(ctx, tx, "SELECT id FROM skill_packages WHERE project_id=? AND state='REFERENCE' ORDER BY id", project)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		v, err := skillTx(ctx, tx, project, id)
		if err != nil {
			return nil, nil, err
		}
		available, err := skillAvailable(ctx, tx, project, id)
		if err != nil {
			return nil, nil, err
		}
		if available {
			entries = append(entries, DependencyEntry{Ref: domain.DependencyRef{Kind: "skill", ID: id, Revision: id, Hash: v.Report.Hash}, Name: v.Report.Name, BranchName: "方法库"})
		}
	}
	return entries, resources, nil
}
func (s *IP) DependencyCatalog(ctx context.Context, a domain.Actor, project string) ([]DependencyEntry, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items, _, err := dependencyCatalogTx(ctx, tx, project)
	return items, err
}

func validateDependencySource(ctx context.Context, tx *sql.Tx, project string, d domain.Dependency) error {
	ref := d.Source
	if ref.Kind == "skill" {
		v, err := skillTx(ctx, tx, project, ref.ID)
		if err != nil {
			return err
		}
		available, err := skillAvailable(ctx, tx, project, ref.ID)
		if err != nil {
			return err
		}
		if !available || v.State != "REFERENCE" || ref.Revision != v.ID || ref.Hash != v.Report.Hash || ref.BranchID != "" {
			return domain.ErrConflict
		}
		return nil
	}
	b, err := branchTx(ctx, tx, project, ref.BranchID)
	if err != nil {
		return err
	}
	if ref.Kind == "resource" {
		r, err := resourceTx(ctx, tx, project, ref.ID)
		if err != nil {
			return err
		}
		if r.BranchID != ref.BranchID {
			return domain.ErrForbidden
		}
		if r.Policy != nil && r.Policy.Withdrawn {
			return domain.ErrConflict
		}
		if !d.Pinned && r.Head != ref.Revision {
			return domain.ErrConflict
		}
		release, err := releaseTx(ctx, tx, project, ref.Revision)
		if err != nil {
			return err
		}
		if release.Status != "PUBLISHED" {
			return domain.ErrConflict
		}
		for _, f := range release.Manifest.Artifacts {
			if f.ResourceID == r.ID && resourceNodeHash(f.Hash, f.Policy, f.Dependencies) == ref.Hash {
				if f.Policy != nil && f.Policy.Withdrawn {
					return domain.ErrConflict
				}
				return nil
			}
		}
		return domain.ErrConflict
	}
	if !d.Pinned && ref.Revision != b.Head {
		return domain.ErrConflict
	}
	// An inherited base belongs to its parent; all later versions must belong to this branch.
	if ref.Revision != b.BaseRevision {
		c, err := candidateTx(ctx, tx, b.ID, ref.Revision)
		if err != nil {
			return err
		}
		if c.Status != "PUBLISHED" {
			return domain.ErrConflict
		}
	}
	snapshot, err := snapshotTx(ctx, tx, project, ref.Revision)
	if err != nil {
		return err
	}
	for _, e := range snapshotEntries(b.ID, ref.Revision, b.Name, snapshot) {
		if e.Ref == ref {
			return nil
		}
	}
	return domain.ErrConflict
}
func (s *IP) validateArtifactDependencies(ctx context.Context, tx *sql.Tx, project string, m domain.Manifest, r domain.Resource, ref domain.ArtifactRef) error {
	if ref.Dependencies != nil {
		if err := ref.Dependencies.Validate(); err != nil {
			return err
		}
	}
	if ref.FrozenChangeReason != "" && !validName(ref.FrozenChangeReason, 2000) {
		return domain.ErrValidation
	}
	changed := r.Hash != ref.Hash || !reflect.DeepEqual(r.Policy, ref.Policy) || !reflect.DeepEqual(r.Dependencies, ref.Dependencies)
	if changed && r.Dependencies != nil && r.Dependencies.Frozen && strings.TrimSpace(ref.FrozenChangeReason) == "" {
		return domain.ErrValidation
	}
	if ref.Dependencies == nil {
		return nil
	}
	for _, d := range ref.Dependencies.Inputs {
		if d.Source.Kind == "resource" && d.Source.ID == r.ID {
			return domain.ErrValidation
		}
		if matched, err := prospectiveDependency(ctx, tx, m, d); matched {
			if err != nil {
				return err
			}
			continue
		}
		if matched, err := validatePeerDependencyTx(ctx, tx, project, m, d); matched || err != nil {
			if err != nil {
				return err
			}
			continue
		}
		if err := validateDependencySource(ctx, tx, project, d); err != nil {
			return err
		}
		// A follow-latest link cannot silently use a superseded input in a joint publication.
		if !d.Pinned && d.Source.Kind == "resource" {
			for _, f := range m.Artifacts {
				if f.ResourceID == d.Source.ID {
					return domain.ErrConflict
				}
			}
		}
		if !d.Pinned && d.Source.Kind != "resource" && d.Source.Kind != "skill" && d.Source.BranchID == m.BranchID && m.IPCandidateID != "" {
			return domain.ErrConflict
		}
	}
	return nil
}

type ChangePlanInput struct {
	OperationID string `json:"operation_id"`
	BranchID    string `json:"branch_id"`
	CandidateID string `json:"candidate_id"`
	ReleaseID   string `json:"release_id,omitempty"`
}
type ChangePlan = domain.ChangePlan
type dependencyGraph = domain.DependencyGraph

func dependencyGraphTx(ctx context.Context, tx *sql.Tx, project string) (dependencyGraph, error) {
	g := dependencyGraph{Links: []domain.DependencyLink{}, Frozen: map[string]bool{}, Untracked: []domain.DependencyRef{}}
	entries, resources, err := dependencyCatalogTx(ctx, tx, project)
	g.Entries = entries
	if err != nil {
		return g, err
	}
	for _, r := range resources {
		if r.Head == "" {
			continue
		}
		target := domain.DependencyRef{Kind: "resource", BranchID: r.BranchID, ID: r.ID, Revision: r.Head, Hash: resourceNodeHash(r.Hash, r.Policy, r.Dependencies)}
		if r.Dependencies == nil || len(r.Dependencies.Inputs) == 0 {
			g.Untracked = append(g.Untracked, target)
		}
		if r.Dependencies != nil {
			g.Frozen[target.Key()] = r.Dependencies.Frozen
			for _, d := range r.Dependencies.Inputs {
				g.Links = append(g.Links, domain.DependencyLink{Source: d.Source, Target: target, Kind: d.Kind, Reason: d.Reason, Pinned: d.Pinned})
			}
		}
	}
	return g, nil
}
func readChangePlanTx(ctx context.Context, tx *sql.Tx, project, id string) (ChangePlan, error) {
	var plan ChangePlan
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT payload,content_hash FROM change_plans WHERE id=? AND project_id=?", id, project).Scan(&raw, &hash)
	if err == sql.ErrNoRows {
		return plan, domain.ErrForbidden
	}
	if err != nil {
		return plan, err
	}
	if err := rejectExpiredRuntime(raw, "change_plans.payload"); err != nil {
		return plan, err
	}
	if json.Unmarshal(raw, &plan) != nil || valueHash(plan) != hash {
		return plan, domain.ErrNotReady
	}
	return plan, nil
}
func (s *IP) CreateChangePlan(ctx context.Context, a domain.Actor, project string, input ChangePlanInput) (ChangePlan, error) {
	var plan ChangePlan
	if !validName(input.OperationID, 120) || (input.CandidateID == "") == (input.ReleaseID == "") {
		return plan, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	requestHash := valueHash(struct {
		Kind, Project string
		Input         ChangePlanInput
	}{"change.plan", project, input})
	var stored, id string
	stored, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if stored != requestHash {
			return plan, domain.ErrConflict
		}
		return readChangePlanTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return plan, err
	}
	b, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return plan, err
	}
	before, err := snapshotTx(ctx, tx, project, b.Head)
	if err != nil {
		return plan, err
	}
	c := Candidate{Snapshot: before}
	var release domain.Release
	candidateID := input.CandidateID
	if input.ReleaseID != "" {
		release, err = releaseTx(ctx, tx, project, input.ReleaseID)
		if err != nil {
			return plan, err
		}
		if release.Manifest.BranchID != b.ID {
			return plan, domain.ErrForbidden
		}
		if !slices.Contains([]string{"PREPARED", "READY"}, release.Status) {
			return plan, domain.ErrConflict
		}
		if err = s.validateManifest(ctx, tx, project, release.Manifest); err != nil {
			return plan, err
		}
		candidateID = release.Manifest.IPCandidateID
	}
	if candidateID != "" {
		c, err = candidateTx(ctx, tx, b.ID, candidateID)
		if err != nil {
			return plan, err
		}
		if c.BaseHead != b.Head || !slices.Contains([]string{"PREPARED", "READY"}, c.Status) {
			return plan, domain.ErrConflict
		}
	}
	old, newer := snapshotEntries(b.ID, b.Head, b.Name, before), snapshotEntries(b.ID, c.ID, b.Name, c.Snapshot)
	refs := map[string]domain.DependencyChange{}
	for _, e := range old {
		refs[e.Ref.Key()] = domain.DependencyChange{Before: e.Ref}
	}
	for _, e := range newer {
		v := refs[e.Ref.Key()]
		v.After = e.Ref
		refs[e.Ref.Key()] = v
	}
	changes := []domain.DependencyChange{}
	for _, v := range refs {
		if v.Before.Hash != v.After.Hash {
			changes = append(changes, v)
		}
	}
	slices.SortFunc(changes, func(a, b domain.DependencyChange) int {
		return strings.Compare(a.Before.Key()+a.After.Key(), b.Before.Key()+b.After.Key())
	})
	g, err := dependencyGraphTx(ctx, tx, project)
	if err != nil {
		return plan, err
	}
	plan = ChangePlan{ID: NewID(), BranchID: b.ID, CandidateID: c.ID, BaseHead: b.Head, GraphHash: valueHash(g), Changes: changes, Items: domain.DependencyImpact(changes, g.Links, g.Frozen), Untracked: g.Untracked, OutsideRecordedClosure: []domain.DependencyRef{}, SemanticReview: "NOT_RUN"}
	plan.Differences = dependencyDifferences(changes, before, c.Snapshot)
	if input.ReleaseID != "" {
		plan.ReleaseID = input.ReleaseID
		if err = s.appendResourceImpact(ctx, tx, project, &plan, release); err != nil {
			return plan, err
		}
		plan.Items = domain.DependencyImpact(plan.Changes, g.Links, g.Frozen)
	}
	affected := map[string]bool{}
	for _, change := range plan.Changes {
		if change.Before.ID != "" {
			affected[change.Before.Key()] = true
		}
		if change.After.ID != "" {
			affected[change.After.Key()] = true
		}
	}
	for _, item := range plan.Items {
		affected[item.Node.Key()] = true
	}
	for _, entry := range g.Entries {
		if entry.Ref.Kind == "resource" && !affected[entry.Ref.Key()] {
			plan.OutsideRecordedClosure = append(plan.OutsideRecordedClosure, entry.Ref)
		}
	}
	plan.GraphSnapshot = &g
	raw, _ := json.Marshal(plan)
	var storedCandidate any
	if c.ID != "" {
		storedCandidate = c.ID
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO change_plans(id,project_id,branch_id,candidate_id,payload,content_hash,created_by) VALUES(?,?,?,?,?,?,?)", plan.ID, project, b.ID, storedCandidate, raw, valueHash(plan), a.ID); err != nil {
		return plan, err
	}
	if plan.ReleaseID != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO change_plan_releases(plan_id,release_id) VALUES(?,?)", plan.ID, plan.ReleaseID); err != nil {
			return plan, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'change.plan',?,?)", a.ID, input.OperationID, requestHash, plan.ID); err != nil {
		return plan, err
	}
	if err = audit(ctx, tx, a, "change.plan", plan.ID); err != nil {
		return plan, err
	}
	if err = s.captureChangePlanSourceTx(ctx, tx, project, plan.ID); err != nil {
		return plan, err
	}
	return plan, tx.Commit()
}
func (s *IP) ReadChangePlan(ctx context.Context, a domain.Actor, project, id string) (ChangePlan, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return ChangePlan{}, err
	}
	defer tx.Rollback()
	plan, err := readChangePlanTx(ctx, tx, project, id)
	if err != nil {
		return plan, err
	}
	g, err := dependencyGraphTx(ctx, tx, project)
	if err != nil {
		return plan, err
	}
	b, err := branchTx(ctx, tx, project, plan.BranchID)
	if err != nil {
		return plan, err
	}
	plan.Stale = plan.SourceState == "SUPERSEDED" || valueHash(g) != plan.GraphHash || b.Head != plan.BaseHead
	return plan, nil
}
