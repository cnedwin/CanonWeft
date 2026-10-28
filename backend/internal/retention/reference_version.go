package retention

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// ReferenceVersion is immutable identity/age evidence, never a body copy or
// permission to erase. Schema 1 records PENDING/PARTIAL lineage and an own-policy
// base deadline; it cannot be consumed as a complete inherited expiry decision.
// Schema 2 records an expiry transition linked to that exact retained parent,
// its effective deadline and a separately persisted body-free proof.
type ReferenceVersion struct {
	// ProvenanceOnly is immutable historical evidence, never a live registry head.
	ProvenanceOnly       bool                       `json:"provenance_only,omitempty"`
	Expiration           *ReferenceExpiration       `json:"expiration,omitempty"`
	Schema               int                        `json:"schema"`
	ProjectID            string                     `json:"project_id"`
	Table                string                     `json:"table"`
	OwnerKey             string                     `json:"owner_key"`
	Field                string                     `json:"field"`
	Class                Class                      `json:"class"`
	Storage              StorageKind                `json:"storage"`
	SourceID             string                     `json:"source_id"`
	SourcePayloadHash    string                     `json:"source_payload_hash"`
	ValueFirstVerifiedAt time.Time                  `json:"value_first_verified_at"`
	LineageState         string                     `json:"lineage_state"`
	Reference            Reference                  `json:"reference"`
	Lifecycle            Lifecycle                  `json:"lifecycle"`
	Deadline             time.Time                  `json:"deadline"`
	RecordedAt           time.Time                  `json:"recorded_at"`
	Accounting           *AccountingMetadata        `json:"accounting,omitempty"`
	DraftVersion         *DraftVersionEvidence      `json:"draft_version,omitempty"`
	WorkingProjection    *WorkingProjectionEvidence `json:"working_projection,omitempty"`
	TextProof            *TextCopyEvidence          `json:"text_proof,omitempty"`
}

// Expiration records the transition separately from the original source body.
type ReferenceExpiration struct {
	PriorRevision   int64     `json:"prior_revision"`
	PriorHash       string    `json:"prior_hash"`
	PriorRecordedAt time.Time `json:"prior_recorded_at"`
	EvidenceHash    string    `json:"evidence_hash"`
	BaseDeadline    time.Time `json:"base_deadline"`
}

