package retention

import "context"

// An accepted late receipt can retain diagnostic bytes even though cancellation
// deliberately leaves the call's adoptable output empty. Only the accepted
// receipt identity plus verified original bytes can establish that lineage.
func (index *SourceIndex) attachCancelledReceiptSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "model_call_receipts" || m.Field != "payload.output_hash" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		call, err := index.sourceRow("model_calls", map[string]string{"id": row.Values["call_id"]})
		if err != nil || call.Values["project_id"] != project {
			return ErrSourceOwnership
		}
		if call.Values["output_hash"] != "" || call.Values["cancel_requested"] != "1" || call.Values["settlement_hash"] == "" || row.Values["id"] != financialDigest([]byte(call.Values["id"]+"\x00"+call.Values["settlement_hash"])) {
			continue
		}
		summary, err := ReceiptEvidence(call.Values["id"], row.Values["id"], []byte(row.Values["payload"]))
		if err != nil || summary.OutputHash != m.ObjectHash {
			return ErrSourceOwnership
		}
		// The receipt identity is immutable even when a financial writer has no
		// blob reader. Keep the same source target across those captures, while
		// recording missing byte verification as incomplete evidence.
		if read == nil {
			m.UnboundSources++
		} else if data, err := read(ctx, project, summary.OutputHash); err != nil || financialDigest(data) != summary.OutputHash {
			m.UnboundSources++
		}
		m.Sources = nil
		b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
		if err := b.link(row, "payload", ""); err != nil {
			return err
		}
	}
	return nil
}
