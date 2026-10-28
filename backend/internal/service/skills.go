package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/skills"
	"strings"
	"unicode/utf8"
)

type Skills struct {
	IP         *IP
	presetStep string
}
type SkillSource struct {
	Kind        string `json:"kind"`
	Repository  string `json:"repository,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Path        string `json:"path,omitempty"`
	ArchiveHash string `json:"archive_hash,omitempty"`
	URL         string `json:"url,omitempty"`
}
type SkillVersion struct {
	Retention  *domain.MaterialRetention `json:"retention,omitempty"`
	ID         string                    `json:"id"`
	ParentID   string                    `json:"parent_id"`
	UpstreamID string                    `json:"upstream_id"`
	State      string                    `json:"state"`
	Available  bool                      `json:"available"`
	Report     skills.Report             `json:"report"`
	Source     SkillSource               `json:"source"`
	CreatedAt  string                    `json:"created_at"`
}
type SkillFile struct {
	Path     string `json:"path"`
	Hash     string `json:"hash"`
	Text     string `json:"text"`
	Editable bool   `json:"editable"`
}
type SkillFileChange struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Delete bool   `json:"delete"`
}
type SkillEdit struct {
	OperationID  string            `json:"operation_id"`
	ExpectedHash string            `json:"expected_hash"`
	Changes      []SkillFileChange `json:"changes"`
}

func skillTx(ctx context.Context, tx *sql.Tx, project, id string) (SkillVersion, error) {
	return skillRecordTx(ctx, tx, project, id, false)
}

func skillRecordTx(ctx context.Context, tx *sql.Tx, project, id string, history bool) (SkillVersion, error) {
	var v SkillVersion
	var report, source []byte
	var storedHash string
	err := tx.QueryRowContext(ctx, "SELECT id,parent_id,upstream_id,state,bundle_hash,report,source,CAST(created_at AS CHAR) FROM skill_packages WHERE id=? AND project_id=? FOR UPDATE", id, project).Scan(&v.ID, &v.ParentID, &v.UpstreamID, &v.State, &storedHash, &report, &source, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return v, domain.ErrForbidden
	}
	if err != nil {
		return v, err
	}
	for _, item := range []struct {
		field string
		raw   []byte
	}{{"report", report}, {"source", source}} {
		if err := rejectExpiredRuntime(item.raw, "skill_packages."+item.field); err != nil {
			if err != domain.ErrMaterialExpired {
				return v, err
			}
			state, err := runtimeTransientTx(ctx, tx, project, "skill_packages", item.field, id)
			if err != nil {
				return v, err
			}
			v.Retention = state
		}
	}
	state, err := runtimeObjectRetentionTx(ctx, tx, project, "skill_packages", id, "bundle_hash", storedHash)
	if err != nil {
		return v, err
	}
	if state != nil {
		v.Retention = state
	}
	if rejectExpiredRuntime(source, "skill_packages.source") == nil {
		var src SkillSource
		if json.Unmarshal(source, &src) != nil {
			return v, domain.ErrNotReady
		}
		if src.ArchiveHash != "" {
			state, err := runtimeObjectRetentionTx(ctx, tx, project, "skill_packages", id, "source.archive_hash", src.ArchiveHash)
			if err != nil {
				return v, err
			}
			if state != nil {
				v.Retention = state
			}
		}
	}
	if v.Retention != nil {
		if !history {
			return v, domain.ErrMaterialExpired
		}
		return v, nil
	}
	if json.Unmarshal(report, &v.Report) != nil || json.Unmarshal(source, &v.Source) != nil || storedHash != v.Report.Hash {
		return v, domain.ErrNotReady
	}
	return v, nil
}

// Walk both immutable lineage edges on every use. Revocation does not rewrite
// descendants or delete history; no cached availability can escape this check.
func skillAvailable(ctx context.Context, tx *sql.Tx, project, id string) (bool, error) {
	pending := []string{id}
	seen := map[string]bool{}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if current == "" || seen[current] {
			continue
		}
		seen[current] = true
		if len(seen) > 128 {
			return false, domain.ErrNotReady
		}
		v, err := skillTx(ctx, tx, project, current)
		if err != nil {
			return false, err
		}
		var revoked int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_revocations WHERE package_id=? AND project_id=?", current, project).Scan(&revoked); err != nil {
			return false, err
		}
		if revoked > 0 {
			return false, nil
		}
		pending = append(pending, v.ParentID, v.UpstreamID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	revoked, err := protectedLedger().RevokedTx(ctx, tx, project, authority.SkillRevoked, ids)
	return !revoked, err
}
func (s *Skills) Read(ctx context.Context, a domain.Actor, project, id string) (SkillVersion, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return SkillVersion{}, err
	}
	defer tx.Rollback()
	v, err := skillTx(ctx, tx, project, id)
	if err != nil {
		return v, err
	}
	v.Available, err = skillAvailable(ctx, tx, project, id)
	return v, err
}
func (s *Skills) List(ctx context.Context, a domain.Actor, project string) ([]SkillVersion, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM skill_packages WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []SkillVersion{}
	for _, id := range ids {
		v, readErr := skillRecordTx(ctx, tx, project, id, true)
		if readErr != nil {
			return nil, readErr
		}
		if v.Retention == nil {
			v.Available, readErr = skillAvailable(ctx, tx, project, id)
		}
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, v)
	}
	return result, nil
}
func (s *Skills) bundle(ctx context.Context, a domain.Actor, project, id string) (SkillVersion, skills.Bundle, error) {
	v, err := s.Read(ctx, a, project, id)
	if err != nil {
		return v, skills.Bundle{}, err
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, project, v.Report.Hash)
	if err != nil {
		return v, skills.Bundle{}, domain.ErrNotReady
	}
	var b skills.Bundle
	if json.Unmarshal(raw, &b) != nil || b.Validate() != nil {
		return v, b, domain.ErrNotReady
	}
	canonical, _ := json.Marshal(b)
	if skills.Hash(canonical) != v.Report.Hash {
		return v, b, domain.ErrNotReady
	}
	return v, b, nil
}
func (s *Skills) File(ctx context.Context, a domain.Actor, project, id, name string) (SkillFile, error) {
	if !skills.SafePath(name) {
		return SkillFile{}, domain.ErrValidation
	}
	_, b, err := s.bundle(ctx, a, project, id)
	if err != nil {
		return SkillFile{}, err
	}
	for _, f := range b.Files {
		if f.Path != name {
			continue
		}
		result := SkillFile{Path: name, Hash: skills.Hash(f.Data), Editable: utf8.Valid(f.Data) && !bytes.ContainsRune(f.Data, 0) && len(f.Data) <= 512*1024}
		if result.Editable {
			result.Text = string(f.Data)
		}
		return result, nil
	}
	return SkillFile{}, domain.ErrForbidden
}
func (s *Skills) save(ctx context.Context, a domain.Actor, project, operation, parent, upstream string, b skills.Bundle, source SkillSource, requestHashes ...string) (SkillVersion, error) {
	return s.saveWithArchive(ctx, a, project, operation, parent, upstream, b, source, nil, requestHashes...)
}

func (s *Skills) saveWithArchive(ctx context.Context, a domain.Actor, project, operation, parent, upstream string, b skills.Bundle, source SkillSource, archive []byte, requestHashes ...string) (SkillVersion, error) {
	if !validName(operation, 120) {
		return SkillVersion{}, domain.ErrValidation
	}
	permission, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return SkillVersion{}, err
	}
	permission.Rollback()
	report, err := skills.Analyze(b)
	if err != nil {
		return SkillVersion{}, domain.ErrValidation
	}
	raw, _ := json.Marshal(b)
	fingerprintRaw, _ := json.Marshal([]any{project, parent, upstream, report.Hash, source})
	fingerprint := digest("skill.save\x00" + string(fingerprintRaw))
	if len(requestHashes) == 1 {
		fingerprint = requestHashes[0]
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return SkillVersion{}, err
	}
	defer tx.Rollback()
	var previous, id string
	if presetID, found, readErr := presetStepTx(ctx, tx, project, s.presetStep, "skill-package", fingerprint); readErr != nil {
		return SkillVersion{}, readErr
	} else if found {
		v, readErr := skillTx(ctx, tx, project, presetID)
		if readErr != nil {
			return v, readErr
		}
		v.Available, readErr = skillAvailable(ctx, tx, project, presetID)
		return v, readErr
	}
	previous, id, err = operationReceiptTx(ctx, tx, a, operation)
	if err == nil {
		if previous != fingerprint {
			return SkillVersion{}, domain.ErrConflict
		}
		v, readErr := skillTx(ctx, tx, project, id)
		if readErr != nil {
			return v, readErr
		}
		v.Available, readErr = skillAvailable(ctx, tx, project, id)
		return v, readErr
	}
	if err != sql.ErrNoRows {
		return SkillVersion{}, err
	}
	for _, ancestor := range []string{parent, upstream} {
		if ancestor == "" {
			continue
		}
		available, readErr := skillAvailable(ctx, tx, project, ancestor)
		if readErr != nil {
			return SkillVersion{}, readErr
		}
		if !available {
			return SkillVersion{}, domain.ErrNotReady
		}
	}
	if archive != nil {
		if source.Kind != "archive" || source.ArchiveHash != digest(string(archive)) {
			return SkillVersion{}, domain.ErrValidation
		}
		if _, err := objectwriter.Write(ctx, tx, s.IP.Blobs, project, archive); err != nil {
			return SkillVersion{}, err
		}
	}
	object, err := objectwriter.Write(ctx, tx, s.IP.Blobs, project, raw)
	if err != nil {
		return SkillVersion{}, err
	}
	if object.Hash != report.Hash {
		return SkillVersion{}, domain.ErrNotReady
	}
	v := SkillVersion{ID: NewID(), ParentID: parent, UpstreamID: upstream, State: "CANDIDATE", Available: true, Report: report, Source: source}
	reportRaw, _ := json.Marshal(report)
	sourceRaw, _ := json.Marshal(source)
	_, err = tx.ExecContext(ctx, "INSERT INTO skill_packages(id,project_id,parent_id,upstream_id,bundle_hash,report,source,created_by) VALUES(?,?,?,?,?,?,?,?)", v.ID, project, parent, upstream, report.Hash, reportRaw, sourceRaw, a.ID)
	if err != nil {
		return v, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'skill.save',?,?)", a.ID, operation, fingerprint, v.ID)
	if err != nil {
		return v, err
	}
	if err = audit(ctx, tx, a, "skill.candidate", v.ID); err != nil {
		return v, err
	}
	if err = savePresetStepTx(ctx, tx, a, project, s.presetStep, "skill-package", fingerprint, v.ID); err != nil {
		return v, err
	}
	if err = s.IP.captureSkillSourceTx(ctx, tx, project, v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Skills) Create(ctx context.Context, a domain.Actor, project, op, markdown string) (SkillVersion, error) {
	return s.save(ctx, a, project, op, "", "", skills.Bundle{Files: []skills.File{{Path: "SKILL.md", Data: []byte(markdown)}}}, SkillSource{Kind: "manual"})
}
func (s *Skills) Import(ctx context.Context, a domain.Actor, project, op string, archive []byte) (SkillVersion, error) {
	permission, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return SkillVersion{}, err
	}
	permission.Rollback()
	b, err := skills.ReadArchive(archive)
	if err != nil {
		return SkillVersion{}, domain.ErrValidation
	}
	return s.saveWithArchive(ctx, a, project, op, "", "", b, SkillSource{Kind: "archive", ArchiveHash: digest(string(archive))}, archive)
}
func (s *Skills) Edit(ctx context.Context, a domain.Actor, project, id string, input SkillEdit) (SkillVersion, error) {
	if len(input.Changes) == 0 || len(input.Changes) > skills.MaxFiles {
		return SkillVersion{}, domain.ErrValidation
	}
	v, b, err := s.bundle(ctx, a, project, id)
	if err != nil {
		return v, err
	}
	if !v.Available {
		return v, domain.ErrNotReady
	}
	if v.Report.Hash != input.ExpectedHash {
		return v, domain.ErrConflict
	}
	seen := map[string]bool{}
	for _, change := range input.Changes {
		if !skills.SafePath(change.Path) || seen[strings.ToLower(change.Path)] || len(change.Text) > 512*1024 || !utf8.ValidString(change.Text) {
			return v, domain.ErrValidation
		}
		seen[strings.ToLower(change.Path)] = true
		next := []skills.File{}
		for _, file := range b.Files {
			if file.Path != change.Path {
				next = append(next, file)
			}
		}
		if !change.Delete {
			next = append(next, skills.File{Path: change.Path, Data: []byte(change.Text)})
		}
		b.Files = next
	}
	return s.save(ctx, a, project, input.OperationID, id, "", b, v.Source)
}
func (s *Skills) Reference(ctx context.Context, a domain.Actor, project, id string) (SkillVersion, error) {
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return SkillVersion{}, err
	}
	defer tx.Rollback()
	if (a.Role != "owner" && a.Role != "publisher") || (p.Role != "owner" && p.Role != "publisher") {
		return SkillVersion{}, domain.ErrForbidden
	}
	v, err := skillTx(ctx, tx, project, id)
	if err != nil {
		return v, err
	}
	v.Available, err = skillAvailable(ctx, tx, project, id)
	if err != nil {
		return v, err
	}
	if !v.Available {
		return v, domain.ErrNotReady
	}
	if v.State == "REFERENCE" {
		return v, nil
	}
	if _, err = tx.ExecContext(ctx, "UPDATE skill_packages SET state='REFERENCE' WHERE id=?", id); err != nil {
		return v, err
	}
	if err = audit(ctx, tx, a, "skill.reference", id); err != nil {
		return v, err
	}
	v.State = "REFERENCE"
	return v, tx.Commit()
}
func (s *Skills) Revoke(ctx context.Context, a domain.Actor, project, id, reason string) error {
	if !validName(reason, 1000) {
		return domain.ErrValidation
	}
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if a.Role != "owner" || p.Role != "owner" {
		return domain.ErrForbidden
	}
	if _, err = skillTx(ctx, tx, project, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO skill_revocations(package_id,project_id,actor_id,reason) VALUES(?,?,?,?)", id, project, a.ID, reason); err != nil {
		return err
	}
	if err = captureDiagnosticKeyTx(ctx, tx, project, "skill_revocations", map[string]string{"package_id": id}); err != nil {
		return err
	}
	if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.SkillRevoked, ProjectID: project, ResourceID: id, ActorID: a.ID, Reason: reason}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE skill_packages SET state='REVOKED' WHERE id=?", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "skill.revoke", id); err != nil {
		return err
	}
	return tx.Commit()
}
