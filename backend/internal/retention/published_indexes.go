package retention

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

// ReadSourceObject reads immutable bytes, without inference or remote model use.
// Callers hold the project/source transaction; absent objects fail registration.
type ReadSourceObject func(context.Context, string, string) ([]byte, error)

type verifiedIndexBody struct {
	size  int
	parts []retrieval.Chunk
}

// Certification is deliberately limited to published knowledge indexes. It
// neither certifies other derived lineage nor permits expiration or object GC.
func retainPublishedIndexes(ctx context.Context, index *SourceIndex, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	if index == nil || !id32.MatchString(project) {
		return ErrSourceOwnership
	}
	positions := make(map[string]int, len(materials))
	for i, m := range materials {
		positions[m.ID] = i
	}
	root := func(row SourceRow, field string) bool {
		key, err := RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return false
		}
		id, err := ReferenceID(project, row.Table, key, field)
		i, ok := positions[id]
		return err == nil && ok && materials[i].ContentRoot
	}
	// Collect promotions first: a failed proof must not leak partial changes.
	promote := map[string]bool{}
	add := func(row SourceRow, fields ...string) error {
		key, err := RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return err
		}
		for _, field := range fields {
			id, err := ReferenceID(project, row.Table, key, field)
			if _, expired := index.expired[id]; err == nil && expired {
				continue
			}
			i, ok := positions[id]
			rule, known := ConcreteRule(row.Table + "." + field)
			if err != nil || !ok || !known || !rule.Promotable || materials[i].Table != row.Table || materials[i].OwnerKey != key || materials[i].Field != field {
				return ErrSourceOwnership
			}
			promote[id] = true
		}
		return nil
	}
	chunks := map[string]map[string]SourceRow{}
	for _, row := range index.rows["knowledge_chunks"] {
		id := row.Values["index_id"]
		if chunks[id] == nil {
			chunks[id] = map[string]SourceRow{}
		}
		chunks[id][row.Values["id"]] = row
	}
	bodies := map[string]verifiedIndexBody{}
	bytesRead := 0
	for _, row := range index.rows["knowledge_indexes"] {
		if row.Values["project_id"] != project {
			continue
		}
		if index.replacedColumn(project, row, "manifest") {
			// The journal already binds the original manifest. Do not invent a
			// replacement manifest or promote remaining material from its marker.
			for _, chunk := range chunks[row.Values["id"]] {
				key, err := RowOwnerKey(chunk.Table, chunk.Values)
				if err != nil {
					return err
				}
				for _, field := range []string{"content", "vector", "source_hash", "resource_name"} {
					id, err := ReferenceID(project, chunk.Table, key, field)
					if err != nil {
						return err
					}
					if _, expired := index.expired[id]; !expired {
						return ErrSourceOwnership
					}
				}
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m, published, sizes, err := publishedIndexManifest(index, project, row, root)
		if err != nil {
			return err
		}
		if !published {
			continue
		}
		expected := 0
		for _, source := range m.Sources {
			body, ok := bodies[source.Hash]
			if !ok {
				if read == nil {
					return ErrSourceOwnership
				}
				data, err := read(ctx, project, source.Hash)
				if err != nil || len(data) > 32<<20 || len(data) > (64<<20)-bytesRead || financialDigest(data) != source.Hash {
					return ErrSourceOwnership
				}
				parts, err := retrieval.Split(string(data))
				if err != nil {
					return ErrSourceOwnership
				}
				bytesRead += len(data)
				body = verifiedIndexBody{size: len(data), parts: parts}
				bodies[source.Hash] = body
			}
			if int64(body.size) != sizes[source.ID] {
				return ErrSourceOwnership
			}
			for _, part := range body.parts {
				if err := ctx.Err(); err != nil {
					return err
				}
				id := financialDigest([]byte(fmt.Sprintf("%s:%s:%d", row.Values["id"], source.ID, part.Start)))
				chunk, ok := chunks[row.Values["id"]][id]
				if !ok || index.verifyRetainedKnowledgeChunk(project, row.Values["id"], source, part, chunk) != nil {
					return ErrSourceOwnership
				}
				if err := add(chunk, "content", "vector", "source_hash"); err != nil {
					return err
				}
				expected++
			}
		}
		if row.Values["chunks"] != strconv.Itoa(expected) || len(chunks[row.Values["id"]]) != expected {
			return ErrSourceOwnership
		}
		if err := add(row, "manifest"); err != nil {
			return err
		}
	}
	for id := range promote {
		i := positions[id]
		materials[i].ContentRoot = true
		materials[i].Sources = nil
	}
	return nil
}

// Verify each surviving value against its exact published source. For erased
// text/vector/name use only the independently bound original digest; never
// accept an expiry-shaped value by itself or restore deleted text to storage.
func (index *SourceIndex) verifyRetainedKnowledgeChunk(project, indexID string, source domain.KnowledgeSourcePin, part retrieval.Chunk, row SourceRow) error {
	v := row.Values
	id := financialDigest([]byte(fmt.Sprintf("%s:%s:%d", indexID, source.ID, part.Start)))
	if v["id"] != id || v["index_id"] != indexID || v["resource_id"] != source.ID || v["resource_revision"] != source.Head || v["source_hash"] != source.Hash || v["start_rune"] != strconv.Itoa(part.Start) || v["end_rune"] != strconv.Itoa(part.End) {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, v)
	if err != nil {
		return err
	}
	match := func(field, hash string) bool {
		ref, err := ReferenceID(project, row.Table, key, field)
		if err != nil {
			return false
		}
		if proof, ok := index.expiryProofs[ref]; ok {
			return index.replacedColumn(project, row, field) && proof.OriginalPayloadHash == hash
		}
		return financialDigest([]byte(v[field])) == hash
	}
	if !match("content", financialDigest([]byte(part.Text))) || !match("resource_name", financialDigest([]byte(source.Name))) || !match("vector", v["vector_hash"]) {
		return ErrSourceOwnership
	}
	if !index.replacedColumn(project, row, "vector") {
		if _, err := retrieval.UnpackVector([]byte(v["vector"])); err != nil {
			return ErrSourceOwnership
		}
	}
	return nil
}

func verifyKnowledgeChunk(indexID string, source domain.KnowledgeSourcePin, part retrieval.Chunk, row SourceRow) error {
	v := row.Values
	id := financialDigest([]byte(fmt.Sprintf("%s:%s:%d", indexID, source.ID, part.Start)))
	if v["id"] != id || v["index_id"] != indexID || v["resource_id"] != source.ID || v["resource_name"] != source.Name || v["resource_revision"] != source.Head || v["source_hash"] != source.Hash || v["start_rune"] != strconv.Itoa(part.Start) || v["end_rune"] != strconv.Itoa(part.End) || v["content"] != part.Text || v["vector_hash"] != financialDigest([]byte(v["vector"])) {
		return ErrSourceOwnership
	}
	if _, err := retrieval.UnpackVector([]byte(v["vector"])); err != nil {
		return ErrSourceOwnership
	}
	return nil
}

func publishedIndexManifest(index *SourceIndex, project string, row SourceRow, root func(SourceRow, string) bool) (domain.KnowledgeIndexManifest, bool, map[string]int64, error) {
	var m domain.KnowledgeIndexManifest
	fail := func() (domain.KnowledgeIndexManifest, bool, map[string]int64, error) {
		return m, false, nil, ErrSourceOwnership
	}
	if err := strictJSONWithLimit([]byte(row.Values["manifest"]), &m, 16<<20); err != nil {
		return fail()
	}
	canonical, err := json.Marshal(m)
	if err != nil || contentJSON([]byte(row.Values["manifest"]), &m, financialDigest(canonical)) != nil || financialDigest(append([]byte(project+"\x00"), canonical...)) != row.Values["id"] || m.Branch != row.Values["branch_id"] || m.IPHead != row.Values["ip_head"] || m.Model != row.Values["model_revision"] || m.Parser != "unicode-512-overlap32-v1" || len(m.Model) != 40 || len(m.Sources) > 250000 {
		return fail()
	}
	if model, err := hex.DecodeString(m.Model); err != nil || hex.EncodeToString(model) != m.Model {
		return fail()
	}
	lineage, err := index.branchLineage(m.Branch, project)
	if err != nil {
		return fail()
	}
	published := m.IPHead != "" || len(m.Sources) > 0
	if m.IPHead != "" {
		ip, err := index.sourceRow("ip_revisions", map[string]string{"id": m.IPHead})
		if err != nil || !lineage[ip.Values["branch_id"]] || !root(ip, "payload") || (ip.Values["status"] != "PUBLISHED" && ip.Values["status"] != "READY") {
			return fail()
		}
		published = published && ip.Values["status"] == "PUBLISHED"
	}
	sizes := map[string]int64{}
	previous := ""
	for _, source := range m.Sources {
		if !id32.MatchString(source.ID) || !id32.MatchString(source.Head) || !hash64.MatchString(source.Hash) || source.ID <= previous {
			return fail()
		}
		previous = source.ID
		resource, err := index.sourceRow("resources", map[string]string{"id": source.ID})
		if err != nil || resource.Values["project_id"] != project || resource.Values["branch_id"] != m.Branch {
			return fail()
		}
		release, err := index.sourceRow("artifact_releases", map[string]string{"id": source.Head})
		if err != nil || release.Values["project_id"] != project || release.Values["branch_id"] != m.Branch || (release.Values["status"] != "PUBLISHED" && release.Values["status"] != "READY") {
			return fail()
		}
		field, err := ArtifactField(source.ID, false)
		if err != nil || !root(release, field) {
			return fail()
		}
		var manifest domain.Manifest
		if contentJSON([]byte(release.Values["manifest"]), &manifest, release.Values["content_hash"]) != nil {
			return fail()
		}
		found := false
		for _, artifact := range manifest.Artifacts {
			if artifact.ResourceID != source.ID {
				continue
			}
			if found || artifact.Hash != source.Hash || stableHash(artifact.Policy) != stableHash(source.Policy) || artifact.Size < 0 {
				return fail()
			}
			found = true
			sizes[source.ID] = artifact.Size
		}
		if !found {
			return fail()
		}
		published = published && release.Values["status"] == "PUBLISHED"
	}
	return m, published, sizes, nil
}
