//go:build !unix

package cli

import (
	"os"

	"github.com/remyz17/odooboat/internal/app"
	"golang.org/x/term"
)

// NewTerminal returns a non-interactive terminal: sessions run without a TTY
// on platforms other than Unix. Only stdout and stderr are detected, for
// results and progress.
func NewTerminal(_, stdout, stderr *os.File) Terminal { return stderrTerminal{stdout, stderr} }

type stderrTerminal struct{ stdout, stderr *os.File }

func (stderrTerminal) Interactive() bool                          { return false }
func (stderrTerminal) Size() (app.TerminalSize, error)            { return app.TerminalSize{}, nil }
func (stderrTerminal) MakeRaw() (func() error, error)             { return func() error { return nil }, nil }
func (stderrTerminal) Resizes() (<-chan app.TerminalSize, func()) { return nil, func() {} }
func (t stderrTerminal) StderrIsTerminal() bool                   { return term.IsTerminal(int(t.stderr.Fd())) }
func (t stderrTerminal) StdoutIsTerminal() bool                   { return term.IsTerminal(int(t.stdout.Fd())) }
