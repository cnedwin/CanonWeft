package retention

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestColumnClassificationCoversEveryActualBusinessColumnExactlyOnce(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../backup/catalog.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tables, total := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != "tableCatalog" {
			return true
		}
		for _, entry := range value.Values[0].(*ast.CompositeLit).Elts {
			row := entry.(*ast.CompositeLit)
			table, _ := strconv.Unquote(row.Elts[0].(*ast.BasicLit).Value)
			raw, _ := strconv.Unquote(row.Elts[1].(*ast.BasicLit).Value)
			want := strings.Split(raw, ",")
			contract, ok := OwnerSpec(table)
			if !ok || contract.Scope == "" || len(contract.Columns) != len(want) {
				t.Fatalf("missing owner column contract: %s", table)
			}
			var got []string
			seen := map[string]bool{}
			for _, column := range contract.Columns {
				if seen[column.Column] || !column.Valid() {
					t.Fatalf("invalid/duplicate classification %s.%s: %+v", table, column.Column, column)
				}
				seen[column.Column] = true
				got = append(got, column.Column)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("field coverage drift %s got=%v want=%v", table, got, want)
			}
			tables++
			total += len(got)
		}
		return false
	})
	if tables != 71 || total != 579 {
		t.Fatalf("expected reviewed schema35 71/579, got %d/%d", tables, total)
	}
	if _, ok := OwnerSpec("future_private_table"); ok {
		t.Fatal("unknown table treated as safe")
	}
}

func TestSensitiveColumnsAreNotMisclassifiedAsSafeMetadata(t *testing.T) {
	cases := map[string]Handling{
		"projects.name": ClearText, "branches.name": AnonymizationRequired, "resources.name": ClearText,
		"source_imports.filename": ClearText, "knowledge_chunks.resource_name": ClearText, "working_chunks.source_name": ClearText,
		"ip_amendments.reason": ClearText, "skill_revocations.reason": ClearText, "capability_revocations.reason": ClearText,
		"skill_discovery_sources.keyword": ClearText, "skill_discovery_runs.source_snapshot": DerivedProjection,
		"qualification_schedules.config": FinancialProjection, "qualification_schedules.reason": ClearText,
		"model_calls.provider_receipt": ClearText, "model_call_receipts.payload": FinancialProjection,
		"model_reconciliations.payload": FinancialProjection, "outbox.payload": DerivedProjection, "working_indexes.manifest": DerivedProjection,
		"operations.operation_id": AnonymizationRequired, "capability_heads.capability_key": AnonymizationRequired, "skill_discovery_candidates.source_path": AnonymizationRequired,
		"generation_tasks.credential_ref": CredentialRevocationRequired, "users.username": WorkspaceLifecycleRequired,
	}
	for key, want := range cases {
		table, column, _ := strings.Cut(key, ".")
		p, ok := Column(table, column)
		if !ok || p.Privacy == Structural || p.Handling != want {
			t.Fatalf("unsafe classification %s: %+v", key, p)
		}
	}
	for _, key := range []string{"working_indexes.manifest", "working_chunks.content", "working_chunks.vector", "generation_previews.content", "qualification_steps.metrics"} {
		table, column, _ := strings.Cut(key, ".")
		p, _ := Column(table, column)
		if p.Class != RawRun {
			t.Fatal("runtime copy can evade raw lifetime", key, p)
		}
	}
	for _, key := range []string{"patch_drafts.payload", "ip_drafts.payload", "resource_drafts.body", "resource_policy_drafts.payload", "capability_versions.capability_key", "capability_heads.capability_key"} {
		table, column, _ := strings.Cut(key, ".")
		p, _ := Column(table, column)
		if p.Class != Content {
			t.Fatal("user draft mistaken for expiring run", key, p)
		}
	}
}

func TestOwnerColumnContractRequiresExactColumnsAndDoesNotAuthorizeCleanup(t *testing.T) {
	contract, _ := OwnerSpec("generation_previews")
	names := []string{"generation_id", "content", "sequence", "content_hash"}
	if ValidateColumnCoverage("generation_previews", names) != nil {
		t.Fatal("exact coverage refused")
	}
	for _, bad := range [][]string{names[:3], append(slices.Clone(names), "new_secret"), append(slices.Clone(names), "content")} {
		if ValidateColumnCoverage("generation_previews", bad) == nil {
			t.Fatal("partial/unknown/duplicate coverage accepted")
		}
	}
	contract.Columns[0].Privacy = PrivateBody
	again, _ := OwnerSpec("generation_previews")
	if again.Columns[0].Privacy == PrivateBody {
		t.Fatal("caller mutated fixed classification")
	}
	if _, ok := Column("generation_previews", "new_secret"); ok {
		t.Fatal("unknown column silently classified")
	}
	scope, _ := OwnerSpec("operations")
	if scope.Scope != ScopeOperationResult {
		t.Fatal("operations incorrectly scoped by actor")
	}
	scope, _ = OwnerSpec("model_connection_versions")
	if scope.Scope != ScopeWorkspace {
		t.Fatal("shared config incorrectly belongs to one project")
	}
}

func TestClassifiedRuntimeCopiesCannotBePromotedByReferenceCatalog(t *testing.T) {
	for _, table := range KnownTables() {
		owner, _ := OwnerSpec(table)
		for _, column := range owner.Columns {
			if column.Class != RawRun {
				continue
			}
			rule, exists := Rule(table + "." + column.Column)
			if exists && (rule.Class != RawRun || rule.Promotable) {
				t.Fatal("reference catalog can retain classified runtime copy", rule)
			}
		}
	}
}
