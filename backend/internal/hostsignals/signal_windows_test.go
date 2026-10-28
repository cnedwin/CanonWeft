package hostsignals

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func interruptSelf() error {
	// The helper owns a separate hidden console; group 0 reaches only processes
	// attached to that console. Go delivers CTRL_BREAK_EVENT as os.Interrupt.
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, 0)
}

func TestInterruptDrainsInFlightHTTP(t *testing.T) {
	assertSignalDrainsHTTP(t, func(command *exec.Cmd) {
		command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	}, func(_ *os.Process, input io.Writer) error {
		_, err := io.WriteString(input, "interrupt\n")
		return err
	})
}
