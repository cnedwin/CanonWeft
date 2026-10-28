package retention

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"time"
)

// StoredReference binds the exact reference revision to its separately persisted
// evidence. It must be loaded from trusted storage, never supplied by a client.
type StoredReference struct {
	ID, ProjectID, Table, OwnerKey, Field   string
	Storage                                 StorageKind
	State                                   PayloadState
	Revision, EvidenceRevision              int64
	PayloadHash, ObjectHash, ProjectionHash string
	Deadline, ExpiredAt, EvidenceExpiredAt  time.Time
}

// StoredOwner is independently resolved from the actual source row and its PK.
type StoredOwner struct {
	TransientScopeHash           string
	RunID, StepKey, FrozenHash   string
	ContextIndexID               string
	RequestIdentity              *RequestIdentitySummary
	ContentHash, SourceRevision  string
	ProjectID, Table, Key, Field string
	CallID                       string
	Value                        []byte
}

// ParseExpiredProjection validates the closed canonical evidence format only.
// It does not establish source ownership or replace ValidateStoredProjection.
func ParseExpiredProjection(raw []byte, rule string) (ExpiredProjection, error) {
	var projection ExpiredProjection
	if financialJSON(raw, &projection) != nil || ValidateProjectionForRule(projection, rule) != nil {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	canonical, err := json.Marshal(projection)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	return projection, nil
}

// These actively handled source fields are physically MySQL JSON, unlike the
// canonical TEXT evidence column. Keep this list tied to the migration DDL.
func sourceProjectionUsesSQLJSON(rule string) bool {
	if rule == "outbox.payload" {
		return true
	}
	if rule == "skill_packages.report" || rule == "skill_packages.source" {
		return true
	}
	if rule == "ip_revisions.payload" || rule == "artifact_releases.manifest" {
		return true
	}
	if slices.Contains([]string{"memory_items.document", "memory_revisions.document", "source_imports.report", "ip_promotions.payload", "change_plans.payload", "knowledge_indexes.manifest"}, rule) {
		return true
	}
	return slices.Contains([]string{"model_call_receipts.payload", "model_reconciliations.payload", "generation_tasks.frozen", "tool_trials.frozen", "context_packages.payload", "skill_discovery_runs.inventory", "skill_discovery_runs.source_snapshot", "working_indexes.manifest", "ip_drafts.payload", "resource_policy_drafts.payload", "memory_sessions.document"}, rule)
}

func ParseSourceProjection(raw []byte, rule string) (ExpiredProjection, error) {
	if !sourceProjectionUsesSQLJSON(rule) {
		return ParseExpiredProjection(raw, rule)
	}
	var projection ExpiredProjection
	if financialJSON(raw, &projection) != nil || ValidateProjectionForRule(projection, rule) != nil {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	canonical, err := json.Marshal(projection)
	if err != nil || !sameStoredJSON(raw, canonical) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	return projection, nil
}

// ValidateStoredProjection is shared by online readers and archive validation.
// Matching an envelope alone is insufficient: the registry, evidence, source
// identity, original digest, representation and expiry clock must all agree.
func ValidateStoredProjection(ref StoredReference, owner StoredOwner, evidence []byte) (ExpiredProjection, error) {
	var projection ExpiredProjection
	fail := func() (ExpiredProjection, error) { return ExpiredProjection{}, ErrInvalidInventory }
	logicalKey := owner.Key
	if owner.RequestIdentity != nil {
		original, current, err := requestIdentityOwnerKeys(owner.Table, ref.PayloadHash, *owner.RequestIdentity)
		if err != nil || owner.Field != "operation_id" || owner.Key != current {
			return fail()
		}
		logicalKey = original
	}
	id, err := ReferenceID(owner.ProjectID, owner.Table, logicalKey, owner.Field)
	rule, known := ConcreteRule(owner.Table + "." + owner.Field)
	if err != nil || !known || ref.ID != id || ref.ProjectID != owner.ProjectID || ref.Table != owner.Table || ref.OwnerKey != logicalKey || ref.Field != owner.Field || rule.Storage != ref.Storage || ref.State != Expired || ref.Revision < 1 || ref.EvidenceRevision != ref.Revision {
		return fail()
	}
	if !hash64.MatchString(ref.PayloadHash) || !hash64.MatchString(ref.ProjectionHash) || ref.Deadline.IsZero() || ref.ExpiredAt.IsZero() || ref.ExpiredAt.Before(ref.Deadline) || !ref.EvidenceExpiredAt.Equal(ref.ExpiredAt) || financialDigest(evidence) != ref.ProjectionHash {
		return fail()
	}
	if financialJSON(evidence, &projection) != nil || ValidateProjectionForRule(projection, rule.Key) != nil || projection.ReferenceID != ref.ID || projection.OriginalPayloadHash != ref.PayloadHash || !projection.ExpiredAt.Equal(ref.ExpiredAt) {
		return fail()
	}
	if (projection.Materialization == AnonymizedRequestKey) != (owner.RequestIdentity != nil) {
		return fail()
	}
	if len(TransientColumns(rule.Key)) > 0 && (!validTransientSummary(rule.Key, projection.Summary) || projection.Summary.ContentScopeHash != owner.TransientScopeHash) {
		return fail()
	}
	if binding := projection.Summary.Context; binding != nil {
		key, err := RowOwnerKey(owner.Table, map[string]string{"id": binding.ID})
		if err != nil || key != owner.Key || binding.ContentHash != owner.ContentHash || binding.IndexID != owner.ContextIndexID {
			return fail()
		}
	}
	if binding := projection.Summary.RecipeRun; binding != nil {
		key, err := RowOwnerKey(owner.Table, map[string]string{"id": binding.ID})
		if err != nil || key != owner.Key || owner.FrozenHash != projection.OriginalPayloadHash {
			return fail()
		}
	}
	if binding := projection.Summary.RecipeStep; binding != nil {
		key, err := RowOwnerKey(owner.Table, map[string]string{"id": binding.ID})
		if err != nil || key != owner.Key || owner.FrozenHash != projection.OriginalPayloadHash || owner.RunID != binding.RunID || LabelIdentityHash(owner.StepKey) != binding.KeyHash {
			return fail()
		}
	}
	financial := rule.Key == "model_call_receipts.payload" || rule.Key == "model_reconciliations.payload"
	if financial {
		if owner.CallID == "" || projection.Summary.CallID != owner.CallID {
			return fail()
		}
		sourceID := ""
		if receipt := projection.Summary.Receipt; receipt != nil {
			sourceID = receipt.ID
		}
		if adjustment := projection.Summary.Reconciliation; adjustment != nil {
			sourceID = adjustment.ID
			if adjustment.ContentHash != owner.ContentHash || strconv.FormatInt(adjustment.Revision, 10) != owner.SourceRevision {
				return fail()
			}
		}
		sourceKey, err := RowOwnerKey(owner.Table, map[string]string{"id": sourceID})
		if err != nil || sourceKey != owner.Key {
			return fail()
		}
	}
	canonical, err := json.Marshal(projection)
	if err != nil || !bytes.Equal(evidence, canonical) {
		return fail()
	}
	switch ref.Storage {
	case Inline:
		if ref.ObjectHash != "" {
			return fail()
		}
		switch projection.Materialization {
		case InlineEnvelope:
			if sourceProjectionUsesSQLJSON(rule.Key) {
				// These source columns are MySQL JSON. Its serializer changes
				// whitespace/key order; the separate evidence is canonical text.
				if !sameStoredJSON(owner.Value, evidence) {
					return fail()
				}
			} else if !bytes.Equal(owner.Value, evidence) {
				return fail()
			}
		case ClearScalar:
			if !ClearScalarAllowed(rule.Key) || len(owner.Value) != 0 {
				return fail()
			}
		case AnonymizedLabel:
			if !AnonymizedLabelAllowed(rule.Key) || string(owner.Value) != retiredLabelPrefix+ref.PayloadHash {
				return fail()
			}
		case AnonymizedRequestKey:
			if *owner.RequestIdentity != *projection.Summary.RequestIdentity || string(owner.Value) != "retired-v1:"+ref.PayloadHash {
				return fail()
			}
		default:
			return fail()
		}
	case Blob:
		if !hash64.MatchString(ref.ObjectHash) || projection.Summary.ValueHash != ref.ObjectHash || financialDigest([]byte(ref.ObjectHash)) != ref.PayloadHash {
			return fail()
		}
		valueHash := ref.ObjectHash
		if recovered := projection.Summary.RecoveryManifestHash; recovered != "" {
			valueHash = recovered
		}
		if string(owner.Value) != valueHash {
			return fail()
		}
	default:
		return fail()
	}
	return projection, nil
}

func sameStoredJSON(left, right []byte) bool {
	normalize := func(raw []byte) ([]byte, error) {
		var value any
		if err := financialJSON(raw, &value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	l, err := normalize(left)
	if err != nil {
		return false
	}
	r, err := normalize(right)
	return err == nil && bytes.Equal(l, r)
}
