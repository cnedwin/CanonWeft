package retention

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
)

type contextSourceBuilder struct {
	index                  *SourceIndex
	project, branch, actor string
	m                      *MaterialReference
	capture                func(SourceRow) error
}

func (b *contextSourceBuilder) link(row SourceRow, field, hash string) error {
	owner, err := b.index.Owner(row.Table, row.Values)
	if err != nil || owner.Shared || owner.ProjectID != b.project {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	id, err := ReferenceID(b.project, row.Table, key, field)
	if err != nil {
		return err
	}
	if hash == "" {
		hash = financialDigest([]byte(row.Values[field]))
	}
	if b.capture != nil {
		if err := b.capture(row); err != nil {
			return err
		}
	}
	return appendSourceRequest(b.m, MaterialSource{ReferenceID: id, PayloadHash: hash})
}

func (index *SourceIndex) attachContextSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "context_packages" || m.Field != "payload" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		row := index.rows[m.Table][m.OwnerKey]
		var p domain.ContextPackage
		if contextJSON([]byte(row.Values["payload"]), &p, row.Values["content_hash"]) != nil || p.ID != row.Values["id"] || p.IndexID != row.Values["index_id"] || !id32.MatchString(p.ID) {
			return fmt.Errorf("context envelope: %w", ErrSourceOwnership)
		}
		b := contextSourceBuilder{index: index, project: project, branch: row.Values["branch_id"], actor: row.Values["actor_id"], m: &m.MaterialReference}
		if err := b.collect(ctx, p, read); err != nil {
			return fmt.Errorf("context source proof: %w", err)
		}
	}
	return nil
}

func (b *contextSourceBuilder) collect(ctx context.Context, p domain.ContextPackage, read ReadSourceObject) error {
	idx, err := b.index.sourceRow("knowledge_indexes", map[string]string{"id": p.IndexID})
	if err != nil || idx.Values["project_id"] != b.project || idx.Values["branch_id"] != b.branch || idx.Values["ip_head"] != p.IPHead {
		return ErrSourceOwnership
	}
	if err := b.link(idx, "manifest", ""); err != nil {
		return err
	}
	if err := b.structured(p); err != nil {
		return fmt.Errorf("structured: %w", err)
	}
	for _, list := range [][]retrieval.Chunk{p.Evidence, p.Working, p.Methods, p.Research} {
		for _, chunk := range list {
			if err := b.chunk(p.IndexID, chunk, nil); err != nil {
				return fmt.Errorf("public chunk: %w", err)
			}
		}
	}
	if p.PrivateIndexID != "" {
		idx, err := b.index.sourceRow("working_indexes", map[string]string{"id": p.PrivateIndexID})
		if err != nil || idx.Values["project_id"] != b.project || idx.Values["branch_id"] != b.branch || idx.Values["actor_id"] != b.actor {
			return ErrSourceOwnership
		}
		if err := b.link(idx, "manifest", ""); err != nil {
			return err
		}
	} else if len(p.PrivateHits) != 0 {
		return ErrSourceOwnership
	}
	for _, hit := range p.PrivateHits {
		if err := b.chunk(p.PrivateIndexID, hit.Chunk, &hit); err != nil {
			return fmt.Errorf("private chunk: %w", err)
		}
	}
	if len(p.Unconfirmed) != len(p.Input.WorkingRefs) || len(p.Unconfirmed) > 16 {
		return ErrSourceOwnership
	}
	for i, input := range p.Unconfirmed {
		if input.Ref != p.Input.WorkingRefs[i] || len(input.Parts) == 0 || len(input.Parts) > 34 {
			return ErrSourceOwnership
		}
		manifest := domain.PrivateManifest{Project: b.project, Branch: b.branch, Actor: b.actor}
		for part, value := range input.Parts {
			pin := domain.PrivatePin{Ref: input.Ref, State: input.State, BaseHead: input.BaseHead, Part: part, Name: value.Name, Format: value.Format, Hash: financialDigest([]byte(value.Text))}
			source, err := b.index.privateInputSource(b.project, manifest, pin)
			if err != nil {
				return err
			}
			if err := appendSourceRequest(b.m, source); err != nil {
				return err
			}
			if input.Ref.Kind == "resource_draft" && part == 0 {
				row, err := b.index.sourceRow("resources", map[string]string{"id": input.Ref.ID})
				if err != nil {
					return err
				}
				if err := b.link(row, "name", financialDigest([]byte(value.Name))); err != nil {
					return err
				}
			}
			if input.Ref.Kind == "release_candidate" && value.Format == "text" {
				row, err := b.index.sourceRow("artifact_releases", map[string]string{"id": input.Ref.ID})
				var release domain.Manifest
				if err != nil || contentJSON([]byte(row.Values["manifest"]), &release, row.Values["content_hash"]) != nil || part < 1 || part > len(release.Artifacts) {
					return ErrSourceOwnership
				}
				resource, err := b.index.sourceRow("resources", map[string]string{"id": release.Artifacts[part-1].ResourceID})
				if err != nil {
					return err
				}
				if err := b.link(resource, "name", financialDigest([]byte(value.Name))); err != nil {
					return err
				}
			}
		}
	}
	if len(p.Required) > 256 {
		return ErrSourceOwnership
	}
	for _, required := range p.Required {
		if err := b.required(ctx, required, read); err != nil {
			return fmt.Errorf("required: %w", err)
		}
	}
	return b.memory(p)
}

