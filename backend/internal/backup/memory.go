package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
)

var memoryTables = []string{"memory_sessions", "memory_items", "memory_revisions"}

func upgradeMemory(a *Archive) error {
	if a.Manifest.Schema >= 35 {
		return nil
	}
	for _, name := range memoryTables {
		if _, exists := a.Data[name]; exists {
			return errors.New("unexpected memory table in legacy archive")
		}
		a.Data[name] = nil
	}
	return nil
}

func validateMemoryBackup(d Data) error {
	return validateMemoryBackupWithEvidence(d, nil)
}

func validateMemoryBackupWithEvidence(d Data, evidence map[string]retention.ExpiredProjection) error {
	return validateMemoryBackupWithSessionEvidence(d, evidence, nil)
}

func validateMemoryBackupWithSessionEvidence(d Data, evidence, sessions map[string]retention.ExpiredProjection) error {
	return validateMemoryBackupWithDocuments(d, evidence, map[string]map[string]retention.ExpiredProjection{"memory_sessions.document": sessions})
}

func validateMemoryBackupWithDocuments(d Data, evidence map[string]retention.ExpiredProjection, documents map[string]map[string]retention.ExpiredProjection) error {
	if len(d["memory_items"])+len(d["memory_revisions"])+len(d["memory_sessions"]) == 0 {
		return validateMemoryContextsWithEvidence(d, evidence)
	}
	owners, err := newRetentionArchiveOwners(d)
	if err != nil {
		return err
	}
	// All independently checked documents are bound before resolving heads.
	for _, table := range memoryTables {
		for _, source := range owners.rows[table] {
			if proof, ok := documents[table+".document"][source.Values["id"]]; ok {
				if err := owners.index.BindVerifiedMemoryExpiry(source, proof); err != nil {
					return err
				}
			}
		}
	}
	for _, spec := range tableCatalog {
		if !slices.Contains(memoryTables, spec.name) {
			continue
		}
		for _, row := range d[spec.name] {
			values := map[string]string{}
			for _, column := range spec.cols() {
				values[column] = spec.value(row, column)
			}
			if spec.name == "memory_items" {
				for _, column := range []string{"actor_id", "session_id"} {
					if values[column] == "" && row[spec.index(column)] != nil {
						return errors.New("shared memory owner must be SQL NULL")
					}
				}
			}
			if _, err := owners.index.Owner(spec.name, values); err != nil {
				return errors.New("invalid memory document, source, lineage or ownership")
			}
		}
	}
	return validateMemoryContextsWithEvidence(d, evidence, documents["memory_revisions.document"])
}

func validateMemoryContexts(d Data) error {
	return validateMemoryContextsWithEvidence(d, nil)
}

