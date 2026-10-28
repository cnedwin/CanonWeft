package worker

import (
	"context"
	"errors"

	"go.temporal.io/sdk/temporal"
	"novelstudio/internal/domain"
)

// Expired source material cannot become available by retrying. Do not include
// wrapped causes in Temporal history; they may contain private source context.
func retentionActivityError(err error) error {
	if errors.Is(err, domain.ErrMaterialExpired) {
		return temporal.NewNonRetryableApplicationError("运行材料已到保留期限；保留已有费用记录", "retention-material-expired", nil)
	}
	return err
}

func retentionStateActivity(fn func(context.Context, string) (string, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, id string) (string, error) {
		state, err := fn(ctx, id)
		return state, retentionActivityError(err)
	}
}
