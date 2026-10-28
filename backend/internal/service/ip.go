package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/storage"
	"strings"
	"time"
)

type IP struct {
	Workspace *Workspace
	Blobs     storage.BlobStore
}
type Branch struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	ParentID     string          `json:"parent_id"`
	BaseRevision string          `json:"base_revision"`
	Head         string          `json:"head"`
	Revision     int64           `json:"revision"`
	Snapshot     domain.Snapshot `json:"snapshot"`
}
type Candidate = domain.IPCandidate

func (s *IP) begin(ctx context.Context, a domain.Actor, project string, write bool) (*sql.Tx, domain.Actor, domain.Project, error) {
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, a, domain.Project{}, err
	}
	a, err = s.Workspace.actorTx(ctx, tx, a.ID)
	if err != nil {
		tx.Rollback()
		return nil, a, domain.Project{}, err
	}
	// Admission precedes project and budget locks everywhere that can create a
	// request. Cleanup takes the same order, preventing cutover/reservation cycles.
	if write {
		if _, _, err = requestIdentityPolicyTx(ctx, tx, a.WorkspaceID); err != nil {
			tx.Rollback()
			return nil, a, domain.Project{}, err
		}
	}
	p, err := projectTx(ctx, tx, a, project)
	if err == nil && write && (!domain.CanEdit(a.Role) || !domain.CanEdit(p.Role)) {
		err = domain.ErrForbidden
	}
	if err != nil {
		tx.Rollback()
		return nil, a, p, err
	}
	return tx, a, p, nil
}

