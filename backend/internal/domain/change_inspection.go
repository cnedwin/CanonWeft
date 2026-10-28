package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

const ChangeInspectionScope = "仅比较固定变更前后内容、所选资料版本和目标分支设定；不证明未选资料、全书一致性或文学质量，不具有采用及发布权限"

// ParseChangeInspectionInput reads only immutable server-rendered JSON frames.
// Callers must still check their source ownership and reconstruct the document.
func ParseChangeInspectionInput(prompt string) (ChangeReviewContext, SemanticReviewDocument, error) {
	var source ChangeReviewContext
	var method struct {
		Input SemanticReviewDocument `json:"input"`
	}
	frame := func(open, close string, value any) error {
		start := strings.LastIndex(prompt, open)
		if start < 0 {
			return ErrValidation
		}
		start += len(open)
		end := strings.Index(prompt[start:], close)
		if end < 0 {
			return ErrValidation
		}
		raw := []byte(prompt[start : start+end])
		if validateReviewJSON(raw) != nil || json.Unmarshal(raw, value) != nil {
			return ErrValidation
		}
		return nil
	}
	if frame("\n\n<proposed-change-data>\n", "\n</proposed-change-data>", &source) != nil || frame("\n<fixed-method-data>\n", "\n</fixed-method-data>", &method) != nil || len(source.FocusTargets) == 0 {
		return source, method.Input, ErrValidation
	}
	return source, method.Input, nil
}

func changeInspectionBodyHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

// ChangeInspectionRefs cannot silently expand an author's fixed plan. Resources
// outside it must enter through the existing versioned supplemental-scope flow.
func ChangeInspectionRefs(plan ChangePlan, branch string, ids []string) ([]DependencyRef, error) {
	if len(ids) == 0 || len(ids) > 16 || plan.Stale || plan.SourceState == "SUPERSEDED" || branch == "" || (branch != plan.BranchID && plan.SourceEventID == "") {
		return nil, ErrValidation
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if id == "" || wanted[id] {
			return nil, ErrValidation
		}
		wanted[id] = true
	}
	refs := []DependencyRef{}
	for _, item := range plan.Items {
		ref := item.Node
		if ref.Kind == "resource" && ref.BranchID == branch && wanted[ref.ID] {
			refs = append(refs, ref)
			delete(wanted, ref.ID)
		}
	}
	if len(wanted) != 0 {
		return nil, ErrValidation
	}
	slices.SortFunc(refs, func(a, b DependencyRef) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return refs, nil
}

func ChangeInspectionFocus(source ChangeReviewContext) ([]DependencyRef, error) {
	branch := source.Plan.BranchID
	if source.FocusBranchID != "" {
		branch = source.FocusBranchID
	}
	ids := []string{}
	for _, ref := range source.FocusTargets {
		ids = append(ids, ref.ID)
	}
	refs, err := ChangeInspectionRefs(source.Plan, branch, ids)
	if err != nil || len(refs) != len(source.Targets) || !slices.Equal(refs, source.FocusTargets) {
		return nil, ErrValidation
	}
	for i, ref := range refs {
		target := source.Targets[i]
		node := struct {
			Hash         string
			Policy       *KnowledgePolicy
			Dependencies *DependencyPolicy
		}{changeInspectionBodyHash(target.Text), target.Policy, target.Dependencies}
		if target.Ref != ref || target.BodyHash != node.Hash || ref.Hash != ArtifactEvidenceHash(node) {
			return nil, ErrValidation
		}
	}
	return refs, nil
}

func BuildChangeInspectionDocument(source ChangeReviewContext, snapshot Snapshot) (SemanticReviewDocument, error) {
	doc := SemanticReviewDocument{Schema: 1, Snapshot: snapshot, Scope: ChangeInspectionScope, Artifacts: []SemanticReviewArtifact{}, HardFindings: []string{}, RequiredCoverage: []string{}, Anchors: []SemanticReviewAnchor{}}
	refs, err := ChangeInspectionFocus(source)
	if err != nil {
		return doc, err
	}
	branch, head := source.Plan.BranchID, source.Plan.BaseHead
	if source.FocusBranchID != "" {
		branch, head = source.FocusBranchID, source.FocusBaseHead
	}
	doc.Target = SemanticReviewTarget{Kind: "change_plan", ID: source.Plan.ID, BranchID: branch, ExpectedIPHead: head}
	if len(source.Plan.Changes) == 0 || len(source.Plan.Differences) == 0 {
		doc.HardFindings = append(doc.HardFindings, "缺少可比较的变更前后材料，不能认定没有影响")
	}
	changes, _ := json.Marshal(struct {
		SourceEventID string                 `json:"source_event_id"`
		SourceState   string                 `json:"source_state"`
		Changes       []DependencyChange     `json:"changes"`
		Differences   []DependencyDifference `json:"differences"`
	}{source.Plan.SourceEventID, source.Plan.SourceState, source.Plan.Changes, source.Plan.Differences})
	doc.RequiredCoverage = append(doc.RequiredCoverage, "change")
	doc.Anchors = append(doc.Anchors, SemanticReviewAnchor{Key: "change", TargetKey: "change", Text: string(changes)})
	for i, ref := range refs {
		target := source.Targets[i]
		key := "resource:" + ref.ID
		doc.Artifacts = append(doc.Artifacts, SemanticReviewArtifact{Target: ArtifactReviewTarget{ResourceID: ref.ID, BranchID: branch, ExpectedHead: ref.Revision, IPHead: head, ContentHash: target.BodyHash}, Text: target.Text})
		raw, _ := json.Marshal(target)
		doc.RequiredCoverage = append(doc.RequiredCoverage, key)
		doc.Anchors = append(doc.Anchors, SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(raw)})
	}
	doc, err = CompleteSemanticReviewSnapshot(doc)
	if err != nil {
		return doc, err
	}
	doc.Target.Hash = ArtifactEvidenceHash(struct {
		PlanHash string
		Document SemanticReviewDocument
	}{ArtifactEvidenceHash(source.Plan), doc})
	return doc, nil
}
