package retention

import (
	"context"
	"strconv"

	"novelstudio/internal/retrieval"
)

// Extraction checkpoints and reports retain their original upload, without
// rerunning a parser, OCR service or model to reconstruct an old result.
func (index *SourceIndex) attachIngestSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if !(m.Table == "ingest_jobs" && m.Field == "progress") && !(m.Table == "source_imports" && m.Field == "report") {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		if read == nil {
			return ErrSourceOwnership
		}
		data, err := read(ctx, project, row.Values["source_hash"])
		size, sizeErr := strconv.ParseInt(row.Values["source_size"], 10, 64)
		if err != nil || sizeErr != nil || size < 1 || size != int64(len(data)) || financialDigest(data) != row.Values["source_hash"] {
			return ErrSourceOwnership
		}
		if m.Table == "ingest_jobs" {
			var progress struct {
				Plan  *retrieval.ImportPlan  `json:"plan"`
				Units []retrieval.ImportUnit `json:"units"`
			}
			raw := row.Values["progress"]
			if len(raw) > 2*1024*1024 || financialDigest([]byte(raw)) != row.Values["progress_hash"] || authorDocument(raw, &progress) != nil {
				return ErrSourceOwnership
			}
			if progress.Plan == nil {
				if len(progress.Units) != 0 {
					return ErrSourceOwnership
				}
			} else if progress.Plan.Parser != row.Values["parser_revision"] || progress.Plan.Count < 1 || progress.Plan.Count > 10000 || len(progress.Units) > progress.Plan.Count {
				return ErrSourceOwnership
			}
		} else {
			var report retrieval.ImportReport
			if authorDocument(row.Values["report"], &report) != nil {
				return ErrSourceOwnership
			}
		}
		// A saved author draft retains its original upload/report as content.
		// It must not inherit the shorter lifetime of a raw execution record.
		if m.ContentRoot {
			continue
		}
		b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
		if err := b.link(row, "source_hash", ""); err != nil {
			return err
		}
		if err := b.link(row, "filename", ""); err != nil {
			return err
		}
	}
	return nil
}
