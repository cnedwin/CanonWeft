package retrieval

import (
	"context"
	"novelstudio/internal/executiontemp"
	"path/filepath"
)

func CleanupTemporary(ctx context.Context) (executiontemp.Report, error) {
	_, _, root, err := documentPaths()
	if err != nil {
		return executiontemp.Report{}, err
	}
	return executiontemp.Sweep(ctx, filepath.Join(root, "managed-v1"))
}
