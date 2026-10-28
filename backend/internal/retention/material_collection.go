package retention

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"novelstudio/internal/domain"
)

// MaterialReference binds a specific inline value or blob reference to the
// actual SQL source column and its observed digest. It is not a registered
// expiry revision, proof of blob existence, or authority to remove anything.
type MaterialReference struct {
	ID, Table, OwnerKey, Field, PayloadHash, ObjectHash string
	SourceID, SourcePayloadHash                         string
	Storage                                             StorageKind
	Class                                               Class
	ContentRoot                                         bool
	Accounting                                          *AccountingMetadata
	Sources                                             []MaterialSource
	DraftVersion                                        *DraftVersionEvidence
	WorkingProjection                                   *WorkingProjectionEvidence
	TextProof                                           *TextCopyEvidence
	UnboundSources                                      int
}

type materialCollector struct {
	index   *SourceIndex
	project string
	refs    map[string]MaterialReference
}

// MaterialReferences includes private scalar/key columns, the explicit nested
// blob catalog and closed accounting projections. Complete lineage remains
// separate: this result alone must never set Inventory.Complete.
func (index *SourceIndex) MaterialReferences(project string) ([]MaterialReference, error) {
	if index == nil || !id32.MatchString(project) {
		return nil, ErrSourceOwnership
	}
	roots, err := index.AdoptedContentReferences(project)
	if err != nil {
		return nil, err
	}
	author, err := index.AuthorContentReferences(project)
	if err != nil {
		return nil, err
	}
	roots = append(roots, author...)
	g := materialCollector{index: index, project: project, refs: map[string]MaterialReference{}}
	byTable := map[string][]ReferenceRule{}
	for _, rule := range referenceRules {
		if rule.Storage != Metadata {
			table := strings.SplitN(rule.Key, ".", 2)[0]
			byTable[table] = append(byTable[table], rule)
		}
	}
	for _, row := range index.Records() {
		owner, err := index.Owner(row.Table, row.Values)
		if err != nil {
			return nil, fmt.Errorf("material scope %s: %w", row.Table, err)
		}
		if owner.Shared || owner.ProjectID != project {
			continue
		}
		if row.Table == "generation_previews" && !index.replacedColumn(project, row, "content") && !validPreviewSource(row) {
			return nil, ErrSourceOwnership
		}
		nested := false
		for _, rule := range byTable[row.Table] {
			field := strings.TrimPrefix(rule.Key, row.Table+".")
			key, err := RowOwnerKey(row.Table, row.Values)
			if err != nil {
				return nil, err
			}
			if _, replaced := index.replacedSources[observationID(project, row.Table, key, strings.SplitN(field, ".", 2)[0])]; replaced {
				continue
			}
			if strings.Contains(field, ".") {
				if !nested {
					if err := g.nested(row); err != nil {
						return nil, err
					}
					nested = true
				}
				continue
			}
			value, exists := row.Values[field]
			if !exists {
				return nil, ErrSourceOwnership
			}
			if row.Nulls[field] || (rule.Storage == Blob && value == "") {
				continue
			}
			if err := g.add(row, field, value); err != nil {
				return nil, err
			}
		}
	}
	for _, root := range roots {
		ref, exists := g.refs[root.ID]
		rule, known := ConcreteRule(root.Table + "." + root.Field)
		if !exists || !known || !rule.Promotable || ref.Table != root.Table || ref.Field != root.Field || ref.OwnerKey != root.OwnerKey || ref.ObjectHash != root.ObjectHash {
			return nil, ErrSourceOwnership
		}
		ref.ContentRoot = true
		g.refs[ref.ID] = ref
	}
	result, err := index.AccountingReferences(project)
	if err != nil {
		return nil, err
	}
	for _, ref := range g.refs {
		result = append(result, ref)
	}
	if len(result) > 250000 {
		return nil, ErrSourceOwnership
	}
	if err := index.attachKnownMaterialSources(project, result); err != nil {
		return nil, err
	}
	if err := index.attachWorkingProjections(project, result); err != nil {
		return nil, err
	}
	if err := index.attachDraftTextProofs(result); err != nil {
		return nil, err
	}
	if err := index.attachPrivateDraftSources(project, result); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (g *materialCollector) add(row SourceRow, field, value string) error {
	rule, ok := ConcreteRule(row.Table + "." + field)
	if !ok || rule.Storage == Metadata || (rule.Storage == Blob && !hash64.MatchString(value)) {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	id, err := ReferenceID(g.project, row.Table, key, field)
	if err != nil {
		return err
	}
	if _, expired := g.index.expired[id]; expired {
		return nil
	}
	column := strings.SplitN(field, ".", 2)[0]
	source, exists := row.Values[column]
	if !exists || row.Nulls[column] || !observedSourceColumn(row.Table, column) {
		return ErrSourceOwnership
	}
	if _, duplicate := g.refs[id]; duplicate || len(g.refs) >= 250000 {
		return ErrSourceOwnership
	}
	ref := MaterialReference{
		ID: id, Table: row.Table, OwnerKey: key, Field: field, PayloadHash: financialDigest([]byte(value)),
		SourceID: observationID(g.project, row.Table, key, column), SourcePayloadHash: financialDigest([]byte(source)),
		Storage: rule.Storage, Class: rule.Class,
	}
	if rule.Storage == Blob {
		ref.ObjectHash = value
	}
	ref.DraftVersion, err = draftVersionEvidence(row, field)
	if err != nil {
		return err
	}
	g.refs[id] = ref
	return nil
}

var materialFrozenFields = map[string]string{
	"generation_tasks":     "creative image novel_chapter novel_detail recipe_run_id recipe_step_id recipe_input_hash semantic_review_id stream capability change_plan_id config price role base_head context_id input_hash estimate_micros",
	"tool_trials":          "binding_id qualification_id task_kind evaluation_id evaluation_step_id version_id version_hash manifest_hash package_id package_hash image_hash input_hash",
	"semantic_review_runs": "content_scope_hash schema input_hash expected_ip_head delta_hash source_generation_ids reviewer_binding_id price_id max_output_tokens scope",
}

// Parse only the source contract needed to find actual blob references. Other
// frozen fields remain opaque here and are never treated as execution grants.
func materialFrozenInput(table, raw string) (string, error) {
	if len(raw) == 0 || len(raw) > 16<<20 {
		return "", ErrSourceOwnership
	}
	shape := json.NewDecoder(strings.NewReader(raw))
	shape.UseNumber()
	if financialJSONValue(shape, 0) != nil {
		return "", ErrSourceOwnership
	}
	var object map[string]json.RawMessage
	d := json.NewDecoder(strings.NewReader(raw))
	if d.Decode(&object) != nil || object == nil || d.Decode(&struct{}{}) != io.EOF {
		return "", ErrSourceOwnership
	}
	allowed := map[string]bool{}
	for _, field := range strings.Fields(materialFrozenFields[table]) {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return "", ErrSourceOwnership
		}
	}
	var hash string
	if json.Unmarshal(object["input_hash"], &hash) != nil || !hash64.MatchString(hash) {
		return "", ErrSourceOwnership
	}
	return hash, nil
}

func (g *materialCollector) nested(row SourceRow) error {
	switch row.Table {
	case "generation_tasks", "tool_trials", "semantic_review_runs":
		hash, err := materialFrozenInput(row.Table, row.Values["frozen"])
		if err != nil {
			return err
		}
		return g.add(row, "frozen.input_hash", hash)
	case "skill_packages":
		var source authorSkillSource
		if authorDocument(row.Values["source"], &source) != nil || source.Kind == "" {
			return ErrSourceOwnership
		}
		if source.ArchiveHash != "" {
			return g.add(row, "source.archive_hash", source.ArchiveHash)
		}
	case "model_call_receipts":
		summary, err := ReceiptEvidence(row.Values["call_id"], row.Values["id"], []byte(row.Values["payload"]))
		if err != nil {
			return err
		}
		if summary.OutputHash != "" {
			return g.add(row, "payload.output_hash", summary.OutputHash)
		}
	case "artifact_releases":
		return g.artifacts(row)
	default:
		return ErrSourceOwnership
	}
	return nil
}

func (g *materialCollector) artifacts(row SourceRow) error {
	if g.index.replacedColumn(g.project, row, "manifest") {
		// Remaining nested hashes are bound from their independent historical
		// references by bindVerifiedExpiryVersions, never rediscovered as new.
		return nil
	}
	var manifest domain.Manifest
	if contentJSON([]byte(row.Values["manifest"]), &manifest, row.Values["content_hash"]) != nil || (manifest.Schema != 1 && manifest.Schema != 2) || manifest.BranchID != row.Values["branch_id"] || (manifest.ReleaseID != "" && manifest.ReleaseID != row.Values["id"]) || len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > 32 {
		return ErrSourceOwnership
	}
	seen := map[string]bool{}
	for _, artifact := range manifest.Artifacts {
		if !id32.MatchString(artifact.ResourceID) || seen[artifact.ResourceID] || artifact.Size < 0 {
			return ErrSourceOwnership
		}
		seen[artifact.ResourceID] = true
		resource, err := g.index.sourceRow("resources", map[string]string{"id": artifact.ResourceID})
		if err != nil || resource.Values["project_id"] != g.project || resource.Values["branch_id"] != row.Values["branch_id"] {
			return ErrSourceOwnership
		}
		if artifact.SourceImportID != "" {
			source, err := g.index.sourceRow("source_imports", map[string]string{"id": artifact.SourceImportID})
			if err != nil || source.Values["project_id"] != g.project || source.Values["branch_id"] != manifest.BranchID || source.Values["source_hash"] != artifact.OriginalHash {
				return ErrSourceOwnership
			}
		} else if artifact.OriginalHash != "" {
			return ErrSourceOwnership
		}
		if manifest.Schema == 2 && (artifact.Origin == nil || artifact.Origin.ValidateTarget(manifest.BranchID, artifact) != nil) {
			return ErrSourceOwnership
		}
		for _, original := range []bool{false, true} {
			hash := artifact.Hash
			if original {
				hash = artifact.OriginalHash
				if hash == "" {
					continue
				}
			}
			field, err := ArtifactField(artifact.ResourceID, original)
			if err != nil {
				return err
			}
			if err := g.add(row, field, hash); err != nil {
				return err
			}
		}
	}
	return nil
}
