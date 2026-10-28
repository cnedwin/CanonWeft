package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Keep erased source bytes erased. Only independently verified journal/proof
// pairs can remove a source column from fresh observation/material collection.
// No document is synthesized and no expiry envelope becomes new author data.
func (index *SourceIndex) bindExpiredSources(ctx context.Context, tx *sql.Tx, project string, now time.Time) error {
	versions, err := readReferenceVersions(ctx, tx, project, now)
	if err != nil {
		return err
	}
	if err := index.bindExpiryVersions(ctx, tx, project, versions, now, true); err != nil {
		return err
	}
	return index.validateRetiredRequestCoverage(project)
}

// Selected writes inspect only the compiled expiry fields of this owner. Never
// turn a bounded business write into an inventory scan of the whole project.
func (index *SourceIndex) bindExpiredRowMode(ctx context.Context, tx *sql.Tx, project string, row SourceRow, now time.Time, diagnosticsOnly bool) error {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	if !diagnosticsOnly && row.Table == "operations" && strings.HasPrefix(strings.ToLower(row.Values["operation_id"]), "retired-v1:") {
		hash := strings.TrimPrefix(row.Values["operation_id"], "retired-v1:")
		alias, err := readRequestAliasRecord(ctx, tx, row.Table, row.Values["actor_id"], hash, true)
		if err != nil {
			return err
		}
		original, current, err := requestIdentityOwnerKeys(row.Table, hash, alias.Identity)
		if err != nil || current != key || alias.OriginalOwnerKey != original || alias.AnonymousKey != row.Values["operation_id"] {
			return ErrSourceOwnership
		}
		key = original
	}
	var ids []string
	for _, rule := range referenceRules {
		if !strings.HasPrefix(rule.Key, row.Table+".") {
			continue
		}
		field := strings.TrimPrefix(rule.Key, row.Table+".")
		if diagnosticsOnly {
			column, ok := Column(row.Table, field)
			if !ok || !diagnosticColumn(column) {
				continue
			}
		}
		if !supportedExpiryField(row.Table, field) && !requestKeyRule(rule.Key) {
			continue
		}
		id, err := ReferenceID(project, row.Table, key, field)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	if !diagnosticsOnly && (row.Table == "memory_items" || row.Table == "memory_revisions") {
		item := row
		if row.Table == "memory_revisions" {
			item, err = index.memoryRow("memory_items", row.Values["item_id"], project)
			if err != nil {
				return err
			}
		}
		head, err := index.memoryRow("memory_revisions", item.Values["head_revision"], project)
		if err != nil || head.Values["item_id"] != item.Values["id"] {
			return ErrSourceOwnership
		}
		// A current item and its head version form one equality proof. Bind both
		// before either Owner check; this remains a bounded three-row read.
		for _, peer := range []SourceRow{item, head} {
			owner, e := RowOwnerKey(peer.Table, peer.Values)
			if e != nil {
				return e
			}
			id, e := ReferenceID(project, peer.Table, owner, "document")
			if e != nil {
				return e
			}
			ids = append(ids, id)
		}
	}
	versions, err := readReferenceVersions(ctx, tx, project, now, ids...)
	if err != nil {
		return err
	}
	if err := index.bindExpiryVersions(ctx, tx, project, versions, now, true); err != nil {
		return err
	}
	if !diagnosticsOnly {
		return index.validateRetiredRequestRow(project, row)
	}
	return nil
}

func (index *SourceIndex) replacedColumn(project string, row SourceRow, field string) bool {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return false
	}
	_, replaced := index.replacedSources[observationID(project, row.Table, key, strings.SplitN(field, ".", 2)[0])]
	return replaced
}

