package retention

import (
	"errors"
	"maps"
	"slices"
	"sort"
)

// SourceRow contains original SQL values, never an ownership assertion from a
// saved cleanup plan. Nulls remains distinct from an empty string for hashing.
type SourceRow struct {
	Table  string
	Values map[string]string
	Nulls  map[string]bool
}

type SourceOwner struct {
	ProjectID   string
	WorkspaceID string
	Shared      bool
}

type SourceIndex struct {
	rows map[string]map[string]SourceRow
	// Installed only after SQL journal/evidence and actual source validation.
	expired              map[string]ReferenceVersion
	expiryProofs         map[string]ExpiredProjection
	replacedSources      map[string]ReferenceVersion
	preservedAfterExpiry []ObservedMaterial
	// Immutable candidate hashes can outlive erased auxiliary text. Reuse only
	// independently registered metadata, never synthesize the missing candidate.
	partialCandidates map[string]ReferenceVersion
	// Only bounded transaction-local capture installs this loader. Full
	// inventories and archive validators remain eagerly populated snapshots.
	loadSource func(string, map[string]string) (SourceRow, error)
}

var ErrSourceOwnership = errors.New("source ownership is incomplete, conflicting or unsupported")

// NewSourceIndex freezes the reviewed columns and canonical primary keys. This
// proves neither a retention deadline nor permission to erase any source row.
func NewSourceIndex(rows []SourceRow) (*SourceIndex, error) {
	if len(rows) > 250000 {
		return nil, ErrSourceOwnership
	}
	index := &SourceIndex{rows: map[string]map[string]SourceRow{}}
	for _, table := range KnownTablesForSchema(38) {
		index.rows[table] = map[string]SourceRow{}
	}
	bytes := 0
	for _, row := range rows {
		columns := make([]string, 0, len(row.Values))
		for name, value := range row.Values {
			columns = append(columns, name)
			bytes += len(name) + len(value)
		}
		if bytes > 64<<20 || ValidateColumnCoverage(row.Table, columns) != nil {
			return nil, ErrSourceOwnership
		}
		for name, isNull := range row.Nulls {
			value, exists := row.Values[name]
			if !exists || !isNull || value != "" {
				return nil, ErrSourceOwnership
			}
		}
		key, err := RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return nil, ErrSourceOwnership
		}
		if _, exists := index.rows[row.Table][key]; exists {
			return nil, ErrSourceOwnership
		}
		index.rows[row.Table][key] = SourceRow{Table: row.Table, Values: maps.Clone(row.Values), Nulls: maps.Clone(row.Nulls)}
	}
	return index, nil
}

func (index *SourceIndex) Owner(table string, primary map[string]string) (SourceOwner, error) {
	if _, err := index.sourceRow(table, primary); err != nil {
		return SourceOwner{}, err
	}
	key, err := RowOwnerKey(table, primary)
	if err != nil {
		return SourceOwner{}, ErrSourceOwnership
	}
	return index.owner(table, key, map[string]bool{})
}

func (index *SourceIndex) Records() []SourceRow {
	var rows []SourceRow
	for _, table := range KnownTablesForSchema(38) {
		keys := make([]string, 0, len(index.rows[table]))
		for key := range index.rows[table] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			row := index.rows[table][key]
			rows = append(rows, SourceRow{Table: table, Values: maps.Clone(row.Values), Nulls: maps.Clone(row.Nulls)})
		}
	}
	return rows
}

// Record returns a defensive copy of a classified source row. NULL flags are
// retained so an executor can distinguish NULL from empty text in its compare
// and swap hash. Returning a row grants no permission to change or erase it.
func (index *SourceIndex) Record(table string, primary map[string]string) (SourceRow, error) {
	row, err := index.sourceRow(table, primary)
	if err != nil {
		return SourceRow{}, err
	}
	return SourceRow{Table: row.Table, Values: maps.Clone(row.Values), Nulls: maps.Clone(row.Nulls)}, nil
}

