package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

// Reuse is confined to this project's immutable resource/hash and the same
// parser/model. Every Unicode range, original text and packed vector is checked.
// Policy, revision and chunk identity are rebuilt from the prospective manifest.
func (s *Knowledge) cachedVectors(ctx context.Context, project, parser string, source SourcePin, parts []retrieval.Chunk) ([][]byte, error) {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var index string
	err = tx.QueryRowContext(ctx, `SELECT i.id FROM knowledge_indexes i JOIN knowledge_chunks c ON c.index_id=i.id WHERE i.project_id=? AND i.model_revision=? AND JSON_UNQUOTE(JSON_EXTRACT(i.manifest,'$.parser'))=? AND c.resource_id=? AND c.source_hash=? ORDER BY i.created_at DESC LIMIT 1`, project, retrieval.ModelRevision, parser, source.ID, source.Hash).Scan(&index)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := requirePublishedIndexLiveTx(ctx, tx, project, index); err != nil {
		if err == domain.ErrMaterialExpired {
			return nil, nil
		}
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT start_rune,end_rune,content,vector,vector_hash FROM knowledge_chunks WHERE index_id=? AND resource_id=? AND source_hash=? ORDER BY start_rune", index, source.ID, source.Hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([][]byte, 0, len(parts))
	for rows.Next() {
		var start, end int
		var text, hash string
		var packed []byte
		if err = rows.Scan(&start, &end, &text, &packed, &hash); err != nil {
			return nil, err
		}
		if len(result) >= len(parts) {
			return nil, nil
		}
		part := parts[len(result)]
		if start != part.Start || end != part.End || text != part.Text || digest(string(packed)) != hash {
			return nil, nil
		}
		if _, err = retrieval.UnpackVector(packed); err != nil {
			return nil, nil
		}
		result = append(result, packed)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(parts) {
		return nil, nil
	}
	return result, nil
}

func (s *Knowledge) sourceVectors(ctx context.Context, project, parser string, source SourcePin, parts []retrieval.Chunk) ([][]byte, error) {
	if len(parts) == 0 {
		return [][]byte{}, nil
	}
	packed, err := s.cachedVectors(ctx, project, parser, source, parts)
	if err != nil || packed != nil {
		return packed, err
	}
	packed = [][]byte{}
	for offset := 0; offset < len(parts); offset += 8 {
		batch := parts[offset:min(offset+8, len(parts))]
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Text
		}
		embedded, err := s.Embedder.Embed(ctx, texts)
		if err != nil {
			return nil, domain.ErrNotReady
		}
		for _, vector := range embedded {
			data, err := retrieval.PackVector(vector)
			if err != nil {
				return nil, domain.ErrNotReady
			}
			packed = append(packed, data)
		}
	}
	return packed, nil
}
