package backup

import (
	"errors"
	"strings"
	"time"

	"novelstudio/internal/retention"
)

var errRetentionIdentity = errors.New("retention archive request identity or admission policy is inconsistent")

type archiveIdentityPolicy struct {
	Closed   bool
	Revision int64
	ClosedAt time.Time
	Created  time.Time
}

// This binds archived aliases to actual result rows. It neither replays a
// request nor grants expiry authority for its original source field/owner key.
func validateArchiveRequestIdentities(business, metadata Data, at time.Time) error {
	_, err := readArchiveRequestIdentities(business, metadata, at)
	return err
}

func readArchiveRequestIdentities(business, metadata Data, at time.Time) ([]retention.RequestAliasRecord, error) {
	if at.IsZero() {
		return nil, errRetentionIdentity
	}
	owners, err := newRetentionArchiveOwners(business)
	if err != nil {
		return nil, err
	}
	policies, err := readArchiveIdentityPolicies(owners, metadata, at)
	if err != nil {
		return nil, err
	}
	spec, err := archiveStateTable(metadata, "retention_operation_keys")
	if err != nil {
		return nil, errRetentionIdentity
	}
	var records []retention.RequestAliasRecord
	seen, bound := map[string]bool{}, map[string]bool{}
	for _, row := range metadata[spec.name] {
		for _, cell := range row {
			if cell == nil {
				return nil, errRetentionIdentity
			}
		}
		workspace, actor, table := spec.value(row, "workspace_id"), spec.value(row, "actor_id"), spec.value(row, "owner_table")
		originalHash, source, hash := spec.value(row, "original_key_hash"), spec.value(row, "source_id"), spec.value(row, "request_hash")
		anonymous, kind := spec.value(row, "anonymous_key"), spec.value(row, "operation_kind")
		policy, exists := policies[workspace]
		created, clockErr := archiveStateClock(row[spec.index("created_at")], false)
		if !exists || !policy.Closed || !hex32.MatchString(actor) || !hex32.MatchString(source) || !hex64.MatchString(originalHash) || !hex64.MatchString(hash) || anonymous != "retired-v1:"+originalHash || clockErr != nil || created.Before(policy.Created) || created.After(at) {
			return nil, errRetentionIdentity
		}
		identity := table + "/" + actor + "/" + originalHash
		if seen[identity] {
			return nil, errRetentionIdentity
		}
		seen[identity] = true
		actorKey, _ := retention.OwnerKey(actor)
		if owners.rows["users"][actorKey].Values["workspace_id"] != workspace {
			return nil, errRetentionIdentity
		}
		idColumn, hashColumn, supported := archiveRequestColumns(table)
		if !supported || (table != "operations" && kind != "") {
			return nil, errRetentionIdentity
		}
		originalOwner, _ := retention.OwnerKey(source)
		primary := map[string]string{"id": source}
		if table == "operations" {
			originalOwner, _ = retention.OwnerKey(actor, "sha256:"+originalHash)
			primary = map[string]string{"actor_id": actor, "operation_id": anonymous}
		}
		currentOwner, keyErr := retention.RowOwnerKey(table, primary)
		if keyErr != nil || originalOwner != spec.value(row, "original_owner_key") {
			return nil, errRetentionIdentity
		}
		actual, exists := owners.rows[table][currentOwner]
		if !exists || actual.Values["operation_id"] != anonymous || actual.Values["actor_id"] != actor || actual.Values[idColumn] != source || actual.Values[hashColumn] != hash || (table == "operations" && actual.Values["kind"] != kind) {
			return nil, errRetentionIdentity
		}
		owner, scopeErr := owners.index.Owner(table, actual.Values)
		if scopeErr != nil || owner.WorkspaceID != workspace {
			return nil, errRetentionIdentity
		}
		sourceKey := table + "/" + currentOwner
		if bound[sourceKey] {
			return nil, errRetentionIdentity
		}
		bound[sourceKey] = true
		records = append(records, retention.RequestAliasRecord{Table: table, OriginalKeyHash: originalHash, OriginalOwnerKey: originalOwner, AnonymousKey: anonymous, Identity: retention.RequestIdentitySummary{WorkspaceID: workspace, ActorID: actor, SourceID: source, RequestHash: hash, Kind: kind}, PolicyWorkspaceID: workspace, PolicyClosed: policy.Closed, PolicyRevision: policy.Revision, CreatedAt: created, PolicyCreatedAt: policy.Created, PolicyClosedAt: policy.ClosedAt})
	}
	// An alias cannot be stripped while leaving an anonymous source behind.
	// Otherwise an old request could lose its original result on restore.
	for table, rows := range owners.rows {
		if _, _, supported := archiveRequestColumns(table); !supported {
			continue
		}
		for key, row := range rows {
			if strings.HasPrefix(strings.ToLower(row.Values["operation_id"]), "retired-v1:") && !bound[table+"/"+key] {
				return nil, errRetentionIdentity
			}
		}
	}
	return records, nil
}

func readArchiveIdentityPolicies(owners *retentionArchiveOwners, metadata Data, at time.Time) (map[string]archiveIdentityPolicy, error) {
	spec, err := archiveStateTable(metadata, "retention_identity_policies")
	if err != nil {
		return nil, errRetentionIdentity
	}
	policies := map[string]archiveIdentityPolicy{}
	for _, row := range metadata[spec.name] {
		workspace := spec.value(row, "workspace_id")
		workspaceKey, keyErr := retention.OwnerKey(workspace)
		_, exists := owners.rows["workspaces"][workspaceKey]
		_, duplicate := policies[workspace]
		closed := spec.value(row, "legacy_writes_closed")
		revision, revisionErr := archiveRevision(spec.value(row, "revision"))
		created, createdErr := archiveStateClock(row[spec.index("created_at")], false)
		closedAt, closedErr := archiveStateClock(row[spec.index("closed_at")], true)
		if !hex32.MatchString(workspace) || keyErr != nil || !exists || duplicate || revisionErr != nil || createdErr != nil || closedErr != nil || created.After(at) || closedAt.After(at) {
			return nil, errRetentionIdentity
		}
		switch closed {
		case "0":
			if revision != 1 || !closedAt.IsZero() {
				return nil, errRetentionIdentity
			}
		case "1":
			if revision < 2 || closedAt.IsZero() || closedAt.Before(created) {
				return nil, errRetentionIdentity
			}
		default:
			return nil, errRetentionIdentity
		}
		policies[workspace] = archiveIdentityPolicy{Closed: closed == "1", Created: created, Revision: revision, ClosedAt: closedAt}
	}
	return policies, nil
}

// This is the same bounded set handled by retention.RetireRequestKeyTx.
func archiveRequestColumns(table string) (id, hash string, supported bool) {
	switch table {
	case "operations":
		return "result_id", "request_hash", true
	case "model_calls":
		return "id", "input_hash", true
	case "source_imports", "ingest_jobs", "generation_tasks", "recipe_runs", "semantic_review_runs":
		return "id", "request_hash", true
	default:
		return "", "", false
	}
}