func branchTx(ctx context.Context, tx *sql.Tx, project, id string) (Branch, error) {
	var b Branch
	err := tx.QueryRowContext(ctx, "SELECT id,name,parent_id,base_revision,head_revision,revision FROM branches WHERE id=? AND project_id=? FOR UPDATE", id, project).Scan(&b.ID, &b.Name, &b.ParentID, &b.BaseRevision, &b.Head, &b.Revision)
	if err == sql.ErrNoRows {
		return b, domain.ErrForbidden
	}
	return b, err
}
func snapshotTx(ctx context.Context, tx *sql.Tx, project, revision string) (domain.Snapshot, error) {
	snapshot := domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
	if revision == "" {
		return snapshot, nil
	}
	var payload []byte
	err := tx.QueryRowContext(ctx, "SELECT r.payload FROM ip_revisions r JOIN branches b ON b.id=r.branch_id WHERE r.id=? AND b.project_id=? AND r.status='PUBLISHED'", revision, project).Scan(&payload)
	if err == sql.ErrNoRows {
		return snapshot, domain.ErrForbidden
	}
	if err != nil {
		return snapshot, err
	}
	err = json.Unmarshal(payload, &snapshot)
	if expiryErr := rejectExpiredRuntime(payload, "ip_revisions.payload"); expiryErr != nil {
		return snapshot, expiryErr
	}
	return snapshot, err
}
func candidateTx(ctx context.Context, tx *sql.Tx, branch, id string) (Candidate, error) {
	var c Candidate
	var payload []byte
	err := tx.QueryRowContext(ctx, "SELECT r.id,r.branch_id,r.base_head,r.content_hash,r.status,r.payload,COALESCE(a.reason,'') FROM ip_revisions r LEFT JOIN ip_amendments a ON a.candidate_id=r.id WHERE r.id=? AND r.branch_id=? FOR UPDATE", id, branch).Scan(&c.ID, &c.BranchID, &c.BaseHead, &c.Hash, &c.Status, &payload, &c.AmendmentReason)
	if err == sql.ErrNoRows {
		return c, domain.ErrForbidden
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(payload, &c.Snapshot)
	var project string
	if err := tx.QueryRowContext(ctx, "SELECT project_id FROM branches WHERE id=?", branch).Scan(&project); err != nil {
		return c, err
	}
	state, readErr := publicationRetentionTx(ctx, tx, project, "ip_revisions", id, c.Hash, payload)
	if readErr != nil {
		return c, readErr
	}
	if state != nil {
		return c, domain.ErrMaterialExpired
	}
	c.Checks = domain.ReviewRules(c.Snapshot)
	if err == nil {
		err = loadPromotion(ctx, tx, &c)
	}
	if err == nil {
		err = loadCandidateDelta(ctx, tx, &c)
	}
	return c, err
}

func (s *IP) CreateBranch(ctx context.Context, a domain.Actor, project, name, parent, base, op string) (Branch, error) {
	var b Branch
	name = strings.TrimSpace(name)
	if !validName(name, 120) || strings.HasPrefix(name, "~erased:") || !validName(op, 120) {
		return b, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	hash := digest("branch.create\x00" + project + "\x00" + name + "\x00" + parent + "\x00" + base)
	var stored, result string
	stored, result, err = operationReceiptTx(ctx, tx, a, op)
	if err == nil {
		if stored != hash {
			return b, domain.ErrConflict
		}
		return branchTx(ctx, tx, project, result)
	}
	if err != sql.ErrNoRows {
		return b, err
	}
	if parent != "" {
		if _, err = branchTx(ctx, tx, project, parent); err != nil {
			return b, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ip_revisions WHERE id=? AND branch_id=? AND status='PUBLISHED'", base, parent).Scan(&count); err != nil {
			return b, err
		}
		if count != 1 {
			return b, domain.ErrValidation
		}
	} else if base != "" {
		return b, domain.ErrValidation
	}
	b = Branch{ID: NewID(), Name: name, ParentID: parent, BaseRevision: base, Head: base, Revision: 1}
	if _, err = tx.ExecContext(ctx, "INSERT INTO branches(id,project_id,name,parent_id,base_revision,head_revision) VALUES(?,?,?,?,?,?)", b.ID, project, name, parent, base, base); err != nil {
		return b, databaseError(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'branch.create',?,?)", a.ID, op, hash, b.ID); err != nil {
		return b, err
	}
	if err = audit(ctx, tx, a, "branch.create", b.ID); err != nil {
		return b, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "branches", b.ID); err != nil {
		return b, err
	}
	return b, tx.Commit()
}
func (s *IP) Branches(ctx context.Context, a domain.Actor, project string) ([]Branch, error) {
	if _, err := s.Workspace.Project(ctx, a, project); err != nil {
		return nil, err
	}
	rows, err := s.Workspace.DB.QueryContext(ctx, "SELECT id,name,parent_id,base_revision,head_revision,revision FROM branches WHERE project_id=? ORDER BY parent_id,name", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Branch{}
	for rows.Next() {
		var b Branch
		if err = rows.Scan(&b.ID, &b.Name, &b.ParentID, &b.BaseRevision, &b.Head, &b.Revision); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
func (s *IP) ReadBranch(ctx context.Context, a domain.Actor, project, id string) (Branch, error) {
	var b Branch
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	b, err = branchTx(ctx, tx, project, id)
	if err != nil {
		return b, err
	}
	b.Snapshot, err = snapshotTx(ctx, tx, project, b.Head)
	return b, err
}

func (s *IP) Propose(ctx context.Context, a domain.Actor, project, branch, base, op string, snapshot domain.Snapshot) (Candidate, error) {
	return s.propose(ctx, a, project, branch, base, op, snapshot, nil, "", nil)
}
func (s *IP) ProposeAmendment(ctx context.Context, a domain.Actor, project, branch, base, op, reason string, snapshot domain.Snapshot) (Candidate, error) {
	if !validName(reason, 2000) {
		return Candidate{}, domain.ErrValidation
	}
	return s.propose(ctx, a, project, branch, base, op, snapshot, nil, reason, nil)
}
func (s *IP) propose(ctx context.Context, a domain.Actor, project, branch, base, op string, snapshot domain.Snapshot, merge *MergeInput, amendmentReason string, promotion *PromotionInput, reasonSets ...map[string]string) (Candidate, error) {
	var c Candidate
	if err := domain.ValidateSnapshot(snapshot); err != nil {
		return c, err
	}
	if !validName(op, 120) {
		return c, domain.ErrValidation
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return c, err
	}
	if len(payload) > 512*1024 {
		return c, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return c, err
	}
	requestHash := digest("ip.propose\x00" + branch + "\x00" + base + "\x00" + string(payload))
	if amendmentReason != "" {
		requestHash = digest("ip.amend\x00" + requestHash + "\x00" + amendmentReason)
	}
	if merge != nil {
		encoded, _ := json.Marshal(merge)
		requestHash = digest("ip.merge\x00" + branch + "\x00" + string(encoded))
	}
	var stored, result string
	if promotion != nil {
		encoded, _ := json.Marshal(promotion)
		requestHash = digest("ip.promote\x00" + branch + "\x00" + string(encoded))
	}
	if len(reasonSets) > 0 {
		encoded, _ := json.Marshal(reasonSets[0])
		requestHash = digest("ip.delta\x00" + requestHash + "\x00" + string(encoded))
	}
	stored, result, err = operationReceiptTx(ctx, tx, a, op)
	if err == nil {
		if stored != requestHash {
			return c, domain.ErrConflict
		}
		return candidateTx(ctx, tx, branch, result)
	}
	if err != sql.ErrNoRows {
		return c, err
	}
	if base != b.Head {
		return c, domain.ErrConflict
	}
	if merge != nil {
		view, mergeErr := mergeViewTx(ctx, tx, project, b)
		if mergeErr != nil {
			return c, mergeErr
		}
		if view.UpstreamHead != merge.UpstreamHead {
			return c, domain.ErrConflict
		}
		snapshot, err = domain.ResolveMerge(view.Base, view.Upstream, view.Local, merge.Resolutions)
		if err != nil {
			return c, err
		}
	}
	previous, err := snapshotTx(ctx, tx, project, b.Head)
	if promotion != nil && err == nil {
		snapshot, err = promotionSnapshotTx(ctx, tx, project, branch, *promotion)
	}
	if err != nil {
		return c, err
	}
	if len(reasonSets) > 0 {
		normalizeDeltaMetadata(previous, &snapshot)
		if _, err = domain.BuildBranchDelta(previous, snapshot, reasonSets[0]); err != nil {
			return c, err
		}
	}
	domain.StampSnapshot(previous, &snapshot, time.Now())
	if err = domain.CheckLocked(previous, snapshot); err != nil && amendmentReason == "" {
		return c, err
	}
	if err == nil && amendmentReason != "" {
		return c, domain.ErrValidation
	}
	payload, err = json.Marshal(snapshot)
	if err != nil {
		return c, err
	}
	if len(payload) > 512*1024 {
		return c, domain.ErrValidation
	}
	object, err := objectwriter.Write(ctx, tx, s.Blobs, project, payload)
	if err != nil {
		return c, err
	}
	hash := object.Hash
	c = Candidate{ID: NewID(), BranchID: branch, BaseHead: base, Hash: hash, Status: "PREPARED", Snapshot: snapshot}
	c.Checks = domain.ReviewRules(snapshot)
	c.AmendmentReason = amendmentReason
	c.Promotion = promotion
	if _, err = tx.ExecContext(ctx, "INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,created_by) VALUES(?,?,?,?,?,?)", c.ID, branch, base, payload, hash, a.ID); err != nil {
		return c, err
	}
	if len(reasonSets) > 0 {
		if err = saveCandidateDelta(ctx, tx, a, project, b, &c, previous, reasonSets[0], merge, promotion); err != nil {
			return c, err
		}
	}
	if merge != nil {
		if _, err = tx.ExecContext(ctx, "INSERT INTO ip_merge_candidates(candidate_id,parent_head,old_base) VALUES(?,?,?)", c.ID, merge.UpstreamHead, b.BaseRevision); err != nil {
			return c, err
		}
	}
	if promotion != nil {
		encoded, _ := json.Marshal(promotion)
		if _, err = tx.ExecContext(ctx, "INSERT INTO ip_promotions(candidate_id,payload) VALUES(?,?)", c.ID, string(encoded)); err != nil {
			return c, err
		}
	}
	if amendmentReason != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO ip_amendments(candidate_id,reason) VALUES(?,?)", c.ID, amendmentReason); err != nil {
			return c, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'ip.propose',?,?)", a.ID, op, requestHash, c.ID); err != nil {
		return c, err
	}
	if err = audit(ctx, tx, a, "ip.propose", c.ID); err != nil {
		return c, err
	}
	if err = s.captureCandidateSourceTx(ctx, tx, project, c.ID); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (s *IP) Candidates(ctx context.Context, a domain.Actor, project, branch string) ([]Candidate, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT r.id,r.branch_id,r.base_head,r.content_hash,r.status,r.payload,COALESCE(a.reason,'') FROM ip_revisions r LEFT JOIN ip_amendments a ON a.candidate_id=r.id WHERE r.branch_id=? ORDER BY r.created_at DESC", branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Candidate{}
	payloads := map[string][]byte{}
	for rows.Next() {
		var c Candidate
		var payload []byte
		if err = rows.Scan(&c.ID, &c.BranchID, &c.BaseHead, &c.Hash, &c.Status, &payload, &c.AmendmentReason); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(payload, &c.Snapshot); err != nil {
			return nil, err
		}
		c.Checks = domain.ReviewRules(c.Snapshot)
		items = append(items, c)
		payloads[c.ID] = payload
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Retention, err = publicationRetentionTx(ctx, tx, project, "ip_revisions", items[i].ID, items[i].Hash, payloads[items[i].ID])
		if err != nil {
			return nil, err
		}
		if items[i].Retention != nil {
			items[i].Snapshot = domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
			items[i].Checks = nil
			continue
		}
		err = loadPromotion(ctx, tx, &items[i])
		if err == nil {
			err = loadCandidateDelta(ctx, tx, &items[i])
		}
		if err == domain.ErrMaterialExpired {
			items[i].Retention, err = candidateAuxiliaryRetentionTx(ctx, tx, project, items[i].ID)
			if err != nil {
				return nil, err
			}
			items[i].Promotion, items[i].Delta = nil, nil
			continue
		}
		if err != nil {
			return nil, err
		}
		scope, e := candidateSemanticScope(items[i])
		if e != nil {
			return nil, e
		}
		negative, e := s.knownSemanticNegativeTx(ctx, tx, branch, "ip_candidate", scope, "")
		if e != nil {
			return nil, e
		}
		items[i].RequiresSemanticReview = items[i].RequiresSemanticReview || negative
	}
	return items, nil
}
func (s *IP) Adopt(ctx context.Context, a domain.Actor, project, branch, id string) (Candidate, error) {
	return s.adopt(ctx, a, project, branch, id, false)
}
func (s *IP) ApproveAmendment(ctx context.Context, a domain.Actor, project, branch, id string) (Candidate, error) {
	return s.adopt(ctx, a, project, branch, id, true)
}
func (s *IP) adopt(ctx context.Context, a domain.Actor, project, branch, id string, approveAmendment bool) (Candidate, error) {
	var c Candidate
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	p, err := projectTx(ctx, tx, a, project)
	if err != nil {
		return c, err
	}
	if a.Role == "reader" || p.Role == "reader" {
		return c, domain.ErrForbidden
	}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return c, err
	}
	c, err = candidateTx(ctx, tx, branch, id)
	if err != nil {
		return c, err
	}
	if c.AmendmentReason != "" {
		if !approveAmendment {
			return c, domain.ErrConflict
		}
		if a.Role != "owner" || p.Role != "owner" {
			return c, domain.ErrForbidden
		}
	} else if approveAmendment {
		return c, domain.ErrValidation
	}
	if c.Promotion != nil && (a.Role != "owner" || p.Role != "owner") {
		return c, domain.ErrForbidden
	}
	if c.Status == "PUBLISHED" {
		return c, nil
	}
	if b.Head != c.BaseHead {
		return c, domain.ErrConflict
	}
	if _, err = validateCandidateAncestryTx(ctx, tx, project, b, c); err != nil {
		return c, err
	}
	if err = domain.ValidateSnapshot(c.Snapshot); err != nil {
		return c, err
	}
	if err = domain.ValidateRuleEvidence(c.Snapshot); err != nil {
		return c, err
	}
	if _, err = s.Blobs.ReadObject(ctx, project, c.Hash); err != nil {
		return c, domain.ErrNotReady
	}
	if c.Status == "READY" {
		if err = s.requireCandidateSemanticReviewTx(ctx, tx, project, c); err != nil {
			return c, err
		}
		return c, nil
	}
	if err = s.requireCandidateSemanticReviewTx(ctx, tx, project, c); err != nil {
		return c, err
	}
	if c.AmendmentReason != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE ip_amendments SET approved_by=?,approved_hash=? WHERE candidate_id=?", a.ID, c.Hash, id); err != nil {
			return c, err
		}
	}
	if err = s.captureCandidateSourceTx(ctx, tx, project, id); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO adoptions(candidate_id,actor_id,content_hash) VALUES(?,?,?)", id, a.ID, c.Hash); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE ip_revisions SET status='READY' WHERE id=?", id); err != nil {
		return c, err
	}
	if err = audit(ctx, tx, a, "ip.adopt", id); err != nil {
		return c, err
	}
	if err = s.captureCandidateSourceTx(ctx, tx, project, id); err != nil {
		return c, err
	}
	c.Status = "READY"
	return c, tx.Commit()
}
func (s *IP) Publish(ctx context.Context, a domain.Actor, project, branch, id string) (Candidate, error) {
	var c Candidate
	if err := s.preparePublicationIndex(ctx, a, project, branch, "", id); err != nil {
		return c, err
	}
	tx, a, p, err := s.begin(ctx, a, project, false)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if (a.Role != "owner" && a.Role != "publisher") || (p.Role != "owner" && p.Role != "publisher") {
		return c, domain.ErrForbidden
	}
	if err = s.requirePublicationIndex(ctx, tx, project, branch, "", id); err != nil {
		return c, err
	}
	c, err = s.publishTx(ctx, tx, a, project, branch, id)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// publishTx is shared by standalone IP publication and atomic artifact bundles.
// The caller owns the project authorization lock and commits the entire unit once.
func (s *IP) publishTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, branch, id string) (Candidate, error) {
	prepared, err := s.prepareCandidatePublicationTx(ctx, tx, project, branch, id)
	if err != nil {
		return prepared.Candidate, err
	}
	return s.applyCandidatePublicationTx(ctx, tx, a, project, branch, prepared)
}

