package isolation

import (
	"context"
	"novelstudio/internal/executiontemp"
	"path/filepath"
)

func CleanupTemporary(ctx context.Context, runtimeRoot string) (executiontemp.Report, error) {
	if runtimeRoot == "" {
		return executiontemp.Report{}, nil
	}
	abs, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return executiontemp.Report{}, executiontemp.ErrUnavailable
	}
	return executiontemp.Sweep(ctx, filepath.Join(filepath.Dir(abs), "execution-temporary"))
}
