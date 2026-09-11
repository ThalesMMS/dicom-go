// Package clisignal provides process-signal contexts shared by command-line tools.
package clisignal

import (
	"context"
	"os"
	"os/signal"
	"sync"
)

// NotifyInterruptContext cancels on the first SIGINT and immediately restores
// the process's default signal behavior. A second SIGINT therefore forces
// termination while the caller is performing bounded protocol cleanup.
func NotifyInterruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	var once sync.Once
	restore := func() {
		once.Do(stop)
	}
	go func() {
		<-ctx.Done()
		restore()
	}()
	return ctx, restore
}
