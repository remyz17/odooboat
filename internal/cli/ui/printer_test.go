package ui

import (
	"bytes"
	"maps"
	"regexp"
	"strings"
	"testing"
)

func TestDetectCapabilities(t *testing.T) {
	utf8 := map[string]string{"LANG": "en_US.UTF-8"}
	with := func(base map[string]string, kv ...string) map[string]string {
		env := maps.Clone(base)
		for i := 0; i+1 < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}
	for _, c := range []struct {
		name            string
		terminal        bool
		env             map[string]string
		colorPreference bool
		noColorFlag     bool
		want            Capabilities
	}{
		{"utf-8 terminal", true, utf8, true, false, Capabilities{Color: true, Unicode: true}},
		{"not a terminal", false, utf8, true, false, Capabilities{}},
		{"NO_COLOR keeps symbols", true, with(utf8, "NO_COLOR", "1"), true, false, Capabilities{Unicode: true}},
		{"empty NO_COLOR is unset", true, with(utf8, "NO_COLOR", ""), true, false, Capabilities{Color: true, Unicode: true}},
		{"CLICOLOR_FORCE on a pipe", false, with(utf8, "CLICOLOR_FORCE", "1"), true, false, Capabilities{Color: true}},
		{"CLICOLOR_FORCE=0", false, with(utf8, "CLICOLOR_FORCE", "0"), true, false, Capabilities{}},
		{"CLICOLOR_FORCE and --no-color", false, with(utf8, "CLICOLOR_FORCE", "1"), true, true, Capabilities{}},
		{"CLICOLOR_FORCE and NO_COLOR", true, with(utf8, "CLICOLOR_FORCE", "1", "NO_COLOR", "1"), true, false, Capabilities{Unicode: true}},
		{"TERM=dumb", true, with(utf8, "TERM", "dumb"), true, false, Capabilities{}},
		{"preference off", true, utf8, false, false, Capabilities{Unicode: true}},
		{"--no-color", true, utf8, true, true, Capabilities{Unicode: true}},
		{"LANG=C", true, map[string]string{"LANG": "C"}, true, false, Capabilities{Color: true}},
		{"LANG=POSIX", true, map[string]string{"LANG": "POSIX"}, true, false, Capabilities{Color: true}},
		{"C.UTF-8", true, map[string]string{"LANG": "C.UTF-8"}, true, false, Capabilities{Color: true, Unicode: true}},
		{"no locale", true, map[string]string{}, true, false, Capabilities{Color: true}},
		{"LC_ALL wins", true, with(utf8, "LC_ALL", "C"), true, false, Capabilities{Color: true}},
		{"LC_CTYPE before LANG", true, map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, true, false, Capabilities{Color: true, Unicode: true}},
	} {
		got := DetectCapabilities(c.terminal, func(k string) string { return c.env[k] }, c.colorPreference, c.noColorFlag)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func printed(caps Capabilities, home string, f func(p *Printer)) string {
	var b bytes.Buffer
	f(NewPrinter(&b, caps, home))
	return b.String()
}

func TestPrinterRows(t *testing.T) {
	got := printed(Capabilities{}, "", func(p *Printer) {
		p.Rows([]string{"url", "http://127.0.0.1:54321"}, []string{"odoo", "running", "ob-a"}, []string{"postgres", "running", "ob-b"})
	})
	want := "  url        http://127.0.0.1:54321\n" +
		"  odoo       running   ob-a\n" +
		"  postgres   running   ob-b\n"
	if got != want {
		t.Fatalf("rows:\n%q\nwant:\n%q", got, want)
	}
}

func TestPrinterRowsAlignStyledCells(t *testing.T) {
	caps := Capabilities{Color: true}
	got := printed(caps, "", func(p *Printer) {
		p.Rows([]string{"odoo", p.Styled("running", ToneActive), "a"}, []string{"postgres", "exited", "b"})
	})
	if plain := stripANSI(got); plain != "  odoo       running   a\n  postgres   exited    b\n" {
		t.Fatalf("styled rows = %q", plain)
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

func TestPrinterDecoration(t *testing.T) {
	render := func(caps Capabilities) string {
		return printed(caps, "", func(p *Printer) {
			p.Headline("dev", "running", ToneActive)
			p.Warnings([]string{"w"})
		})
	}
	if got := render(Capabilities{}); got != "dev  * running\n  ! w\n" {
		t.Errorf("ascii = %q", got)
	}
	if got := render(Capabilities{Unicode: true}); got != "dev  ● running\n  ⚠ w\n" {
		t.Errorf("unicode = %q", got)
	}
	colored := render(Capabilities{Color: true})
	if !strings.Contains(colored, "\x1b[") || stripANSI(colored) != render(Capabilities{}) {
		t.Errorf("colour = %q", colored)
	}
	if got := printed(Capabilities{}, "", func(p *Printer) { p.Headline("odooVersion", "18.0", ToneNone) }); got != "odooVersion  18.0\n" {
		t.Errorf("no tone = %q", got)
	}
	for _, tone := range []Tone{ToneActive, ToneDone, ToneIdle, ToneWarn, ToneFailed} {
		if s := printed(Capabilities{}, "", func(p *Printer) { p.Headline("x", "y", tone) }); strings.ContainsAny(s, "\x1b●✓○⚠✗") {
			t.Errorf("tone %d without capabilities = %q", tone, s)
		}
	}
}

func TestPrinterPath(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, Capabilities{}, "/home/dev")
	for path, want := range map[string]string{
		"/home/dev": "~", "/home/dev/x": "~/x", "/home/dev2/x": "/home/dev2/x", "/srv/x": "/srv/x",
	} {
		if got := p.Path(path); got != want {
			t.Errorf("Path(%q) = %q, want %q", path, got, want)
		}
	}
	if got := NewPrinter(&bytes.Buffer{}, Capabilities{}, "").Path("/home/dev/x"); got != "/home/dev/x" {
		t.Errorf("without home: %q", got)
	}
}

func TestPrinterIdentifiers(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, Capabilities{}, "")
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct{ got, want string }{
		{p.ID(digest), "0123456789ab"},
		{p.ID("0123456789abcdef"), "0123456789ab"},
		{p.ID("abc"), "abc"},
		{p.Ref("postgres:16@" + digest), "postgres:16@sha256:0123456789ab"},
		{p.Ref("odooboat/environment:1a2b"), "odooboat/environment:1a2b"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestPrinterKeepsFirstError(t *testing.T) {
	f := &failingWriter{failures: 1}
	p := NewPrinter(f, Capabilities{}, "")
	p.Headline("a", "b", ToneNone)
	p.Rows([]string{"c", "d"})
	if p.Err() == nil || f.calls != 1 {
		t.Fatalf("err = %v, calls = %d", p.Err(), f.calls)
	}
}
