package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/remyz17/odooboat/internal/runtime"
)

var _ runtime.SessionStore = (*Engine)(nil)

// markerVar tags an exec's processes so they can be signalled: Docker gives
// no exec PID in the container's namespace, and closing the attach stops
// nothing (spike docker-exec, ADR 0010 §7). Descendants inherit it.
const markerVar = "ODOOBOAT_EXEC"

// signalScript signals every process carrying the marker given as $1 with
// signal $2. It needs only sh, tr, grep, and kill, so it runs in busybox and
// Debian images without procps. It never matches itself: its own
// environment lacks the marker.
const signalScript = `for p in /proc/[0-9]*; do { tr "\0" "\n" < "$p/environ"; } 2>/dev/null | grep -qx "` + markerVar +
	`=$1" && kill -s "$2" "${p#/proc/}"; done; true`

// settleWait bounds the wait for the engine to report an exec as finished
// once its output has ended.
const settleWait = 2 * time.Second

type session struct {
	engine      *Engine
	containerID string
	execID      string
	user        string
	marker      string
	tty         bool
	conn        client.HijackedResponse
	// output is closed when the output stream has been fully copied.
	output    chan struct{}
	outputErr error
}

func newMarker() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (e *Engine) StartExec(ctx context.Context, containerID string, req runtime.ExecRequest) (runtime.ExecSession, error) {
	marker, err := newMarker()
	if err != nil {
		return nil, fmt.Errorf("exec in container %s: %w", containerID, err)
	}
	options := client.ExecCreateOptions{
		User: req.User, Cmd: req.Cmd, Env: append(append([]string(nil), req.Env...), markerVar+"="+marker),
		TTY: req.TTY, AttachStdin: req.Stdin != nil, AttachStdout: true, AttachStderr: true,
	}
	attach := client.ExecAttachOptions{TTY: req.TTY}
	if req.TTY {
		options.ConsoleSize = client.ConsoleSize{Height: req.Size.Height, Width: req.Size.Width}
		attach.ConsoleSize = options.ConsoleSize
	}
	created, err := e.client.ExecCreate(ctx, containerID, options)
	if err != nil {
		return nil, classify(err, "exec in container %s", containerID)
	}
	attached, err := e.client.ExecAttach(ctx, created.ID, attach)
	if err != nil {
		return nil, classify(err, "attach to exec in container %s", containerID)
	}
	s := &session{engine: e, containerID: containerID, execID: created.ID, user: req.User, marker: marker,
		tty: req.TTY, conn: attached.HijackedResponse, output: make(chan struct{})}
	go func() {
		defer close(s.output)
		stdout, stderr := orDiscard(req.Stdout), orDiscard(req.Stderr)
		if req.TTY {
			_, s.outputErr = io.Copy(stdout, attached.Reader)
		} else {
			// Without a TTY the engine multiplexes both streams; stdcopy
			// separates them as they arrive (streams are bytes, ADR 0005 §2).
			_, s.outputErr = stdcopy.StdCopy(stdout, stderr, attached.Reader)
		}
	}()
	if req.Stdin != nil {
		// Never awaited: a terminal or pipe may stay open after the command
		// ends. With a TTY, closing stdin would end the whole attach.
		go func() {
			_, _ = io.Copy(attached.Conn, req.Stdin)
			if !req.TTY {
				_ = attached.CloseWrite()
			}
		}()
	}
	return s, nil
}

func (s *session) Resize(ctx context.Context, size runtime.TerminalSize) error {
	if _, err := s.engine.client.ExecResize(ctx, s.execID, client.ExecResizeOptions{Height: size.Height, Width: size.Width}); err != nil {
		return classify(err, "resize exec in container %s", s.containerID)
	}
	return nil
}

// Signal runs the signalling script as the session's user: Docker's default
// capabilities exclude CAP_SYS_PTRACE, so even root cannot read another
// user's /proc/<pid>/environ (spike docker-exec).
func (s *session) Signal(ctx context.Context, sig runtime.Signal) error {
	if !sig.Valid() {
		return fmt.Errorf("signal exec in container %s: unsupported signal %q", s.containerID, sig)
	}
	result, err := s.engine.ExecContainer(ctx, s.containerID, runtime.ExecRequest{
		User: s.user, Cmd: []string{"sh", "-c", signalScript, "sh", s.marker, string(sig)},
	})
	if err != nil {
		return fmt.Errorf("signal exec in container %s: %w", s.containerID, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("signal exec in container %s: signalling command exited %d", s.containerID, result.ExitCode)
	}
	return nil
}

func (s *session) Wait(ctx context.Context) (runtime.ExecResult, error) {
	select {
	case <-s.output:
	case <-ctx.Done():
		return runtime.ExecResult{}, ctx.Err()
	}
	if s.outputErr != nil {
		return runtime.ExecResult{}, fmt.Errorf("read exec output in container %s: %w", s.containerID, s.outputErr)
	}
	streams := runtime.StreamsSeparate
	if s.tty {
		streams = runtime.StreamsTTY
	}
	deadline := time.Now().Add(settleWait)
	for {
		inspected, err := s.engine.client.ExecInspect(ctx, s.execID, client.ExecInspectOptions{})
		if err != nil {
			return runtime.ExecResult{}, classify(err, "inspect exec in container %s", s.containerID)
		}
		if !inspected.Running {
			return runtime.ExecResult{ExitCode: inspected.ExitCode, Streams: streams}, nil
		}
		if time.Now().After(deadline) {
			return runtime.ExecResult{}, fmt.Errorf("exec in container %s: output ended before the command", s.containerID)
		}
		select {
		case <-ctx.Done():
			return runtime.ExecResult{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s *session) Close() error {
	s.conn.Close()
	return nil
}