type candidatePublication struct {
	Candidate
	mergeParent string
}

func (s *IP) prepareCandidatePublicationTx(ctx context.Context, tx *sql.Tx, project, branch, id string) (candidatePublication, error) {
	var c candidatePublication
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return c, err
	}
	c.Candidate, err = candidateTx(ctx, tx, branch, id)
	if err != nil {
		return c, err
	}
	if c.Status == "PUBLISHED" {
		return c, nil
	}
	if b.Head != c.BaseHead || c.Status != "READY" {
		return c, domain.ErrConflict
	}
	mergeParent, err := validateCandidateAncestryTx(ctx, tx, project, b, c.Candidate)
	if err != nil {
		return c, err
	}
	if err = s.requireCandidateSemanticReviewTx(ctx, tx, project, c.Candidate); err != nil {
		return c, err
	}
	if c.AmendmentReason != "" {
		var approved sql.NullString
		if err = tx.QueryRowContext(ctx, "SELECT approved_hash FROM ip_amendments WHERE candidate_id=? AND approved_by IS NOT NULL", id).Scan(&approved); err != nil || !approved.Valid || approved.String != c.Hash {
			return c, domain.ErrConflict
		}
	}
	var adopted string
	if err = tx.QueryRowContext(ctx, "SELECT content_hash FROM adoptions WHERE candidate_id=?", id).Scan(&adopted); err != nil || adopted != c.Hash {
		return c, domain.ErrConflict
	}
	if _, err = s.Blobs.ReadObject(ctx, project, c.Hash); err != nil {
		return c, domain.ErrNotReady
	}
	if err = domain.ValidateSnapshot(c.Snapshot); err != nil {
		return c, err
	}
	if err = domain.ValidateRuleEvidence(c.Snapshot); err != nil {
		return c, err
	}
	if err = s.captureCandidateSourceTx(ctx, tx, project, id); err != nil {
		return c, err
	}
	c.mergeParent = mergeParent
	return c, nil
}

