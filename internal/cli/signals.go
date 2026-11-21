package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/remyz17/odooboat/internal/app"
)

// Interrupts routes process signals. By default a signal cancels the
// command's context; an exec session without a TTY captures them while it
// runs, to forward them to the remote command (ADR 0010 §7).
type Interrupts interface {
	// Capture delivers SIGINT, SIGTERM, SIGHUP, and SIGQUIT to the returned
	// channel, without cancelling the context, until release is called.
	Capture() (signals <-chan app.Signal, release func())
}

var routedSignals = map[os.Signal]app.Signal{
	syscall.SIGINT: app.SignalInt, syscall.SIGTERM: app.SignalTerm,
	syscall.SIGHUP: app.SignalHup, syscall.SIGQUIT: app.SignalQuit,
}

type signalRouter struct {
	mu       sync.Mutex
	captured chan app.Signal
}

// WatchSignals starts routing process signals. The returned context is
// cancelled by the first signal received while nothing captures them; stop
// releases the signals and cancels the context.
func WatchSignals(parent context.Context) (context.Context, Interrupts, func()) {
	ctx, cancel := context.WithCancel(parent)
	incoming := make(chan os.Signal, 4)
	sigs := make([]os.Signal, 0, len(routedSignals))
	for s := range routedSignals {
		sigs = append(sigs, s)
	}
	signal.Notify(incoming, sigs...)
	r := &signalRouter{}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-incoming:
				r.mu.Lock()
				captured := r.captured
				r.mu.Unlock()
				if captured == nil {
					cancel()
					continue
				}
				select {
				case captured <- routedSignals[s]:
				default: // a burst beyond the buffer adds nothing
				}
			case <-done:
				return
			}
		}
	}()
	return ctx, r, func() {
		signal.Stop(incoming)
		close(done)
		cancel()
	}
}

func (r *signalRouter) Capture() (<-chan app.Signal, func()) {
	ch := make(chan app.Signal, 4)
	r.mu.Lock()
	r.captured = ch
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		if r.captured == ch {
			r.captured = nil
		}
		r.mu.Unlock()
	}
}
