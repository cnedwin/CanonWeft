package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// RecoveryExpiryPlan is derived exclusively from locked database rows. It is
// transaction-local and never deserialized from an API request or backup file.
type RecoveryExpiryPlan struct {
	project  string
	current  map[string]ReferenceVersion
	versions []ReferenceVersion
	journals []ExpiryJournalRecord
	fields   map[RecoveryOwner]map[string]ExpiredProjection
	sources  map[string]recoverySourceHistory
}

// Erased memory documents need their independent proof bound before ordinary
// content ownership can be resolved. Check durable scope first; bindExpiryVersions
// below still verifies the full evidence and ordinary ownership before any use.
func recoveryExpiryOwnerScope(index *SourceIndex, row SourceRow) (SourceOwner, error) {
	if row.Table == "memory_sessions" || row.Table == "memory_items" || row.Table == "memory_revisions" {
		return index.MemoryDocumentMetadataOwner(row)
	}
	return index.Owner(row.Table, row.Values)
}

func (p *RecoveryExpiryPlan) Field(table, key, field string) (ExpiredProjection, bool) {
	if p == nil {
		return ExpiredProjection{}, false
	}
	v, ok := p.fields[RecoveryOwner{table, key}][field]
	return v, ok
}

func ReadRecoveryExpiriesTx(ctx context.Context, tx *sql.Tx, project string, keys []SourceKey) (*RecoveryExpiryPlan, error) {
	p := &RecoveryExpiryPlan{project: project, current: map[string]ReferenceVersion{}, fields: map[RecoveryOwner]map[string]ExpiredProjection{}}
	if tx == nil || !id32.MatchString(project) || len(keys) > 65536 {
		return nil, ErrInvalidInventory
	}
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil || !enabled {
		return p, err
	}
	var clock string
	if err = tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return nil, err
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return nil, err
	}
	requested := map[RecoveryOwner]SourceKey{}
	for _, key := range keys {
		owner, err := RowOwnerKey(key.Table, key.Primary)
		if err != nil {
			return nil, err
		}
		requested[RecoveryOwner{key.Table, owner}] = key
	}
	current, err := readReferenceVersions(ctx, tx, project, now)
	if err != nil {
		return nil, err
	}
	index, _ := NewSourceIndex(nil)
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index.loadSource = reader.read
	for id, v := range current {
		key, exists := requested[RecoveryOwner{v.Table, v.OwnerKey}]
		if !exists || v.Reference.State != Expired {
			continue
		}
		row, err := index.sourceRow(key.Table, key.Primary)
		if err != nil {
			return nil, err
		}
		owner, err := recoveryExpiryOwnerScope(index, row)
		if err != nil || owner.Shared || owner.ProjectID != project {
			return nil, ErrSourceOwnership
		}
		p.current[id] = v
	}
	if len(p.current) == 0 {
		return p, nil
	}
	if err = index.bindExpiryVersions(ctx, tx, project, p.current, now, true); err != nil {
		return nil, err
	}
	// Historical deadlines use the lifecycle recorded in the source graph. The
	// caller independently enforces today's deletion/recovery window.
	known, err := readRecoveryJournalTx(ctx, tx, project, p.current, now)
	if err != nil {
		return nil, err
	}
	for _, v := range known {
		if v.Schema != 2 {
			continue
		}
		journal, err := readRecoveryExpiryJournalTx(ctx, tx, v)
		if err != nil {
			return nil, err
		}
		_, proof, err := ValidateExpiryJournalRecord(journal)
		if err != nil {
			return nil, err
		}
		parent, err := decodeReferenceVersion(journal.Parent, journal.ParentHash)
		if err != nil {
			return nil, err
		}
		k := referenceKey(parent.Reference)
		if previous, exists := known[k]; exists && stableHash(previous) != stableHash(parent) {
			return nil, ErrInvalidInventory
		}
		known[k] = parent
		p.journals = append(p.journals, journal)
		if source, isCurrent := p.current[v.Reference.ID]; isCurrent && source.Reference.Revision == v.Reference.Revision {
			owner := RecoveryOwner{v.Table, v.OwnerKey}
			if p.fields[owner] == nil {
				p.fields[owner] = map[string]ExpiredProjection{}
			}
			p.fields[owner][v.Field] = proof
		}
	}
	for _, v := range known {
		p.versions = append(p.versions, v)
	}
	if err = ValidateArchivedPins(p.versions); err != nil {
		return nil, err
	}
	p.sources, err = readRecoverySourceHistoryTx(ctx, tx, p.versions)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func readRecoveryExpiryJournalTx(ctx context.Context, tx *sql.Tx, v ReferenceVersion) (ExpiryJournalRecord, error) {
	var j ExpiryJournalRecord
	raw, err := json.Marshal(v)
	if err != nil || v.Expiration == nil {
		return j, ErrInvalidInventory
	}
	j.Version, j.VersionHash = string(raw), financialDigest(raw)
	err = tx.QueryRowContext(ctx, `SELECT p.snapshot,p.record_hash,e.id,e.projection,e.projection_hash,e.original_payload_hash,CAST(e.expired_at AS CHAR)
 FROM retention_reference_versions p JOIN retention_evidence e ON e.project_id=p.project_id AND e.reference_id=p.reference_id AND e.reference_revision=?
 WHERE p.project_id=? AND p.reference_id=? AND p.revision=? FOR SHARE`, v.Reference.Revision, v.ProjectID, v.Reference.ID, v.Expiration.PriorRevision).Scan(&j.Parent, &j.ParentHash, &j.EvidenceID, &j.Evidence, &j.EvidenceHash, &j.OriginalPayloadHash, &j.EvidenceExpiredAt)
	return j, err
}

