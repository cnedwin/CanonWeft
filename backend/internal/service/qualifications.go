package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/models"
	"novelstudio/internal/qualification"
	"os"
	"slices"
)

type Qualifications struct {
	IP          *IP
	Generations *Generations
	ToolTrials  *ToolTrials
	RuntimeRoot string
}

func (s *Qualifications) Suites(ctx context.Context, a domain.Actor, project string) ([]qualification.CatalogEntry, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	tx.Rollback()
	return qualification.Catalog(), nil
}

func (s *Qualifications) Report(ctx context.Context, a domain.Actor, project, id string) ([]byte, error) {
	r, err := s.Read(ctx, a, project, id)
	if err != nil {
		return nil, err
	}
	if r.ReportHash == "" {
		return nil, domain.ErrNotReady
	}
	// Keep the project lock through file IO. Read's earlier transaction has
	// ended, so its eligibility alone cannot authorize a later file read.
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := qualificationRunTx(ctx, tx, project, id)
	if err != nil {
		return nil, err
	}
	if current.ReportHash != r.ReportHash {
		return nil, domain.ErrConflict
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, project, r.ReportHash)
	if err != nil || digest(string(raw)) != r.ReportHash {
		return nil, domain.ErrNotReady
	}
	return raw, nil
}

type QualificationInput = qualification.QualificationInput

type QualificationFrozen = qualification.QualificationFrozen

type EvaluationRequestQuote = qualification.EvaluationRequestQuote

type QualificationQuote struct {
	Ready   bool                `json:"ready"`
	Reasons []string            `json:"reasons"`
	Frozen  QualificationFrozen `json:"frozen"`
	Calls   int                 `json:"calls"`
}

type EvaluationMetrics = qualification.EvaluationMetrics

type QualificationStep = qualification.QualificationStep

type QualificationRun struct {
	Retention       *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial []byte
	ID              string                              `json:"id"`
	ProjectID       string                              `json:"project_id"`
	BranchID        string                              `json:"branch_id"`
	ActorID         string                              `json:"-"`
	Frozen          QualificationFrozen                 `json:"frozen"`
	State           string                              `json:"state"`
	Reason          string                              `json:"reason"`
	Paused          bool                                `json:"paused"`
	Cancelled       bool                                `json:"cancelled"`
	ReportHash      string                              `json:"report_hash"`
	QualificationID string                              `json:"qualification_id"`
	Stale           bool                                `json:"stale"`
	Steps           []QualificationStep                 `json:"steps"`
	NovelSemantics  *qualification.NovelSemanticSummary `json:"novel_semantics,omitempty"`
}

const qualificationRunColumns = "id,project_id,branch_id,actor_id,frozen,state,reason,paused,cancelled,report_hash,qualification_id"

func scanQualificationRun(row interface{ Scan(...any) error }) (QualificationRun, error) {
	var r QualificationRun
	var raw []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.BranchID, &r.ActorID, &raw, &r.State, &r.Reason, &r.Paused, &r.Cancelled, &r.ReportHash, &r.QualificationID)
	if err == sql.ErrNoRows {
		return r, domain.ErrForbidden
	}
	if err != nil {
		return r, err
	}
	if err := rejectExpiredRuntime(raw, "qualification_runs.frozen"); err != nil {
		r.expiredMaterial = raw
		return r, err
	}
	if json.Unmarshal(raw, &r.Frozen) != nil {
		return r, domain.ErrNotReady
	}
	r.Steps = []QualificationStep{}
	return r, nil
}

