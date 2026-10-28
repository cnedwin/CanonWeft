package main

import (
	"strings"
	"testing"

	"novelstudio/internal/isolation"
)

func TestDecodePackageRequest(t *testing.T) {
	request, err := decodeRequest(strings.NewReader(`{"language":"python","entrypoint":"main.py","files":[{"path":"main.py","data":"cHJpbnQoMSk="}],"input":"","timeout_ms":1000,"max_output_bytes":4096}`))
	if err != nil || request.Entrypoint != "main.py" || len(request.Files) != 1 || string(request.Files[0].Data) != "print(1)" {
		t.Fatalf("package decoding: %+v %v", request, err)
	}
}

func TestDecodeRequestRejectsUnboundedOrUnknownInput(t *testing.T) {
	for _, raw := range []string{
		`{"files":[{"path":"main.py","data":"","symlink":"/etc/passwd"}],"timeout_ms":1000}`,
		`{"host_path":"C:/secret","timeout_ms":1000}`,
		`{"timeout_ms":9223372036854775807}`,
		`{"timeout_ms":1000} {}`,
		`{"timeout_ms":1000}` + strings.Repeat(" ", isolation.MaxRequestJSONBytes),
	} {
		if _, err := decodeRequest(strings.NewReader(raw)); err == nil {
			t.Fatal("unsafe CLI input accepted")
		}
	}
}
