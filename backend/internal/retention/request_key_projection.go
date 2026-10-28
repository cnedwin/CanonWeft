package retention

import (
	"strings"
	"time"
	"unicode/utf8"
)

// RequestIdentitySummary retains result identity only. It contains neither the
// original free-form request key nor a replacement accounting/execution state.
type RequestIdentitySummary struct {
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"actor_id"`
	SourceID    string `json:"source_id"`
	RequestHash string `json:"request_hash"`
	Kind        string `json:"kind"`
}

func requestKeyRule(rule string) bool {
	parts := strings.Split(rule, ".")
	if len(parts) != 2 || parts[1] != "operation_id" {
		return false
	}
	query, _ := requestKeyStatements(parts[0])
	column, ok := Column(parts[0], parts[1])
	return query != "" && ok && column.Privacy == PrivateKey && column.Handling == AnonymizationRequired
}

func validRequestIdentity(s RequestIdentitySummary) bool {
	return id32.MatchString(s.WorkspaceID) && id32.MatchString(s.ActorID) && id32.MatchString(s.SourceID) && hash64.MatchString(s.RequestHash) && (s.Kind == "" || operationOwnerTable(s.Kind) != "")
}

// Both owner identities are derived, not supplied as a trusted alias assertion.
// Only operations changes its physical primary key when operation_id is erased.
func requestIdentityOwnerKeys(table, originalHash string, identity RequestIdentitySummary) (string, string, error) {
	if !requestKeyRule(table+".operation_id") || !hash64.MatchString(originalHash) || !validRequestIdentity(identity) {
		return "", "", ErrInvalidInventory
	}
	if table == "operations" {
		if identity.Kind == "" {
			return "", "", ErrInvalidInventory
		}
		original, err := OwnerKey(identity.ActorID, "sha256:"+originalHash)
		if err != nil {
			return "", "", err
		}
		current, err := RowOwnerKey(table, map[string]string{"actor_id": identity.ActorID, "operation_id": "retired-v1:" + originalHash})
		return original, current, err
	}
	if identity.Kind != "" {
		return "", "", ErrInvalidInventory
	}
	key, err := RowOwnerKey(table, map[string]string{"id": identity.SourceID})
	return key, key, err
}

// ProjectRetiredRequestKey prepares evidence only. Its caller still must verify
// the current plan/leases/ledger and persist alias, source and journal atomically.
func ProjectRetiredRequestKey(project string, expiry Expiry, in RequestKeyRetirement, at time.Time) (ExpiredProjection, error) {
	if !id32.MatchString(project) || expiry.Rule != in.Table+".operation_id" || expiry.Storage != Inline || !utf8.ValidString(in.OriginalKey) || utf8.RuneCountInString(in.OriginalKey) < 1 || utf8.RuneCountInString(in.OriginalKey) > 120 || strings.HasPrefix(strings.ToLower(in.OriginalKey), "retired-v1:") || at.Nanosecond()%1000 != 0 || expiry.Deadline.Nanosecond()%1000 != 0 {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	identity := RequestIdentitySummary{WorkspaceID: in.WorkspaceID, ActorID: in.ActorID, SourceID: in.SourceID, RequestHash: in.RequestHash, Kind: in.Kind}
	original, _, err := requestIdentityOwnerKeys(in.Table, expiry.PayloadHash, identity)
	id, idErr := ReferenceID(project, in.Table, original, "operation_id")
	if err != nil || idErr != nil || original != in.OriginalOwnerKey || id != expiry.ReferenceID {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	proof, err := ProjectExpired(expiry, []byte(in.OriginalKey), EvidenceSummary{}, at)
	if err != nil {
		return ExpiredProjection{}, err
	}
	proof.Materialization = AnonymizedRequestKey
	proof.Summary.RequestIdentity = &identity
	if err := ValidateProjectionForRule(proof, expiry.Rule); err != nil {
		return ExpiredProjection{}, err
	}
	return proof, nil
}
