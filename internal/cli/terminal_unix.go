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
	stdin, stdout *os.File
}

// NewTerminal returns the terminal on stdin and stdout.
func NewTerminal(stdin, stdout *os.File) Terminal {
	return fileTerminal{stdin: stdin, stdout: stdout}
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
