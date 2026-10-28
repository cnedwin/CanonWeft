package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"slices"
	"strings"
)

type Knowledge struct {
	IP          *IP
	Embedder    *retrieval.Embedder
	MemoryCache memory.Cache
	Progress    func(context.Context, int, int) error
}
type SourcePin = domain.KnowledgeSourcePin
type IndexManifest = domain.KnowledgeIndexManifest
type IndexInfo struct {
	ID       string        `json:"id"`
	Chunks   int           `json:"chunks"`
	Manifest IndexManifest `json:"manifest"`
}
type SearchInput = domain.SearchInput
type ContextPackage = domain.ContextPackage

func knowledgeManifest(ctx context.Context, tx *sql.Tx, project, branch string) (IndexInfo, error) {
	var info IndexInfo
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return info, err
	}
	info.Manifest = IndexManifest{Branch: branch, IPHead: b.Head, Model: retrieval.ModelRevision, Parser: "unicode-512-overlap32-v1", Sources: []SourcePin{}}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,head_revision,content_hash FROM resources WHERE project_id=? AND branch_id=? AND head_revision<>'' ORDER BY id", project, branch)
	if err != nil {
		return info, err
	}
	defer rows.Close()
	for rows.Next() {
		var p SourcePin
		if err = rows.Scan(&p.ID, &p.Name, &p.Head, &p.Hash); err != nil {
			return info, err
		}
		info.Manifest.Sources = append(info.Manifest.Sources, p)
	}
	if err = rows.Err(); err != nil {
		return info, err
	}
	rows.Close()
	for i := range info.Manifest.Sources {
		pin := &info.Manifest.Sources[i]
		pin.Policy, err = sourcePolicyTx(ctx, tx, project, pin.ID, pin.Head)
		if err != nil {
			return info, err
		}
	}
	raw, _ := json.Marshal(info.Manifest)
	info.ID = digest(project + "\x00" + string(raw))
	return info, nil
}
func readyIndex(ctx context.Context, tx *sql.Tx, info *IndexInfo) error {
	var project string
	var manifest []byte
	err := tx.QueryRowContext(ctx, "SELECT chunks,project_id,manifest FROM knowledge_indexes WHERE id=? AND model_revision=?", info.ID, retrieval.ModelRevision).Scan(&info.Chunks, &project, &manifest)
	if err == sql.ErrNoRows {
		return domain.ErrNotReady
	}
	if err != nil {
		return err
	}
	if err := rejectExpiredRuntime(manifest, "knowledge_indexes.manifest"); err != nil {
		return err
	}
	if err := requirePublishedIndexLiveTx(ctx, tx, project, info.ID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM knowledge_chunks WHERE index_id=?", info.ID).Scan(&count); err != nil {
		return err
	}
	if count != info.Chunks {
		return domain.ErrNotReady
	}
	return err
}

// Build never holds authority locks while running inference. The final transaction
// checks the exact source manifest again before making the complete index visible.
func (s *Knowledge) Build(ctx context.Context, a domain.Actor, project, branch string) (IndexInfo, error) {
	return s.build(ctx, a, project, branch, false, func(tx *sql.Tx) (IndexInfo, error) {
		return knowledgeManifest(ctx, tx, project, branch)
	})
}

func (s *Knowledge) beginIndex(ctx context.Context, a domain.Actor, project string, publishing bool) (*sql.Tx, domain.Actor, error) {
	tx, current, p, err := s.IP.begin(ctx, a, project, !publishing)
	if err != nil {
		return nil, current, err
	}
	if publishing && (!canPublish(current.Role) || !canPublish(p.Role)) {
		tx.Rollback()
		return nil, current, domain.ErrForbidden
	}
	return tx, current, nil
}

func canPublish(role string) bool { return role == "owner" || role == "publisher" }

