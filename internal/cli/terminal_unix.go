//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/remyz17/odooboat/internal/app"
	"golang.org/x/term"
)

type fileTerminal struct {
	stdin, stdout, stderr *os.File
}

// NewTerminal returns the terminal on stdin, stdout, and stderr.
func NewTerminal(stdin, stdout, stderr *os.File) Terminal {
	return fileTerminal{stdin: stdin, stdout: stdout, stderr: stderr}
}

func (t fileTerminal) StderrIsTerminal() bool { return term.IsTerminal(int(t.stderr.Fd())) }
func (t fileTerminal) StdoutIsTerminal() bool { return term.IsTerminal(int(t.stdout.Fd())) }

func (t fileTerminal) StderrSize() (width, height int) {
	width, height, err := term.GetSize(int(t.stderr.Fd()))
	if err != nil {
		return 0, 0
	}
	return width, height
}

func (t fileTerminal) Interactive() bool {
	return term.IsTerminal(int(t.stdin.Fd())) && term.IsTerminal(int(t.stdout.Fd()))
}

func (t fileTerminal) Size() (app.TerminalSize, error) {
	width, height, err := term.GetSize(int(t.stdout.Fd()))
	if err != nil {
		return app.TerminalSize{}, err
	}
	return app.TerminalSize{Width: uint(width), Height: uint(height)}, nil
}

func (t fileTerminal) MakeRaw() (func() error, error) {
	fd := int(t.stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, state) }, nil
}

func (t fileTerminal) Resizes() (<-chan app.TerminalSize, func()) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	sizes := make(chan app.TerminalSize, 1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-winch:
				if size, err := t.Size(); err == nil {
					select {
					case sizes <- size:
					case <-done:
						return
					}
				}
			case <-done:
				return
			}
		}
	}()
	return sizes, func() {
		signal.Stop(winch)
		close(done)
	}
}