func validateMemoryContextsWithEvidence(d Data, evidence map[string]retention.ExpiredProjection, retired ...map[string]retention.ExpiredProjection) error {
	revisions := map[string]memory.Item{}
	for _, spec := range tableCatalog {
		if spec.name != "memory_revisions" {
			continue
		}
		for _, row := range d[spec.name] {
			if len(retired) > 0 {
				if _, ok := retired[0][spec.value(row, "id")]; ok {
					continue
				}
			}
			var item memory.Item
			if strictJSON([]byte(spec.value(row, "document")), &item) != nil {
				return errors.New("invalid memory revision")
			}
			revisions[spec.value(row, "id")] = item
		}
	}
	for _, spec := range tableCatalog {
		if spec.name != "context_packages" {
			continue
		}
		for _, row := range d[spec.name] {
			var payload struct {
				Memory *memory.Pack `json:"memory"`
				State  string       `json:"state"`
			}
			if json.Unmarshal([]byte(spec.value(row, "payload")), &payload) != nil {
				return errors.New("invalid memory context payload")
			}
			proof, hasProof := evidence[spec.value(row, "id")]
			if payload.State == "EXPIRED" || hasProof {
				source, err := retention.ParseSourceProjection([]byte(spec.value(row, "payload")), "context_packages.payload")
				binding := proof.Summary.Context
				a, _ := json.Marshal(source)
				b, _ := json.Marshal(proof)
				if err != nil || !hasProof || binding == nil || !bytes.Equal(a, b) || binding.ID != spec.value(row, "id") || binding.IndexID != spec.value(row, "index_id") || binding.ContentHash != spec.value(row, "content_hash") {
					return errExpiredArchiveEvidence
				}
				continue
			}
			if payload.Memory == nil {
				continue
			}
			pack := *payload.Memory
			storedHash := pack.Hash
			pack.Hash = ""
			raw, err := json.Marshal(pack)
			if err != nil || !hex64.MatchString(storedHash) || digest(raw) != storedHash || pack.Request.Scope.Project != spec.value(row, "project_id") || pack.Request.Scope.Branch != spec.value(row, "branch_id") || pack.Request.Scope.Actor != spec.value(row, "actor_id") {
				return errors.New("memory context identity or hash mismatch")
			}
			items := make([]memory.Item, 0, len(pack.Items))
			legacyPrivateControl := false
			for _, selected := range pack.Items {
				original, exists := revisions[selected.Item.Revision]
				if !exists {
					return errors.New("memory context revision missing")
				}
				// Relevance is derived per query, never persisted authority.
				original.Score = selected.Item.Score
				a, _ := json.Marshal(original)
				b, _ := json.Marshal(selected.Item)
				if !bytes.Equal(a, b) {
					return errors.New("memory context changed its pinned revision")
				}
				items = append(items, selected.Item)
				if selected.Channel == "control" && selected.Item.Control && (selected.Item.State == "candidate" || selected.Item.State == "working") {
					legacyPrivateControl = true
				}
			}
			var compiled memory.Pack
			if legacyPrivateControl {
				// Preserve the immutable old classification only for archive
				// verification. Runtime always recompiles with current semantics,
				// so this does not restore execution eligibility or alter hashes.
				compiled, err = memory.CompileLegacyPrivateControl(pack.Request, items)
			} else {
				compiled, err = memory.Compile(pack.Request, items)
			}
			if err != nil || len(compiled.Items) != len(items) {
				return errors.New("memory context contains unavailable private or temporal material")
			}
			for i := range compiled.Items {
				if compiled.Items[i].Item.ID != pack.Items[i].Item.ID || compiled.Items[i].Channel != pack.Items[i].Channel || compiled.Items[i].Reason != pack.Items[i].Reason {
					return errors.New("memory context changed compiled authority channel")
				}
			}
		}
	}
	return nil
}

// Memory source hashes are pinned published resource objects already enumerated
// by the release manifest. Do not treat arbitrary JSON hash fields as blobs.
// verifiedRetention may be true only after validatedRetentionReferences has
// certified every actual row and its independent journal. Here only retained
// object anchors remain to be checked; this is not an expiry proof validator.
func validateMemoryObjects(d Data, blobs map[string][]byte, verifiedRetention ...bool) error {
	for _, spec := range tableCatalog {
		if spec.name != "memory_items" && spec.name != "memory_revisions" {
			continue
		}
		for _, row := range d[spec.name] {
			if len(verifiedRetention) > 0 && verifiedRetention[0] {
				if _, err := retention.ParseSourceProjection([]byte(spec.value(row, "document")), spec.name+".document"); err == nil {
					continue
				}
			}
			var item memory.Item
			if strictJSON([]byte(spec.value(row, "document")), &item) != nil {
				return errors.New("invalid memory source document")
			}
			for _, source := range item.Sources {
				raw, exists := blobs["blobs/"+item.Project+"/"+source.Hash]
				if !exists || !utf8.Valid(raw) || digest(raw) != source.Hash || source.Start < 0 || source.End <= source.Start || source.End > utf8.RuneCount(raw) {
					return errors.New("memory source anchor exceeds exact published object")
				}
			}
		}
	}
	return nil
}
