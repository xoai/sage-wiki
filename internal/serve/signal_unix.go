//go:build unix

package serve

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func signalNotify() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
