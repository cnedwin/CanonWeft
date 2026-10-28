package tooltrial

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/skills"
	"testing"
	"time"
)

func contractFixture() (domain.CapabilityDefinition, skills.Bundle) {
	b := skills.Bundle{Files: []skills.File{{Path: "SKILL.md", Data: []byte("---\nname: tool-contract\ndescription: Use when testing approved inputs.\n---\nOnly propose results.")}, {Path: "scripts/main.py", Data: []byte("import json,sys\nfrom helper import result\nprint(json.dumps(result(json.load(sys.stdin))))\n")}, {Path: "helper.py", Data: []byte("def result(value):\n return {'summary':value['source']}\n")}}}
	_ = b.Validate()
	raw, _ := json.Marshal(b)
	d := domain.CapabilityDefinition{Kind: "tool", Key: "check-source", Name: "来源检查", Description: "结构化来源检查", Instructions: "读取批准输入并输出JSON候选", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`), Dependencies: []domain.CapabilityRef{}, PackageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PackageHash: skills.Hash(raw), Tool: &domain.ToolDefinition{Runtime: "linux-python", Entrypoint: "scripts/main.py", TimeoutSeconds: 10, MemoryMiB: 256}}
	return d, b
}

func TestPreparePinsFullApprovedPackageAndActualLimits(t *testing.T) {
	d, b := contractFixture()
	request, err := Prepare(d, b, []byte(`{"source":"批准材料"}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Language != "python" || request.Script != "" || request.Entrypoint != "scripts/main.py" || len(request.Files) != 3 || request.Timeout != 10*time.Second {
		t.Fatal("incomplete or host-script request", request)
	}
	before := string(request.Files[0].Data)
	b.Files[0].Data[0] = 'X'
	if string(request.Files[0].Data) != before {
		t.Fatal("prepared bytes alias mutable input")
	}
	d, b = contractFixture()
	d.Tool.TimeoutSeconds = 31
	if _, err = Prepare(d, b, []byte(`{"source":"x"}`)); err == nil {
		t.Fatal("runtime limit expanded")
	}
}

func TestToolPreparationRejectsWrongHashSchemaAndUnapprovedRuntime(t *testing.T) {
	d, b := contractFixture()
	if _, err := Prepare(d, b, []byte(`{"source":3}`)); err == nil {
		t.Fatal("invalid typed input accepted")
	}
	d.Tool.MemoryMiB = 64
	if _, err := Prepare(d, b, []byte(`{"source":"x"}`)); err == nil {
		t.Fatal("smaller requested memory bound cannot be fulfilled by fixed guest")
	}
	d.Tool.MemoryMiB = 256
	d.PackageHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := Prepare(d, b, []byte(`{"source":"x"}`)); err == nil {
		t.Fatal("tampered package accepted")
	}
	d, b = contractFixture()
	d.Tool.Entrypoint = "../secret.py"
	if _, err := Prepare(d, b, []byte(`{"source":"x"}`)); err == nil {
		t.Fatal("unapproved entrypoint accepted")
	}
}

func TestToolSuccessRequiresProcessReceiptAndOutputContract(t *testing.T) {
	d, _ := contractFixture()
	result := isolation.Result{Reason: "completed", ExitCode: 0, Stdout: []byte(`{"summary":"来源检查候选"}`)}
	if err := ValidateOutput(d, result); err != nil {
		t.Fatal(err)
	}
	result.ExitCode = 7
	if err := ValidateOutput(d, result); err == nil {
		t.Fatal("failed script claimed success")
	}
	result.ExitCode = 0
	result.Reason = "output_limit"
	if err := ValidateOutput(d, result); err == nil {
		t.Fatal("truncated output accepted")
	}
	result.Reason = "completed"
	result.Stdout = []byte(`{"summary":"候选","qualified":true}`)
	if err := ValidateOutput(d, result); err == nil {
		t.Fatal("undeclared output self-qualification accepted")
	}
}
