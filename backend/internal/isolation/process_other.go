//go:build !windows

package isolation

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type containedProcess struct{ Stdout io.ReadCloser }

func (*containedProcess) Close()       {}
func (*containedProcess) Kill() error  { return nil }
func (*containedProcess) Wait() error  { return nil }
func cleanEnvironment(string) []string { return []string{} }
func startContained(context.Context, *exec.Cmd) (*containedProcess, error) {
	return nil, errors.New("isolation: this runtime requires Windows Job Object containment")
}