func (index *SourceIndex) single(table, id string, path map[string]bool) (SourceOwner, error) {
	primary := PrimaryKey(table)
	if len(primary) != 1 || id == "" {
		return SourceOwner{}, ErrSourceOwnership
	}
	key, err := RowOwnerKey(table, map[string]string{primary[0].Name: id})
	if err != nil {
		return SourceOwner{}, ErrSourceOwnership
	}
	if _, err := index.sourceRow(table, map[string]string{primary[0].Name: id}); err != nil {
		return SourceOwner{}, err
	}
	return index.owner(table, key, path)
}

func (index *SourceIndex) owner(table, key string, path map[string]bool) (SourceOwner, error) {
	identity := table + ":" + key
	if index == nil || path[identity] {
		return SourceOwner{}, ErrSourceOwnership
	}
	row, exists := index.rows[table][key]
	if !exists {
		return SourceOwner{}, ErrSourceOwnership
	}
	spec, ok := OwnerSpec(table)
	if !ok {
		return SourceOwner{}, ErrSourceOwnership
	}
	path[identity] = true
	defer delete(path, identity)
	value := func(name string) string { return row.Values[name] }
	via := func(parent, column string) (SourceOwner, error) { return index.single(parent, value(column), path) }
	direct := func() (SourceOwner, error) { return via("projects", "project_id") }
	var owner SourceOwner
	var err error
	switch spec.Scope {
	case ScopeNovelSetup:
		if value("confirmed_novel_id") == "" {
			owner, err = via("workspaces", "workspace_id")
		} else {
			owner, err = via("novels", "confirmed_novel_id")
		}
	case ScopeNovel:
		owner, err = via("novels", "novel_id")
	case ScopeWorkspace:
		switch table {
		case "workspaces":
			owner = SourceOwner{WorkspaceID: value("id"), Shared: true}
		case "sessions":
			owner, err = via("users", "user_id")
		case "model_connection_versions":
			owner, err = via("model_connections", "connection_id")
		default:
			owner, err = via("workspaces", "workspace_id")
		}
	case ScopeProjectSelf:
		owner, err = via("workspaces", "workspace_id")
		owner.ProjectID = value("id")
		owner.Shared = false
	case ScopeDirect, ScopeDirectBranch, ScopeDirectImport, ScopeDirectRecipe, ScopeDirectCandidate, ScopeDirectRun, ScopeDirectRunCall:
		owner, err = direct()
	case ScopeBranch:
		owner, err = via("branches", "branch_id")
	case ScopeCandidate:
		owner, err = via("ip_revisions", "candidate_id")
	case ScopeResource:
		owner, err = via("resources", "resource_id")
	case ScopeKnowledgeIndex:
		owner, err = via("knowledge_indexes", "index_id")
	case ScopeWorkingIndex:
		owner, err = via("working_indexes", "index_id")
	case ScopeGeneration:
		owner, err = via("generation_tasks", "generation_id")
	case ScopeImport:
		owner, err = via("source_imports", "import_id")
	case ScopeCall:
		owner, err = via("model_calls", "call_id")
	case ScopePlan:
		owner, err = via("change_plans", "plan_id")
	case ScopeOperationResult:
		target := operationOwnerTable(value("kind"))
		if target == "" {
			return SourceOwner{}, ErrSourceOwnership
		}
		owner, err = via(target, "result_id")
	case ScopeAuditResource:
		target := auditOwnerTable(value("action"))
		if target == "" {
			return SourceOwner{}, ErrSourceOwnership
		}
		if target == "workspace" {
			if value("resource_id") != "" {
				return SourceOwner{}, ErrSourceOwnership
			}
			owner, err = via("workspaces", "workspace_id")
		} else {
			owner, err = via(target, "resource_id")
		}
	default:
		return SourceOwner{}, ErrSourceOwnership
	}
	if err != nil || !id32.MatchString(owner.WorkspaceID) || (!owner.Shared && !id32.MatchString(owner.ProjectID)) {
		return SourceOwner{}, ErrSourceOwnership
	}
	required := map[OwnerScope][]string{
		ScopeDirectBranch: {"branch_id"}, ScopeDirectImport: {"import_id"}, ScopeDirectCandidate: {"candidate_id"},
		ScopeDirectRecipe: {"run_id"}, ScopeDirectRun: {"run_id"}, ScopeDirectRunCall: {"run_id", "call_id"},
	}
	for _, column := range required[spec.Scope] {
		if value(column) == "" {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if workspace := value("workspace_id"); workspace != "" && workspace != owner.WorkspaceID {
		return SourceOwner{}, ErrSourceOwnership
	}
	if actor := value("actor_id"); actor != "" {
		actorOwner, err := index.single("users", actor, path)
		if err != nil || actorOwner.WorkspaceID != owner.WorkspaceID {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	// These concrete columns have one fixed meaning across the reviewed tables.
	// Other contextual IDs are handled explicitly, never guessed by suffix.
	for column, parent := range map[string]string{"branch_id": "branches", "candidate_id": "ip_revisions", "resource_id": "resources", "import_id": "source_imports", "call_id": "model_calls", "plan_id": "change_plans", "generation_id": "generation_tasks", "release_id": "artifact_releases"} {
		if (table == "audit_events" || table == "outbox") && column == "resource_id" {
			continue
		}
		if value(column) == "" {
			continue
		}
		related, err := via(parent, column)
		if err != nil || related.ProjectID != owner.ProjectID || related.WorkspaceID != owner.WorkspaceID {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if table == "outbox" {
		target := map[string]string{
			"task.start": "platform_tasks", "generation.start": "generation_tasks", "index.start": "index_jobs",
			"discovery.start": "skill_discovery_runs", "discovery.schedule": "skill_discovery_sources",
			"tool.start": "tool_trials", "qualification.start": "qualification_runs", "semantic.review.start": "semantic_review_runs",
			"recipe.start": "recipe_runs", "recipe.retry": "recipe_runs", "ip.published": "ip_revisions", "release.published": "artifact_releases", "ingest.start": "ingest_jobs", "research.step": "resources",
		}[value("kind")]
		var related SourceOwner
		if target != "" {
			related, err = via(target, "resource_id")
		} else if value("kind") == "qualification.schedule" || value("kind") == "qualification.receipt" {
			// These messages use their event ID as resource_id; their one explicit
			// payload field names the actual subject. Do not guess an ID table.
			if value("resource_id") != value("id") {
				return SourceOwner{}, ErrSourceOwnership
			}
			subject, e := eventOwnershipSubject(row)
			if e != nil {
				return SourceOwner{}, ErrSourceOwnership
			}
			parent := "qualification_runs"
			if value("kind") == "qualification.schedule" {
				parent = "qualification_schedules"
			}
			related, err = index.single(parent, subject, path)
		} else {
			return SourceOwner{}, ErrSourceOwnership
		}
		if err != nil || related != owner {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	for _, column := range []string{"user_id", "created_by", "adopted_by", "approved_by"} {
		if value(column) == "" {
			continue
		}
		related, err := via("users", column)
		if err != nil || related.WorkspaceID != owner.WorkspaceID {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if spec.Scope == ScopeDirectRecipe || spec.Scope == ScopeDirectRun || spec.Scope == ScopeDirectRunCall {
		parent := "qualification_runs"
		if spec.Scope == ScopeDirectRecipe {
			parent = "recipe_runs"
		}
		related, err := via(parent, "run_id")
		if err != nil || related != owner {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if value("parent_id") != "" && slices.Contains([]string{"branches", "skill_packages", "capability_versions"}, table) {
		related, err := via(table, "parent_id")
		if err != nil || related != owner {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if err := index.validateSourceLinks(row, owner, path); err != nil {
		return SourceOwner{}, err
	}
	return owner, nil
}

func operationOwnerTable(kind string) string {
	return map[string]string{
		"novel.create":           "novels",
		"novel.settings.confirm": "novels",
		"project.create":         "projects", "project.delete": "projects", "project.recover": "projects", "branch.create": "branches", "ip.propose": "ip_revisions",
		"capability.save": "capability_versions", "change.plan": "change_plans", "index.job": "index_jobs", "qualification.start": "qualification_runs",
		"qualification.bind": "capability_bindings", "model.reconcile": "model_reconciliations", "recipe.retry": "recipe_runs", "resource.create": "resources",
		"release.prepare": "artifact_releases", "skill.save": "skill_packages", "discovery.source": "skill_discovery_sources", "discovery.run": "skill_discovery_runs",
		"release.joint-publish": "artifact_releases", "release.joint-member": "artifact_releases", "release.joint-restored": "artifact_releases",
		"tool.trial": "tool_trials", "task.review": "platform_tasks",
		"memory.update": "memory_items", "memory.session": "memory_sessions",
	}[kind]
}

func auditOwnerTable(action string) string {
	groups := map[string][]string{
		"novel_setup_drafts": {"novel.setup.save"}, "novels": {"novel.create", "novel.settings.save", "novel.settings.confirm"},
		// The local account-recovery tool records the reset user as its target.
		// Resolve the actual user and workspace, as for other identity audits.
		"workspaces": {"budget.configure"}, "workspace": {"diagnostics.export"}, "users": {"member.create", "local_password_reset"},
		"model_connections": {"model.configure", "model.bind.architect", "model.bind.planner", "model.bind.writer", "model.bind.bookkeeper", "model.bind.critic", "model.bind.researcher", "model.bind.character_designer", "model.bind.continuity_editor", "model.bind.style_editor", "model.bind.asset_designer", "model.bind.image_generator", "model.bind.import_reader"},
		"model_prices":      {"model.price"}, "model_calls": {"model.cancel"}, "model_reconciliations": {"model.reconcile"},
		"projects": {"project.create", "project.rename", "project.delete", "project.recover", "project.membership"}, "branches": {"branch.create", "knowledge.index", "knowledge.private_index"},
		"ip_revisions": {"ip.propose", "ip.adopt", "ip.publish"}, "artifact_releases": {"release.prepare", "release.adopt", "release.publish", "release.joint-publish"}, "resources": {"resource.create", "character.design.apply", "name.catalog.save"},
		"capability_versions": {"capability.candidate", "capability.revoke"}, "change_plans": {"change.plan"},
		"generation_tasks": {"generation.prepare", "generation.start", "generation.pause", "generation.resume", "generation.cancel"},
		"source_imports":   {"source.import"}, "ingest_jobs": {"source.ingest", "source.ingest.pause", "source.ingest.resume", "source.ingest.cancel"},
		"index_jobs": {"index.job", "index.cancel"}, "qualification_runs": {"qualification.start", "qualification.pause", "qualification.resume", "qualification.cancel"},
		"capability_qualifications": {"qualification.mechanical.passed"},
		"capability_bindings":       {"qualification.bind", "qualification.binding.disable"}, "qualification_schedules": {"qualification.schedule.configure"},
		"recipe_runs":             {"recipe.prepare", "recipe.start", "recipe.pause", "recipe.resume", "recipe.cancel", "recipe.confirm", "recipe.retry"},
		"semantic_review_runs":    {"semantic.review.prepare", "semantic.review.start", "semantic.review.pause", "semantic.review.resume", "semantic.review.cancel", "semantic.review.select"},
		"skill_packages":          {"skill.candidate", "skill.reference", "skill.revoke", "discovery.candidate"},
		"skill_discovery_sources": {"discovery.source"}, "skill_discovery_runs": {"discovery.run", "discovery.pause", "discovery.resume", "discovery.cancel"},
		"platform_tasks": {"task.create", "task.pause", "task.resume", "task.cancel", "task.refresh"}, "tool_trials": {"tool.trial", "tool.cancel", "tool.result"},
		"memory_items": {"memory.update"}, "memory_sessions": {"memory.session"},
	}
	for table, actions := range groups {
		if slices.Contains(actions, action) {
			return table
		}
	}
	return ""
}
