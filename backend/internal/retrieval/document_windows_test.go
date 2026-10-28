package retrieval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type documentPIDOutput struct {
	line   chan uint32
	buffer bytes.Buffer
}

func (output *documentPIDOutput) Write(data []byte) (int, error) {
	output.buffer.Write(data)
	if strings.Contains(output.buffer.String(), "\n") {
		pid, err := strconv.ParseUint(strings.TrimSpace(output.buffer.String()), 10, 32)
		if err != nil {
			return 0, err
		}
		select {
		case output.line <- uint32(pid):
		default:
		}
	}
	return len(data), nil
}

func TestDocumentProcessCancellationKillsVenvTree(t *testing.T) {
	if os.Getenv("STUDIO_TEST_INGEST") != "1" {
		t.Skip("requires dedicated local runtime")
	}
	python, _, _, err := documentPaths()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := "import sys,subprocess,time; p=subprocess.Popen([sys._base_executable,'-B','-I','-c','import time;time.sleep(60)']); print(p.pid,flush=True); time.sleep(60)"
	command := exec.CommandContext(ctx, python, "-B", "-I", "-c", code)
	command.Dir, _ = os.Getwd()
	command.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "WINDIR=" + os.Getenv("WINDIR")}
	configureDocumentProcess(command)
	output := &documentPIDOutput{line: make(chan uint32, 1)}
	command.Stdout = output
	finished := make(chan error, 1)
	go func() { finished <- runDocumentCommand(ctx, command) }()
	var childPID uint32
	select {
	case childPID = <-output.line:
	case err = <-finished:
		t.Fatalf("process creation failed: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("child process did not become ready")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, childPID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	cancel()
	select {
	case err = <-finished:
		if err == nil {
			t.Fatal("cancelled process returned success")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancellation blocked")
	}
	state, err := windows.WaitForSingleObject(handle, 1000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatal(fmt.Sprintf("orphan child %d: state=%d error=%v", childPID, state, err))
	}
}
