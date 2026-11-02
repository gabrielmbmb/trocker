package native

import (
	"context"
	"os"
	"os/signal"
)

type signalCause struct{ signal os.Signal }

func (e *signalCause) Error() string { return "process interrupted" }

// NotifyContext retains the received signal for forwarding, unlike a plain
// cancellation context. Call stop to release the signal subscription.
func NotifyContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, forwardedSignals()...)
	go func() {
		select {
		case received := <-signals:
			cancel(&signalCause{signal: received})
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(signals); cancel(context.Canceled) }
}
