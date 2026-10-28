package retention

import "novelstudio/internal/memory"

// Capture only the selected item's actual head, frozen summary inputs and
// declared memory dependencies. Ownership validation checks the immutable
// parent chain, but saving one item does not journal every unrelated memory.
// Pre-write capture retains the previous head before it is replaced.
// Source release metadata is validated by validateMemoryLinks; release bodies
// are registered by their own publication writer/full inventory. Do not read
// those bodies again here: revocation must work while blob storage is offline.
func memoryCaptureKeys(row SourceRow) ([]SourceKey, error) {
	var item memory.Item
	if memoryJSON(row.Values["document"], &item) != nil || memory.ValidateItem(item) != nil {
		return nil, ErrSourceOwnership
	}
	var keys []SourceKey
	add := func(table, id string) {
		keys = append(keys, SourceKey{Table: table, Primary: map[string]string{"id": id}})
	}
	if row.Table == "memory_items" {
		add("memory_revisions", item.Revision)
	}
	if item.Summary != nil {
		for _, pin := range item.Summary.Inputs {
			add("memory_revisions", pin.Revision)
		}
	}
	for _, id := range item.Dependencies {
		add("memory_items", id)
	}
	return keys, nil
}
