package backup

import (
	"errors"
	"fmt"

	"novelstudio/internal/retention"
)

// Only consume materials bound to actual business rows and independent expiry
// proofs by archiveRetentionMaterials. An overdue retained reference still
// needs its bytes; a deadline alone never substitutes for a committed expiry.
func archiveRetainedBlobReferences(materials map[string][]retention.MaterialReference) (map[string]bool, error) {
	refs := map[string]bool{}
	for project, items := range materials {
		if !hex32.MatchString(project) {
			return nil, errors.New("invalid retained blob project")
		}
		for _, item := range items {
			if item.Storage != retention.Blob {
				continue
			}
			if !hex64.MatchString(item.ObjectHash) {
				return nil, errors.New("invalid retained blob identity")
			}
			refs["blobs/"+project+"/"+item.ObjectHash] = true
			if len(refs) > maxEntries {
				return nil, errors.New("retained blob inventory exceeds archive limit")
			}
		}
	}
	return refs, nil
}

func validateRetainedBlobInventory(materials map[string][]retention.MaterialReference, blobs map[string][]byte) error {
	refs, err := archiveRetainedBlobReferences(materials)
	if err != nil {
		return err
	}
	return validateBlobReferenceSet(refs, blobs)
}

func validateBlobReferenceSet(refs map[string]bool, blobs map[string][]byte) error {
	if len(refs) != len(blobs) {
		return errors.New("unreferenced or missing blob")
	}
	for path := range refs {
		if _, exists := blobs[path]; !exists {
			return fmt.Errorf("missing object %s", path)
		}
	}
	// ZIP admission already checked path shape and each file's content hash.
	// This gate proves exact retained membership, never filesystem deletion.
	return nil
}
