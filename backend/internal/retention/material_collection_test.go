package retention

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestMaterialCollectionBindsNestedBlobAndPreservesAuthorWork(t *testing.T) {
	rows := authorContentFixture()
	project := strings.Repeat("2", 32)
	rows = append(rows, inventoryRow("generation_tasks", map[string]string{
		"id": strings.Repeat("c", 32), "project_id": project, "workspace_id": strings.Repeat("1", 32), "branch_id": strings.Repeat("5", 32), "actor_id": strings.Repeat("4", 32),
		"input_hash": strings.Repeat("e", 64), "frozen": `{"input_hash":"` + strings.Repeat("d", 64) + `"}`, "operation_id": "PRIVATE_REQUEST_TEXT",
	}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.MaterialReferences(project)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]MaterialReference{}
	for _, ref := range refs {
		found[ref.Table+"."+ref.Field] = ref
	}
	for _, required := range []string{"projects.name", "branches.name", "operations.operation_id", "ip_drafts.payload", "skill_packages.source.archive_hash", "generation_tasks.frozen.input_hash", "generation_tasks.operation_id"} {
		if _, ok := found[required]; !ok {
			t.Error("missing material", required)
		}
	}
	frozen, nested := found["generation_tasks.frozen"], found["generation_tasks.frozen.input_hash"]
	if nested.ObjectHash != strings.Repeat("d", 64) || nested.SourceID != frozen.SourceID || nested.SourcePayloadHash != frozen.PayloadHash || nested.PayloadHash != financialDigest([]byte(nested.ObjectHash)) || nested.ContentRoot {
		t.Fatal("nested object lost source binding or became permanent", nested)
	}
	if !found["ip_drafts.payload"].ContentRoot || !found["skill_packages.source.archive_hash"].ContentRoot {
		t.Fatal("author preservation lost")
	}
	if _, ok := found["generation_tasks.input_hash"]; ok {
		t.Fatal("request fingerprint treated as a blob")
	}
	raw, _ := json.Marshal(refs)
	for _, private := range []string{"PRIVATE_REQUEST_TEXT", "unfinished", "作者草稿", "instructions"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("material descriptors contain private source bytes")
		}
	}
}

func TestMaterialCollectionRejectsHiddenFrozenReferences(t *testing.T) {
	valid := `{"input_hash":"` + strings.Repeat("d", 64) + `"}`
	for _, raw := range []string{
		`{"input_hash":"../file"}`, `{"input_hash":null}`, `null`, `[]`,
		`{"Input_Hash":"` + strings.Repeat("d", 64) + `"}`,
		strings.TrimSuffix(valid, "}") + `,"input_hash":"` + strings.Repeat("e", 64) + `"}`,
		strings.TrimSuffix(valid, "}") + `,"unexpected_blob":"` + strings.Repeat("e", 64) + `"}`,
		valid + `{}`, `{"schema":1,"state":"EXPIRED"}`,
	} {
		if _, err := materialFrozenInput("generation_tasks", raw); err == nil {
			t.Fatal("malformed or unsupported frozen source accepted")
		}
	}
}

func TestMaterialCollectionFrozenSelectorsTrackWriterContracts(t *testing.T) {
	for table, target := range map[string]struct{ file, name string }{
		"generation_tasks":     {"../service/generation.go", "GenerationFrozen"},
		"tool_trials":          {"../tooltrial/frozen.go", "Frozen"},
		"semantic_review_runs": {"../domain/semantic_review.go", "SemanticReviewFrozen"},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), target.file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var actual []string
		ast.Inspect(file, func(n ast.Node) bool {
			typeSpec, ok := n.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != target.name {
				return true
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("frozen writer is not a struct")
			}
			for _, field := range structure.Fields.List {
				if field.Tag == nil {
					t.Fatal("unreviewed field without JSON tag", target.name)
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					t.Fatal(err)
				}
				actual = append(actual, strings.SplitN(reflect.StructTag(tag).Get("json"), ",", 2)[0])
			}
			return false
		})
		wanted := strings.Fields(materialFrozenFields[table])
		slices.Sort(actual)
		slices.Sort(wanted)
		if len(actual) == 0 || !slices.Equal(actual, wanted) {
			t.Fatal("frozen writer changed: review actual blob selectors", table, actual, wanted)
		}
	}
}

func preparedMaterialFixture() ([]SourceRow, domain.Manifest) {
	rows, manifest := contentRootFixture()
	rows[10].Values["head_revision"], rows[10].Values["content_hash"] = "", ""
	rows[11].Values["status"], rows[11].Values["adopted_hash"], rows[11].Values["adopted_by"] = "PREPARED", "", ""
	return rows, manifest
}

