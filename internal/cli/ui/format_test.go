package ui

import "testing"

func TestResolveFormat(t *testing.T) {
	for _, c := range []struct {
		requested        Format
		live, resultJSON bool
		want             Format
	}{
		{FormatAuto, true, false, FormatPlain},
		{FormatAuto, true, true, FormatPlain}, // tty until step 3
		{FormatAuto, false, true, FormatJSON},
		{FormatAuto, false, false, FormatPlain},
		{FormatPlain, false, true, FormatPlain},
		{FormatJSON, true, false, FormatJSON},
	} {
		if got := ResolveFormat(c.requested, c.live, c.resultJSON); got != c.want {
			t.Errorf("ResolveFormat(%s, live %v, json %v) = %s, want %s", c.requested, c.live, c.resultJSON, got, c.want)
		}
	}
}

func TestLiveRedraw(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}
	if !LiveRedraw(true, env(map[string]string{"TERM": "xterm"})) {
		t.Error("a terminal should redraw")
	}
	for name, c := range map[string]struct {
		tty bool
		env map[string]string
	}{
		"not a terminal": {false, map[string]string{"TERM": "xterm"}},
		"TERM=dumb":      {true, map[string]string{"TERM": "dumb"}},
		"CI set":         {true, map[string]string{"TERM": "xterm", "CI": "true"}},
	} {
		if LiveRedraw(c.tty, env(c.env)) {
			t.Errorf("%s: LiveRedraw = true", name)
		}
	}
}
