// Package hostsignals selects the host signals that request a graceful shutdown.
package hostsignals

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// NotifyContext preserves parent cancellation and returns a stop function that
// releases the process-wide signal subscription when the server has stopped.
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
