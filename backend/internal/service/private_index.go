package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"sort"
)

type PrivatePin = domain.PrivatePin
type PrivateOmission = domain.PrivateOmission
type PrivateManifest = domain.PrivateManifest
type PrivateIndex struct {
	ID       string          `json:"id"`
	Chunks   int             `json:"chunks"`
	Ready    bool            `json:"ready"`
	Manifest PrivateManifest `json:"manifest"`
}
type PrivateHit = domain.PrivateHit

// Opening an anchor rechecks the saved context, actor and entire current pool.
// The part is returned in full only if the exact rune slice still matches.
func (s *Knowledge) PrivateSource(ctx context.Context, a domain.Actor, project, branch, contextID, chunkID string) (WorkingPart, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return WorkingPart{}, err
	}
	defer tx.Rollback()
	saved, err := s.readContextTx(ctx, tx, a.ID, project, branch, contextID)
	if err != nil {
		return WorkingPart{}, err
	}
	for _, hit := range saved.PrivateHits {
		if hit.ID != chunkID {
			continue
		}
		b, err := branchTx(ctx, tx, project, branch)
		if err != nil {
			return WorkingPart{}, err
		}
		item, err := s.workingInput(ctx, tx, a.ID, project, b, hit.Ref)
		if err != nil {
			return WorkingPart{}, err
		}
		if hit.Part < 0 || hit.Part >= len(item.Parts) {
			return WorkingPart{}, domain.ErrNotReady
		}
		part := item.Parts[hit.Part]
		runes := []rune(part.Text)
		if digest(part.Text) != hit.SourceHash || hit.Start < 0 || hit.End > len(runes) || hit.End <= hit.Start || string(runes[hit.Start:hit.End]) != hit.Text {
			return WorkingPart{}, domain.ErrNotReady
		}
		return part, nil
	}
	return WorkingPart{}, domain.ErrForbidden
}

