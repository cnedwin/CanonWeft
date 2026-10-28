package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
	"slices"
	"strings"
)

type Capabilities struct {
	IP         *IP
	presetStep string
}
type CapabilityVersion = domain.CapabilityVersion
type CapabilitySave struct {
	InputSchemaText  string                      `json:"input_schema_text,omitempty"`
	OutputSchemaText string                      `json:"output_schema_text,omitempty"`
	OperationID      string                      `json:"operation_id"`
	ExpectedHead     string                      `json:"expected_head"`
	Definition       domain.CapabilityDefinition `json:"definition"`
}
type CapabilityFinding struct {
	VersionID string `json:"version_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}
type CapabilityManifest struct {
	Root       domain.CapabilityRef `json:"root"`
	Hash       string               `json:"hash"`
	Versions   []CapabilityVersion  `json:"versions"`
	TrialReady bool                 `json:"trial_ready"`
	Qualified  bool                 `json:"qualified"`
	Findings   []CapabilityFinding  `json:"findings"`
}

// Normalize object order and whitespace without rounding any numeric literal.
// Contracts use text storage because MySQL JSON can round decimal literals.
func capabilityDigest(value any) string {
	raw, _ := json.Marshal(value)
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&normalized)
	raw, _ = json.Marshal(normalized)
	return digest(string(raw))
}
func capabilityVersionHash(v CapabilityVersion) string {
	return capabilityDigest([]any{v.ParentID, v.Definition})
}
func capabilityTx(ctx context.Context, tx *sql.Tx, project, id string) (CapabilityVersion, error) {
	return capabilityRecordTx(ctx, tx, project, id, false)
}

func capabilityRecordTx(ctx context.Context, tx *sql.Tx, project, id string, history bool) (CapabilityVersion, error) {
	var v CapabilityVersion
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,parent_id,content_hash,definition,CAST(created_at AS CHAR) FROM capability_versions WHERE project_id=? AND id=? FOR UPDATE", project, id).Scan(&v.ID, &v.ParentID, &v.Hash, &raw, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return v, domain.ErrForbidden
	}
	if err != nil {
		return v, err
	}
	if err := rejectExpiredRuntime(raw, "capability_versions.definition"); err != nil {
		if err != domain.ErrMaterialExpired {
			return v, err
		}
		v.Retention, err = runtimeTransientTx(ctx, tx, project, "capability_versions", "definition", id)
		if err != nil {
			return v, err
		}
		if !history {
			return v, domain.ErrMaterialExpired
		}
		return v, nil
	}
	if json.Unmarshal(raw, &v.Definition) != nil || v.Definition.Validate() != nil || capabilityVersionHash(v) != v.Hash {
		return v, domain.ErrNotReady
	}
	v.InputSchemaText = string(v.Definition.InputSchema)
	v.OutputSchemaText = string(v.Definition.OutputSchema)
	return v, nil
}
func capabilityAvailable(ctx context.Context, tx *sql.Tx, project, id string) (bool, error) {
	seen := map[string]bool{}
	for id != "" {
		if seen[id] || len(seen) >= 256 {
			return false, domain.ErrNotReady
		}
		seen[id] = true
		v, err := capabilityTx(ctx, tx, project, id)
		if err != nil {
			return false, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM capability_revocations WHERE project_id=? AND version_id=?", project, id).Scan(&count); err != nil {
			return false, err
		}
		if count != 0 {
			return false, nil
		}
		id = v.ParentID
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	revoked, err := protectedLedger().RevokedTx(ctx, tx, project, authority.CapabilityRevoked, ids)
	return !revoked, err
}
func (s *Capabilities) Read(ctx context.Context, a domain.Actor, project, id string) (CapabilityVersion, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return CapabilityVersion{}, err
	}
	defer tx.Rollback()
	return capabilityTx(ctx, tx, project, id)
}
func (s *Capabilities) Save(ctx context.Context, a domain.Actor, project string, input CapabilitySave) (CapabilityVersion, error) {
	var v CapabilityVersion
	if input.InputSchemaText != "" {
		input.Definition.InputSchema = json.RawMessage(input.InputSchemaText)
	}
	if input.OutputSchemaText != "" {
		input.Definition.OutputSchema = json.RawMessage(input.OutputSchemaText)
	}
	if !validName(input.OperationID, 120) || input.Definition.Validate() != nil {
		return v, domain.ErrValidation
	}
	raw, _ := json.Marshal(input.Definition)
	if len(raw) > 256*1024 {
		return v, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	fingerprint := capabilityDigest([]any{"capability.save", project, input.ExpectedHead, input.Definition})
	if presetID, found, readErr := presetStepTx(ctx, tx, project, s.presetStep, "capability", fingerprint); readErr != nil {
		return v, readErr
	} else if found {
		return capabilityTx(ctx, tx, project, presetID)
	}
	var previous, id string
	previous, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if previous != fingerprint {
			return v, domain.ErrConflict
		}
		return capabilityTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return v, err
	}
	var head string
	err = tx.QueryRowContext(ctx, "SELECT version_id FROM capability_heads WHERE project_id=? AND capability_key=? FOR UPDATE", project, input.Definition.Key).Scan(&head)
	if err != nil && err != sql.ErrNoRows {
		return v, err
	}
	if head != input.ExpectedHead {
		return v, domain.ErrConflict
	}
	if head != "" {
		parent, readErr := capabilityTx(ctx, tx, project, head)
		if readErr != nil {
			return v, readErr
		}
		if parent.Definition.Kind != input.Definition.Kind {
			return v, domain.ErrValidation
		}
		available, readErr := capabilityAvailable(ctx, tx, project, head)
		if readErr != nil {
			return v, readErr
		}
		if !available {
			return v, domain.ErrNotReady
		}
	}
	for _, ref := range input.Definition.Dependencies {
		dep, readErr := capabilityTx(ctx, tx, project, ref.ID)
		if readErr != nil {
			return v, readErr
		}
		if dep.Hash != ref.Hash || dep.Definition.Kind != ref.Kind {
			return v, domain.ErrConflict
		}
		manifest, readErr := s.manifestTx(ctx, tx, project, ref.ID)
		if readErr != nil {
			return v, readErr
		}
		for _, finding := range manifest.Findings {
			if finding.Code == "REVOKED" {
				return v, domain.ErrNotReady
			}
		}
	}
	if input.Definition.PackageID != "" {
		pkg, readErr := skillTx(ctx, tx, project, input.Definition.PackageID)
		if readErr != nil {
			return v, readErr
		}
		if pkg.Report.Hash != input.Definition.PackageHash {
			return v, domain.ErrConflict
		}
		available, readErr := skillAvailable(ctx, tx, project, pkg.ID)
		if readErr != nil {
			return v, readErr
		}
		if !available {
			return v, domain.ErrNotReady
		}
		bundle, readErr := s.packageTx(ctx, project, pkg)
		if readErr != nil {
			return v, readErr
		}
		if input.Definition.Tool != nil && (!skills.SafePath(input.Definition.Tool.Entrypoint) || bundle.Find(input.Definition.Tool.Entrypoint) == nil) {
			return v, domain.ErrValidation
		}
	}
	v = CapabilityVersion{ID: NewID(), ParentID: head, Definition: input.Definition}
	v.Hash = capabilityVersionHash(v)
	if _, err = tx.ExecContext(ctx, "INSERT INTO capability_versions(id,project_id,capability_key,kind,parent_id,content_hash,definition,created_by) VALUES(?,?,?,?,?,?,?,?)", v.ID, project, v.Definition.Key, v.Definition.Kind, head, v.Hash, raw, a.ID); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO capability_heads(project_id,capability_key,version_id) VALUES(?,?,?) ON DUPLICATE KEY UPDATE version_id=?", project, v.Definition.Key, v.ID, v.ID); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'capability.save',?,?)", a.ID, input.OperationID, fingerprint, v.ID); err != nil {
		return v, err
	}
	if err = audit(ctx, tx, a, "capability.candidate", v.ID); err != nil {
		return v, err
	}
	if err = savePresetStepTx(ctx, tx, a, project, s.presetStep, "capability", fingerprint, v.ID); err != nil {
		return v, err
	}
	v, err = capabilityTx(ctx, tx, project, v.ID)
	if err != nil {
		return v, err
	}
	if err = s.IP.captureCapabilitySourceTx(ctx, tx, project, v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Capabilities) packageTx(ctx context.Context, project string, pkg SkillVersion) (skills.Bundle, error) {
	raw, err := s.IP.Blobs.ReadObject(ctx, project, pkg.Report.Hash)
	var b skills.Bundle
	if err != nil || skills.Hash(raw) != pkg.Report.Hash || json.Unmarshal(raw, &b) != nil || b.Validate() != nil {
		return b, domain.ErrNotReady
	}
	return b, nil
}
func (s *Capabilities) Manifest(ctx context.Context, a domain.Actor, project, id string) (CapabilityManifest, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return CapabilityManifest{}, err
	}
	defer tx.Rollback()
	return s.manifestTx(ctx, tx, project, id)
}
func (s *Capabilities) manifestTx(ctx context.Context, tx *sql.Tx, project, id string) (CapabilityManifest, error) {
	result := CapabilityManifest{Versions: []CapabilityVersion{}, Findings: []CapabilityFinding{}, TrialReady: true}
	pending := []string{id}
	seen := map[string]CapabilityVersion{}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if _, ok := seen[current]; ok {
			continue
		}
		if len(seen) >= 256 {
			return result, domain.ErrNotReady
		}
		v, err := capabilityTx(ctx, tx, project, current)
		if err != nil {
			return result, err
		}
		seen[current] = v
		if current == id {
			result.Root = domain.CapabilityRef{ID: id, Hash: v.Hash, Kind: v.Definition.Kind}
		}
		result.Versions = append(result.Versions, v)
		available, err := capabilityAvailable(ctx, tx, project, current)
		if err != nil {
			return result, err
		}
		if !available {
			result.block(current, "REVOKED", "此版本或其编辑祖先已撤销")
		}
		for _, ref := range v.Definition.Dependencies {
			dep, err := capabilityTx(ctx, tx, project, ref.ID)
			if err != nil {
				return result, err
			}
			if dep.Hash != ref.Hash || dep.Definition.Kind != ref.Kind {
				return result, domain.ErrConflict
			}
			pending = append(pending, ref.ID)
		}
		if v.Definition.PackageID != "" {
			pkg, err := skillTx(ctx, tx, project, v.Definition.PackageID)
			if err != nil {
				return result, err
			}
			if pkg.Report.Hash != v.Definition.PackageHash {
				return result, domain.ErrNotReady
			}
			if _, err = s.packageTx(ctx, project, pkg); err != nil {
				return result, err
			}
			available, err = skillAvailable(ctx, tx, project, pkg.ID)
			if err != nil {
				return result, err
			}
			if !available {
				result.block(current, "REVOKED", "方法来源或其上游已撤销")
			}
			if len(pkg.Report.RequestedTools) > 0 {
				// Declarative names cannot authorize tools. Explicit executable edges need qualification.
				result.block(current, "TOOL_QUALIFICATION_REQUIRED", "方法声明了工具需求，须完成显式绑定及隔离评测")
			}
		}
		if v.Definition.Kind == "tool" {
			result.block(current, "TOOL_QUALIFICATION_REQUIRED", "工具待隔离运行与保留集评测，不可执行")
		}
	}
	slices.SortFunc(result.Versions, func(a, b CapabilityVersion) int { return strings.Compare(a.ID, b.ID) })
	result.Hash = capabilityDigest([]any{result.Root, result.Versions})
	if result.Root.Kind != "agent" {
		result.TrialReady = false
	}
	result.Findings = append(result.Findings, CapabilityFinding{VersionID: id, Code: "QUALIFICATION_REQUIRED", Message: "仅完成版本与依赖检查；生产资格须有独立评测证据"})
	return result, nil
}
func (m *CapabilityManifest) block(id, code, message string) {
	m.TrialReady = false
	m.Findings = append(m.Findings, CapabilityFinding{VersionID: id, Code: code, Message: message})
}
func (s *Capabilities) List(ctx context.Context, a domain.Actor, project, after string) ([]CapabilityVersion, string, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT v.id FROM capability_versions v WHERE v.project_id=? AND v.id>? ORDER BY v.id LIMIT 101", project, after)
	if err != nil {
		return nil, "", err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, "", err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > 100 {
		ids = ids[:100]
		next = ids[99]
	}
	items := []CapabilityVersion{}
	for _, id := range ids {
		v, readErr := capabilityRecordTx(ctx, tx, project, id, true)
		if readErr != nil {
			return nil, "", readErr
		}
		items = append(items, v)
	}
	return items, next, nil
}
func (s *Capabilities) Revoke(ctx context.Context, a domain.Actor, project, id, reason string) error {
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
	if _, err = capabilityTx(ctx, tx, project, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO capability_revocations(version_id,project_id,actor_id,reason) VALUES(?,?,?,?)", id, project, a.ID, reason); err != nil {
		return err
	}
	if err = captureDiagnosticKeyTx(ctx, tx, project, "capability_revocations", map[string]string{"version_id": id}); err != nil {
		return err
	}
	if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.CapabilityRevoked, ProjectID: project, ResourceID: id, ActorID: a.ID, Reason: reason}); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "capability.revoke", id); err != nil {
		return err
	}
	return tx.Commit()
}
