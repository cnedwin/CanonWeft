package isolation

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This opt-in harness is for a locally reviewed, trusted Go regression binary.
// It never routes the private guest through Runner or changes ApprovedImageSHA256.
// Normal Skill requests still have the production image and resource limits.
func TestTrustedSignalGuest(t *testing.T) {
	base := os.Getenv("STUDIO_SIGNAL_GUEST_BASE_RUNTIME")
	root := os.Getenv("STUDIO_SIGNAL_GUEST_ROOT")
	pin := os.Getenv("STUDIO_SIGNAL_GUEST_MANIFEST_SHA256")
	if base == "" && root == "" && pin == "" {
		t.Skip("trusted signal guest requires explicit local image and manifest pin")
	}
	if !filepath.IsAbs(base) || !filepath.IsAbs(root) || len(pin) != 64 {
		t.Fatal("absolute base/private guest paths and an explicit manifest pin are required")
	}
	relative, err := filepath.Rel(base, root)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		t.Fatal("private test guest must be outside the production runtime")
	}
	if _, err := NewRunner(base, ApprovedImageSHA256); err != nil {
		t.Fatalf("production base integrity: %v", err)
	}
	manifestBytes := readPinnedSignalFile(t, filepath.Join(root, "manifest.json"), 65536, pin)
	var manifest struct {
		Format     int    `json:"format"`
		Kind       string `json:"kind"`
		BaseImage  string `json:"base_image_sha256"`
		Initramfs  string `json:"initramfs_sha256"`
		TestBinary string `json:"test_binary_sha256"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil || manifest.Format != 1 || manifest.Kind != "trusted-hostsignals-test-only" || manifest.BaseImage != ApprovedImageSHA256 || len(manifest.TestBinary) != 64 {
		t.Fatal("invalid trusted signal guest manifest")
	}
	_ = readPinnedSignalFile(t, filepath.Join(root, "initramfs.gz"), 96*1024*1024, manifest.Initramfs)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.Command(filepath.Join(base, "qemu", "qemu-system-x86_64.exe"),
		"-no-user-config", "-nodefaults", "-machine", "pc", "-accel", "tcg,thread=single,tb-size=16",
		"-cpu", "max", "-m", "256", "-smp", "1", "-nic", "none", "-display", "none",
		"-vga", "none", "-serial", "stdio", "-monitor", "none", "-no-reboot",
		"-L", filepath.Join(base, "qemu", "share"),
		"-kernel", filepath.Join(base, "vmlinuz-virt"),
		"-initrd", filepath.Join(root, "initramfs.gz"),
		"-append", "console=ttyS0 quiet panic=-1 rdinit=/signal-test-init")
	command.Dir = filepath.Join(base, "qemu")
	command.Env = cleanEnvironment(command.Dir)
	var diagnostic boundedBuffer
	command.Stderr = &diagnostic
	process, err := startContained(ctx, command)
	if err != nil {
		t.Fatalf("creation-time Job containment unavailable: %v", err)
	}
	defer process.Close()
	pid, err := windows.GetProcessId(process.handle)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(io.LimitReader(process.Stdout, 2*1024*1024))
	scanner.Buffer(make([]byte, 4096), 1536*1024)
	var boot boundedBuffer
	var result []byte
	ready := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "SIGNAL_GUEST_READY" {
			ready = true
		} else if ready && strings.HasPrefix(line, "SIGNAL_GUEST_RESULT:") {
			result = []byte(strings.TrimPrefix(line, "SIGNAL_GUEST_RESULT:"))
			break
		} else {
			_, _ = boot.Write([]byte(line + "\n"))
		}
	}
	// Stop only the process/Job created by this test, including on protocol error.
	_ = process.Kill()
	_ = process.Wait()
	status, waitErr := windows.WaitForSingleObject(process.handle, 0)
	if waitErr != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned QEMU %d did not stop: status=%d error=%v", pid, status, waitErr)
	}
	if len(result) == 0 {
		t.Fatalf("trusted guest returned no result: context=%v scanner=%v boot=%s emulator=%s", ctx.Err(), scanner.Err(), boot.String(), diagnostic.String())
	}
	if err := os.WriteFile(filepath.Join(root, "guest-result.json"), result, 0600); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Format     int    `json:"format"`
		Kind       string `json:"kind"`
		Reason     string `json:"reason"`
		ReturnCode int    `json:"return_code"`
		TestBinary string `json:"test_binary_sha256"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
	}
	if err := json.Unmarshal(result, &report); err != nil || report.Format != 1 || report.Kind != "trusted-hostsignals-tests" || report.TestBinary != manifest.TestBinary {
		t.Fatal("invalid trusted guest result")
	}
	output, outErr := base64.StdEncoding.DecodeString(report.Stdout)
	errors, errErr := base64.StdEncoding.DecodeString(report.Stderr)
	if outErr != nil || errErr != nil || len(output)+len(errors) > 1024*1024 {
		t.Fatal("invalid bounded test output")
	}
	t.Logf("owned QEMU %d stopped; base=%s binary=%s\n%s%s", pid, ApprovedImageSHA256, manifest.TestBinary, output, errors)
	if report.Reason != "completed" || report.ReturnCode != 0 {
		t.Fatalf("real Linux Go tests failed: reason=%s return_code=%d", report.Reason, report.ReturnCode)
	}
}

func readPinnedSignalFile(t *testing.T, path string, limit int64, expected string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || info.Size() < 1 || len(expected) != 64 {
		t.Fatalf("unavailable or invalid pinned signal guest artifact: %s", filepath.Base(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		t.Fatal(fmt.Sprintf("signal guest hash mismatch: %s", filepath.Base(path)))
	}
	return data
}
