package hostsignals

import (
	"io"
	"os"
	"syscall"
	"testing"
)

func interruptSelf() error { return syscall.Kill(os.Getpid(), syscall.SIGINT) }

func TestSIGTERMDrainsInFlightHTTP(t *testing.T) {
	assertSignalDrainsHTTP(t, nil, func(process *os.Process, _ io.Writer) error {
		return process.Signal(syscall.SIGTERM)
	})
}

func TestInterruptDrainsInFlightHTTP(t *testing.T) {
	assertSignalDrainsHTTP(t, nil, func(process *os.Process, _ io.Writer) error {
		return process.Signal(os.Interrupt)
	})
}