// resolve must read a single prospective or current manifest inside the held
// authority transaction. Inference happens outside it, then every pin is reread.
func (s *Knowledge) build(ctx context.Context, a domain.Actor, project, branch string, publishing bool, resolve func(*sql.Tx) (IndexInfo, error)) (IndexInfo, error) {
	tx, _, err := s.beginIndex(ctx, a, project, publishing)
	if err != nil {
		return IndexInfo{}, err
	}
	info, err := resolve(tx)
	if err == nil {
		readyErr := readyIndex(ctx, tx, &info)
		if readyErr == nil {
			tx.Rollback()
			return info, nil
		}
		if readyErr != domain.ErrNotReady {
			tx.Rollback()
			return info, readyErr
		}
	}
	tx.Rollback()
	if err != nil {
		return info, err
	}
	chunks := []retrieval.Chunk{}
	vectors := [][]byte{}
	if s.Progress != nil {
		if err = s.Progress(ctx, 0, 0); err != nil {
			return info, err
		}
	}
	for position, source := range info.Manifest.Sources {
		data, readErr := s.IP.Blobs.ReadObject(ctx, project, source.Hash)
		if readErr != nil {
			return info, domain.ErrNotReady
		}
		parts, splitErr := retrieval.Split(string(data))
		if splitErr != nil {
			return info, domain.ErrValidation
		}
		packed, embedErr := s.sourceVectors(ctx, project, info.Manifest.Parser, source, parts)
		if embedErr != nil {
			return info, embedErr
		}
		for i, c := range parts {
			c.ResourceID = source.ID
			c.Name = source.Name
			c.Revision = source.Head
			c.SourceHash = source.Hash
			c.ID = digest(fmt.Sprintf("%s:%s:%d", info.ID, source.ID, c.Start))
			chunks = append(chunks, c)
			vectors = append(vectors, packed[i])
		}
		if s.Progress != nil {
			if err = s.Progress(ctx, position+1, len(chunks)); err != nil {
				return info, err
			}
		}
	}
	tx, a, err = s.beginIndex(ctx, a, project, publishing)
	if err != nil {
		return info, err
	}
	defer tx.Rollback()
	current, err := resolve(tx)
	if err != nil {
		return info, err
	}
	if current.ID != info.ID {
		return info, domain.ErrConflict
	}
	readyErr := readyIndex(ctx, tx, &info)
	if readyErr == nil {
		return info, nil
	}
	if readyErr != domain.ErrNotReady {
		return info, readyErr
	}
	raw, _ := json.Marshal(info.Manifest)
	info.Chunks = len(chunks)
	_, err = tx.ExecContext(ctx, "INSERT INTO knowledge_indexes(id,project_id,branch_id,ip_head,manifest,model_revision,chunks) VALUES(?,?,?,?,?,?,?)", info.ID, project, branch, info.Manifest.IPHead, raw, retrieval.ModelRevision, info.Chunks)
	if err != nil {
		return info, err
	}
	for i, c := range chunks {
		_, err = tx.ExecContext(ctx, "INSERT INTO knowledge_chunks(id,index_id,resource_id,resource_name,resource_revision,source_hash,start_rune,end_rune,content,vector,vector_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)", c.ID, info.ID, c.ResourceID, c.Name, c.Revision, c.SourceHash, c.Start, c.End, c.Text, vectors[i], digest(string(vectors[i])))
		if err != nil {
			return info, err
		}
	}
	if err = audit(ctx, tx, a, "knowledge.index", branch); err != nil {
		return info, err
	}
	if err = s.IP.capturePublicIndexSourceTx(ctx, tx, project, info.ID); err != nil {
		return info, err
	}
	return info, tx.Commit()
}

func (s *Knowledge) Status(ctx context.Context, a domain.Actor, project, branch string) (IndexInfo, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return IndexInfo{}, err
	}
	defer tx.Rollback()
	info, err := knowledgeManifest(ctx, tx, project, branch)
	if err == nil {
		err = readyIndex(ctx, tx, &info)
	}
	return info, err
}