// Rebind returns new envelopes for the caller's destination rows. It does not
// write them or commit; the outer author savepoint owns the entire operation.
func (p *RecoveryExpiryPlan) Rebind(mapping RecoveryExpiryMap) (RecoveryExpiryGraph, error) {
	if p == nil || mapping.SourceProject != p.project {
		return RecoveryExpiryGraph{}, ErrInvalidInventory
	}
	return RebindRecoveryExpiryGraph(p.versions, p.journals, p.bindHistoricalOwners(mapping))
}

func (p *RecoveryExpiryPlan) ReboundField(table, key, field string, mapping RecoveryExpiryMap, graph RecoveryExpiryGraph) (ExpiredProjection, bool, error) {
	original, exists := p.Field(table, key, field)
	if !exists {
		return ExpiredProjection{}, false, nil
	}
	owner := key
	if target, ok := mapping.Owners[RecoveryOwner{table, key}]; ok {
		owner = target
	}
	targetField, err := recoveryExpiryField(table, field, mapping.Resources)
	if err != nil {
		return ExpiredProjection{}, false, err
	}
	id, err := ReferenceID(mapping.TargetProject, table, owner, targetField)
	if err != nil {
		return ExpiredProjection{}, false, err
	}
	current, ok := p.current[original.ReferenceID]
	if !ok {
		return ExpiredProjection{}, false, ErrInvalidInventory
	}
	proof, ok := graph.Proofs[RecoveryReferenceKey{id, current.Reference.Revision}]
	if !ok {
		return ExpiredProjection{}, false, ErrInvalidInventory
	}
	return proof, true, nil
}

