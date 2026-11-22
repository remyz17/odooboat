package ui

import (
	"bytes"
	"io"
	"sync"
)

// Writer guards stderr: after the first write error every write is dropped
// and reported as successful, so a closed stream (EPIPE) never blocks or
// fails the process (ADR 0011 §6). While raw, "\n" is written as "\r\n"
// for a terminal in raw mode (ADR 0011 §10).
type Writer struct {
	mu     sync.Mutex
	w      io.Writer
	failed bool
	raw    bool
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return len(p), nil
	}
	out := p
	if w.raw {
		out = bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))
	}
	if _, err := w.w.Write(out); err != nil {
		w.failed = true
	}
	return len(p), nil
}

// SetRaw switches "\n" translation on or off.
func (w *Writer) SetRaw(raw bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.raw = raw
}
