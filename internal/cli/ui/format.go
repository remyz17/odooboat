package ui

// Format is a progress format (ADR 0011 §7).
type Format string

const (
	FormatAuto  Format = "auto"
	FormatPlain Format = "plain"
	FormatJSON  Format = "json"
)

// ResolveFormat resolves auto: json when stderr cannot redraw and the result
// is json, plain otherwise. Where it would resolve to tty, it resolves to
// plain until the tty renderer exists (ADR 0011 §11).
func ResolveFormat(requested Format, live, resultJSON bool) Format {
	if requested != FormatAuto {
		return requested
	}
	if !live && resultJSON {
		return FormatJSON
	}
	return FormatPlain
}

// LiveRedraw reports whether a stream can be redrawn: it is a terminal,
// TERM is not dumb, and CI is unset (ADR 0011 §3).
func LiveRedraw(isTerminal bool, getenv func(string) string) bool {
	return isTerminal && getenv("TERM") != "dumb" && getenv("CI") == ""
}