func (b *contextSourceBuilder) ip(id, branch string) (domain.Snapshot, error) {
	var snapshot domain.Snapshot
	if id == "" {
		return snapshot, nil
	}
	lineage, err := b.index.branchLineage(branch, b.project)
	row, rowErr := b.index.sourceRow("ip_revisions", map[string]string{"id": id})
	if err != nil || rowErr != nil || !lineage[row.Values["branch_id"]] || contentJSON([]byte(row.Values["payload"]), &snapshot, row.Values["content_hash"]) != nil {
		return snapshot, ErrSourceOwnership
	}
	if err := b.link(row, "payload", ""); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (b *contextSourceBuilder) structured(p domain.ContextPackage) error {
	snapshot, err := b.ip(p.IPHead, b.branch)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range snapshot.Assertions {
		values["assertion:"+v.ID] = stableHash(v)
	}
	for _, v := range snapshot.Entities {
		values["entity:"+v.ID] = stableHash(v)
	}
	for _, v := range snapshot.Rules {
		values["rule:"+v.ID] = stableHash(v)
	}
	for _, list := range [][]domain.Assertion{p.Facts, p.Hidden} {
		for _, v := range list {
			if values["assertion:"+v.ID] != stableHash(v) {
				return ErrSourceOwnership
			}
		}
	}
	for _, v := range p.Entities {
		if values["entity:"+v.ID] != stableHash(v) {
			return ErrSourceOwnership
		}
	}
	for _, v := range p.Rules {
		if values["rule:"+v.ID] != stableHash(v) {
			return ErrSourceOwnership
		}
	}
	return nil
}

func (b *contextSourceBuilder) chunk(indexID string, c retrieval.Chunk, private *domain.PrivateHit) error {
	table := "knowledge_chunks"
	if private != nil {
		table = "working_chunks"
	}
	row, err := b.index.sourceRow(table, map[string]string{"id": c.ID})
	v := row.Values
	if err != nil || v["index_id"] != indexID || v["source_hash"] != c.SourceHash || v["start_rune"] != strconv.Itoa(c.Start) || v["end_rune"] != strconv.Itoa(c.End) || v["content"] != c.Text {
		return ErrSourceOwnership
	}
	if private == nil {
		if v["resource_id"] != c.ResourceID || v["resource_name"] != c.Name || v["resource_revision"] != c.Revision {
			return ErrSourceOwnership
		}
	} else {
		if v["source_key"] != c.ResourceID || v["source_name"] != c.Name || v["ref_kind"] != private.Ref.Kind || v["ref_id"] != private.Ref.ID || v["ref_version"] != private.Ref.Version || v["ref_version"] != c.Revision || v["part_number"] != strconv.Itoa(private.Part) {
			return ErrSourceOwnership
		}
		idx, err := b.index.sourceRow("working_indexes", map[string]string{"id": indexID})
		var manifest domain.PrivateManifest
		if err != nil || authorDocument(idx.Values["manifest"], &manifest) != nil || !slices.ContainsFunc(manifest.Sources, func(pin domain.PrivatePin) bool { return stableHash(pin) == c.ResourceID && pin.State == private.State }) {
			return ErrSourceOwnership
		}
	}
	fields := []string{"content", "resource_name", "source_hash"}
	if private != nil {
		fields = []string{"content", "source_name"}
	}
	for _, field := range fields {
		if err := b.link(row, field, ""); err != nil {
			return err
		}
	}
	return nil
}

func (b *contextSourceBuilder) memory(p domain.ContextPackage) error {
	if p.Memory == nil {
		if p.SessionSource != nil {
			return ErrSourceOwnership
		}
		return nil
	}
	pack := *p.Memory
	want := pack.Hash
	pack.Hash = ""
	scope := pack.Request.Scope
	canon := p.IPHead
	if canon == "" {
		canon = "empty"
	}
	if stableHash(pack) != want || scope.Project != b.project || scope.Branch != b.branch || scope.Actor != b.actor || scope.Canon != canon || scope.Purpose != p.Input.Purpose || scope.Perspective != p.Input.Perspective || scope.StoryTime != p.Input.StoryTime || len(pack.Items) > 8192 {
		return ErrSourceOwnership
	}
	for _, selected := range pack.Items {
		item := selected.Item
		row, err := b.index.memoryRow("memory_revisions", item.Revision, b.project)
		var source memory.Item
		if err != nil || memoryJSON(row.Values["document"], &source) != nil || contextJSON([]byte(row.Values["document"]), &source, stableHash(source)) != nil || source.ID != item.ID || source.Branch != b.branch || (source.Actor != "" && source.Actor != b.actor) {
			return ErrSourceOwnership
		}
		// Relevance is derived for this query; every authored field must still
		// match the immutable memory revision, including its source ranges.
		item.Score = source.Score
		if stableHash(source) != stableHash(item) {
			return ErrSourceOwnership
		}
		if err := b.link(row, "document", ""); err != nil {
			return err
		}
	}
	if scope.Session == "" {
		if p.SessionSource != nil {
			return ErrSourceOwnership
		}
		return nil
	}
	if p.SessionSource == nil {
		b.m.UnboundSources++ // Pre-upgrade context never captured this edit.
		return nil
	}
	s := p.SessionSource
	n, versionErr := strconv.ParseInt(s.Revision, 10, 64)
	if s.ID != scope.Session || !hash64.MatchString(s.Hash) || versionErr != nil || n < 1 || strconv.FormatInt(n, 10) != s.Revision || b.index.memorySession(s.ID, b.project, b.branch, b.actor) != nil {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey("memory_sessions", map[string]string{"id": s.ID})
	if err != nil {
		return err
	}
	id, err := ReferenceID(b.project, "memory_sessions", key, "document")
	if err != nil {
		return err
	}
	return appendSourceRequest(b.m, MaterialSource{ReferenceID: id, WorkingProjection: &WorkingProjectionEvidence{Kind: "memory_session", Version: s.Revision, State: "PRIVATE_DRAFT", Hash: s.Hash}})
}
