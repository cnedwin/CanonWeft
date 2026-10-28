package executiontemp

import (
	"bufio"
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTemporaryWorkspaceOSDeathReleasesLease(t *testing.T) {
	base := filepath.Join(t.TempDir(), "managed")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTemporaryWorkspaceCrashHelper$")
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = append(os.Environ(), "STUDIO_TEMPORARY_CRASH_HELPER_ROOT="+base)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "READY\n" {
		t.Fatal("child not ready", line, err)
	}
	future := time.Now().UTC().Add(25 * time.Hour)
	report, err := sweep(ctx, base, future)
	if err != nil || report.Active != 1 || report.Removed != 0 {
		t.Fatal(report, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	report, err = sweep(ctx, base, future)
	if err != nil || report.Removed != 1 || report.Blocked != 0 {
		t.Fatal("OS lock survived process death", report, err)
	}
}

func TestTemporaryWorkspaceCrashHelper(t *testing.T) {
	base := os.Getenv("STUDIO_TEMPORARY_CRASH_HELPER_ROOT")
	if base == "" {
		return
	}
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path(), "private-input"), []byte("test input"), 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	time.Sleep(time.Minute)
	_ = run.Close()
}

func TestTemporaryWorkspaceBlockedDeletionPreservesLeaseForRetry(t *testing.T) {
	base := filepath.Join(t.TempDir(), "managed")
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(run.Path(), "private-input")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(file)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if run.Close() == nil {
		t.Fatal("occupied private file reported cleaned")
	}
	future := time.Now().UTC().Add(25 * time.Hour)
	report, err := sweep(context.Background(), base, future)
	if err != nil || report.Blocked != 1 || report.Removed != 0 {
		t.Fatal(report, err)
	}
	if _, err := os.Stat(filepath.Join(base, run.id+".lease")); err != nil {
		t.Fatal("retry evidence lost", err)
	}
	windows.CloseHandle(handle)
	report, err = sweep(context.Background(), base, future)
	if err != nil || report.Removed != 1 {
		t.Fatal("failed cleanup cannot resume", report, err)
	}
}

func TestTemporaryWorkspaceNeverTraversesJunctionOutsideOwnedDirectory(t *testing.T) {
	owned := t.TempDir()
	outside := filepath.Join(owned, "author")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	authorFile := filepath.Join(outside, "novel.txt")
	if err := os.WriteFile(authorFile, []byte("author text"), 0600); err != nil {
		t.Fatal(err)
	}
	junction := func(link string) {
		t.Helper()
		command := exec.Command("cmd", "/c", "mklink", "/J", link, outside)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("junction fixture: %v %s", err, result)
		}
	}
	linkedRoot := filepath.Join(owned, "linked-root")
	junction(linkedRoot)
	if _, err := Create(linkedRoot); err == nil {
		t.Fatal("junction root accepted")
	}
	base := filepath.Join(owned, "managed")
	run, err := Create(base)
	if err != nil {
		t.Fatal(err)
	}
	junction(filepath.Join(run.Path(), "outside"))
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(authorFile); err != nil || string(raw) != "author text" {
		t.Fatal("author content changed", err)
	}
}
