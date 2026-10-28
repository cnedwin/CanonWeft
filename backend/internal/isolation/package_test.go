package isolation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func packageRequest() Request {
	return Request{Language: "python", Entrypoint: "main.py", Files: []File{{Path: "main.py", Data: []byte("print(1)")}}, Timeout: time.Second, MaxOutputBytes: 4096}
}

func TestPackageAPIReachesRuntimeValidation(t *testing.T) {
	r := &Runner{root: t.TempDir()}
	_, err := r.Run(context.Background(), packageRequest())
	if err == nil || !strings.Contains(err.Error(), "runtime unavailable") {
		t.Fatalf("package rejected before runtime validation: %v", err)
	}
}

func TestPackagePolicyRejectsUnsafeInputs(t *testing.T) {
	cases := map[string]func(*Request){
		"mixed":           func(r *Request) { r.Script = "print(1)" },
		"missing-entry":   func(r *Request) { r.Entrypoint = "other.py" },
		"empty-entry":     func(r *Request) { r.Entrypoint = "" },
		"empty-files":     func(r *Request) { r.Files = nil },
		"invalid-utf8":    func(r *Request) { r.Files[0].Data = []byte{255} },
		"entry-too-large": func(r *Request) { r.Files[0].Data = bytes.Repeat([]byte("a"), 256*1024+1) },
		"file-too-large":  func(r *Request) { r.Files = append(r.Files, File{Path: "data", Data: make([]byte, 2*1024*1024+1)}) },
		"total-too-large": func(r *Request) {
			for i := 0; i < 4; i++ {
				r.Files = append(r.Files, File{Path: fmt.Sprint(i), Data: make([]byte, 2*1024*1024)})
			}
		},
		"too-many-files": func(r *Request) {
			for i := 0; i < 128; i++ {
				r.Files = append(r.Files, File{Path: fmt.Sprint(i)})
			}
		},
		"stdin-too-large": func(r *Request) { r.Input = make([]byte, 256*1024+1) },
	}
	for _, path := range []string{"../bad", "/absolute", "a/../bad", `C:\bad`, `a\bad`, "a//b", "./bad", "a/", "nul.txt", "COM1", "dir/LPT9.py", "a:stream", "a\x00b", "a. ", "main.py", "MAIN.PY", "main.py/child", strings.Repeat("a", 1025)} {
		path := path
		cases["path:"+path] = func(r *Request) { r.Files = append(r.Files, File{Path: path, Data: []byte("x")}) }
	}
	cases["directory-case-alias"] = func(r *Request) { r.Files = append(r.Files, File{Path: "Dir/a"}, File{Path: "dir/b"}) }
	cases["unicode-fold-alias"] = func(r *Request) { r.Files = append(r.Files, File{Path: "K/a"}, File{Path: "K/b"}) }
	cases["unicode-full-fold-alias"] = func(r *Request) { r.Files = append(r.Files, File{Path: "strasse.py"}, File{Path: "straße.py"}) }
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := packageRequest()
			change(&r)
			if _, err := prepareRequest(r); err == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
}

func TestPackageHashCoversFilesPathsAndEntrypoint(t *testing.T) {
	r := packageRequest()
	r.Files = append(r.Files, File{Path: "other.py", Data: []byte("print(1)")}, File{Path: "data.bin", Data: []byte{0, 255}})
	payload, err := prepareRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	base := sha256.Sum256(payload)
	for _, kind := range []string{"bytes", "path", "entry"} {
		t.Run(kind, func(t *testing.T) {
			copyRequest := r
			copyRequest.Files = append([]File(nil), r.Files...)
			switch kind {
			case "bytes":
				copyRequest.Files[2].Data = []byte{0, 254}
			case "path":
				copyRequest.Files[2].Path = "renamed.bin"
			case "entry":
				copyRequest.Entrypoint = "other.py"
			}
			p, err := prepareRequest(copyRequest)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(p) == base {
				t.Fatal("hash omitted package input")
			}
		})
	}
	r.Files[0], r.Files[2] = r.Files[2], r.Files[0]
	reordered, err := prepareRequest(r)
	if err != nil || sha256.Sum256(reordered) != base {
		t.Fatal("file order changed canonical package hash", err)
	}
}

func TestPackageMaximumSizeAccepted(t *testing.T) {
	r := packageRequest()
	r.Files[0].Data = bytes.Repeat([]byte("a"), 256*1024)
	for i := 0; i < 3; i++ {
		r.Files = append(r.Files, File{Path: fmt.Sprint(i), Data: make([]byte, 2*1024*1024)})
	}
	r.Files = append(r.Files, File{Path: "last", Data: make([]byte, 2*1024*1024-256*1024)})
	r.Input = make([]byte, 256*1024)
	p, err := prepareRequest(r)
	if err != nil || len(p) > MaxRequestJSONBytes {
		t.Fatal("maximum valid package rejected", err)
	}
}

func TestCanceledRequestDoesNotPreparePackage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &Runner{root: t.TempDir()}
	request := packageRequest()
	request.Files = append(request.Files, File{Path: "../unsafe"})
	_, err := r.Run(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input reached package preparation: %v", err)
	}
}
