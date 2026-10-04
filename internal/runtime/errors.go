package runtime

import (
	"errors"
	"fmt"
	"strings"
)

// Error classes. The CLI maps these onto process exit codes.
var (
	ErrUnavailable = errors.New("runtime is unavailable")
	ErrUnsupported = errors.New("runtime feature is unsupported")
)

// ConnectionError reports a runtime connection that could not be resolved or reached.
type ConnectionError struct {
	Alias    string
	Endpoint string
	Rule     string
	Hint     string

	class error
}

func NewConnectionError(class error, alias, endpoint, rule, hint string) *ConnectionError {
	return &ConnectionError{Alias: alias, Endpoint: endpoint, Rule: rule, Hint: hint, class: class}
}

func (e *ConnectionError) Error() string {
	var b strings.Builder
	if e.Alias != "" {
		fmt.Fprintf(&b, "runtime connection %q: ", e.Alias)
	}
	b.WriteString(e.Rule)
	if e.Endpoint != "" {
		fmt.Fprintf(&b, " (endpoint %s)", e.Endpoint)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "; %s", e.Hint)
	}
	return b.String()
}

func (e *ConnectionError) Unwrap() error { return e.class }