func TestMaterialCollectionUsesArtifactIdentityAndValidatesCandidates(t *testing.T) {
	rows, manifest := preparedMaterialFixture()
	project := strings.Repeat("2", 32)
	resource := strings.Repeat("c", 32)
	rows = append(rows, inventoryRow("resources", map[string]string{"id": resource, "project_id": project, "branch_id": strings.Repeat("5", 32)}))
	second := manifest.Artifacts[0]
	second.ResourceID, second.Hash = resource, strings.Repeat("b", 64)
	origin := *second.Origin
	origin.ResourceID, origin.ContentHash = resource, second.Hash
	second.Origin = &origin
	manifest.Artifacts = append(manifest.Artifacts, second)
	collect := func() map[string]string {
		t.Helper()
		raw, _ := json.Marshal(manifest)
		rows[11].Values["manifest"], rows[11].Values["content_hash"] = string(raw), financialDigest(raw)
		index, err := NewSourceIndex(rows)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := index.MaterialReferences(project)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]string{}
		for _, ref := range refs {
			if ref.Table == "artifact_releases" && strings.HasPrefix(ref.Field, "manifest.artifacts.") {
				if ref.ContentRoot {
					t.Fatal("unadopted candidate was promoted")
				}
				found[ref.Field] = ref.ID
			}
		}
		return found
	}
	before := collect()
	slices.Reverse(manifest.Artifacts)
	after := collect()
	if len(before) != 2 || !reflect.DeepEqual(before, after) {
		t.Fatal("array order changed material identity", before, after)
	}
	for _, mode := range []string{"duplicate", "foreign-resource", "bad-hash", "hidden-field", "missing-import", "foreign-import"} {
		t.Run(mode, func(t *testing.T) {
			rows, m := preparedMaterialFixture()
			switch mode {
			case "duplicate":
				m.Artifacts = append(m.Artifacts, m.Artifacts[0])
			case "foreign-resource":
				rows[10].Values["project_id"], rows[10].Values["branch_id"] = strings.Repeat("3", 32), strings.Repeat("6", 32)
			case "missing-import":
				m.Artifacts[0].OriginalHash = strings.Repeat("f", 64)
			case "foreign-import":
				m.Artifacts[0].SourceImportID, m.Artifacts[0].OriginalHash = strings.Repeat("d", 32), strings.Repeat("f", 64)
				rows = append(rows, inventoryRow("source_imports", map[string]string{"id": strings.Repeat("d", 32), "project_id": strings.Repeat("3", 32), "branch_id": strings.Repeat("6", 32), "actor_id": strings.Repeat("4", 32), "source_hash": strings.Repeat("f", 64)}))
			}
			raw, _ := json.Marshal(m)
			if mode == "hidden-field" {
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"extra_hash":"hidden"}`)
			}
			rows[11].Values["manifest"], rows[11].Values["content_hash"] = string(raw), financialDigest(raw)
			if mode == "bad-hash" {
				rows[11].Values["content_hash"] = strings.Repeat("f", 64)
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			if refs, err := index.MaterialReferences(project); err == nil || refs != nil {
				t.Fatal("invalid candidate produced partial material inventory", mode, err)
			}
		})
	}
}

func TestMaterialCollectionVerifiesReceiptBlobTuple(t *testing.T) {
	rows := sourceOwnershipRows()
	project, callID := strings.Repeat("2", 32), strings.Repeat("a", 32)
	output := strings.Repeat("b", 64)
	raw, _ := json.Marshal([]any{"SUCCEEDED", int64(25), "PRIVATE_PROVIDER_REFERENCE", output})
	receiptID := financialDigest([]byte(callID + "\x00" + financialDigest(raw)))
	rows = append(rows,
		inventoryRow("model_calls", map[string]string{"id": callID, "workspace_id": strings.Repeat("1", 32), "project_id": project, "actor_id": strings.Repeat("4", 32), "state": "SUCCEEDED", "output_hash": output, "input_hash": strings.Repeat("c", 64), "reserved_micros": "30", "charged_micros": "25"}),
		inventoryRow("model_call_accounting", map[string]string{"call_id": callID, "currency": "USD", "revision": "0"}),
		inventoryRow("model_call_receipts", map[string]string{"id": receiptID, "call_id": callID, "payload": string(raw)}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.MaterialReferences(project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.Table == "model_call_receipts" && ref.Field == "payload.output_hash" {
			found = ref.ObjectHash == output && !ref.ContentRoot
			parentKey, _ := RowOwnerKey("model_calls", map[string]string{"id": callID})
			parentID, _ := ReferenceID(project, "model_calls", parentKey, "output_hash")
			if len(ref.Sources) != 1 || ref.Sources[0].ReferenceID != parentID || ref.Sources[0].PayloadHash != financialDigest([]byte(output)) {
				t.Fatal("receipt output source was guessed or omitted", ref.Sources)
			}
		}
	}
	if !found {
		t.Fatal("receipt output blob was not linked")
	}
	rows[len(rows)-1].Values["payload"] = strings.Replace(string(raw), output, strings.Repeat("c", 64), 1)
	index, _ = NewSourceIndex(rows)
	if _, err := index.MaterialReferences(project); err == nil {
		t.Fatal("receipt identity mismatch accepted")
	}
}
