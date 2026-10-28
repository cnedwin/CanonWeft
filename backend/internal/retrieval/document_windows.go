package retrieval

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

func configureDocumentProcess(command *exec.Cmd) {}

func runDocumentCommand(ctx context.Context, command *exec.Cmd) error {
	process, err := startDocumentContained(ctx, command)
	if err != nil {
		return err
	}
	defer process.Close()
	writer := command.Stdout
	if writer == nil {
		writer = io.Discard
	}
	_, copyErr := io.Copy(writer, process.Stdout)
	if copyErr != nil {
		process.Kill()
	}
	return errors.Join(copyErr, process.Wait())
}
