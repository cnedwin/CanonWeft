package isolation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 10+: creation-time Job assignment, never post-creation assignment.
// Windows closes this non-inherited kill-on-close handle if the executor dies,
// including while CreateProcess is creating the child or before it returns.
const procThreadAttributeJobList = 0x0002000d

type processLaunch struct {
	command      *exec.Cmd
	job          windows.Handle
	attributes   *windows.ProcThreadAttributeListContainer
	childHandles []windows.Handle
	stdout       *os.File
	stderr       *os.File
}

type containedProcess struct {
	Stdout     io.ReadCloser
	handle     windows.Handle
	job        windows.Handle
	stopWatch  chan struct{}
	watchDone  chan struct{}
	stderrDone chan error
	waitOnce   sync.Once
	waitErr    error
	closeOnce  sync.Once
}

func cleanEnvironment(qemuDir string) []string {
	root := os.Getenv("SystemRoot")
	return []string{"SystemRoot=" + root, "WINDIR=" + root, "PATH=" + qemuDir + ";" + filepath.Join(root, "System32")}
}

func prepareProcess(command *exec.Cmd) (_ *processLaunch, prepareErr error) {
	launch := &processLaunch{command: command}
	defer func() {
		if prepareErr != nil {
			launch.Close()
		}
	}()
	var err error
	launch.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	limits.ProcessMemoryLimit = 768 * 1024 * 1024
	_, err = windows.SetInformationJobObject(launch.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return nil, err
	}
	launch.attributes, err = windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	if err = launch.attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&launch.job), unsafe.Sizeof(launch.job)); err != nil {
		return nil, fmt.Errorf("creation-time Job assignment unavailable: %w", err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	var outputWrite, errorWrite *os.File
	launch.stdout, outputWrite, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	defer outputWrite.Close()
	launch.stderr, errorWrite, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	defer errorWrite.Close()
	for _, file := range []*os.File{input, outputWrite, errorWrite} {
		var handle windows.Handle
		err = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &handle, 0, true, windows.DUPLICATE_SAME_ACCESS)
		if err != nil {
			return nil, err
		}
		launch.childHandles = append(launch.childHandles, handle)
	}
	// The Job handle is excluded: inheriting it would defeat kill-on-host-death.
	if err = launch.attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&launch.childHandles[0]), uintptr(len(launch.childHandles))*unsafe.Sizeof(launch.childHandles[0])); err != nil {
		return nil, err
	}
	return launch, nil
}

// create is the sole OS creation operation. All limits and the Job list exist
// before entry; no running or suspended uncontained child is ever created.
func (launch *processLaunch) create() (windows.ProcessInformation, error) {
	var information windows.ProcessInformation
	executable, err := windows.UTF16PtrFromString(launch.command.Path)
	if err != nil {
		return information, err
	}
	arguments, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(launch.command.Args))
	if err != nil {
		return information, err
	}
	directory, err := windows.UTF16PtrFromString(launch.command.Dir)
	if err != nil {
		return information, err
	}
	environment := append([]string(nil), launch.command.Env...)
	for _, entry := range environment {
		if strings.ContainsRune(entry, 0) {
			return information, errors.New("NUL in process environment")
		}
	}
	sort.Slice(environment, func(i, j int) bool { return strings.ToUpper(environment[i]) < strings.ToUpper(environment[j]) })
	block := utf16.Encode([]rune(strings.Join(environment, "\x00") + "\x00\x00"))
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:         uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
			ShowWindow: windows.SW_HIDE,
			StdInput:   launch.childHandles[0],
			StdOutput:  launch.childHandles[1],
			StdErr:     launch.childHandles[2],
		},
		ProcThreadAttributeList: launch.attributes.List(),
	}
	err = windows.CreateProcess(executable, arguments, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		&block[0], directory, &startup.StartupInfo, &information)
	return information, err
}

func (launch *processLaunch) closeSetup() {
	if launch.attributes != nil {
		launch.attributes.Delete()
		launch.attributes = nil
	}
	for _, handle := range launch.childHandles {
		_ = windows.CloseHandle(handle)
	}
	launch.childHandles = nil
}

func (launch *processLaunch) Close() {
	launch.closeSetup()
	if launch.job != 0 {
		_ = windows.CloseHandle(launch.job)
		launch.job = 0
	}
	if launch.stdout != nil {
		_ = launch.stdout.Close()
		launch.stdout = nil
	}
	if launch.stderr != nil {
		_ = launch.stderr.Close()
		launch.stderr = nil
	}
}

func startContained(ctx context.Context, command *exec.Cmd) (*containedProcess, error) {
	launch, err := prepareProcess(command)
	if err != nil {
		return nil, err
	}
	defer launch.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	information, err := launch.create()
	if err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(information.Thread)
	launch.closeSetup()
	process := &containedProcess{Stdout: launch.stdout, handle: information.Process, job: launch.job,
		stopWatch: make(chan struct{}), watchDone: make(chan struct{}), stderrDone: make(chan error, 1)}
	errorReader := launch.stderr
	launch.stdout, launch.stderr, launch.job = nil, nil, 0
	go func() {
		defer close(process.watchDone)
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-process.stopWatch:
		}
	}()
	go func() {
		writer := command.Stderr
		if writer == nil {
			writer = io.Discard
		}
		_, copyErr := io.Copy(writer, errorReader)
		_ = errorReader.Close()
		process.stderrDone <- copyErr
	}()
	return process, nil
}

func (process *containedProcess) Kill() error { return windows.TerminateJobObject(process.job, 1) }

func (process *containedProcess) Wait() error {
	process.waitOnce.Do(func() {
		_, process.waitErr = windows.WaitForSingleObject(process.handle, windows.INFINITE)
		close(process.stopWatch)
		<-process.watchDone
		process.waitErr = errors.Join(process.waitErr, <-process.stderrDone)
		var status uint32
		if err := windows.GetExitCodeProcess(process.handle, &status); err != nil {
			process.waitErr = errors.Join(process.waitErr, err)
		} else if status != 0 {
			process.waitErr = errors.Join(process.waitErr, fmt.Errorf("emulator exit status %d", status))
		}
	})
	return process.waitErr
}

func (process *containedProcess) Close() {
	process.closeOnce.Do(func() {
		_ = process.Kill()
		_ = process.Wait()
		_ = process.Stdout.Close()
		_ = windows.CloseHandle(process.handle)
		_ = windows.CloseHandle(process.job)
	})
}
