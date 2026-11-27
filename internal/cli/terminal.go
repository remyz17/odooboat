package cli

import "github.com/remyz17/odooboat/internal/app"

// Terminal is the local terminal an exec session may take over (ADR 0010
// §3). Raw mode, size, and resize events stay in the CLI.
type Terminal interface {
	// Interactive reports whether both stdin and stdout are terminals.
	Interactive() bool
	Size() (app.TerminalSize, error)
	// MakeRaw switches stdin to raw mode; restore must be called on every
	// exit path.
	MakeRaw() (restore func() error, err error)
	// Resizes delivers the terminal's new size after each change, until stop.
	Resizes() (sizes <-chan app.TerminalSize, stop func())
	// StderrIsTerminal reports whether stderr is a terminal (ADR 0011 §3).
	StderrIsTerminal() bool
	// StderrSize is the size of the terminal on stderr; 0 means unknown.
	StderrSize() (width, height int)
	// StdoutIsTerminal reports whether stdout is a terminal (ADR 0011 §3).
	StdoutIsTerminal() bool
}

// noTerminal is used when no terminal is available, as in tests.
type noTerminal struct{}

func (noTerminal) Interactive() bool                          { return false }
func (noTerminal) Size() (app.TerminalSize, error)            { return app.TerminalSize{}, nil }
func (noTerminal) MakeRaw() (func() error, error)             { return func() error { return nil }, nil }
func (noTerminal) Resizes() (<-chan app.TerminalSize, func()) { return nil, func() {} }
func (noTerminal) StderrIsTerminal() bool                     { return false }
func (noTerminal) StderrSize() (int, int)                     { return 0, 0 }
func (noTerminal) StdoutIsTerminal() bool                     { return false }