func (p *RecoveryExpiryPlan) WriteTx(ctx context.Context, tx *sql.Tx, mapping RecoveryExpiryMap, graph RecoveryExpiryGraph) error {
	mapping = p.bindHistoricalOwners(mapping)
	// Recompute from the verified plan instead of accepting caller-made evidence.
	expected, err := p.Rebind(mapping)
	if err != nil {
		return err
	}
	if stableHash(expected.Versions) != stableHash(graph.Versions) || len(expected.Proofs) != len(graph.Proofs) {
		return ErrInvalidInventory
	}
	for key, proof := range expected.Proofs {
		actual, ok := graph.Proofs[key]
		if !ok || stableHash(actual) != stableHash(proof) {
			return ErrInvalidInventory
		}
	}
	current := map[string]ReferenceVersion{}
	wanted := map[RecoveryReferenceKey]bool{}
	for _, source := range p.current {
		target := source.OwnerKey
		if next, exists := mapping.Owners[RecoveryOwner{source.Table, source.OwnerKey}]; exists {
			target = next
		}
		field, e := recoveryExpiryField(source.Table, source.Field, mapping.Resources)
		if e != nil {
			return e
		}
		id, e := ReferenceID(mapping.TargetProject, source.Table, target, field)
		if e != nil {
			return e
		}
		wanted[RecoveryReferenceKey{id, source.Reference.Revision}] = true
	}
	for _, v := range graph.Versions {
		// Current identities are selected through the exact original mapping. Other
		// ancestors remain immutable metadata only, with no restored execution rows.
		if wanted[RecoveryReferenceKey{v.Reference.ID, v.Reference.Revision}] {
			current[v.Reference.ID] = v
		}
	}
	if len(current) != len(wanted) {
		return ErrInvalidInventory
	}
	if err = ValidateJournalClosure(graph.Versions, current); err != nil {
		return err
	}
	if err = p.writeSourcesTx(ctx, tx, mapping); err != nil {
		return err
	}
	for _, v := range graph.Versions {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var storedHash string
		err = tx.QueryRowContext(ctx, "SELECT record_hash FROM retention_reference_versions WHERE reference_id=? AND revision=?", v.Reference.ID, v.Reference.Revision).Scan(&storedHash)
		if err == nil {
			if storedHash != financialDigest(raw) {
				return ErrInvalidInventory
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO retention_reference_versions(reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, v.Reference.ID, v.Reference.Revision, v.ProjectID, v.SourceID, v.SourcePayloadHash, financialDigest(raw), raw, lifecycleSQLTime(v.RecordedAt)); err != nil {
			return err
		}
		if v.Schema == 2 {
			proof := graph.Proofs[RecoveryReferenceKey{v.Reference.ID, v.Reference.Revision}]
			payload, _ := json.Marshal(proof)
			id, e := EvidenceID(v.Reference.ID, v.Reference.Revision)
			if e != nil {
				return e
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO retention_evidence(id,project_id,reference_id,reference_revision,original_payload_hash,projection_hash,projection,expired_at) VALUES(?,?,?,?,?,?,?,?)`, id, v.ProjectID, v.Reference.ID, v.Reference.Revision, proof.OriginalPayloadHash, financialDigest(payload), payload, lifecycleSQLTime(proof.ExpiredAt)); err != nil {
				return err
			}
		}
	}
	for _, v := range current {
		if _, err = tx.ExecContext(ctx, `INSERT INTO retention_references(id,project_id,owner_table,owner_key,field_key,class,storage_kind,object_hash,payload_hash,source_created_at,time_basis,first_verified_at,deadline,state,expired_at,content_root,accounting_open,lease_until,revision,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, referenceRowArgs(v)...); err != nil {
			return err
		}
	}
	index, _ := NewSourceIndex(nil)
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index.loadSource = reader.read
	for owner, row := range mapping.Rows {
		if _, exists := p.fields[owner]; !exists {
			continue
		}
		primary := map[string]string{}
		for _, column := range PrimaryKey(row.Table) {
			primary[column.Name] = row.Values[column.Name]
		}
		actual, e := index.sourceRow(row.Table, primary)
		if e != nil {
			return e
		}
		target, e := recoveryExpiryOwnerScope(index, actual)
		if e != nil || target.Shared || target.ProjectID != mapping.TargetProject {
			return ErrSourceOwnership
		}
	}
	return index.bindExpiryVersions(ctx, tx, mapping.TargetProject, current, time.Now().UTC(), true)
}
