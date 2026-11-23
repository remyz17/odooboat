package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

// fakeSession exits when told to, or on a signal listed in exitOn.
type fakeSession struct {
	mu       sync.Mutex
	exitOn   map[runtime.Signal]int
	done     chan struct{}
	once     sync.Once
	exitCode int
	signals  []runtime.Signal
	resizes  []runtime.TerminalSize
	closed   bool
	// resizeErr fails every resize.
	resizeErr error
}

func newFakeSession(exitOn map[runtime.Signal]int) *fakeSession {
	return &fakeSession{exitOn: exitOn, done: make(chan struct{})}
}

func (s *fakeSession) exit(code int) {
	s.once.Do(func() {
		s.mu.Lock()
		s.exitCode = code
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *fakeSession) Resize(_ context.Context, size runtime.TerminalSize) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, size)
	return s.resizeErr
}

func (s *fakeSession) Signal(_ context.Context, sig runtime.Signal) error {
	s.mu.Lock()
	s.signals = append(s.signals, sig)
	code, exits := s.exitOn[sig]
	s.mu.Unlock()
	if exits {
		s.exit(code)
	}
	return nil
}

func (s *fakeSession) Wait(ctx context.Context) (runtime.ExecResult, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return runtime.ExecResult{ExitCode: s.exitCode, Streams: runtime.StreamsSeparate}, nil
	case <-ctx.Done():
		return runtime.ExecResult{}, ctx.Err()
	}
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeSession) recorded() ([]runtime.Signal, []runtime.TerminalSize, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.signals), slices.Clone(s.resizes), s.closed
}

// sessionCall records a started session.
type sessionCall struct {
	containerID string
	req         runtime.ExecRequest
}

func (e *resourceEngine) StartExec(_ context.Context, containerID string, req runtime.ExecRequest) (runtime.ExecSession, error) {
	if c := e.byID(containerID); c == nil || c.State != runtime.ContainerRunning {
		return nil, fmt.Errorf("container %s is not running", containerID)
	}
	e.sessionCalls = append(e.sessionCalls, sessionCall{containerID: containerID, req: req})
	if e.nextSession == nil {
		s := newFakeSession(nil)
		s.exit(0)
		return s, nil
	}
	return e.nextSession, nil
}

func shortGrace(t *testing.T) {
	t.Helper()
	terminateGrace, killWait = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { terminateGrace, killWait = 10*time.Second, 5*time.Second })
}

type execRun struct {
	result SessionResult
	err    error
}

func (f *lifecycleFixture) startExec(ctx context.Context, s Session) chan execRun {
	done := make(chan execRun, 1)
	go func() {
		result, err := f.service.Exec(ctx, ExecRequest{Selector: f.selector, Role: runtime.RoleOdoo, Cmd: []string{"true"}, Session: s})
		done <- execRun{result, err}
	}()
	return done
}

func receive(t *testing.T, done chan execRun) execRun {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("exec did not return")
		return execRun{}
	}
}

func TestExecPassesExitCode(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	f.engine.nextSession = newFakeSession(nil)
	f.engine.nextSession.exit(7)
	r := receive(t, f.startExec(context.Background(), Session{}))
	if r.err != nil || r.result.ExitCode != 7 {
		t.Fatalf("exec = %+v, %v", r.result, r.err)
	}
	call := f.engine.sessionCalls[len(f.engine.sessionCalls)-1]
	if call.containerID != f.engine.containers[f.name(first, runtime.RoleOdoo)].ID || !slices.Equal(call.req.Cmd, []string{"/opt/odooboat/bin/entrypoint", "true"}) {
		t.Fatalf("session call = %+v", call)
	}
	if _, _, closed := f.engine.nextSession.recorded(); !closed {
		t.Fatal("session was not closed")
	}
}

func TestExecForwardsResizes(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	session := newFakeSession(nil)
	f.engine.nextSession = session
	resizes := make(chan TerminalSize)
	done := f.startExec(context.Background(), Session{TTY: true, Size: TerminalSize{Width: 120, Height: 40}, Resizes: resizes})
	resizes <- TerminalSize{Width: 80, Height: 24}
	session.exit(0)
	receive(t, done)
	if _, got, _ := session.recorded(); !slices.Equal(got, []runtime.TerminalSize{{Width: 80, Height: 24}}) {
		t.Fatalf("resizes = %v", got)
	}
	if req := f.engine.sessionCalls[len(f.engine.sessionCalls)-1].req; !req.TTY || req.Size != (runtime.TerminalSize{Width: 120, Height: 40}) {
		t.Fatalf("request TTY %v size %+v", req.TTY, req.Size)
	}
}

func TestExecForwardsSignals(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	session := newFakeSession(map[runtime.Signal]int{runtime.SignalKill: 137})
	f.engine.nextSession = session
	signals := make(chan Signal)
	done := f.startExec(context.Background(), Session{Signals: signals})
	signals <- runtime.SignalTerm
	signals <- runtime.SignalInt
	signals <- runtime.SignalInt
	r := receive(t, done)
	if got, _, _ := session.recorded(); !slices.Equal(got, []runtime.Signal{runtime.SignalTerm, runtime.SignalInt, runtime.SignalKill}) {
		t.Fatalf("signals = %v", got)
	}
	if r.err != nil || r.result.ExitCode != 137 {
		t.Fatalf("exec = %+v, %v", r.result, r.err)
	}
}

func TestExecIgnoresSignalsWithTTY(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	session := newFakeSession(nil)
	f.engine.nextSession = session
	signals := make(chan Signal, 1)
	done := f.startExec(context.Background(), Session{TTY: true, Signals: signals})
	signals <- runtime.SignalInt
	time.Sleep(20 * time.Millisecond)
	session.exit(0)
	receive(t, done)
	if got, _, _ := session.recorded(); len(got) != 0 {
		t.Fatalf("signals forwarded with a TTY: %v", got)
	}
}

