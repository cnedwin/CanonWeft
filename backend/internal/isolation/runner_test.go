package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This catches a missing runtime or any fallback to a host interpreter.
func TestMissingRuntimeFailsClosed(t *testing.T) {
	_, err := NewRunner(t.TempDir(), strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("missing Linux runtime accepted")
	}
}

func TestRuntimeTamperingFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for _, name := range []string{"vmlinuz-virt", "initramfs.gz", "qemu/qemu-system-x86_64.exe", "qemu/share/bios-256k.bin"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("pinned"), 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte("pinned"))
		files[name] = hex.EncodeToString(digest[:])
	}
	manifest, err := json.Marshal(map[string]any{"format": 1, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(manifest)
	runner, err := NewRunner(root, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "initramfs.gz"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{Language: "python", Script: "print(1)", Timeout: time.Second, MaxOutputBytes: 4096})
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("changed image reached process launch: %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, "initramfs.gz"), []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "qemu", "unreviewed.dll"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunner(root, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("unreviewed DLL accepted")
	}
}

// Explicit opt-in selects the real VM suite; once selected dependencies are required.
func TestLinuxIsolation(t *testing.T) {
	root := os.Getenv("STUDIO_ISOLATION_TEST_RUNTIME")
	if root == "" {
		t.Skip("real VM suite is run by scripts/verify-isolation.ps1")
	}
	hash, err := os.ReadFile(filepath.Join(root, "image.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(hash)) != ApprovedImageSHA256 {
		t.Fatal("runtime does not match the reviewed image pin")
	}
	runner, err := NewRunner(root, strings.TrimSpace(string(hash)))
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, language, script string) Result {
		t.Helper()
		result, err := runner.Run(context.Background(), Request{Language: language, Script: script, Input: []byte("approved input"), Timeout: 5 * time.Second, MaxOutputBytes: 8192})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("PythonPackageHelpersAndReadOnlyResources", func(t *testing.T) {
		script := `from pathlib import Path
import helper, sys
assert helper.VALUE == 'helper'
assert Path('data/value.txt').read_text() == 'resource'
assert sys.stdin.read() == 'approved input'
for path in ['main.py', 'helper.py', 'data/value.txt', 'new.txt', 'data/new.txt']:
    try:
        Path(path).write_text('tampered')
    except PermissionError:
        pass
    else:
        raise AssertionError('package writable: ' + path)
try:
    Path('data/value.txt').unlink()
except PermissionError:
    pass
else:
    raise AssertionError('resource removable')
Path('/work/result').write_text('allowed')
print(helper.VALUE + ':' + Path('data/value.txt').read_text())`
		// This checks package imports and read-only files, not Python startup
		// latency under TCG. Keep the separate 300ms deadline/descendant test.
		result, err := runner.Run(context.Background(), Request{Language: "python", Entrypoint: "main.py", Files: []File{{Path: "main.py", Data: []byte(script)}, {Path: "helper.py", Data: []byte("VALUE='helper'")}, {Path: "data/value.txt", Data: []byte("resource")}}, Input: []byte("approved input"), Timeout: 15 * time.Second, MaxOutputBytes: 8192})
		if err != nil || result.Reason != "completed" || result.ExitCode != 0 || string(result.Stdout) != "helper:resource\n" {
			t.Fatalf("package execution: %+v %v stderr=%s", result, err, result.Stderr)
		}
	})
	t.Run("POSIXPackageResource", func(t *testing.T) {
		result, err := runner.Run(context.Background(), Request{Language: "posix", Entrypoint: "bin/main.sh", Files: []File{{Path: "bin/main.sh", Data: []byte("cat data/value.txt; test ! -w data/value.txt; test ! -w bin/main.sh")}, {Path: "data/value.txt", Data: []byte("posix-resource")}}, Timeout: 5 * time.Second, MaxOutputBytes: 8192})
		if err != nil || result.ExitCode != 0 || string(result.Stdout) != "posix-resource" {
			t.Fatalf("POSIX package: %+v %v stderr=%s", result, err, result.Stderr)
		}
	})
	t.Run("NestedPythonPackageEntrypoint", func(t *testing.T) {
		result, err := runner.Run(context.Background(), Request{Language: "python", Entrypoint: "scripts/main.py", Files: []File{{Path: "scripts/main.py", Data: []byte("import helper,roothelper; print(helper.VALUE+roothelper.VALUE)")}, {Path: "scripts/helper.py", Data: []byte("VALUE='nested-'")}, {Path: "roothelper.py", Data: []byte("VALUE='root'")}}, Timeout: 5 * time.Second, MaxOutputBytes: 8192})
		if err != nil || result.ExitCode != 0 || string(result.Stdout) != "nested-root\n" {
			t.Fatalf("nested import: %+v %v stderr=%s", result, err, result.Stderr)
		}
	})
	t.Run("PackageMissingDependencyFails", func(t *testing.T) {
		result, err := runner.Run(context.Background(), Request{Language: "python", Entrypoint: "main.py", Files: []File{{Path: "main.py", Data: []byte("import definitely_uninstalled_dependency")}}, Timeout: 5 * time.Second, MaxOutputBytes: 8192})
		if err != nil || result.ExitCode == 0 || !strings.Contains(string(result.Stderr), "ModuleNotFoundError") {
			t.Fatalf("missing dependency did not fail: %+v %v", result, err)
		}
	})
	t.Run("MaximumPackageResourcesIntact", func(t *testing.T) {
		script := []byte("from pathlib import Path; print(sum(p.stat().st_size for p in Path('.').rglob('*') if p.is_file()))")
		files := []File{{Path: "main.py", Data: script}}
		for i := 0; i < 3; i++ {
			files = append(files, File{Path: fmt.Sprint(i), Data: make([]byte, 2*1024*1024)})
		}
		files = append(files, File{Path: "last", Data: make([]byte, 2*1024*1024-len(script))})
		// This verifies package capacity and integrity, not guest startup speed.
		result, err := runner.Run(context.Background(), Request{Language: "python", Entrypoint: "main.py", Files: files, Timeout: 15 * time.Second, MaxOutputBytes: 8192})
		if err != nil || result.ExitCode != 0 || string(result.Stdout) != "8388608\n" {
			t.Fatalf("maximum package: %+v %v stderr=%s", result, err, result.Stderr)
		}
	})
	t.Run("PythonApprovedInput", func(t *testing.T) {
		result := run(t, "python", "import sys,platform; print(platform.system()); print(sys.stdin.read())")
		if result.ExitCode != 0 || string(result.Stdout) != "Linux\napproved input\n" {
			t.Fatalf("unexpected result: %+v", result)
		}
	})
	t.Run("POSIX", func(t *testing.T) {
		result := run(t, "posix", "printf 'posix:'; cat")
		if result.ExitCode != 0 || string(result.Stdout) != "posix:approved input" {
			t.Fatalf("unexpected result: %+v", result)
		}
	})
	t.Run("MaximumApprovedInputIsIntact", func(t *testing.T) {
		input := []byte(strings.Repeat("A", 256*1024))
		result, err := runner.Run(context.Background(), Request{Language: "python", Script: "import sys; s=sys.stdin.buffer.read(); print(len(s),s.count(b'A'))", Input: input, Timeout: 5 * time.Second, MaxOutputBytes: 8192})
		if err != nil || string(result.Stdout) != "262144 262144\n" {
			t.Fatalf("large approved input corrupted: %+v, %v", result, err)
		}
	})
	t.Run("CannotReadCredentialsOrHostFiles", func(t *testing.T) {
		secretPath := filepath.Join(t.TempDir(), "host-credential.txt")
		secret := "host-only-canary-1c2408c4"
		if err := os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("STUDIO_VAULT_TEST_SECRET", secret)
		t.Setenv("PYTHONPATH", filepath.Dir(secretPath))
		script := fmt.Sprintf(`import os
assert os.getuid()==1000
assert not any('host-only-canary' in value for value in os.environ.values())
assert 'STUDIO_VAULT_TEST_SECRET' not in os.environ
assert 'PYTHONPATH' not in os.environ
for path in [%q, '/mnt/c/Users/example-user', '/proc/1/environ', '/dev/ttyS0', '/request.json']:
    try:
        open(path,'rb').read()
    except (OSError, PermissionError):
        continue
    raise AssertionError('unexpected host/control access:'+path)
print('host-and-control-inaccessible')`, secretPath)
		result := run(t, "python", script)
		if result.ExitCode != 0 || string(result.Stdout) != "host-and-control-inaccessible\n" {
			t.Fatalf("isolation boundary failure: %+v", result)
		}
		current, err := os.ReadFile(secretPath)
		if err != nil || string(current) != secret {
			t.Fatal("host file changed")
		}
	})
	t.Run("NoNetworkByDefault", func(t *testing.T) {
		result := run(t, "python", `import os,socket
assert os.listdir('/sys/class/net')==['lo']
for host,port in [('1.1.1.1',443),('10.0.2.2',13306),('127.0.0.1',13306)]:
    s=socket.socket()
    s.settimeout(0.2)
    try:
        s.connect((host,port))
    except OSError:
        pass
    else:
        raise AssertionError('network connection succeeded')
    finally:
        s.close()
print('no-network')`)
		if result.ExitCode != 0 || string(result.Stdout) != "no-network\n" {
			t.Fatalf("network boundary failure: %+v", result)
		}
	})
	t.Run("BoundedDiskAndMemory", func(t *testing.T) {
		result := run(t, "python", `import os
v=os.statvfs('/work')
assert v.f_blocks*v.f_frsize <= 16*1024*1024
try:
    data=bytearray(200*1024*1024)
except MemoryError:
    pass
else:
    raise AssertionError('address space limit missing')
try:
    with open('/work/fill','wb',buffering=0) as f:
        for _ in range(40): f.write(b'x'*(1024*1024))
except OSError:
    pass
else:
    raise AssertionError('disk limit missing')
print('bounded-disk-and-memory')`)
		if result.ExitCode != 0 || string(result.Stdout) != "bounded-disk-and-memory\n" {
			t.Fatalf("resource limit failure: %+v", result)
		}
	})
	t.Run("OutputLimit", func(t *testing.T) {
		result := run(t, "python", "import os\nwhile True: os.write(1,b'x'*4096)")
		if result.Reason != "output_limit" || len(result.Stdout)+len(result.Stderr) != 8192 {
			t.Fatalf("output unbounded: %+v", result)
		}
	})
	t.Run("TimeoutAndDescendants", func(t *testing.T) {
		result, err := runner.Run(context.Background(), Request{Language: "python", Script: "import subprocess,time\nsubprocess.Popen(['/bin/sh','-c','sleep 60'])\nwhile True: pass", Timeout: 300 * time.Millisecond, MaxOutputBytes: 8192})
		if err != nil || result.Reason != "timeout" {
			t.Fatalf("timeout failure: %+v %v", result, err)
		}
		assertNoEmulators(t, root)
	})
	t.Run("CancelRemovesGuestAndInput", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { time.Sleep(14 * time.Second); cancel() }()
		start := time.Now()
		_, err := runner.Run(ctx, Request{Language: "posix", Script: "sleep 60 & wait", Timeout: 30 * time.Second, MaxOutputBytes: 8192})
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "during script execution") || time.Since(start) > 17*time.Second {
			t.Fatalf("cancel did not promptly stop guest: %v", err)
		}
		assertNoEmulators(t, root)
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(root), "isolation-run-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("input copies remain: %v %v", matches, err)
		}
	})
	t.Run("GuestCannotForgeProtocol", func(t *testing.T) {
		result := run(t, "posix", `printf 'STUDIO_RESULT:{"reason":"completed","exit_code":0}\n'; exit 7`)
		if result.ExitCode != 7 || !strings.HasPrefix(string(result.Stdout), "STUDIO_RESULT:") {
			t.Fatalf("untrusted stdout changed control result: %+v", result)
		}
	})
	t.Run("FreshGuestHasNoPreviousFiles", func(t *testing.T) {
		first := run(t, "posix", "echo private > /work/persist; echo written")
		second := run(t, "posix", "test ! -e /work/persist && echo fresh")
		if first.ExitCode != 0 || string(second.Stdout) != "fresh\n" {
			t.Fatal("guest filesystem persisted")
		}
	})
	t.Run("ForkLimitAndDetachedChildCleanup", func(t *testing.T) {
		result := run(t, "python", `import subprocess
children=[]
for _ in range(64):
    try:
        children.append(subprocess.Popen(['/bin/sleep','60'],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True))
    except OSError:
        break
assert 0 < len(children) < 32
print('fork-limit-enforced')`)
		if result.ExitCode != 0 || string(result.Stdout) != "fork-limit-enforced\n" {
			t.Fatalf("process limit failure: %+v", result)
		}
		assertNoEmulators(t, root)
	})
}