func qualificationRunTx(ctx context.Context, tx *sql.Tx, project, id string) (QualificationRun, error) {
	run, err := scanQualificationRun(tx.QueryRowContext(ctx, "SELECT "+qualificationRunColumns+" FROM qualification_runs WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if err == nil {
		err = requireRuntimeObjectTx(ctx, tx, project, "qualification_runs", id, "report_hash", run.ReportHash)
	}
	return run, err
}

func (s *Qualifications) generationService() *Generations {
	if s.Generations != nil {
		return s.Generations
	}
	return &Generations{IP: s.IP, Adapter: models.New(nil)}
}
func (s *Qualifications) toolService() *ToolTrials {
	if s.ToolTrials != nil {
		return s.ToolTrials
	}
	root := s.RuntimeRoot
	if root == "" {
		root = os.Getenv("STUDIO_ISOLATION_ROOT")
	}
	return &ToolTrials{IP: s.IP, RuntimeRoot: root}
}

func bindingHeadTx(ctx context.Context, tx *sql.Tx, project, task string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT binding_id FROM capability_binding_heads WHERE project_id=? AND task_kind=? FOR UPDATE", project, task).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Qualifications) Quote(ctx context.Context, a domain.Actor, project string, input QualificationInput) (QualificationQuote, error) {
	q := QualificationQuote{Reasons: []string{}}
	suite, err := qualification.Lookup(input.SuiteID)
	if err != nil {
		return q, domain.ErrValidation
	}
	if input.MaxOutputTokens < 1 || input.MaxOutputTokens > 8192 {
		return q, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return q, err
	}
	defer tx.Rollback()
	branch, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return q, err
	}
	if branch.Head != input.ExpectedIPHead {
		return q, domain.ErrConflict
	}
	head, err := bindingHeadTx(ctx, tx, project, suite.Public().TaskKind)
	if err != nil {
		return q, err
	}
	if head != input.ExpectedBinding {
		return q, domain.ErrConflict
	}
	candidate, err := capabilityTx(ctx, tx, project, input.VersionID)
	if err != nil {
		return q, err
	}
	if !slices.Contains([]string{"agent", "skill", "tool"}, candidate.Definition.Kind) {
		return q, domain.ErrValidation
	}
	m, err := (&Capabilities{IP: s.IP}).manifestTx(ctx, tx, project, candidate.ID)
	if err != nil {
		return q, err
	}
	for _, finding := range m.Findings {
		if finding.Code == "REVOKED" {
			return q, domain.ErrConflict
		}
	}
	entry := suite.Public()
	if capabilityDigest(candidate.Definition.InputSchema) != capabilityDigest(entry.InputSchema) || capabilityDigest(candidate.Definition.OutputSchema) != capabilityDigest(entry.OutputSchema) {
		return q, domain.ErrValidation
	}
	input.OperationID = ""
	q.Frozen = QualificationFrozen{Input: input, Suite: entry, Candidate: m.Root, CandidateManifest: m.Hash, TokenProfile: "exact-max-output-tokens-v1"}
	var currentBaseline *CapabilityQualification
	q.Calls = len(entry.Cases) * entry.Repeats * 3
	if input.BaselineID == "" {
		q.Reasons = append(q.Reasons, "现有基线未配置：对照为 NOT_RUN，不能取得资格")
	} else {
		baseline, readErr := (&Capabilities{IP: s.IP}).manifestTx(ctx, tx, project, input.BaselineID)
		if readErr != nil {
			return q, readErr
		}
		if baseline.Root.Kind != candidate.Definition.Kind {
			return q, domain.ErrValidation
		}
		for _, finding := range baseline.Findings {
			if finding.Code == "REVOKED" {
				return q, domain.ErrConflict
			}
			if candidate.Definition.Kind != "tool" && finding.Code == "TOOL_QUALIFICATION_REQUIRED" {
				q.Reasons = append(q.Reasons, "现有模型基线需要尚未支持的工具组合；NOT_RUN，未发送请求")
			}
		}
		q.Frozen.Baseline = baseline.Root
		q.Frozen.BaselineManifest = baseline.Hash
		base, readErr := capabilityTx(ctx, tx, project, input.BaselineID)
		if readErr != nil {
			return q, readErr
		}
		if capabilityDigest(base.Definition.InputSchema) != capabilityDigest(entry.InputSchema) || capabilityDigest(base.Definition.OutputSchema) != capabilityDigest(entry.OutputSchema) {
			return q, domain.ErrValidation
		}
		if head != "" {
			binding, readErr := bindingTx(ctx, tx, project, head)
			if readErr != nil {
				return q, readErr
			}
			record, readErr := s.recordTx(ctx, tx, project, binding.QualificationID)
			if readErr != nil {
				return q, readErr
			}
			if record.Frozen.Candidate.ID != input.BaselineID {
				return q, domain.ErrConflict
			}
			currentBaseline = &record
		}
	}
	if candidate.Definition.Kind == "tool" {
		q.Frozen.RuntimeHash = isolation.ApprovedImageSHA256
		if currentBaseline != nil && currentBaseline.Frozen.RuntimeHash != q.Frozen.RuntimeHash {
			q.Reasons = append(q.Reasons, "当前用途绑定的冻结工具镜像不同：NOT_RUN。请先停用当前绑定，再明确选择初始基线与新运行环境重新评测")
		}
		var control string
		err = tx.QueryRowContext(ctx, "SELECT result_id FROM capability_preset_steps WHERE project_id=? AND step_key='qualification.v1.no-method.tool'", project).Scan(&control)
		if err == sql.ErrNoRows {
			q.Reasons = append(q.Reasons, "尚未准备可执行的无方法工具对照：NOT_RUN")
		} else if err != nil {
			return q, err
		} else {
			v, readErr := capabilityTx(ctx, tx, project, control)
			if readErr != nil {
				return q, readErr
			}
			q.Frozen.NoMethod = domain.CapabilityRef{ID: v.ID, Hash: v.Hash, Kind: "tool"}
		}
		if _, err = isolation.NewRunner(s.toolService().RuntimeRoot, isolation.ApprovedImageSHA256); err != nil {
			q.Reasons = append(q.Reasons, "隔离运行环境不可用；不能执行工具评测")
		}
	} else {
		if candidate.Definition.Kind == "agent" && candidate.Definition.Role != input.Role {
			return q, domain.ErrValidation
		}
		var connection string
		var revision int64
		err = tx.QueryRowContext(ctx, "SELECT connection_id,connection_revision FROM model_bindings WHERE workspace_id=? AND role=?", a.WorkspaceID, input.Role).Scan(&connection, &revision)
		if err == sql.ErrNoRows {
			q.Reasons = append(q.Reasons, "模型用途未绑定；未发送评测请求")
		} else if err != nil {
			return q, err
		} else {
			config, ref, readErr := modelTx(ctx, tx, a.WorkspaceID, connection)
			if readErr != nil {
				return q, readErr
			}
			q.Frozen.Config = config
			if currentBaseline != nil && (capabilityDigest(currentBaseline.Frozen.Config) != capabilityDigest(config) || currentBaseline.Frozen.Input.Role != input.Role || currentBaseline.Frozen.Input.MaxOutputTokens != input.MaxOutputTokens) {
				q.Reasons = append(q.Reasons, "当前用途绑定的冻结模型配置或精确输出档位不同：NOT_RUN。请先停用当前绑定，再明确选择初始基线与新配置进行完整评测")
			}
			if !config.Enabled || ref == "" || config.Revision != revision {
				q.Reasons = append(q.Reasons, "模型配置或凭据版本未就绪；未发送请求")
			}
			price, priceErr := priceTx(ctx, tx, a.WorkspaceID, input.PriceID)
			if priceErr != nil {
				return q, priceErr
			}
			q.Frozen.Price = price
			if price.Validate() != nil || price.ConnectionID != connection || price.ConnectionRevision != revision {
				return q, domain.ErrConflict
			}
			maxEstimate := int64(0)
			q.Frozen.QuoteProfile = "complete-request-v1"
			for _, arm := range []string{"no_method", "baseline", "candidate"} {
				for _, task := range entry.Cases {
					request, _, buildErr := (&Capabilities{IP: s.IP}).evaluationTaskRequestTx(ctx, tx, project, q.Frozen, arm, task.ID)
					if buildErr != nil {
						return q, buildErr
					}
					raw, encodeErr := json.Marshal(request)
					if encodeErr != nil || len(raw) > 1024*1024 {
						return q, domain.ErrValidation
					}
					estimate, quoteErr := price.Quote(len(raw), input.MaxOutputTokens)
					if quoteErr != nil {
						return q, quoteErr
					}
					q.Frozen.RequestQuotes = append(q.Frozen.RequestQuotes, EvaluationRequestQuote{Arm: arm, CaseID: task.ID, RequestHash: digest(string(raw)), InputBytes: len(raw), Estimate: estimate})
					q.Frozen.Estimate += estimate * int64(entry.Repeats)
					maxEstimate = max(maxEstimate, estimate)
				}
			}
			budget, budgetErr := budgetTx(ctx, tx, a.WorkspaceID)
			if budgetErr != nil {
				return q, budgetErr
			}
			if !budget.Enabled {
				q.Reasons = append(q.Reasons, "付费预算默认关闭；请在明确授权后开启专用预算")
			} else if budget.Currency != price.Currency || maxEstimate > budget.PerCall || q.Frozen.Estimate > budget.Limit-budget.Spent-budget.Reserved {
				q.Reasons = append(q.Reasons, "全部重复对照的可用预算不足；未发送请求")
			}
		}
		for _, v := range m.Versions {
			if v.Definition.Kind == "tool" {
				q.Reasons = append(q.Reasons, "模型评测尚不执行工具依赖；此对照为 NOT_RUN，不能取得资格")
			}
		}
		for _, finding := range m.Findings {
			if finding.Code == "TOOL_QUALIFICATION_REQUIRED" {
				q.Reasons = append(q.Reasons, "候选模型方法声明了尚未支持的工具组合；NOT_RUN，未发送请求")
			}
		}
	}
	q.Ready = len(q.Reasons) == 0
	if err = s.freezeWeeklyTx(ctx, tx, a, project, &q, input.Schedule, false); err != nil {
		return q, err
	}
	return q, nil
}

