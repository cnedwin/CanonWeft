//go:build !windows

package retrieval

import (
	"context"
	"os/exec"
)

func configureDocumentProcess(command *exec.Cmd) {}

func runDocumentCommand(ctx context.Context, command *exec.Cmd) error { return command.Run() }
