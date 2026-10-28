package retention

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

type publicChunkProof struct {
	source domain.KnowledgeSourcePin
	part   retrieval.Chunk
}

type publicIndexProof struct {
	id        string
	manifest  domain.KnowledgeIndexManifest
	published bool
	keys      []SourceKey
	expected  map[string]publicChunkProof
}

// CaptureKnowledgeIndex registers all freshly written chunks in bounded pages
// inside the caller's index transaction. Verification failure must roll back
// that transaction; no partially certified index can become visible.
func CaptureKnowledgeIndex(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project, id string, read ReadSourceObject) error {
	if tx == nil || !id32.MatchString(project) || !hash64.MatchString(id) {
		return ErrSourceOwnership
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil || !enabled {
		return err
	}
	read = boundedSourceReader(project, read)
	index, _ := NewSourceIndex(nil)
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index.loadSource = reader.read
	row, err := index.sourceRow("knowledge_indexes", map[string]string{"id": id})
	if err != nil || row.Values["project_id"] != project {
		return ErrSourceOwnership
	}
	proof, err := index.preparePublicIndexProof(ctx, project, row, read)
	if err != nil {
		return err
	}
	key := SourceKey{Table: "knowledge_indexes", Primary: map[string]string{"id": id}}
	if err := captureSourceValuesWithObjects(ctx, tx, ledger, project, read, &proof, key); err != nil {
		return err
	}
	after, count := "", 0
	for {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM knowledge_chunks WHERE index_id=? AND id>? ORDER BY id LIMIT 128 FOR SHARE", id, after)
		if err != nil {
			return err
		}
		keys := []SourceKey{}
		for rows.Next() {
			var chunk string
			if err := rows.Scan(&chunk); err != nil {
				rows.Close()
				return err
			}
			if _, ok := proof.expected[chunk]; !ok || chunk <= after {
				rows.Close()
				return ErrSourceOwnership
			}
			keys = append(keys, SourceKey{Table: "knowledge_chunks", Primary: map[string]string{"id": chunk}})
			after = chunk
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			break
		}
		if err := captureSourceValuesWithObjects(ctx, tx, ledger, project, read, &proof, keys...); err != nil {
			return err
		}
		count += len(keys)
	}
	if count != len(proof.expected) {
		return ErrSourceOwnership
	}
	return nil
}

func (index *SourceIndex) preparePublicIndexProof(ctx context.Context, project string, row SourceRow, read ReadSourceObject) (publicIndexProof, error) {
	p := publicIndexProof{id: row.Values["id"], expected: map[string]publicChunkProof{}}
	if authorDocument(row.Values["manifest"], &p.manifest) != nil {
		return p, ErrSourceOwnership
	}
	add := func(table, id string) error {
		if id == "" {
			return nil
		}
		if _, err := index.sourceRow(table, map[string]string{"id": id}); err != nil {
			return err
		}
		p.keys = append(p.keys, SourceKey{Table: table, Primary: map[string]string{"id": id}})
		return nil
	}
	if err := add("ip_revisions", p.manifest.IPHead); err != nil {
		return p, err
	}
	for _, source := range p.manifest.Sources {
		if err := add("resources", source.ID); err != nil {
			return p, err
		}
		if err := add("artifact_releases", source.Head); err != nil {
			return p, err
		}
	}
	roots, err := index.AdoptedContentReferences(project)
	if err != nil {
		return p, err
	}
	preserved := map[string]bool{}
	for _, root := range roots {
		preserved[root.ID] = true
	}
	root := func(source SourceRow, field string) bool {
		key, err := RowOwnerKey(source.Table, source.Values)
		if err != nil {
			return false
		}
		id, err := ReferenceID(project, source.Table, key, field)
		return err == nil && preserved[id]
	}
	var sizes map[string]int64
	p.manifest, p.published, sizes, err = publishedIndexManifest(index, project, row, root)
	if err != nil {
		return p, err
	}
	for _, source := range p.manifest.Sources {
		data, err := read(ctx, project, source.Hash)
		if err != nil || financialDigest(data) != source.Hash || int64(len(data)) != sizes[source.ID] {
			return p, ErrSourceOwnership
		}
		parts, err := retrieval.Split(string(data))
		if err != nil {
			return p, ErrSourceOwnership
		}
		for _, part := range parts {
			if len(p.expected) >= 250000 {
				return p, ErrSourceOwnership
			}
			id := financialDigest([]byte(fmt.Sprintf("%s:%s:%d", p.id, source.ID, part.Start)))
			p.expected[id] = publicChunkProof{source, part}
		}
	}
	if row.Values["chunks"] != strconv.Itoa(len(p.expected)) {
		return p, ErrSourceOwnership
	}
	return p, nil
}

func (p *publicIndexProof) attach(index *SourceIndex, project string, materials []ObservedMaterial) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "knowledge_indexes" && m.Table != "knowledge_chunks" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		if m.Table == "knowledge_chunks" {
			part, exists := p.expected[row.Values["id"]]
			if !exists || verifyKnowledgeChunk(p.id, part.source, part.part, row) != nil {
				return ErrSourceOwnership
			}
			if p.published && (m.Field == "content" || m.Field == "vector" || m.Field == "source_hash") {
				m.ContentRoot, m.Sources = true, nil
			}
			continue
		}
		if row.Values["id"] != p.id || m.Field != "manifest" {
			return ErrSourceOwnership
		}
		if p.published {
			m.ContentRoot, m.Sources = true, nil
			continue
		}
		b := contextSourceBuilder{index: index, project: project, branch: p.manifest.Branch, m: &m.MaterialReference}
		if _, err := b.ip(p.manifest.IPHead, p.manifest.Branch); err != nil {
			return err
		}
		for _, source := range p.manifest.Sources {
			release, err := index.sourceRow("artifact_releases", map[string]string{"id": source.Head})
			if err != nil {
				return err
			}
			field, err := ArtifactField(source.ID, false)
			if err != nil {
				return err
			}
			if err := b.link(release, field, financialDigest([]byte(source.Hash))); err != nil {
				return err
			}
		}
	}
	return nil
}