func (s *Qualifications) Create(ctx context.Context, a domain.Actor, project string, input QualificationInput) (QualificationRun, error) {
	var empty QualificationRun
	if !validName(input.OperationID, 120) {
		return empty, domain.ErrValidation
	}
	fingerprint := capabilityDigest([]any{"qualification.start", project, input, input.Schedule})
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return empty, err
	}
	var prior, id string
	prior, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		defer tx.Rollback()
		if prior != fingerprint {
			return empty, domain.ErrConflict
		}
		return qualificationRunTx(ctx, tx, project, id)
	}
	tx.Rollback()
	if err != sql.ErrNoRows {
		return empty, err
	}
	q, err := s.Quote(ctx, a, project, input)
	if err != nil {
		return empty, err
	}
	r := QualificationRun{ID: NewID(), ProjectID: project, BranchID: input.BranchID, ActorID: a.ID, Frozen: q.Frozen, State: "QUEUED", Steps: []QualificationStep{}}
	if !q.Ready {
		r.State = "BLOCKED"
		for _, reason := range q.Reasons {
			r.Reason += reason + "；"
		}
	}
	tx, a, _, err = s.IP.begin(ctx, a, project, true)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	prior, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if prior != fingerprint {
			return r, domain.ErrConflict
		}
		return qualificationRunTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return r, err
	}
	if err = s.freezeWeeklyTx(ctx, tx, a, project, &q, input.Schedule, true); err != nil {
		return r, err
	}
	r.Frozen = q.Frozen
	if !q.Ready {
		r.State = "BLOCKED"
		r.Reason = ""
		for _, reason := range q.Reasons {
			r.Reason += reason + "；"
		}
	}
	if q.Ready {
		if err = s.guardRunTx(ctx, tx, r, false); err != nil {
			return r, err
		}
	}
	raw, _ := json.Marshal(r.Frozen)
	if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_runs(id,project_id,branch_id,actor_id,frozen,state,reason) VALUES(?,?,?,?,?,?,?)", r.ID, project, input.BranchID, a.ID, raw, r.State, r.Reason); err != nil {
		return r, err
	}
	ordinal := 0
	for _, arm := range []string{"no_method", "baseline", "candidate"} {
		for _, c := range q.Frozen.Suite.Cases {
			for repeat := 0; repeat < q.Frozen.Suite.Repeats; repeat++ {
				step := QualificationStep{ID: NewID(), Ordinal: ordinal, Arm: arm, CaseID: c.ID, Repeat: repeat, State: "NOT_RUN"}
				metrics, _ := json.Marshal(step.Metrics)
				if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_steps(id,run_id,project_id,ordinal,arm,case_key,repeat_index,metrics) VALUES(?,?,?,?,?,?,?,?)", step.ID, r.ID, project, ordinal, arm, c.ID, repeat, metrics); err != nil {
					return r, err
				}
				r.Steps = append(r.Steps, step)
				ordinal++
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'qualification.start',?,?)", a.ID, input.OperationID, fingerprint, r.ID); err != nil {
		return r, err
	}
	if q.Ready {
		if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'qualification.start',?,'{}')", NewID(), project, r.ID); err != nil {
			return r, err
		}
	}
	if err = audit(ctx, tx, a, "qualification.start", r.ID); err != nil {
		return r, err
	}
	if err = s.IP.captureEvaluationCreationTx(ctx, tx, r); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *Qualifications) guardRunTx(ctx context.Context, tx *sql.Tx, r QualificationRun, terminal bool) error {
	if r.Cancelled || r.State == "CANCELLED" {
		return domain.ErrConflict
	}
	if !terminal {
		if err := validateWeeklyTx(ctx, tx, r, 0, false); err != nil {
			return err
		}
	}
	suite, err := qualification.Lookup(r.Frozen.Suite.ID)
	if err != nil || suite.Public().Hash != r.Frozen.Suite.Hash {
		return domain.ErrConflict
	}
	b, err := branchTx(ctx, tx, r.ProjectID, r.BranchID)
	if err != nil {
		return err
	}
	if b.Head != r.Frozen.Input.ExpectedIPHead {
		return domain.ErrConflict
	}
	head, err := bindingHeadTx(ctx, tx, r.ProjectID, r.Frozen.Suite.TaskKind)
	if err != nil {
		return err
	}
	if head != r.Frozen.Input.ExpectedBinding {
		return domain.ErrConflict
	}
	for _, entry := range []struct {
		ref  domain.CapabilityRef
		hash string
	}{{r.Frozen.Candidate, r.Frozen.CandidateManifest}, {r.Frozen.Baseline, r.Frozen.BaselineManifest}} {
		if entry.ref.ID == "" {
			return domain.ErrNotReady
		}
		m, readErr := (&Capabilities{IP: s.IP}).manifestTx(ctx, tx, r.ProjectID, entry.ref.ID)
		if readErr != nil {
			return readErr
		}
		if m.Hash != entry.hash || m.Root != entry.ref {
			return domain.ErrConflict
		}
		for _, f := range m.Findings {
			if f.Code == "REVOKED" {
				return domain.ErrConflict
			}
		}
	}
	if r.Frozen.Candidate.Kind == "tool" {
		if r.Frozen.RuntimeHash != isolation.ApprovedImageSHA256 || r.Frozen.NoMethod.ID == "" {
			return domain.ErrConflict
		}
		available, readErr := capabilityAvailable(ctx, tx, r.ProjectID, r.Frozen.NoMethod.ID)
		if readErr != nil {
			return readErr
		}
		if !available {
			return domain.ErrConflict
		}
		return nil
	}
	actor, err := s.IP.Workspace.actorTx(ctx, tx, r.ActorID)
	if err != nil {
		return err
	}
	config, _, err := modelTx(ctx, tx, actor.WorkspaceID, r.Frozen.Config.ID)
	if err != nil {
		return err
	}
	if !config.Enabled || capabilityDigest(config) != capabilityDigest(r.Frozen.Config) {
		return domain.ErrConflict
	}
	var connection string
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT connection_id,connection_revision FROM model_bindings WHERE workspace_id=? AND role=?", actor.WorkspaceID, r.Frozen.Input.Role).Scan(&connection, &revision)
	if err != nil {
		return err
	}
	if connection != config.ID || revision != config.Revision {
		return domain.ErrConflict
	}
	if !terminal && r.Frozen.Price.Validate() != nil {
		return domain.ErrConflict
	}
	return nil
}

