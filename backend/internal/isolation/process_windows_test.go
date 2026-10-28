package isolation

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The helper deliberately stalls immediately after OS child creation, before
// any post-create management. Killing this host must never orphan the child.
func TestExecutorDeathAtCreationDoesNotOrphanChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCreationCrashHelper$")
	command.Env = append(os.Environ(), "STUDIO_CREATION_HELPER=host")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("host failed before child creation")
	}
	pid, err := strconv.ParseUint(scanner.Text(), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	status, err := windows.WaitForSingleObject(child, 3000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child %d survived executor death during creation: wait=%d error=%v", pid, status, err)
	}
}

func TestCreationCrashHelper(t *testing.T) {
	switch os.Getenv("STUDIO_CREATION_HELPER") {
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "host":
		command := exec.Command(os.Args[0], "-test.run=^TestCreationCrashHelper$")
		command.Env = append(cleanEnvironment(filepath.Dir(os.Args[0])), "STUDIO_CREATION_HELPER=child")
		command.Dir = filepath.Dir(os.Args[0])
		launch, err := prepareProcess(command)
		if err != nil {
			fmt.Println("prepare-failed:", err)
			os.Exit(2)
		}
		information, err := launch.create()
		if err != nil {
			fmt.Println("create-failed:", err)
			os.Exit(2)
		}
		fmt.Println(information.ProcessId)
		// Stall before any post-create management, directly at the old gap.
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func assertNoEmulators(t *testing.T, root string) {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "qemu-system-x86_64.exe") {
			handle, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
			if openErr != nil {
				t.Fatalf("cannot inspect remaining QEMU: %v", openErr)
			}
			buffer := make([]uint16, 32768)
			size := uint32(len(buffer))
			queryErr := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size)
			windows.CloseHandle(handle)
			if queryErr != nil {
				t.Fatal(queryErr)
			}
			if strings.EqualFold(windows.UTF16ToString(buffer[:size]), filepath.Join(root, "qemu", "qemu-system-x86_64.exe")) {
				t.Fatalf("QEMU process %d remains after cleanup", entry.ProcessID)
			}
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	if err != windows.ERROR_NO_MORE_FILES {
		t.Fatal(err)
	}
}
