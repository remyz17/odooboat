package ui

// Format is a progress format (ADR 0011 §7).
type Format string

const (
	FormatAuto  Format = "auto"
	FormatTTY   Format = "tty"
	FormatPlain Format = "plain"
	FormatJSON  Format = "json"
)

// ResolveFormat resolves auto (ADR 0011 §7): tty when stderr can be
// redrawn, else json when the result is json, else plain.
func ResolveFormat(requested Format, live, resultJSON bool) Format {
	switch {
	case requested != FormatAuto:
		return requested
	case live:
		return FormatTTY
	case resultJSON:
		return FormatJSON
	}
	return FormatPlain
}

// LiveRedraw reports whether a stream can be redrawn: it is a terminal,
// TERM is not dumb, and CI is unset (ADR 0011 §3).
func LiveRedraw(isTerminal bool, getenv func(string) string) bool {
	return isTerminal && getenv("TERM") != "dumb" && getenv("CI") == ""
}
