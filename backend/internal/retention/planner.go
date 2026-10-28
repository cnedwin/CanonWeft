package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"time"
)

func stableHash(value any) string {
	raw, _ := json.Marshal(value) // All callers use validated, finite data structs.
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// BuildPlan has no side effects and cannot authorize arbitrary paths. A trusted
// scanner supplies the complete inventory; an executor must lock and revalidate
// its hash/revisions before detaching references and deleting an exact object.
func BuildPlan(inventory Inventory, now time.Time) (Plan, error) {
	if !inventory.Complete || (inventory.CatalogVersion < 1 || inventory.CatalogVersion > 3) || !id32.MatchString(inventory.ProjectID) || inventory.Revision < 1 || now.IsZero() || inventory.CapturedAt.IsZero() || now.Before(inventory.CapturedAt) || !validLifecycle(inventory.Lifecycle) || inventory.Lifecycle.DeletedAt.After(now) || len(inventory.References) > 250000 {
		return Plan{}, ErrInvalidInventory
	}
	covered := slices.Clone(inventory.CoveredTables)
	want := KnownTables()
	if inventory.CatalogVersion == 2 {
		want = KnownTablesForSchema(37)
	}
	if inventory.CatalogVersion == 3 {
		want = KnownTablesForSchema(38)
	}
	sort.Strings(covered)
	sort.Strings(want)
	if !slices.Equal(covered, want) {
		return Plan{}, ErrInvalidInventory
	}
	refs := slices.Clone(inventory.References)
	deadlines, err := referenceDeadlinesWithHistory(refs, inventory.HistoricalReferences, inventory.Lifecycle, now)
	if err != nil {
		return Plan{}, err
	}
	for i := range refs {
		refs[i].Sources = slices.Clone(refs[i].Sources)
		sort.Slice(refs[i].Sources, func(a, b int) bool {
			return refs[i].Sources[a].ReferenceID < refs[i].Sources[b].ReferenceID
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	history := slices.Clone(inventory.HistoricalReferences)
	for i := range history {
		history[i].Sources = slices.Clone(history[i].Sources)
		sort.Slice(history[i].Sources, func(a, b int) bool { return history[i].Sources[a].ReferenceID < history[i].Sources[b].ReferenceID })
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].ID != history[j].ID {
			return history[i].ID < history[j].ID
		}
		return history[i].Revision < history[j].Revision
	})
	plan := Plan{ProjectID: inventory.ProjectID, InventoryRevision: inventory.Revision, Cutoff: now.UTC(), Expire: []Expiry{}, DeleteObjects: []string{}, Blocked: []string{}}
	physical := slices.Clone(inventory.Objects)
	sort.Slice(physical, func(i, j int) bool { return physical[i].Hash < physical[j].Hash })
	objectStates := map[string]ObjectState{}
	for i, object := range physical {
		if object.FirstSeenAt.After(now) || object.LeaseUntil.After(now.Add(day)) {
			return Plan{}, ErrInvalidInventory
		}
		if !hash64.MatchString(object.Hash) || object.Revision < 1 || (object.State != ObjectPresent && object.State != ObjectDeleted) || (i > 0 && physical[i-1].Hash == object.Hash) {
			return Plan{}, ErrInvalidInventory
		}
		objectStates[object.Hash] = object.State
	}
	// true means a still-live or leased reference prevents physical removal.
	objects := map[string]bool{}
	for i, ref := range refs {
		rule, ok := ConcreteRule(ref.Rule)
		if !ok || !hash64.MatchString(ref.ID) || !hash64.MatchString(ref.PayloadHash) || ref.Revision < 1 || (i > 0 && refs[i-1].ID == ref.ID) || (ref.ContentRoot && !rule.Promotable) || ref.LeaseUntil.After(now.Add(day)) {
			return Plan{}, ErrInvalidInventory
		}
		if (rule.Storage == Blob && !hash64.MatchString(ref.ObjectHash)) || (rule.Storage != Blob && ref.ObjectHash != "") {
			return Plan{}, ErrInvalidInventory
		}
		if ref.AccountingOpen && rule.Class != Audit {
			return Plan{}, ErrInvalidInventory
		}
		if rule.Storage == Blob {
			state, exists := objectStates[ref.ObjectHash]
			// A recovered expiry journal can describe a file that never existed
			// in this project. The full scan verifies its absence and the journal;
			// do not invent a deletion receipt just to represent that history.
			// Missing previously-PRESENT files are rejected by the scanner.
			if (!exists || state == ObjectDeleted) && ref.State != Expired {
				return Plan{}, ErrInvalidInventory
			}
		}
		class := rule.Class
		if ref.ContentRoot {
			class = Content
		}
		deadline := deadlines[ref.ID]
		if ref.State != Retained && ref.State != Expired {
			return Plan{}, ErrInvalidInventory
		}
		if ref.State == Expired && (ref.ExpiredAt.IsZero() || ref.ExpiredAt.After(now) || deadline.IsZero() || ref.ExpiredAt.Before(deadline)) {
			return Plan{}, ErrInvalidInventory
		}
		if ref.State == Retained && !ref.ExpiredAt.IsZero() {
			return Plan{}, ErrInvalidInventory
		}
		due := ref.State == Expired || (!deadline.IsZero() && !now.Before(deadline))
		if ref.AccountingOpen {
			if ref.State == Expired {
				return Plan{}, ErrInvalidInventory
			}
			due = false
		}
		leased := ref.LeaseUntil.After(now)
		if due && leased {
			plan.Blocked = append(plan.Blocked, ref.ID)
		}
		if due && !leased && ref.State == Retained {
			plan.Expire = append(plan.Expire, Expiry{ReferenceID: ref.ID, Rule: ref.Rule, PayloadHash: ref.PayloadHash, Revision: ref.Revision, Deadline: deadline, Storage: rule.Storage})
		}
		if rule.Storage == Blob {
			objects[ref.ObjectHash] = objects[ref.ObjectHash] || !due || leased
		}
		if !inventory.Lifecycle.OnlineDeadline.IsZero() && !now.Before(inventory.Lifecycle.OnlineDeadline) && class != Audit && class != ProtectedAuthority && class != Backup && (ref.State != Expired || leased || (rule.Storage == Blob && objectStates[ref.ObjectHash] == ObjectPresent)) {
			plan.Overdue = true
		}
	}
	for _, object := range physical {
		if object.State != ObjectPresent {
			continue
		}
		keep, referenced := objects[object.Hash]
		if keep {
			continue
		}
		if !referenced {
			// first_seen_at is the first persisted trusted UTC registration,
			// never a file mtime or a timestamp reset by rescanning/replaying.
			if object.FirstSeenAt.IsZero() {
				plan.Blocked = append(plan.Blocked, "object:"+object.Hash)
				continue
			}
			if now.Before(object.FirstSeenAt.Add(30 * day)) {
				continue
			}
		}
		if object.LeaseUntil.After(now) {
			plan.Blocked = append(plan.Blocked, "object:"+object.Hash)
			continue
		}
		plan.DeleteObjects = append(plan.DeleteObjects, object.Hash)
	}
	if !inventory.Lifecycle.OnlineDeadline.IsZero() && !now.Before(inventory.Lifecycle.OnlineDeadline) {
		for _, object := range physical {
			if object.State == ObjectPresent {
				plan.Overdue = true
			}
		}
	}
	sort.Strings(plan.Blocked)
	sort.Strings(plan.DeleteObjects)
	inventory.CoveredTables = covered
	inventory.References = refs
	inventory.HistoricalReferences = history
	inventory.Objects = physical
	plan.InventoryHash = stableHash(inventory)
	plan.Hash = stableHash(plan)
	return plan, nil
}
