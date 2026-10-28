package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

func (b *contextSourceBuilder) semanticRun(id string) (SourceRow, domain.SemanticReviewFrozen, error) {
	row, err := b.index.sourceRow("semantic_review_runs", map[string]string{"id": id})
	var frozen domain.SemanticReviewFrozen
	if err != nil || row.Values["project_id"] != b.project || authorDocument(row.Values["frozen"], &frozen) != nil || frozen.Schema != 1 || frozen.Scope != domain.SemanticReviewScope {
		return row, frozen, ErrSourceOwnership
	}
	return row, frozen, nil
}

func (index *SourceIndex) attachSemanticSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "semantic_review_runs" && m.Table != "semantic_review_acceptances" {
			continue
		}
		if m.Field != "frozen" && m.Field != "frozen.input_hash" && m.Field != "report_hash" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
		id := row.Values["id"]
		if m.Table == "semantic_review_acceptances" {
			id = row.Values["review_id"]
		}
		run, frozen, err := b.semanticRun(id)
		if err != nil {
			return err
		}
		b.branch, b.actor = run.Values["branch_id"], run.Values["actor_id"]
		if m.Table == "semantic_review_acceptances" {
			for _, field := range []string{"target_kind", "target_id", "target_hash", "report_hash"} {
				if row.Values[field] != run.Values[field] {
					return ErrSourceOwnership
				}
			}
			if err := b.link(run, "report_hash", ""); err != nil {
				return err
			}
			continue
		}
		switch m.Field {
		case "frozen":
			if err := b.link(run, "frozen.input_hash", financialDigest([]byte(frozen.InputHash))); err != nil {
				return err
			}
		case "frozen.input_hash":
			if _, err := b.semanticDocument(ctx, run, frozen, read); err != nil {
				return err
			}
		case "report_hash":
			doc, err := b.semanticDocument(ctx, run, frozen, read)
			if err != nil {
				return err
			}
			child, err := index.sourceRow("generation_tasks", map[string]string{"id": run.Values["generation_id"]})
			if err != nil || child.Values["project_id"] != project || child.Values["branch_id"] != b.branch || child.Values["actor_id"] != b.actor {
				return ErrSourceOwnership
			}
			if _, err := b.semanticGeneration(child, id); err != nil {
				return err
			}
			call, err := index.sourceRow("model_calls", map[string]string{"id": child.Values["call_id"]})
			if err != nil || call.Values["project_id"] != project {
				return ErrSourceOwnership
			}
			raw, err := read(ctx, project, call.Values["output_hash"])
			var output models.Result
			if err != nil || contentJSON(raw, &output, call.Values["output_hash"]) != nil || financialDigest([]byte(output.Text)) != run.Values["report_hash"] {
				return ErrSourceOwnership
			}
			reportBytes, err := read(ctx, project, run.Values["report_hash"])
			if err != nil || !bytes.Equal(reportBytes, []byte(output.Text)) {
				return ErrSourceOwnership
			}
			report, err := domain.ParseSemanticReviewReport([]byte(output.Text), doc)
			if err != nil || report.Verdict != run.Values["verdict"] {
				return ErrSourceOwnership
			}
			if err := b.link(child, "frozen", ""); err != nil {
				return err
			}
			if err := b.link(call, "output_hash", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *contextSourceBuilder) semanticDocument(ctx context.Context, row SourceRow, frozen domain.SemanticReviewFrozen, read ReadSourceObject) (domain.SemanticReviewDocument, error) {
	var actual domain.SemanticReviewDocument
	raw, err := read(ctx, b.project, frozen.InputHash)
	if err != nil || contentJSON(raw, &actual, frozen.InputHash) != nil {
		return actual, ErrSourceOwnership
	}
	doc := domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: row.Values["target_kind"], ID: row.Values["target_id"], Hash: row.Values["target_hash"], BranchID: b.branch, ExpectedIPHead: frozen.ExpectedIPHead}, Artifacts: []domain.SemanticReviewArtifact{}, HardFindings: []string{}, RequiredCoverage: []string{}, Anchors: []domain.SemanticReviewAnchor{}, Scope: domain.SemanticReviewScope}
	sources := []string{}
	if _, err := b.ip(frozen.ExpectedIPHead, b.branch); err != nil {
		return doc, err
	}
	switch doc.Target.Kind {
	case "ip_candidate":
		candidate, err := b.index.sourceRow("ip_revisions", map[string]string{"id": doc.Target.ID})
		if err != nil || candidate.Values["branch_id"] != b.branch || candidate.Values["base_head"] != frozen.ExpectedIPHead || candidate.Values["content_hash"] != doc.Target.Hash {
			return doc, ErrSourceOwnership
		}
		doc.Snapshot, err = b.ip(doc.Target.ID, b.branch)
		if err != nil {
			return doc, err
		}
		if delta, exists := b.index.optionalCandidateRow("ip_branch_deltas", doc.Target.ID); exists {
			var value domain.BranchDelta
			if authorDocument(delta.Values["payload"], &value) != nil {
				return doc, ErrSourceOwnership
			}
			doc.Delta = &value
			if err := b.link(delta, "payload", ""); err != nil {
				return doc, err
			}
		}
	case "artifact_release":
		release, err := b.index.sourceRow("artifact_releases", map[string]string{"id": doc.Target.ID})
		var manifest domain.Manifest
		if err != nil || release.Values["project_id"] != b.project || release.Values["branch_id"] != b.branch || release.Values["content_hash"] != doc.Target.Hash || contentJSON([]byte(release.Values["manifest"]), &manifest, doc.Target.Hash) != nil || manifest.BranchID != b.branch || manifest.ExpectedIPHead != frozen.ExpectedIPHead {
			return doc, ErrSourceOwnership
		}
		if err := b.link(release, "manifest", ""); err != nil {
			return doc, err
		}
		snapshotID := frozen.ExpectedIPHead
		if manifest.IPCandidateID != "" {
			snapshotID = manifest.IPCandidateID
		}
		doc.Snapshot, err = b.ip(snapshotID, b.branch)
		if err != nil {
			return doc, err
		}
		if snapshotID == "" {
			doc.Snapshot = domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
		}
		for _, ref := range manifest.Artifacts {
			body, err := read(ctx, b.project, ref.Hash)
			if err != nil || financialDigest(body) != ref.Hash {
				return doc, ErrSourceOwnership
			}
			source := ""
			if ref.Origin != nil && ref.Origin.Generation != nil {
				source = ref.Origin.Generation.GenerationID
				sources = append(sources, source)
			}
			target := domain.ArtifactReviewTarget{ReleaseID: doc.Target.ID, ManifestHash: doc.Target.Hash, ResourceID: ref.ResourceID, BranchID: b.branch, ExpectedHead: ref.ExpectedHead, IPHead: frozen.ExpectedIPHead, ContentHash: ref.Hash, SourceGenerationID: source}
			doc.Artifacts = append(doc.Artifacts, domain.SemanticReviewArtifact{Target: target, Text: string(body)})
			key := "resource:" + ref.ResourceID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(body)})
			field, err := ArtifactField(ref.ResourceID, false)
			if err != nil {
				return doc, err
			}
			if err := b.link(release, field, financialDigest([]byte(ref.Hash))); err != nil {
				return doc, err
			}
		}
		if err = b.chapterReviewPlans(ctx, manifest, &doc, read); err != nil {
			return doc, err
		}
	default:
		return doc, ErrSourceOwnership
	}
	doc, err = domain.CompleteSemanticReviewSnapshot(doc)
	if err != nil || !exactJSONBytes(doc, raw) {
		return doc, ErrSourceOwnership
	}
	deltaHash := ""
	if doc.Delta != nil {
		value, _ := json.Marshal(doc.Delta)
		deltaHash = financialDigest(value)
	}
	slices.Sort(sources)
	sources = slices.Compact(sources)
	if deltaHash != frozen.DeltaHash || !slices.Equal(sources, frozen.SourceGenerationIDs) {
		return doc, ErrSourceOwnership
	}
	if frozen.ContentScopeHash != "" {
		hash, err := domain.SemanticContentScopeHash(doc)
		if err != nil || hash != frozen.ContentScopeHash {
			return doc, ErrSourceOwnership
		}
	}
	return doc, nil
}