func scanQualificationSteps(ctx context.Context, tx *sql.Tx, run string) ([]QualificationStep, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,ordinal,arm,case_key,repeat_index,child_id,child_kind,state,metrics FROM qualification_steps WHERE run_id=? ORDER BY ordinal", run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QualificationStep{}
	for rows.Next() {
		var step QualificationStep
		var raw []byte
		if err = rows.Scan(&step.ID, &step.Ordinal, &step.Arm, &step.CaseID, &step.Repeat, &step.ChildID, &step.ChildKind, &step.State, &raw); err != nil {
			return nil, err
		}
		if err := rejectExpiredRuntime(raw, "qualification_steps.metrics"); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &step.Metrics) != nil {
			return nil, domain.ErrNotReady
		}
		items = append(items, step)
	}
	return items, rows.Err()
}

func (s *Qualifications) Read(ctx context.Context, a domain.Actor, project, id string) (QualificationRun, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return QualificationRun{}, err
	}
	defer tx.Rollback()
	r, err := qualificationRunTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if a.ID != r.ActorID && a.Role != "owner" && a.Role != "publisher" {
		return r, domain.ErrForbidden
	}
	r.Steps, err = scanQualificationSteps(ctx, tx, id)
	if err != nil {
		return r, err
	}
	if check := s.guardRunTx(ctx, tx, r, true); check != nil {
		if !toolTrialGuardFailure(check) && !errors.Is(check, sql.ErrNoRows) {
			return r, check
		}
		r.Stale = true
	}
	if r.Frozen.Suite.ID == qualification.NovelSemanticSuiteID {
		suite, lookupErr := qualification.Lookup(r.Frozen.Suite.ID)
		if lookupErr == nil && suite.Public().Hash == r.Frozen.Suite.Hash {
			r.NovelSemantics = suite.NovelSemanticMetrics(r.Steps)
		}
	}
	return r, nil
}

