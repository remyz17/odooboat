package runtimetest

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

// syncBuffer is written by a session's output goroutine and read by the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// liveProcesses counts the container's processes named comm, ignoring
// zombies: PID 1 does not necessarily reap orphans (spike docker-exec).
const liveProcesses = `n=0; for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = "$1" ] && ! grep -q "^State:.*Z" $p/status 2>/dev/null && n=$((n+1)); done; echo $n`

// testSessions covers exec sessions: exit codes, streams, stdin, TTY,
// resize, signals, and confirmed termination (ADR 0005 §2, ADR 0010 §8).
func testSessions(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	requireCapabilities(t, engine, runtime.CapLabelsContainer, runtime.CapLabelsFilter, runtime.CapExecTTY,
		runtime.CapExecResize, runtime.CapExecSignal, runtime.CapExecCancelConfirmed)
	sessions, ok := engine.(runtime.SessionStore)
	if !ok {
		t.Fatal("adapter does not implement runtime.SessionStore")
	}
	f := newFixture(ctx, t, engine)
	id := f.container("sessions", "", nil, nil, "sleep", "600")
	if err := f.containers.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.waitState(id, runtime.ContainerRunning)

	start := func(t *testing.T, req runtime.ExecRequest) runtime.ExecSession {
		t.Helper()
		s, err := sessions.StartExec(ctx, id, req)
		if err != nil {
			t.Fatalf("start %v: %v", req.Cmd, err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	wait := func(t *testing.T, s runtime.ExecSession, within time.Duration) runtime.ExecResult {
		t.Helper()
		waitCtx, stop := context.WithTimeout(ctx, within)
		defer stop()
		result, err := s.Wait(waitCtx)
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
		return result
	}
	count := func(t *testing.T, comm string) string {
		t.Helper()
		_, out, _ := f.exec(id, "sh", "-c", liveProcesses, "sh", comm)
		return strings.TrimSpace(out)
	}

	t.Run("exit codes", func(t *testing.T) {
		var out syncBuffer
		if r := wait(t, start(t, runtime.ExecRequest{Cmd: []string{"sh", "-c", "exit 7"}, Stdout: &out, Stderr: &out}), 10*time.Second); r.ExitCode != 7 {
			t.Fatalf("exit = %d, want 7", r.ExitCode)
		}
		if r := wait(t, start(t, runtime.ExecRequest{Cmd: []string{"odooboat-no-such-command"}, Stdout: &out, Stderr: &out}), 10*time.Second); r.ExitCode != 126 && r.ExitCode != 127 {
			t.Fatalf("missing command exit = %d, want 126 or 127", r.ExitCode)
		}
	})
	t.Run("separate streams", func(t *testing.T) {
		var out, errOut syncBuffer
		r := wait(t, start(t, runtime.ExecRequest{Cmd: []string{"sh", "-c", "echo o; echo e >&2"}, Stdout: &out, Stderr: &errOut}), 10*time.Second)
		if out.String() != "o\n" || errOut.String() != "e\n" || r.Streams != runtime.StreamsSeparate {
			t.Fatalf("stdout %q, stderr %q, streams %q", out.String(), errOut.String(), r.Streams)
		}
	})
	t.Run("stdin EOF", func(t *testing.T) {
		var out syncBuffer
		r := wait(t, start(t, runtime.ExecRequest{Cmd: []string{"cat"}, Stdin: strings.NewReader("hello"), Stdout: &out}), 10*time.Second)
		if out.String() != "hello" || r.ExitCode != 0 {
			t.Fatalf("cat = %q, exit %d", out.String(), r.ExitCode)
		}
	})
	t.Run("tty size", func(t *testing.T) {
		var out syncBuffer
		r := wait(t, start(t, runtime.ExecRequest{Cmd: []string{"stty", "size"}, TTY: true,
			Size: runtime.TerminalSize{Width: 120, Height: 40}, Stdout: &out}), 10*time.Second)
		if !strings.Contains(out.String(), "40 120") || r.Streams != runtime.StreamsTTY {
			t.Fatalf("stty size = %q, streams %q", out.String(), r.Streams)
		}
	})
	t.Run("resize", func(t *testing.T) {
		var out syncBuffer
		stdin, input := io.Pipe()
		defer input.Close()
		s := start(t, runtime.ExecRequest{Cmd: []string{"sh", "-c", "read x; stty size"}, TTY: true,
			Size: runtime.TerminalSize{Width: 80, Height: 24}, Stdin: stdin, Stdout: &out})
		time.Sleep(500 * time.Millisecond)
		if err := s.Resize(ctx, runtime.TerminalSize{Width: 100, Height: 50}); err != nil {
			t.Fatal(err)
		}
		if _, err := input.Write([]byte("\n")); err != nil {
			t.Fatal(err)
		}
		wait(t, s, 10*time.Second)
		if !strings.Contains(out.String(), "50 100") {
			t.Fatalf("stty size after resize = %q", out.String())
		}
	})
	t.Run("signal reaches children", func(t *testing.T) {
		var out syncBuffer
		s := start(t, runtime.ExecRequest{Cmd: []string{"sh", "-c", "sleep 601 & wait"}, Stdout: &out, Stderr: &out})
		time.Sleep(500 * time.Millisecond)
		if n := count(t, "sleep"); n != "2" {
			t.Fatalf("live sleeps before = %s, want 2 (main and child)", n)
		}
		if err := s.Signal(ctx, runtime.SignalTerm); err != nil {
			t.Fatal(err)
		}
		wait(t, s, 5*time.Second)
		if n := count(t, "sleep"); n != "1" {
			t.Fatalf("live sleeps after = %s, want 1 (the container's own)", n)
		}
	})
	t.Run("kill confirmed", func(t *testing.T) {
		s := start(t, runtime.ExecRequest{Cmd: []string{"sh", "-c", `trap "" TERM; sleep 602`}})
		time.Sleep(500 * time.Millisecond)
		if err := s.Signal(ctx, runtime.SignalTerm); err != nil {
			t.Fatal(err)
		}
		waitCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		_, err := s.Wait(waitCtx)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait after ignored TERM = %v, want DeadlineExceeded", err)
		}
		if err := s.Signal(ctx, runtime.SignalKill); err != nil {
			t.Fatal(err)
		}
		if r := wait(t, s, 10*time.Second); r.ExitCode != 137 {
			t.Fatalf("exit after KILL = %d, want 137", r.ExitCode)
		}
	})
	t.Run("signal as another user", func(t *testing.T) {
		s := start(t, runtime.ExecRequest{Cmd: []string{"sleep", "603"}, User: "1000"})
		time.Sleep(500 * time.Millisecond)
		if err := s.Signal(ctx, runtime.SignalKill); err != nil {
			t.Fatal(err)
		}
		if r := wait(t, s, 10*time.Second); r.ExitCode != 137 {
			t.Fatalf("exit after KILL = %d, want 137", r.ExitCode)
		}
	})
}