func TestExecCancelledTerminates(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	shortGrace(t)
	session := newFakeSession(map[runtime.Signal]int{runtime.SignalTerm: 143})
	f.engine.nextSession = session
	ctx, cancel := context.WithCancel(context.Background())
	done := f.startExec(ctx, Session{})
	time.Sleep(20 * time.Millisecond)
	cancel()
	r := receive(t, done)
	if got, _, _ := session.recorded(); !slices.Equal(got, []runtime.Signal{runtime.SignalTerm}) {
		t.Fatalf("signals = %v", got)
	}
	if !errors.Is(r.err, context.Canceled) || r.result.ExitCode != 143 {
		t.Fatalf("exec = %+v, %v", r.result, r.err)
	}
}

func TestExecCancelledKillsAfterGrace(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	shortGrace(t)
	session := newFakeSession(map[runtime.Signal]int{runtime.SignalKill: 137})
	f.engine.nextSession = session
	ctx, cancel := context.WithCancel(context.Background())
	done := f.startExec(ctx, Session{})
	time.Sleep(20 * time.Millisecond)
	cancel()
	r := receive(t, done)
	if got, _, _ := session.recorded(); !slices.Equal(got, []runtime.Signal{runtime.SignalTerm, runtime.SignalKill}) {
		t.Fatalf("signals = %v", got)
	}
	if !errors.Is(r.err, context.Canceled) || errors.Is(r.err, ErrTerminationUnconfirmed) || r.result.ExitCode != 137 {
		t.Fatalf("exec = %+v, %v", r.result, r.err)
	}
}

func TestExecCancelledUnconfirmed(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	shortGrace(t)
	f.engine.nextSession = newFakeSession(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := f.startExec(ctx, Session{})
	time.Sleep(20 * time.Millisecond)
	cancel()
	if r := receive(t, done); !errors.Is(r.err, ErrTerminationUnconfirmed) {
		t.Fatalf("exec = %+v, %v; want ErrTerminationUnconfirmed", r.result, r.err)
	}
}

func TestExecReturnsWhileStdinStaysOpen(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	stdin, input := io.Pipe()
	defer input.Close()
	session := newFakeSession(nil)
	f.engine.nextSession = session
	done := f.startExec(context.Background(), Session{Stdin: stdin})
	session.exit(0)
	if r := receive(t, done); r.err != nil {
		t.Fatal(r.err)
	}
	if req := f.engine.sessionCalls[len(f.engine.sessionCalls)-1].req; req.Stdin == nil {
		t.Fatal("stdin was not attached")
	}
}

func TestExecRequiresRunningAndCapabilities(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	exec := func(s Session) error {
		_, err := f.service.Exec(context.Background(), ExecRequest{Selector: f.selector, Role: runtime.RoleOdoo, Cmd: []string{"true"}, Session: s})
		return err
	}
	odoo := f.engine.containers[f.name(first, runtime.RoleOdoo)]
	odoo.State = runtime.ContainerExited
	if err := exec(Session{}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped odoo: %v, want ErrNotRunning", err)
	}
	odoo.State = runtime.ContainerRunning

	without := func(c runtime.Capability) runtime.Capabilities {
		return slices.DeleteFunc(slices.Clone(accessCapabilities), func(x runtime.Capability) bool { return x == c })
	}
	f.engine.capabilities = without(runtime.CapExecTTY)
	if err := exec(Session{TTY: true}); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("TTY without exec.tty: %v", err)
	}
	if err := exec(Session{}); err != nil {
		t.Fatalf("no TTY needs no exec.tty: %v", err)
	}
	f.engine.capabilities = without(runtime.CapExecSignal)
	if err := exec(Session{}); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("without exec.signal: %v", err)
	}
}

// Commands in the Odoo container run through the image's entrypoint, which
// resolves the odoo command and sets USER (ADR 0008 A2); exec bypasses the
// container's entrypoint otherwise.
func TestExecRunsThroughEntrypointOnlyInOdoo(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	for role, want := range map[runtime.Role][]string{
		runtime.RoleOdoo:     {"/opt/odooboat/bin/entrypoint", "odoo", "--version"},
		runtime.RolePostgres: {"odoo", "--version"},
	} {
		if _, err := f.service.Exec(context.Background(), ExecRequest{Selector: f.selector, Role: role, Cmd: []string{"odoo", "--version"}}); err != nil {
			t.Fatal(err)
		}
		if got := f.engine.sessionCalls[len(f.engine.sessionCalls)-1].req.Cmd; !slices.Equal(got, want) {
			t.Errorf("%s: command = %q, want %q", role, got, want)
		}
	}
}

func TestSessionReportsDegradedResize(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	session := newFakeSession(nil)
	session.resizeErr = errors.New("no such exec")
	f.engine.nextSession = session
	resizes := make(chan TerminalSize)
	var r recorder
	done := f.startExec(context.Background(), Session{TTY: true, Resizes: resizes, Reporter: &r})
	resizes <- TerminalSize{Width: 80, Height: 24}
	resizes <- TerminalSize{Width: 81, Height: 24} // the first one was handled
	session.exit(0)
	if run := receive(t, done); run.err != nil {
		t.Fatalf("session failed: %v", run.err)
	}
	events := r.all()
	if len(events) == 0 {
		t.Fatal("no event")
	}
	degraded, ok := events[0].(SessionDegraded)
	if !ok || degraded.Operation != SessionResize || degraded.Err == nil {
		t.Fatalf("events = %#v", events)
	}
}
