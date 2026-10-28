package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestRetentionCoverageTracksActualBusinessSchema(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../backup/catalog.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	var layouts []string
	ast.Inspect(file, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || value.Names[0].Name != "tableCatalog" {
			return true
		}
		catalog := value.Values[0].(*ast.CompositeLit)
		for _, entry := range catalog.Elts {
			row := entry.(*ast.CompositeLit)
			name, _ := strconv.Unquote(row.Elts[0].(*ast.BasicLit).Value)
			columns, _ := strconv.Unquote(row.Elts[1].(*ast.BasicLit).Value)
			layouts = append(layouts, name+":"+columns)
			tables = append(tables, name)
		}
		return false
	})
	got := KnownTables()
	sort.Strings(got)
	sort.Strings(tables)
	if len(tables) == 0 || !slices.Equal(got, tables) {
		t.Fatalf("retention scanner catalog drift: declared=%v actual=%v", got, tables)
	}
	sort.Strings(layouts)
	hash := sha256.Sum256([]byte(strings.Join(layouts, "\n")))
	if hex.EncodeToString(hash[:]) != "788bc4f032b161dd973fa470ff657c499ad12ee3461579bbb44f525d2576f47e" {
		t.Fatal("business field layout changed: review retention rules before accepting the new schema")
	}
	seen := map[string]bool{}
	for _, rule := range Rules() {
		// The tableCatalog above is the frozen legacy archive layout. Current
		// rules also cover the explicitly registered schema37/38 novel tables.
		if seen[rule.Key] || !slices.Contains(KnownTablesForSchema(38), strings.Split(rule.Key, ".")[0]) {
			t.Fatal("duplicate or unknown reference rule", rule)
		}
		seen[rule.Key] = true
	}
}

func TestWorkingIndexCannotKeepExpiredRuntimeCopiesAlive(t *testing.T) {
	for _, key := range []string{"working_chunks.content", "working_chunks.vector"} {
		rule, ok := lookupRule(key)
		if !ok || rule.Class != RawRun || rule.Promotable {
			t.Fatal("derived working index can retain expired preview/input text", key)
		}
	}
}

func TestCatalogClassifiesActualBlobReferencesAndInlineRawCopies(t *testing.T) {
	// Explicit fields independently inventoried from backup.blobReferences and
	// execution/report writers. An accounting fingerprint is not a blob.
	for _, key := range strings.Fields(`ip_revisions.content_hash resources.content_hash artifact_releases.content_hash artifact_releases.manifest.artifacts.hash artifact_releases.manifest.artifacts.original_hash source_imports.source_hash skill_packages.bundle_hash skill_packages.source.archive_hash generation_tasks.frozen.input_hash tool_trials.frozen.input_hash tool_trials.output_hash model_calls.output_hash model_call_receipts.payload.output_hash knowledge_chunks.source_hash platform_tasks.input_hash qualification_runs.report_hash capability_qualifications.report_hash semantic_review_runs.frozen.input_hash semantic_review_runs.report_hash semantic_review_acceptances.report_hash`) {
		rule, ok := lookupRule(key)
		if !ok || rule.Storage != Blob {
			t.Fatal("actual blob field missing from retention catalog", key)
		}
	}
	for _, key := range strings.Fields(`generation_tasks.frozen generation_previews.content qualification_steps.metrics qualification_runs.frozen capability_qualifications.payload qualification_approvals.payload tool_trials.frozen semantic_review_runs.frozen context_packages.payload model_reconciliations.payload`) {
		rule, ok := lookupRule(key)
		if !ok || rule.Storage != Inline || rule.Class != RawRun || rule.Promotable {
			t.Fatal("raw inline copy lacks mandatory expiry", key)
		}
	}
	if _, ok := lookupRule("model_calls.input_hash"); ok {
		t.Fatal("accounting fingerprint mistaken for stored bytes")
	}
}
