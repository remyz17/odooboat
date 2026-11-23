//go:build !unix

package cli

import (
	"os"

	"github.com/remyz17/odooboat/internal/app"
	"golang.org/x/term"
)

// NewTerminal returns a non-interactive terminal: sessions run without a TTY
// on platforms other than Unix. Only stderr is detected, for progress.
func NewTerminal(_, _, stderr *os.File) Terminal { return stderrTerminal{stderr} }

type stderrTerminal struct{ stderr *os.File }

func (stderrTerminal) Interactive() bool                          { return false }
func (stderrTerminal) Size() (app.TerminalSize, error)            { return app.TerminalSize{}, nil }
func (stderrTerminal) MakeRaw() (func() error, error)             { return func() error { return nil }, nil }
func (stderrTerminal) Resizes() (<-chan app.TerminalSize, func()) { return nil, func() {} }
func (t stderrTerminal) StderrIsTerminal() bool                   { return term.IsTerminal(int(t.stderr.Fd())) }
