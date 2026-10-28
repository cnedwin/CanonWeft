package retention

import "novelstudio/internal/memory"

// A half-cleaned pair can still contain a real retained document. Its IDs are
// remapped by the memory copier, so the surviving head-equality digest changes.
// Bind the new digest only to that actual document; never synthesize erased text
// or change OriginalPayloadHash, the immutable record of what was removed.
func recoveryMemoryCounterpartHash(row SourceRow, targets map[string]SourceRow, fallback string) (string, error) {
	if row.Table != "memory_items" && row.Table != "memory_revisions" {
		return fallback, nil
	}
	var counterpart SourceRow
	var exists bool
	itemID, revisionID := row.Values["id"], row.Values["head_revision"]
	if row.Table == "memory_items" {
		counterpart, exists = targets["memory_revisions:"+revisionID]
		if !exists || counterpart.Values["item_id"] != itemID {
			return "", ErrInvalidInventory
		}
	} else {
		itemID, revisionID = row.Values["item_id"], row.Values["id"]
		counterpart, exists = targets["memory_items:"+itemID]
		if !exists {
			return "", ErrInvalidInventory
		}
		if counterpart.Values["head_revision"] != revisionID {
			return fallback, nil
		}
	}
	if counterpart.Values["project_id"] != row.Values["project_id"] {
		return "", ErrInvalidInventory
	}
	if _, err := ParseSourceProjection([]byte(counterpart.Values["document"]), counterpart.Table+".document"); err == nil {
		return fallback, nil
	}
	var item memory.Item
	if authorDocument(counterpart.Values["document"], &item) != nil || memory.ValidateItem(item) != nil || item.Project != row.Values["project_id"] || item.ID != itemID || item.Revision != revisionID {
		return "", ErrInvalidInventory
	}
	return stableHash(item), nil
}
