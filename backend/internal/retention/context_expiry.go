package retention

import (
	"context"
	"database/sql"
	"time"

	"novelstudio/internal/domain"
)

// ContextBindingSummary preserves identity only, never queries or retrieved text.
type ContextBindingSummary struct {
	ID          string `json:"id"`
	IndexID     string `json:"index_id"`
	IPHead      string `json:"ip_head,omitempty"`
	ContentHash string `json:"content_hash"`
}

func validContextBinding(binding ContextBindingSummary) bool {
	return id32.MatchString(binding.ID) && hash64.MatchString(binding.IndexID) &&
		hash64.MatchString(binding.ContentHash) && (binding.IPHead == "" || id32.MatchString(binding.IPHead))
}

func ContextEvidence(raw []byte, contentHash string) (EvidenceSummary, error) {
	var pack domain.ContextPackage
	if contextJSON(raw, &pack, contentHash) != nil {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	binding := ContextBindingSummary{ID: pack.ID, IndexID: pack.IndexID, IPHead: pack.IPHead, ContentHash: contentHash}
	if !validContextBinding(binding) {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	return EvidenceSummary{Context: &binding}, nil
}

// ContextHasLiveConsumersTx uses exact recorded references under the caller's
// project lock. Original context bytes remain available until every direct
// consumer has expired, so subsequent source verification never needs guesses.
func ContextHasLiveConsumersTx(ctx context.Context, tx *sql.Tx, project, id string, at time.Time) (bool, error) {
	if !hash64.MatchString(id) {
		return false, ErrInvalidInventory
	}
	current, err := readReferenceVersions(ctx, tx, project, at)
	if err != nil {
		return false, err
	}
	for _, version := range current {
		if version.Reference.State != Retained {
			continue
		}
		for _, source := range version.Reference.Sources {
			if source.ReferenceID == id {
				return true, nil
			}
		}
	}
	return false, nil
}
