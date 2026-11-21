package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
)

// Runtime types used in requests are re-exported so the CLI does not depend
// on the runtime package.
type (
	TerminalSize = runtime.TerminalSize
	Signal       = runtime.Signal
	Role         = runtime.Role
	LogOptions   = runtime.LogOptions
)

const (
	SignalInt  = runtime.SignalInt
	SignalTerm = runtime.SignalTerm
	SignalHup  = runtime.SignalHup
	SignalQuit = runtime.SignalQuit

	RoleOdoo     = runtime.RoleOdoo
	RolePostgres = runtime.RolePostgres
)

// ErrTerminationUnconfirmed: the session lost its client and the remote
// command did not stop after SIGTERM then SIGKILL (ADR 0010 §7).
var ErrTerminationUnconfirmed = errors.New("remote command may still be running")

// terminateGrace and killWait bound the termination of a session whose
// client is gone (ADR 0010 §7).
var (
	terminateGrace = 10 * time.Second
	killWait       = 5 * time.Second
)

// Session carries the local side of an exec session. The CLI owns the
// terminal; the application only forwards what it is given.
type Session struct {
	// TTY allocates a remote terminal of initial Size; Resizes carries size
	// changes. Without a TTY, Signals carries the signals to forward.
	TTY     bool
	Size    TerminalSize
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Resizes <-chan TerminalSize
	Signals <-chan Signal
	// Notice receives Odooboat's own messages, never the command's output.
	Notice io.Writer
}

type ExecRequest struct {
	Selector Selector
	Role     runtime.Role
	// User overrides the image's user.
	User    string
	Cmd     []string
	Session Session
}

// SessionResult is the outcome of a remote command that ran.
type SessionResult struct {
	ExitCode int `yaml:"exitCode" json:"exitCode"`
}

// Exec runs a command in one of the environment's running containers,
// without the lock (ADR 0006 §7, ADR 0010 §1).
func (s WorkspaceService) Exec(ctx context.Context, req ExecRequest) (SessionResult, error) {
	b, release, err := s.connectBound(ctx, req.Selector)
	if err != nil {
		return SessionResult{}, err
	}
	defer release()
	return b.exec(ctx, req.Role, runtime.ExecRequest{Cmd: req.Cmd, User: req.User}, req.Session)
}

// exec runs req in the running container of role, after checking the
// capabilities the session needs.
func (b boundEnvironment) exec(ctx context.Context, role runtime.Role, req runtime.ExecRequest, s Session) (SessionResult, error) {
	caps := []runtime.Capability{runtime.CapExecSignal, runtime.CapExecCancelConfirmed}
	if s.TTY {
		caps = append(caps, runtime.CapExecTTY, runtime.CapExecResize)
	}
	if err := b.require(caps...); err != nil {
		return SessionResult{}, err
	}
	store, ok := b.engine.(runtime.SessionStore)
	if !ok {
		return SessionResult{}, fmt.Errorf("%w: runtime connection %q does not run exec sessions",
			ErrRuntimeUnsupported, b.binding.Connection.Alias)
	}
	c, err := b.running(ctx, role)
	if err != nil {
		return SessionResult{}, err
	}
	if role == runtime.RoleOdoo {
		// An exec bypasses the container's entrypoint, which resolves the
		// odoo command and sets USER (ADR 0008 A2).
		req.Cmd = append([]string{envspec.Entrypoint}, req.Cmd...)
	}
	return runSession(ctx, store, c.ID, req, s)
}

// runSession runs a command and forwards resizes and signals until it exits.
// When ctx is cancelled, the client is gone: the command is sent SIGTERM,
// then SIGKILL, and Odooboat reports whether it stopped (ADR 0010 §7).
func runSession(ctx context.Context, store runtime.SessionStore, containerID string, req runtime.ExecRequest, s Session) (SessionResult, error) {
	notice := s.Notice
	if notice == nil {
		notice = io.Discard
	}
	req.Stdin, req.Stdout, req.Stderr, req.TTY, req.Size = s.Stdin, s.Stdout, s.Stderr, s.TTY, s.Size
	session, err := store.StartExec(ctx, containerID, req)
	if err != nil {
		return SessionResult{}, err
	}
	defer session.Close()

	// Waiting and signalling outlive ctx: the remote command does not stop
	// because the client did.
	detached := context.WithoutCancel(ctx)
	waitCtx, stopWaiting := context.WithCancel(detached)
	defer stopWaiting()
	exited := make(chan sessionOutcome, 1)
	go func() {
		result, err := session.Wait(waitCtx)
		exited <- sessionOutcome{result, err}
	}()

	signals := s.Signals
	if s.TTY {
		// Ctrl-C and friends are bytes typed into the remote terminal.
		signals = nil
	}
	interrupts := 0
	for {
		select {
		case o := <-exited:
			if o.err != nil {
				return SessionResult{}, o.err
			}
			return SessionResult{ExitCode: o.result.ExitCode}, nil
		case size := <-s.Resizes:
			if err := session.Resize(detached, size); err != nil {
				fmt.Fprintln(notice, "odooboat: resize:", err)
			}
		case sig := <-signals:
			if sig == runtime.SignalInt {
				if interrupts++; interrupts > 1 {
					sig = runtime.SignalKill
				}
			}
			if err := session.Signal(detached, sig); err != nil {
				fmt.Fprintln(notice, "odooboat: forward signal:", err)
			}
		case <-ctx.Done():
			return terminate(detached, session, exited, ctx.Err())
		}
	}
}

type sessionOutcome struct {
	result runtime.ExecResult
	err    error
}

// terminate stops a session whose client is gone and reports whether the
// remote command stopped.
func terminate(ctx context.Context, session runtime.ExecSession, exited <-chan sessionOutcome, cause error) (SessionResult, error) {
	for _, step := range []struct {
		sig  runtime.Signal
		wait time.Duration
	}{{runtime.SignalTerm, terminateGrace}, {runtime.SignalKill, killWait}} {
		_ = session.Signal(ctx, step.sig)
		select {
		case o := <-exited:
			if o.err != nil {
				return SessionResult{}, errors.Join(cause, o.err)
			}
			return SessionResult{ExitCode: o.result.ExitCode}, cause
		case <-time.After(step.wait):
		}
	}
	return SessionResult{}, fmt.Errorf("%w: %w", ErrTerminationUnconfirmed, cause)
}