func (b *contextSourceBuilder) semanticGeneration(child SourceRow, id string) (string, error) {
	run, frozen, err := b.semanticRun(id)
	var generation struct {
		SemanticReviewID string                `json:"semantic_review_id"`
		BaseHead         string                `json:"base_head"`
		Role             string                `json:"role"`
		Price            models.PriceCard      `json:"price"`
		Capability       *domain.CapabilityUse `json:"capability"`
	}
	if err != nil || json.Unmarshal([]byte(child.Values["frozen"]), &generation) != nil || run.Values["generation_id"] != child.Values["id"] || run.Values["actor_id"] != child.Values["actor_id"] || run.Values["branch_id"] != child.Values["branch_id"] || child.Values["project_id"] != b.project || generation.SemanticReviewID != id || generation.BaseHead != frozen.ExpectedIPHead || generation.Role != "critic" || generation.Price.ID != frozen.PriceID || slices.Contains(frozen.SourceGenerationIDs, child.Values["id"]) {
		return "", ErrSourceOwnership
	}
	use := generation.Capability
	if use == nil || use.Mode != "QUALIFIED" || use.TaskKind != "shared.semantic-review" || use.BindingID != frozen.ReviewerBindingID || use.MaxOutputTokens != frozen.MaxOutputTokens {
		return "", ErrSourceOwnership
	}
	if err := b.link(run, "frozen", ""); err != nil {
		return "", err
	}
	return frozen.InputHash, nil
}