func (s *Qualifications) List(ctx context.Context, a domain.Actor, project string) ([]QualificationRun, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+qualificationRunColumns+" FROM qualification_runs WHERE project_id=? AND (actor_id=? OR ? IN ('owner','publisher')) ORDER BY created_at DESC,id LIMIT 100", project, a.ID, a.Role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []QualificationRun{}
	for rows.Next() {
		r, readErr := scanQualificationRun(rows)
		if readErr != nil && !errors.Is(readErr, domain.ErrMaterialExpired) {
			return nil, readErr
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		r := &items[i]
		if len(r.expiredMaterial) == 0 {
			continue
		}
		marker, err := runtimeRetentionTx(ctx, tx, project, "qualification_runs", r.ID, "", r.expiredMaterial)
		if err != nil {
			return nil, err
		}
		*r = QualificationRun{ID: r.ID, ProjectID: project, BranchID: r.BranchID, State: r.State, Retention: marker, Steps: []QualificationStep{}}
	}
	return items, nil
}

func (s *Qualifications) Control(ctx context.Context, a domain.Actor, project, id, action string) (QualificationRun, error) {
	if !slices.Contains([]string{"pause", "resume", "cancel"}, action) {
		return QualificationRun{}, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return QualificationRun{}, err
	}
	defer tx.Rollback()
	r, err := qualificationRunTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if a.ID != r.ActorID && a.Role != "owner" {
		return r, domain.ErrForbidden
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING", "PAUSED"}, r.State) {
		return r, nil
	}
	if action == "cancel" {
		r.Cancelled = true
		r.Paused = false
		r.State = "CANCELLED"
		r.Reason = "已取消；保留费用和已有证据，不采用晚到结果"
	} else {
		r.Paused = action == "pause"
		if r.Paused {
			r.State = "PAUSED"
		} else {
			if err = s.guardRunTx(ctx, tx, r, false); err != nil {
				return r, err
			}
			r.State = "QUEUED"
		}
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "qualification_runs", id); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET state=?,reason=?,paused=?,cancelled=? WHERE id=?", r.State, r.Reason, r.Paused, r.Cancelled, id); err != nil {
		return r, err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "qualification_runs", id); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "qualification."+action, id); err != nil {
		return r, err
	}
	if action == "cancel" {
		if _, err = tx.ExecContext(ctx, "UPDATE generation_tasks g JOIN qualification_steps s ON s.child_id=g.id SET g.cancel_requested=TRUE,g.paused=FALSE WHERE s.run_id=? AND s.child_kind='generation'", id); err != nil {
			return r, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE model_calls c JOIN generation_tasks g ON g.call_id=c.id JOIN qualification_steps s ON s.child_id=g.id SET c.cancel_requested=TRUE WHERE s.run_id=? AND s.child_kind='generation'", id); err != nil {
			return r, err
		}
		if err = qualificationReceiptOutboxTx(ctx, tx, project, id); err != nil {
			return r, err
		}
	}
	return r, tx.Commit()
}
