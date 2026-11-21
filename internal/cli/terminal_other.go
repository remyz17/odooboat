//go:build !unix

package cli

import "os"

// NewTerminal returns a non-interactive terminal: sessions run without a TTY
// on platforms other than Unix.
func NewTerminal(_, _ *os.File) Terminal { return noTerminal{} }