// A single authority transaction freezes every current input, including rejected
// refs. No LIMIT is applied: later rows must not disappear from the watermark.
func (s *Knowledge) privateManifest(ctx context.Context, tx *sql.Tx, actor, project, branch string) (PrivateIndex, []string, error) {
	info := PrivateIndex{Manifest: PrivateManifest{Project: project, Branch: branch, Actor: actor, Model: retrieval.ModelRevision, Parser: "private-unicode-512-overlap32-v1", Sources: []PrivatePin{}, Omissions: []PrivateOmission{}}}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return info, nil, err
	}
	info.Manifest.Head = b.Head
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,version FROM (
 SELECT 'resource_draft' AS kind,r.id,CAST(d.revision AS CHAR) AS version FROM resource_drafts d JOIN resources r ON r.id=d.resource_id WHERE r.project_id=? AND r.branch_id=? AND d.actor_id=? AND r.kind<>'novel_review_dispositions'
 UNION ALL SELECT 'ip_draft',branch_id,CAST(revision AS CHAR) FROM ip_drafts WHERE branch_id=? AND actor_id=?
 UNION ALL SELECT 'ip_candidate',id,content_hash FROM ip_revisions WHERE branch_id=? AND status IN ('PREPARED','READY')
 UNION ALL SELECT 'release_candidate',id,content_hash FROM artifact_releases WHERE project_id=? AND branch_id=? AND status IN ('PREPARED','READY')
 ) refs ORDER BY kind,id`, project, branch, actor, branch, actor, branch, project, branch)
	if err != nil {
		return info, nil, err
	}
	refs := []WorkingRef{}
	for rows.Next() {
		var ref WorkingRef
		if err = rows.Scan(&ref.Kind, &ref.ID, &ref.Version); err != nil {
			rows.Close()
			return info, nil, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return info, nil, err
	}
	texts := []string{}
	for _, ref := range refs {
		item, readErr := s.workingInput(ctx, tx, actor, project, b, ref)
		if readErr != nil {
			if errors.Is(readErr, domain.ErrMaterialExpired) {
				info.Manifest.Omissions = append(info.Manifest.Omissions, PrivateOmission{Ref: ref, Reason: "源材料已到保留期限，未纳入检索；历史记录保留，请重新准备候选"})
				continue
			}
			if errors.Is(readErr, domain.ErrConflict) || errors.Is(readErr, domain.ErrNotReady) {
				info.Manifest.Omissions = append(info.Manifest.Omissions, PrivateOmission{Ref: ref, Reason: "源版本或基础版本已变化，未纳入；请更新草稿或重新准备候选"})
				continue
			}
			return info, nil, readErr
		}
		for part, p := range item.Parts {
			info.Manifest.Sources = append(info.Manifest.Sources, PrivatePin{Ref: ref, State: item.State, BaseHead: item.BaseHead, Part: part, Name: p.Name, Format: p.Format, Hash: digest(p.Text)})
			texts = append(texts, p.Text)
		}
	}
	info.ID = valueHash(info.Manifest)
	return info, texts, nil
}

func readyPrivateIndex(ctx context.Context, tx *sql.Tx, info *PrivateIndex) error {
	if err := requirePrivateIndexLiveTx(ctx, tx, info.Manifest.Project, info.ID); err != nil {
		return err
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT manifest,chunks FROM working_indexes WHERE id=? AND project_id=? AND branch_id=? AND actor_id=? AND model_revision=? AND enabled=TRUE", info.ID, info.Manifest.Project, info.Manifest.Branch, info.Manifest.Actor, retrieval.ModelRevision).Scan(&raw, &info.Chunks)
	if err == sql.ErrNoRows {
		return domain.ErrNotReady
	}
	if err != nil {
		return err
	}
	var manifest PrivateManifest
	if err := rejectExpiredRuntime(raw, "working_indexes.manifest"); err != nil {
		return err
	}
	if json.Unmarshal(raw, &manifest) != nil || valueHash(manifest) != info.ID {
		return domain.ErrNotReady
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM working_chunks WHERE index_id=?", info.ID).Scan(&count); err != nil {
		return err
	}
	if count != info.Chunks {
		return domain.ErrNotReady
	}
	info.Ready = true
	return nil
}

func (s *Knowledge) PrivateStatus(ctx context.Context, a domain.Actor, project, branch string) (PrivateIndex, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return PrivateIndex{}, err
	}
	defer tx.Rollback()
	info, _, err := s.privateManifest(ctx, tx, a.ID, project, branch)
	if err != nil {
		return info, err
	}
	err = readyPrivateIndex(ctx, tx, &info)
	if errors.Is(err, domain.ErrNotReady) {
		return info, nil
	}
	return info, err
}

func (s *Knowledge) buildPrivate(ctx context.Context, a domain.Actor, project, branch string, guard func(*sql.Tx) error) (PrivateIndex, error) {
	tx, a, err := s.beginIndex(ctx, a, project, false)
	if err != nil {
		return PrivateIndex{}, err
	}
	if err = guard(tx); err != nil {
		tx.Rollback()
		return PrivateIndex{}, err
	}
	info, texts, err := s.privateManifest(ctx, tx, a.ID, project, branch)
	if err == nil {
		readyErr := readyPrivateIndex(ctx, tx, &info)
		if readyErr == nil {
			tx.Rollback()
			return info, nil
		}
		if !errors.Is(readyErr, domain.ErrNotReady) {
			err = readyErr
		}
	}
	tx.Rollback()
	if err != nil {
		return info, err
	}
	chunks := []PrivateHit{}
	vectors := [][]byte{}
	for n, pin := range info.Manifest.Sources {
		parts, err := retrieval.Split(texts[n])
		if err != nil {
			return info, err
		}
		for offset := 0; offset < len(parts); offset += 8 {
			if s.Progress != nil {
				if err = s.Progress(ctx, n, len(chunks)); err != nil {
					return info, err
				}
			}
			batch := parts[offset:min(offset+8, len(parts))]
			inputs := make([]string, len(batch))
			for i, c := range batch {
				inputs[i] = c.Text
			}
			values, err := s.Embedder.Embed(ctx, inputs)
			if err != nil {
				return info, domain.ErrNotReady
			}
			for i, c := range batch {
				raw, err := retrieval.PackVector(values[i])
				if err != nil {
					return info, domain.ErrNotReady
				}
				c.ResourceID = valueHash(pin)
				c.Name = pin.Name
				c.Revision = pin.Ref.Version
				c.SourceHash = pin.Hash
				c.ID = digest(fmt.Sprintf("%s:%s:%d", info.ID, c.ResourceID, c.Start))
				chunks = append(chunks, PrivateHit{Chunk: c, Ref: pin.Ref, Part: pin.Part, State: pin.State})
				vectors = append(vectors, raw)
			}
		}
		if s.Progress != nil {
			if err = s.Progress(ctx, n+1, len(chunks)); err != nil {
				return info, err
			}
		}
	}
	tx, a, err = s.beginIndex(ctx, a, project, false)
	if err != nil {
		return info, err
	}
	defer tx.Rollback()
	if err = guard(tx); err != nil {
		return info, err
	}
	current, _, err := s.privateManifest(ctx, tx, a.ID, project, branch)
	if err != nil {
		return info, err
	}
	if current.ID != info.ID {
		return info, domain.ErrConflict
	}
	readyErr := readyPrivateIndex(ctx, tx, &info)
	if readyErr == nil {
		return info, nil
	}
	if !errors.Is(readyErr, domain.ErrNotReady) {
		return info, readyErr
	}
	// A disabled restore index may have this same fingerprint. It is rebuilt only
	// after the actor is explicitly re-enabled and a new authorized job is run.
	if err = s.IP.capturePrivateIndexSourceTx(ctx, tx, project, info.ID, true); err != nil {
		return info, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM working_chunks WHERE index_id=?", info.ID); err != nil {
		return info, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM working_indexes WHERE id=?", info.ID); err != nil {
		return info, err
	}
	raw, _ := json.Marshal(info.Manifest)
	info.Chunks = len(chunks)
	if _, err = tx.ExecContext(ctx, "INSERT INTO working_indexes(id,project_id,branch_id,actor_id,manifest,model_revision,chunks) VALUES(?,?,?,?,?,?,?)", info.ID, project, branch, a.ID, raw, retrieval.ModelRevision, info.Chunks); err != nil {
		return info, err
	}
	for i, c := range chunks {
		if _, err = tx.ExecContext(ctx, "INSERT INTO working_chunks(id,index_id,source_key,source_name,ref_kind,ref_id,ref_version,part_number,source_hash,start_rune,end_rune,content,vector,vector_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", c.ID, info.ID, c.ResourceID, c.Name, c.Ref.Kind, c.Ref.ID, c.Ref.Version, c.Part, c.SourceHash, c.Start, c.End, c.Text, vectors[i], digest(string(vectors[i]))); err != nil {
			return info, err
		}
	}
	if err = audit(ctx, tx, a, "knowledge.private_index", branch); err != nil {
		return info, err
	}
	if err = s.IP.capturePrivateIndexSourceTx(ctx, tx, project, info.ID, false); err != nil {
		return info, err
	}
	info.Ready = true
	return info, tx.Commit()
}

func searchPrivateChunks(ctx context.Context, tx *sql.Tx, info PrivateIndex, texts []string, query string, vector []float32, limit int) ([]PrivateHit, error) {
	semantic, lexical := []retrieval.Chunk{}, []retrieval.Chunk{}
	pins := map[string]PrivatePin{}
	originals := map[string][]rune{}
	for i, p := range info.Manifest.Sources {
		pins[valueHash(p)] = p
		originals[valueHash(p)] = []rune(texts[i])
	}
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, "SELECT id,source_key,source_name,ref_version,source_hash,start_rune,end_rune,content,vector,vector_hash,MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) FROM working_chunks WHERE index_id=? AND id>? ORDER BY id LIMIT 128", query, info.ID, after)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var c retrieval.Chunk
			var raw []byte
			var hash string
			if err = rows.Scan(&c.ID, &c.ResourceID, &c.Name, &c.Revision, &c.SourceHash, &c.Start, &c.End, &c.Text, &raw, &hash, &c.Lexical); err != nil {
				rows.Close()
				return nil, err
			}
			count++
			after = c.ID
			pin, ok := pins[c.ResourceID]
			original := originals[c.ResourceID]
			if !ok || pin.Hash != c.SourceHash || pin.Ref.Version != c.Revision || digest(string(raw)) != hash || c.Start < 0 || c.End > len(original) || c.End <= c.Start || string(original[c.Start:c.End]) != c.Text || c.ID != digest(fmt.Sprintf("%s:%s:%d", info.ID, c.ResourceID, c.Start)) {
				rows.Close()
				return nil, domain.ErrNotReady
			}
			values, err := retrieval.UnpackVector(raw)
			if err != nil {
				rows.Close()
				return nil, domain.ErrNotReady
			}
			c.Semantic = retrieval.Cosine(vector, values)
			if c.Semantic >= 0.45 {
				semantic = append(semantic, c)
			}
			if c.Lexical > 0 {
				lexical = append(lexical, c)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if count < 128 {
			break
		}
	}
	hits := []PrivateHit{}
	for _, c := range rankPrivateChunks(semantic, lexical, limit) {
		p := pins[c.ResourceID]
		hits = append(hits, PrivateHit{Chunk: c, Ref: p.Ref, Part: p.Part, State: p.State})
	}
	return hits, nil
}

func rankPrivateChunks(semantic, lexical []retrieval.Chunk, limit int) []retrieval.Chunk {
	// A candidate below a per-modality cutoff can still win after two votes.
	// Keep the complete lists and their ranks until RRF applies the final limit.
	sort.Slice(semantic, func(i, j int) bool {
		if semantic[i].Semantic == semantic[j].Semantic {
			return semantic[i].ID < semantic[j].ID
		}
		return semantic[i].Semantic > semantic[j].Semantic
	})
	sort.Slice(lexical, func(i, j int) bool {
		if lexical[i].Lexical == lexical[j].Lexical {
			return lexical[i].ID < lexical[j].ID
		}
		return lexical[i].Lexical > lexical[j].Lexical
	})
	return retrieval.Rank(semantic, lexical, limit)
}
