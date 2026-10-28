package retention

import (
	"context"
	"encoding/json"
	"strconv"

	"novelstudio/internal/models"
	"novelstudio/internal/retrieval"
)

// The call owns billing; the generation owns the exact authored request. Bind
// an actual stored output to that request without granting execution/adoption.
func (index *SourceIndex) attachGenerationOutputSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	origins := map[string]SourceRow{}
	for _, row := range index.rows["generation_tasks"] {
		callID := row.Values["call_id"]
		if row.Values["project_id"] != project || callID == "" {
			continue
		}
		if _, duplicate := origins[callID]; duplicate {
			return ErrSourceOwnership
		}
		origins[callID] = row
	}
	for i := range materials {
		m := &materials[i]
		if m.Table == "source_imports" && m.Field == "report" {
			source := index.rows[m.Table][m.OwnerKey]
			var report retrieval.ImportReport
			if json.Unmarshal([]byte(source.Values["report"]), &report) != nil {
				return ErrSourceOwnership
			}
			var generated *retrieval.CharacterImageOrigin
			if report.CharacterImage != nil {
				generated = report.CharacterImage.Generated
			}
			if report.NovelImage != nil {
				generated = report.NovelImage.Generated
			}
			if generated != nil {
				origin, err := index.sourceRow("generation_tasks", map[string]string{"id": generated.GenerationID})
				if err != nil || origin.Values["project_id"] != project || origin.Values["branch_id"] != source.Values["branch_id"] || origin.Values["actor_id"] != source.Values["actor_id"] {
					return ErrSourceOwnership
				}
				// An explicitly saved/adopted image is author content. Its origin
				// remains verifiable, but runtime expiry cannot shorten its life.
				if m.ContentRoot {
					continue
				}
				b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
				if err = b.link(origin, "frozen", ""); err != nil {
					return err
				}
			}
		}
		if m.Table != "model_calls" || m.Field != "output_hash" {
			continue
		}
		call := index.rows[m.Table][m.OwnerKey]
		origin, ok := origins[call.Values["id"]]
		if !ok {
			// Standalone financial capture preserves already journaled pins.
			continue
		}
		hash, err := materialFrozenInput(origin.Table, origin.Values["frozen"])
		var frozen struct {
			Price    models.PriceCard `json:"price"`
			Estimate int64            `json:"estimate_micros"`
		}
		if err != nil || json.Unmarshal([]byte(origin.Values["frozen"]), &frozen) != nil || hash != origin.Values["input_hash"] {
			return ErrSourceOwnership
		}
		reserved, err := strconv.ParseInt(call.Values["reserved_micros"], 10, 64)
		if err != nil || reserved != frozen.Estimate || models.CallFingerprint(project, reserved, frozen.Price.Currency, hash) != call.Values["input_hash"] {
			return ErrSourceOwnership
		}
		if origin.Values["actor_id"] != call.Values["actor_id"] || call.Values["project_id"] != project || m.ObjectHash != call.Values["output_hash"] || read == nil {
			return ErrSourceOwnership
		}
		data, err := read(ctx, project, m.ObjectHash)
		var output models.Result
		if err != nil || contentJSON(data, &output, m.ObjectHash) != nil {
			return ErrSourceOwnership
		}
		b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
		if err := b.link(origin, "frozen", ""); err != nil {
			return err
		}
		if output.Image != nil {
			source, err := index.sourceRow("source_imports", map[string]string{"id": output.Image.ImportID})
			if err != nil || source.Values["project_id"] != project || source.Values["actor_id"] != origin.Values["actor_id"] || source.Values["branch_id"] != origin.Values["branch_id"] || source.Values["source_hash"] != output.Image.Hash {
				return ErrSourceOwnership
			}
			if err = b.link(source, "source_hash", financialDigest([]byte(output.Image.Hash))); err != nil {
				return err
			}
			if err = b.link(source, "report", ""); err != nil {
				return err
			}
		}
	}
	return nil
}
