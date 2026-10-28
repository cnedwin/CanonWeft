package retention

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ValidatePlanRecord checks the unchanged BuildPlan serialization and digest.
// A stored plan is historical intent, never permission to execute against a
// current database or filesystem. Executors must rescan and revalidate it.
func ValidatePlanRecord(raw string) (Plan, error) {
	var p Plan
	if len(raw) > 16<<20 || json.Unmarshal([]byte(raw), &p) != nil {
		return Plan{}, ErrInvalidInventory
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) || !id32.MatchString(p.ProjectID) || p.InventoryRevision < 1 || !hash64.MatchString(p.InventoryHash) || !hash64.MatchString(p.Hash) || p.Cutoff.IsZero() || p.Expire == nil || p.DeleteObjects == nil || p.Blocked == nil || len(p.Expire)+len(p.DeleteObjects)+len(p.Blocked) > 250000 {
		return Plan{}, ErrInvalidInventory
	}
	hash := p.Hash
	p.Hash = ""
	if stableHash(p) != hash {
		return Plan{}, ErrInvalidInventory
	}
	p.Hash = hash
	items := map[string]bool{}
	for i, e := range p.Expire {
		rule, ok := ConcreteRule(e.Rule)
		if !ok || rule.Storage != e.Storage || !hash64.MatchString(e.ReferenceID) || !hash64.MatchString(e.PayloadHash) || e.Revision < 1 || e.Deadline.IsZero() || e.Deadline.After(p.Cutoff) || (i > 0 && p.Expire[i-1].ReferenceID >= e.ReferenceID) {
			return Plan{}, ErrInvalidInventory
		}
		items[e.ReferenceID] = true
	}
	for i, hash := range p.DeleteObjects {
		if !hash64.MatchString(hash) || (i > 0 && p.DeleteObjects[i-1] >= hash) {
			return Plan{}, ErrInvalidInventory
		}
		items["object:"+hash] = true
	}
	for i, key := range p.Blocked {
		if !hash64.MatchString(strings.TrimPrefix(key, "object:")) || items[key] || (i > 0 && p.Blocked[i-1] >= key) {
			return Plan{}, ErrInvalidInventory
		}
	}
	return p, nil
}