func (index *SourceIndex) bindExpiryVersions(ctx context.Context, tx *sql.Tx, project string, versions map[string]ReferenceVersion, now time.Time, current bool) error {
	return index.bindVerifiedExpiryVersions(project, versions,
		func(v ReferenceVersion) (ExpiredProjection, error) {
			proof, err := verifyExpiryVersionStorageMode(ctx, tx, v, current)
			if err != nil {
				return ExpiredProjection{}, err
			}
			if identity := proof.Summary.RequestIdentity; identity != nil {
				alias, err := readRequestAliasRecord(ctx, tx, v.Table, identity.ActorID, v.Reference.PayloadHash, current)
				if err != nil || validateRequestAlias(v, proof, alias) != nil || alias.CreatedAt.After(now) || alias.PolicyCreatedAt.After(now) || alias.PolicyClosedAt.After(now) {
					return ExpiredProjection{}, ErrSourceOwnership
				}
			}
			return proof, nil
		},
		func(row SourceRow, v ReferenceVersion) (string, error) {
			return replayNestedSourceValueMode(ctx, tx, project, row, v, versions, now, current)
		})
}

func (index *SourceIndex) bindVerifiedExpiryVersions(project string, versions map[string]ReferenceVersion, proofFor func(ReferenceVersion) (ExpiredProjection, error), nestedValue func(SourceRow, ReferenceVersion) (string, error)) error {
	if index.expired == nil {
		index.expired = map[string]ReferenceVersion{}
		index.expiryProofs = map[string]ExpiredProjection{}
		index.replacedSources = map[string]ReferenceVersion{}
	}
	memoryProofs := map[string]ExpiredProjection{}
	for id, v := range versions {
		if v.Reference.State != Expired || v.Field != "document" || (v.Table != "memory_items" && v.Table != "memory_revisions" && v.Table != "memory_sessions") {
			continue
		}
		proof, err := proofFor(v)
		row, exists := index.rows[v.Table][v.OwnerKey]
		if err != nil || !exists || index.BindVerifiedMemoryExpiry(row, proof) != nil {
			return ErrSourceOwnership
		}
		memoryProofs[id] = proof
	}
	for id, v := range versions {
		if v.Reference.State != Expired {
			continue
		}
		if !supportedExpiryField(v.Table, v.Field) && !requestKeyRule(v.Reference.Rule) {
			return ErrSourceOwnership
		}
		proof, loaded := memoryProofs[id]
		var err error
		if !loaded {
			proof, err = proofFor(v)
		}
		if err != nil {
			return err
		}
		physicalKey := v.OwnerKey
		if identity := proof.Summary.RequestIdentity; identity != nil {
			original, current, err := requestIdentityOwnerKeys(v.Table, v.Reference.PayloadHash, *identity)
			if err != nil || original != v.OwnerKey {
				return ErrSourceOwnership
			}
			physicalKey = current
		}
		row, exists := index.rows[v.Table][physicalKey]
		if !exists {
			return ErrSourceOwnership
		}
		owner, err := index.Owner(v.Table, row.Values)
		if err != nil || owner.Shared || owner.ProjectID != project {
			return ErrSourceOwnership
		}
		value := row.Values[v.Field]
		if v.Storage == Inline && row.Nulls[v.Field] {
			return ErrSourceOwnership
		}
		if v.Storage == Blob {
			value, err = nestedValue(row, v)
			if err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(proof)
		stored := StoredReference{ID: id, ProjectID: project, Table: v.Table, OwnerKey: v.OwnerKey, Field: v.Field, Storage: v.Storage, State: Expired, Revision: v.Reference.Revision, EvidenceRevision: v.Reference.Revision, PayloadHash: v.Reference.PayloadHash, ObjectHash: v.Reference.ObjectHash, ProjectionHash: v.Expiration.EvidenceHash, Deadline: v.Deadline, ExpiredAt: v.Reference.ExpiredAt, EvidenceExpiredAt: proof.ExpiredAt}
		actual := StoredOwner{ProjectID: project, Table: v.Table, Key: physicalKey, Field: v.Field, Value: []byte(value), CallID: row.Values["call_id"], ContentHash: row.Values["content_hash"], SourceRevision: row.Values["revision"], ContextIndexID: row.Values["index_id"]}
		actual.RunID, actual.StepKey, actual.FrozenHash = row.Values["run_id"], row.Values["step_key"], row.Values["frozen_hash"]
		if len(TransientColumns(v.Reference.Rule)) > 0 {
			actual.TransientScopeHash, err = TransientIdentity(v.Reference.Rule, row.Values)
			if err != nil || validateTransientOwner(row, v.Field, proof) != nil {
				return ErrSourceOwnership
			}
		}
		if proof.Summary.RequestIdentity != nil {
			identity, err := requestIdentityFromRow(row, owner)
			if err != nil {
				return err
			}
			actual.RequestIdentity = &identity
		}
		if _, err := ValidateStoredProjection(stored, actual, raw); err != nil {
			return err
		}
		if v.Table == "generation_tasks" && v.Field == "frozen" && proof.Summary.CallID != row.Values["call_id"] {
			return ErrSourceOwnership
		}
		index.expired[id], index.expiryProofs[id] = v, proof
		if v.Storage == Inline {
			if strings.Contains(v.Field, ".") {
				return ErrSourceOwnership
			}
			index.replacedSources[v.SourceID] = v
			if proof.Materialization == AnonymizedRequestKey {
				index.replacedSources[observationID(project, v.Table, physicalKey, v.Field)] = v
			}
		}
	}
	for _, v := range versions {
		if v.Reference.State == Retained && v.Table == "ip_revisions" && v.Field == "payload" && v.WorkingProjection != nil {
			row := index.rows[v.Table][v.OwnerKey]
			p := v.WorkingProjection
			if financialDigest([]byte(row.Values["payload"])) != v.Reference.PayloadHash || p.Version != row.Values["content_hash"] || p.State != row.Values["status"] || p.BaseHead != row.Values["base_head"] {
				continue
			}
			missing, valid := false, true
			for _, dep := range p.Dependencies {
				if erased, ok := index.expired[dep.ReferenceID]; ok {
					missing = true
					valid = valid && erased.Reference.PayloadHash == dep.PayloadHash
				} else if current, ok := versions[dep.ReferenceID]; !ok || current.Reference.PayloadHash != dep.PayloadHash {
					valid = false
				}
			}
			if missing && valid {
				if _, err := TransientEvidence(row, "payload"); err != nil {
					return err
				}
				if index.partialCandidates == nil {
					index.partialCandidates = map[string]ReferenceVersion{}
				}
				index.partialCandidates[v.Reference.ID] = v
			}
		}
		parent, replaced := index.replacedSources[v.SourceID]
		if !replaced || v.Reference.State == Expired {
			continue
		}
		if v.Storage != Blob || !supportedExpiryField(v.Table, v.Field) || v.SourcePayloadHash != parent.SourcePayloadHash {
			return ErrSourceOwnership
		}
		row := index.rows[v.Table][v.OwnerKey]
		value, err := nestedValue(row, v)
		if err != nil || value != v.Reference.ObjectHash || financialDigest([]byte(value)) != v.Reference.PayloadHash {
			return ErrSourceOwnership
		}
		m := MaterialReference{ID: v.Reference.ID, Table: v.Table, OwnerKey: v.OwnerKey, Field: v.Field, PayloadHash: v.Reference.PayloadHash, ObjectHash: v.Reference.ObjectHash, SourceID: v.SourceID, SourcePayloadHash: v.SourcePayloadHash, Storage: v.Storage, Class: v.Class, ContentRoot: v.Reference.ContentRoot, DraftVersion: v.DraftVersion, WorkingProjection: v.WorkingProjection, TextProof: v.TextProof}
		for _, pin := range v.Reference.Sources {
			m.Sources = append(m.Sources, MaterialSource{ReferenceID: pin.ReferenceID, Revision: pin.Revision, PayloadHash: pin.PayloadHash})
		}
		index.preservedAfterExpiry = append(index.preservedAfterExpiry, ObservedMaterial{MaterialReference: m, TimeBasis: v.Reference.TimeBasis, FirstVerifiedAt: v.Reference.FirstVerifiedAt, ValueFirstVerifiedAt: v.ValueFirstVerifiedAt})
	}
	return index.validateRetiredLabelCoverage(project)
}

func (index *SourceIndex) expiredPayload(row SourceRow) (ExpiredProjection, bool) {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return ExpiredProjection{}, false
	}
	owner, err := index.Owner(row.Table, row.Values)
	if err != nil {
		return ExpiredProjection{}, false
	}
	id, err := ReferenceID(owner.ProjectID, row.Table, key, "payload")
	if err != nil {
		return ExpiredProjection{}, false
	}
	proof, ok := index.expiryProofs[id]
	return proof, ok
}
