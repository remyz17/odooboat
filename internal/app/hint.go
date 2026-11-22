package app

import "errors"

// hinted carries the action that would resolve an error, as data next to
// the message rather than inside it (ADR 0011 §9).
type hinted struct {
	err  error
	hint string
}

func (h *hinted) Error() string { return h.err.Error() }
func (h *hinted) Unwrap() error { return h.err }

// WithHint attaches hint to err without changing its message or what it
// wraps.
func WithHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	return &hinted{err: err, hint: hint}
}

// Hint returns the outermost hint attached to err, or "".
func Hint(err error) string {
	var h *hinted
	if errors.As(err, &h) {
		return h.hint
	}
	return ""
}