func (s *IP) applyCandidatePublicationTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, branch string, prepared candidatePublication) (Candidate, error) {
	c := prepared.Candidate
	if c.Status == "PUBLISHED" {
		return c, nil
	}
	id, mergeParent := c.ID, prepared.mergeParent
	var err error
	if _, err = tx.ExecContext(ctx, "UPDATE ip_revisions SET status='PUBLISHED' WHERE id=?", id); err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE branches SET head_revision=?,revision=revision+1 WHERE id=?", id, branch); err != nil {
		return c, err
	}
	if mergeParent != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE branches SET base_revision=? WHERE id=?", mergeParent, branch); err != nil {
			return c, err
		}
	}
	event, _ := json.Marshal(map[string]string{"branch_id": branch, "base_head": c.BaseHead, "head": id})
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'ip.published',?,?)", NewID(), project, id, event); err != nil {
		return c, err
	}
	if err = audit(ctx, tx, a, "ip.publish", id); err != nil {
		return c, err
	}
	if err = s.captureCandidateSourceTx(ctx, tx, project, id); err != nil {
		return c, err
	}
	c.Status = "PUBLISHED"
	return c, nil
}
func (s *IP) MergePreview(ctx context.Context, a domain.Actor, project, branch string) (MergeView, error) {
	empty := MergeView{}
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return empty, err
	}
	return mergeViewTx(ctx, tx, project, b)
}
