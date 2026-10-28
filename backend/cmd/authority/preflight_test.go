package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func preflight(t *testing.T, root, address string) (string, error) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("Windows local provisioning preflight")
	}
	script, err := filepath.Abs("../../../scripts/authority-preflight.ps1")
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "run-preflight.ps1")
	if err = os.WriteFile(runner, []byte("param($Preflight,$Root,$Address)\n$ErrorActionPreference='Stop'\n. $Preflight\nAssert-AuthorityServicesStopped -ProjectRoot $Root -Business studio -ConfiguredAddress $Address\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("pwsh", "-NoProfile", "-File", runner, "-Preflight", script, "-Root", root, "-Address", address)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestAuthorityPreflightRejectsConfiguredAlternateLoopbackListeners(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if output, err := preflight(t, t.TempDir(), listener.Addr().String()); err == nil || !strings.Contains(output, "Studio endpoint "+listener.Addr().String()+" is listening") {
				t.Fatal("configured listener was not the reason for refusal", output, err)
			}
		})
	}
}

func TestAuthorityPreflightRejectsManagedProcessWithoutListeningPort(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows local provisioning preflight")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "backend", "bin", "studio-prod.exe")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestAuthorityPreflightProcessHelper$")
	child.Env = append(os.Environ(), "AUTHORITY_PREFLIGHT_CHILD=1")
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || scanner.Text() != "preflight process ready" {
		t.Fatal("process fixture not ready")
	}
	if output, err := preflight(t, root, ""); err == nil || !strings.Contains(output, "managed Studio API/worker process is active") {
		t.Fatal("managed process was not the reason for refusal", output, err)
	}
	// The guard is read-only; the process must remain alive after refusal.
	if err := child.Process.Kill(); err != nil {
		t.Fatal("preflight stopped the managed process", err)
	}
	_ = child.Wait()
}

func TestAuthorityPreflightProcessHelper(t *testing.T) {
	if os.Getenv("AUTHORITY_PREFLIGHT_CHILD") != "1" {
		return
	}
	os.Stdout.WriteString("preflight process ready\n")
	for {
		time.Sleep(time.Second)
	}
}

func TestAuthorityPreflightAcceptsStoppedServices(t *testing.T) {
	if output, err := preflight(t, t.TempDir(), ""); err != nil {
		t.Fatal("stopped services were not accepted", output, err)
	}
}