func materialReferenceVersion(project string, material ObservedMaterial, lifecycle Lifecycle, now time.Time) (ReferenceVersion, error) {
	ref := Reference{
		ID: material.ID, Rule: material.Table + "." + material.Field, ObjectHash: material.ObjectHash, PayloadHash: material.PayloadHash,
		// This is the conservative legacy age baseline, not a claimed UTC time
		// of original source creation. Unknown historical wall clocks are unused.
		CreatedAt: material.FirstVerifiedAt.UTC(), FirstVerifiedAt: material.FirstVerifiedAt.UTC(),
		TimeBasis: LegacyRegistered, Revision: 1, State: Retained, ContentRoot: material.ContentRoot,
	}
	if material.Accounting != nil {
		ref.AccountingOpen = material.Accounting.open()
	}
	class := material.Class
	if ref.ContentRoot {
		class = Content
	}
	deadline, err := Deadline(class, ref.FirstVerifiedAt, lifecycle)
	if err != nil || material.TimeBasis != LegacyRegistered {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	v := ReferenceVersion{
		Schema: 1, ProjectID: project, Table: material.Table, OwnerKey: material.OwnerKey, Field: material.Field,
		Class: material.Class, Storage: material.Storage, SourceID: material.SourceID, SourcePayloadHash: material.SourcePayloadHash,
		ValueFirstVerifiedAt: material.ValueFirstVerifiedAt.UTC(), LineageState: "PENDING", Reference: ref,
		Lifecycle: lifecycle, Deadline: deadline, RecordedAt: now.UTC(), Accounting: material.Accounting, DraftVersion: material.DraftVersion, WorkingProjection: material.WorkingProjection, TextProof: material.TextProof,
	}
	if !validReferenceVersion(v) {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	return v, nil
}

func validReferenceVersion(v ReferenceVersion) bool {
	if v.Schema == 2 {
		return validExpiredReferenceVersion(v)
	}
	if v.Expiration != nil {
		return false
	}
	if !validWorkingProjection(v) || !validTextCopyEvidence(v) {
		return false
	}
	r := v.Reference
	rule, ok := ConcreteRule(v.Table + "." + v.Field)
	id, err := ReferenceID(v.ProjectID, v.Table, v.OwnerKey, v.Field)
	if err != nil || !ok || v.Schema != 1 || v.Class != rule.Class || v.Storage != rule.Storage || r.ID != id || r.Rule != rule.Key || r.Revision < 1 || !hash64.MatchString(r.PayloadHash) || r.State != Retained || !r.ExpiredAt.IsZero() || (r.ContentRoot && !rule.Promotable) {
		return false
	}
	if (v.LineageState == "PENDING" && len(r.Sources) != 0) || (v.LineageState == "PARTIAL" && len(r.Sources) == 0) || (v.LineageState != "PENDING" && v.LineageState != "PARTIAL") || len(r.Sources) > 4096 {
		return false
	}
	if len(r.Sources) > 0 && (r.ContentRoot || rule.Storage == Metadata || rule.Class == Audit || rule.Class == ProtectedAuthority) {
		return false
	}
	previous := ""
	for _, pin := range r.Sources {
		if !hash64.MatchString(pin.ReferenceID) || pin.ReferenceID == r.ID || pin.ReferenceID <= previous || pin.Revision < 1 || !hash64.MatchString(pin.PayloadHash) {
			return false
		}
		previous = pin.ReferenceID
	}
	if v.Storage == Metadata {
		if v.Accounting == nil || !validAccountingMetadata(*v.Accounting, v.Table, v.OwnerKey) || v.Field != accountingField(v.Table) || r.AccountingOpen != v.Accounting.open() || r.PayloadHash != stableHash(v.Accounting) || v.SourcePayloadHash != r.PayloadHash {
			return false
		}
	} else if v.Accounting != nil || r.AccountingOpen {
		return false
	}
	if v.DraftVersion != nil && !validDraftVersion(v.Table, v.Field, v.DraftVersion) {
		return false
	}
	if v.DraftVersion != nil && v.Table != "resource_policy_drafts" && v.DraftVersion.ProjectionHash != r.PayloadHash {
		return false
	}
	if (v.Storage == Blob && !hash64.MatchString(r.ObjectHash)) || (v.Storage != Blob && r.ObjectHash != "") || !hash64.MatchString(v.SourcePayloadHash) || v.SourceID != observationID(v.ProjectID, v.Table, v.OwnerKey, strings.SplitN(v.Field, ".", 2)[0]) {
		return false
	}
	if r.TimeBasis != LegacyRegistered || !r.CreatedAt.Equal(r.FirstVerifiedAt) || r.FirstVerifiedAt.IsZero() || v.ValueFirstVerifiedAt.Before(r.FirstVerifiedAt) || v.ValueFirstVerifiedAt.After(v.RecordedAt) || v.RecordedAt.IsZero() || r.LeaseUntil.After(v.RecordedAt.Add(day)) || v.Lifecycle.DeletedAt.After(v.RecordedAt) {
		return false
	}
	born, err := referenceBirth(r, v.RecordedAt)
	if err != nil {
		return false
	}
	class := rule.Class
	if r.ContentRoot {
		class = Content
	}
	want, err := Deadline(class, born, v.Lifecycle)
	return err == nil && want.Equal(v.Deadline)
}

func decodeReferenceVersion(raw, hash string) (ReferenceVersion, error) {
	var v ReferenceVersion
	// Up to4096 pins can exceed the financial payload's64KiB cap. The entire
	// history scan remains separately bounded to64MiB across all records.
	if !hash64.MatchString(hash) || financialDigest([]byte(raw)) != hash || strictJSONWithLimit([]byte(raw), &v, 1<<20) != nil || !validReferenceVersion(v) {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	return v, nil
}

// ValidateReferenceVersionRecord verifies a canonical immutable journal record
// for offline archive/log readers. It does not prove its source or live authority.
func ValidateReferenceVersionRecord(raw, hash string) (ReferenceVersion, error) {
	return decodeReferenceVersion(raw, hash)
}
