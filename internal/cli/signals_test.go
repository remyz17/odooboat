//go:build unix

package cli

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

func raise(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
}

func TestWatchSignalsCancelsWhenNotCaptured(t *testing.T) {
	ctx, _, stop := WatchSignals(context.Background())
	defer stop()
	raise(t, syscall.SIGHUP)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an uncaptured SIGHUP did not cancel the context")
	}
}

func TestWatchSignalsCaptured(t *testing.T) {
	ctx, interrupts, stop := WatchSignals(context.Background())
	defer stop()
	signals, release := interrupts.Capture()
	raise(t, syscall.SIGINT)
	select {
	case sig := <-signals:
		if sig != app.SignalInt {
			t.Fatalf("captured %q, want INT", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("captured SIGINT was not delivered")
	}
	if ctx.Err() != nil {
		t.Fatal("a captured signal cancelled the context")
	}
	release()
	raise(t, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM after release did not cancel the context")
	}
}

func TestSecondInterruptExits(t *testing.T) {
	exited := make(chan int, 1)
	ctx, interrupts, stop := watchSignals(context.Background(), func(code int) { exited <- code })
	defer stop()

	signals, release := interrupts.Capture()
	// Pending signals of one kind merge, so each is raised after the last
	// one arrived.
	for range 2 {
		raise(t, syscall.SIGHUP)
		select {
		case <-signals:
		case <-time.After(2 * time.Second):
			t.Fatal("captured SIGHUP was not delivered")
		}
	}
	select {
	case code := <-exited:
		t.Fatalf("exited %d while signals were captured", code)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	raise(t, syscall.SIGHUP)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an uncaptured SIGHUP did not cancel the context")
	}
	raise(t, syscall.SIGHUP)
	select {
	case code := <-exited:
		if code != 130 {
			t.Fatalf("exit code %d, want 130", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second uncaptured signal did not exit")
	}
}

// A TTY session captures no signal, but its termination (ADR 0010 §7) must
// not be cut short by a second one.
func TestHoldSuppressesSecondInterruptExit(t *testing.T) {
	exited := make(chan int, 1)
	ctx, interrupts, stop := watchSignals(context.Background(), func(code int) { exited <- code })
	defer stop()

	release := interrupts.Hold()
	raise(t, syscall.SIGHUP)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a signal during a held session did not cancel the context")
	}
	raise(t, syscall.SIGHUP)
	select {
	case code := <-exited:
		t.Fatalf("exited %d while a session held the exit", code)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	raise(t, syscall.SIGHUP)
	select {
	case code := <-exited:
		if code != ExitInterrupted {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("after release, a second signal did not exit")
	}
}
