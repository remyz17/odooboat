package ui

// Capabilities are what one output stream can show (ADR 0011 §3). They
// change decoration only, never the content of a result.
type Capabilities struct {
	Color   bool
	Unicode bool
}

// DetectCapabilities computes a stream's capabilities from whether it is a
// terminal, the process controls NO_COLOR, CLICOLOR_FORCE, TERM, and the
// locale, preferences.color, and --no-color.
func DetectCapabilities(isTerminal bool, getenv func(string) string, colorPreference, noColorFlag bool) Capabilities {
	dumb := getenv("TERM") == "dumb"
	force := getenv("CLICOLOR_FORCE") != "" && getenv("CLICOLOR_FORCE") != "0"
	return Capabilities{
		Color:   (isTerminal || force) && getenv("NO_COLOR") == "" && !dumb && colorPreference && !noColorFlag,
		Unicode: isTerminal && !dumb && !plainLocale(getenv),
	}
}

// plainLocale reports whether the effective locale is C or POSIX; an unset
// locale is C.
func plainLocale(getenv func(string) string) bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := getenv(name); value != "" {
			return value == "C" || value == "POSIX"
		}
	}
	return true
}
