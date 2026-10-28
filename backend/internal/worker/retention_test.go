package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.temporal.io/sdk/temporal"
	"novelstudio/internal/domain"
)

func TestExpiredMaterialStopsDurableRetryWithoutExposingSource(t *testing.T) {
	fn := retentionStateActivity(func(context.Context, string) (string, error) {
		return "", fmt.Errorf("PRIVATE_SOURCE: %w", domain.ErrMaterialExpired)
	})
	_, err := fn(context.Background(), "opaque-task-id")
	var app *temporal.ApplicationError
	if !errors.As(err, &app) || !app.NonRetryable() || app.Type() != "retention-material-expired" || strings.Contains(err.Error(), "PRIVATE_SOURCE") {
		t.Fatal("expired material was retried or leaked private context", err)
	}
	transient := errors.New("database temporarily unavailable")
	if got := retentionActivityError(transient); got != transient {
		t.Fatal("ordinary outage lost its existing retry behavior")
	}
	if retentionActivityError(nil) != nil {
		t.Fatal("success converted to error")
	}
}