func (s *Knowledge) Search(ctx context.Context, a domain.Actor, project, branch string, input SearchInput) (ContextPackage, error) {
	if input.TokenBudget == 0 {
		input.TokenBudget = 65536
	}
	result := ContextPackage{ID: NewID(), Input: input, RetrievalVersion: "ngram-bge-exact-rrf60-v1", Facts: []domain.Assertion{}, Hidden: []domain.Assertion{}, Entities: []domain.Entity{}, Rules: []domain.WorldRule{}, Evidence: []retrieval.Chunk{}, Omissions: []string{}}
	if !validName(strings.TrimSpace(input.Query), 1000) || !slices.Contains([]string{"author", "writer", "critic", "npc", "research"}, input.Purpose) || input.Limit < 1 || input.Limit > 20 {
		return result, domain.ErrValidation
	}
	if input.TokenBudget < 4096 || input.TokenBudget > 262144 {
		return result, domain.ErrValidation
	}
	if err := domain.ValidateStructuredScope(input.Structured); err != nil {
		return result, err
	}
	if err := validateRequiredIDs(input.RequiredResourceIDs); err != nil {
		return result, err
	}
	if err := validateWorkingRefs(input); err != nil {
		return result, err
	}
	if (input.Purpose == "writer" || input.Purpose == "npc") && (input.IncludeWorking || input.IncludeMethods) {
		return result, domain.ErrValidation
	}
	if input.IncludePrivateWorking && (!input.IncludeWorking || !slices.Contains([]string{"author", "critic", "research"}, input.Purpose)) {
		return result, domain.ErrValidation
	}
	// Permission is checked before the query leaves the API process and again at commit.
	initial, err := s.Status(ctx, a, project, branch)
	if err != nil {
		return result, err
	}
	lookup, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	initialSnapshot, err := snapshotTx(ctx, lookup, project, initial.Manifest.IPHead)
	var initialPrivate PrivateIndex
	if err == nil && input.IncludePrivateWorking {
		initialPrivate, _, err = s.privateManifest(ctx, lookup, a.ID, project, branch)
		if err == nil {
			err = readyPrivateIndex(ctx, lookup, &initialPrivate)
		}
	}
	lookup.Rollback()
	if err != nil {
		return result, err
	}
	expanded := input.Query
	if input.Purpose == "author" || input.Purpose == "critic" || input.Purpose == "research" {
		expanded, result.MatchedEntities = domain.ExpandQuery(input.Query, initialSnapshot.Entities)
		result.ExpandedQuery = expanded
	}
	query, err := s.Embedder.Embed(ctx, []string{expanded})
	if err != nil {
		return result, domain.ErrNotReady
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	info, err := knowledgeManifest(ctx, tx, project, branch)
	if err != nil {
		return result, err
	}
	if initial.ID != info.ID {
		return result, domain.ErrNotReady
	}
	if err = readyIndex(ctx, tx, &info); err != nil {
		return result, err
	}
	result.IndexID = info.ID
	result.IPHead = info.Manifest.IPHead
	snapshot, err := snapshotTx(ctx, tx, project, info.Manifest.IPHead)
	if err != nil {
		return result, err
	}
	if err = applyStructuredContext(&result, snapshot, input.TokenBudget/6); err != nil {
		return result, err
	}
	result.Required, err = s.requiredInputs(ctx, tx, project, branch, input)
	if err != nil {
		return result, err
	}
	result.Unconfirmed, err = s.workingInputs(ctx, tx, a.ID, project, branch, input)
	if err != nil {
		return result, err
	}
	if input.IncludePrivateWorking {
		private, texts, err := s.privateManifest(ctx, tx, a.ID, project, branch)
		if err != nil {
			return result, err
		}
		if private.ID != initialPrivate.ID {
			return result, domain.ErrConflict
		}
		if err = readyPrivateIndex(ctx, tx, &private); err != nil {
			return result, err
		}
		result.PrivateIndexID = private.ID
		result.PrivateHits, err = searchPrivateChunks(ctx, tx, private, texts, expanded, query[0], input.Limit)
		if err != nil {
			return result, err
		}
		for _, omitted := range private.Manifest.Omissions {
			result.Omissions = append(result.Omissions, "未确认资料 "+omitted.Ref.Kind+":"+omitted.Ref.ID+"："+omitted.Reason)
		}
	}
	allowed := map[string]domain.KnowledgePolicy{}
	for _, source := range info.Manifest.Sources {
		policy := domain.DefaultKnowledgePolicy()
		if source.Policy != nil {
			policy = *source.Policy
		}
		if policy.Allows(input.Purpose, input.Perspective, input.StoryTime, input.IncludeWorking, input.IncludeMethods) {
			allowed[source.ID] = policy
		}
	}
	if len(allowed) < len(info.Manifest.Sources) {
		result.Omissions = append(result.Omissions, "部分资料因知识空间、用途、时间或知情范围未纳入；不据此推断资料不存在")
	}
	selected, err := searchChunks(ctx, tx, info.ID, expanded, query[0], input.Limit, allowed)
	if err != nil {
		return result, err
	}
	for _, chunk := range selected {
		switch allowed[chunk.ResourceID].Space {
		case "working":
			result.Working = append(result.Working, chunk)
		case "methods":
			result.Methods = append(result.Methods, chunk)
		case "research":
			result.Research = append(result.Research, chunk)
		default:
			result.Evidence = append(result.Evidence, chunk)
		}
	}
	if len(result.Evidence) == 0 {
		result.Omissions = append(result.Omissions, "未取得可引用原文；不能据此认定事实不存在")
	}
	base := result
	err = s.attachMemory(ctx, tx, a, project, branch, &result)
	if err == nil {
		err = fitContext(&result, input.TokenBudget)
	}
	if errors.Is(err, domain.ErrContextBudget) && hasOptionalStructure(result) {
		// Retry with hard constraints only; never let optional old character
		// details crowd out mandatory memory or complete explicitly selected text.
		result = base
		result.Memory = nil
		result.Omissions = append([]string(nil), base.Omissions...)
		if err = applyStructuredContext(&result, snapshot, 0); err != nil {
			return result, err
		}
		result.Omissions = append(result.Omissions, "容量优先保留必需设定和记忆；其余结构化背景本次未纳入，不表示不存在")
		err = s.attachMemory(ctx, tx, a, project, branch, &result)
		if err == nil {
			err = fitContext(&result, input.TokenBudget)
		}
	}
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO context_packages(id,project_id,branch_id,actor_id,index_id,content_hash,payload) VALUES(?,?,?,?,?,?,?)", result.ID, project, branch, a.ID, info.ID, digest(string(raw)), raw)
	if err != nil {
		return result, err
	}
	sources, err := contextWorkingSourceKeys(a.ID, result)
	if err != nil {
		return result, err
	}
	if err := s.IP.captureSourceValuesTx(ctx, tx, project, sources...); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func searchChunks(ctx context.Context, tx *sql.Tx, index, query string, vector []float32, limit int, allowed map[string]domain.KnowledgePolicy) ([]retrieval.Chunk, error) {
	semantic, lexical := []retrieval.Chunk{}, []retrieval.Chunk{}
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, "SELECT id,resource_id,resource_name,resource_revision,source_hash,start_rune,end_rune,content,vector,vector_hash,MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) FROM knowledge_chunks WHERE index_id=? AND id>? ORDER BY id LIMIT 128", query, index, after)
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
			after = c.ID
			count++
			if _, ok := allowed[c.ResourceID]; !ok {
				continue
			}
			if digest(string(raw)) != hash {
				rows.Close()
				return nil, domain.ErrNotReady
			}
			values, decodeErr := retrieval.UnpackVector(raw)
			if decodeErr != nil {
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
	return retrieval.Rank(retrieval.Top(semantic, false), retrieval.Top(lexical, true), limit), nil
}

func (s *Knowledge) ReadContext(ctx context.Context, a domain.Actor, project, branch, id string) (ContextPackage, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return ContextPackage{}, err
	}
	defer tx.Rollback()
	return s.readContextTx(ctx, tx, a.ID, project, branch, id)
}

func (s *Knowledge) readContextTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (ContextPackage, error) {
	var result ContextPackage
	var raw []byte
	var hash, index string
	err := tx.QueryRowContext(ctx, "SELECT payload,content_hash,index_id FROM context_packages WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?", id, project, branch, actor).Scan(&raw, &hash, &index)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	var header struct {
		State string `json:"state"`
	}
	if json.Unmarshal(raw, &header) == nil && header.State == "EXPIRED" {
		key, keyErr := retention.RowOwnerKey("context_packages", map[string]string{"id": id})
		if keyErr != nil {
			return result, domain.ErrNotReady
		}
		_, proofErr := storedExpiredProjectionTx(ctx, tx, retention.StoredOwner{ProjectID: project, Table: "context_packages", Key: key, Field: "payload", Value: raw, ContentHash: hash, ContextIndexID: index})
		if proofErr != nil {
			return result, proofErr
		}
		return result, domain.ErrNotReady
	}
	if json.Unmarshal(raw, &result) != nil {
		return result, domain.ErrNotReady
	}
	canonical, _ := json.Marshal(result)
	if digest(string(canonical)) != hash {
		return result, domain.ErrNotReady
	}
	info, err := knowledgeManifest(ctx, tx, project, branch)
	if err != nil {
		return result, err
	}
	if info.ID != index {
		return result, domain.ErrNotReady
	}
	if err = s.validateRequiredContext(ctx, tx, project, branch, result); err != nil {
		return result, err
	}
	working, err := s.workingInputs(ctx, tx, actor, project, branch, result.Input)
	if err != nil {
		return result, err
	}
	if valueHash(working) != valueHash(result.Unconfirmed) {
		return result, domain.ErrConflict
	}
	if result.Input.IncludePrivateWorking {
		private, _, err := s.privateManifest(ctx, tx, actor, project, branch)
		if err != nil {
			return result, err
		}
		if private.ID != result.PrivateIndexID {
			return result, domain.ErrConflict
		}
		if err = readyPrivateIndex(ctx, tx, &private); err != nil {
			return result, err
		}
	}
	if err = readyIndex(ctx, tx, &info); err != nil {
		return result, err
	}
	if err = s.validateMemoryContext(ctx, tx, actor, project, branch, result); err != nil {
		return result, err
	}
	return result, nil
}
