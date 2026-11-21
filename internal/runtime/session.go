package runtime

import "context"

// TerminalSize is a terminal's size in character cells.
type TerminalSize struct {
	Width  uint
	Height uint
}

// Signal names a signal an exec session can deliver (exec.signal).
type Signal string

const (
	SignalInt  Signal = "INT"
	SignalTerm Signal = "TERM"
	SignalHup  Signal = "HUP"
	SignalQuit Signal = "QUIT"
	SignalKill Signal = "KILL"
)

// Valid reports whether s is one of the signals sessions deliver.
func (s Signal) Valid() bool {
	switch s {
	case SignalInt, SignalTerm, SignalHup, SignalQuit, SignalKill:
		return true
	}
	return false
}

// StreamIdentity declares how far an output's streams can be told apart
// (ADR 0005 §2): a TTY or a backend that merges them cannot be presented as
// separate stdout and stderr.
type StreamIdentity string

const (
	StreamsSeparate StreamIdentity = "separate"
	StreamsMerged   StreamIdentity = "merged"
	StreamsTTY      StreamIdentity = "tty"
)

// ExecSession is a command running in a container (ADR 0010 §8). Closing it
// releases the connection and does not stop the command: stopping is a
// signal, and its confirmation is Wait returning.
type ExecSession interface {
	// Resize changes the session's terminal size (exec.resize).
	Resize(ctx context.Context, size TerminalSize) error
	// Signal delivers sig to the command and its descendants (exec.signal).
	Signal(ctx context.Context, sig Signal) error
	// Wait returns once the command has exited and its output is drained, or
	// with the context's error. It may be called again after a context error.
	Wait(ctx context.Context) (ExecResult, error)
	Close() error
}

// SessionStore starts exec sessions with stdin, a TTY, resize, and signals.
type SessionStore interface {
	StartExec(ctx context.Context, containerID string, req ExecRequest) (ExecSession, error)
}
