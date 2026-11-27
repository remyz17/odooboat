package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/remyz17/odooboat/internal/app"
)

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

const fullHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	pgImage   = app.Subject{Kind: app.ResourceImage, Name: "postgres:16", Role: app.RolePostgres}
	envImage  = app.Subject{Kind: app.ResourceImage, Name: "odooboat/environment:" + fullHash}
	odooCtr   = app.Subject{Kind: app.ResourceContainer, Name: "ob-shop-dev-odoo-1a2b3c4d", Role: app.RoleOdoo}
	pipStep   = app.Subject{Kind: app.ResourceBuildStep, Name: "[3/7] RUN pip install -r requirements.txt"}
	layerName = func(n int) app.Subject {
		return app.Subject{Kind: app.ResourceLayer, Name: fmt.Sprintf("a1b2c3d4e5f%d", n)}
	}
)

func newTestTTY(w io.Writer, unicode bool) *tty {
	return NewTTY(w, RetainOnFailure, View{Caps: Capabilities{Unicode: unicode}}).(*tty)
}

func frameText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// buildRunning is a build whose pip step has printed 12 lines.
func buildRunning() []timed {
	events := []timed{
		{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: envImage}},
		{secs(1), app.StepStarted{ID: "2", Parent: "1", Kind: app.StepBuild, Subject: pipStep}},
	}
	var out strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&out, "line %d\n", i)
	}
	return append(events, timed{secs(2), app.StepOutput{ID: "2", Stream: app.StreamStdout, Data: []byte(out.String())}})
}

func TestTTYFrameGolden(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"pull": func(t *testing.T) string {
			s := newTestTTY(io.Discard, true)
			feed(s,
				timed{0, app.StepStarted{ID: "1", Kind: app.StepPull, Subject: pgImage}},
				timed{0, app.StepStarted{ID: "2", Parent: "1", Kind: app.StepPull, Subject: layerName(1)}},
				timed{0, app.StepStarted{ID: "3", Parent: "1", Kind: app.StepPull, Subject: layerName(2)}},
				timed{0, app.StepStarted{ID: "4", Parent: "1", Kind: app.StepPull, Subject: layerName(3)}},
				timed{secs(1), app.Transfer{ID: "2", Current: 10_000_000, Total: 100_000_000}},
				timed{secs(1), app.Transfer{ID: "3", Current: 5_000_000, Total: 50_000_000}},
			)
			first := frameText(s.frame(epoch.Add(secs(4.2)), 80, 0))
			feed(s, timed{secs(5), app.Transfer{ID: "4", Current: 1_000_000, Total: 20_000_000}})
			return first + "--\n" + frameText(s.frame(epoch.Add(secs(5)), 80, 0))
		},
		"build": func(t *testing.T) string {
			s := newTestTTY(io.Discard, true)
			feed(s, buildRunning()...)
			return frameText(s.frame(epoch.Add(secs(21)), 80, 0))
		},
		"nested": func(t *testing.T) string {
			s := newTestTTY(io.Discard, true)
			feed(s,
				timed{0, app.StepStarted{ID: "1", Kind: app.StepStart, Subject: odooCtr}},
				timed{secs(1), app.StepStarted{ID: "2", Parent: "1", Kind: app.StepWait, Subject: odooCtr}},
			)
			return frameText(s.frame(epoch.Add(secs(3)), 80, 0))
		},
		"finished_child": func(t *testing.T) string {
			s := newTestTTY(io.Discard, true)
			feed(s,
				timed{0, app.StepStarted{ID: "1", Kind: app.StepStart, Subject: odooCtr}},
				timed{secs(1), app.StepStarted{ID: "2", Parent: "1", Kind: app.StepWait, Subject: odooCtr}},
				timed{secs(3), app.StepFinished{ID: "2", Outcome: app.OutcomeDone}},
			)
			return frameText(s.frame(epoch.Add(secs(4)), 80, 0))
		},
		"ascii": func(t *testing.T) string {
			s := newTestTTY(io.Discard, false)
			feed(s, buildRunning()...)
			return frameText(s.frame(epoch.Add(secs(21)), 80, 0))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { golden(t, "tty_frame_"+name+".golden", []byte(run(t))) })
	}
}

func TestTransferText(t *testing.T) {
	for _, c := range []struct {
		current, total int64
		want           string
	}{
		{0, 0, "0 B"},
		{999, 0, "999 B"},
		{4_200_000, 0, "4.2 MB"},
		{48_000_000, 112_000_000, "48/112 MB"},
		{4_200_000, 112_000_000, "4.2/112 MB"},
		{1_500_000_000, 0, "1.5 GB"},
	} {
		if got := transferText(c.current, c.total); got != c.want {
			t.Errorf("transferText(%d, %d) = %q, want %q", c.current, c.total, got, c.want)
		}
	}
}

func TestTTYFrameFitsWidth(t *testing.T) {
	for _, wide := range []bool{false, true} {
		s := newTestTTY(io.Discard, true)
		feed(s, buildRunning()...)
		if wide {
			feed(s,
				timed{secs(3), app.StepStarted{ID: "3", Kind: app.StepPull, Subject: app.Subject{Kind: app.ResourceImage, Name: "日本語のイメージ:latest"}}},
				timed{secs(3), app.StepOutput{ID: "3", Data: []byte("🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀\n")}},
			)
		}
		for _, width := range []int{10, 40, 80} {
			lines := s.frame(epoch.Add(secs(21)), width, 0)
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > width-1 {
					t.Errorf("wide %v, width %d: %q is %d cells", wide, width, l, w)
				}
			}
			if width == 40 && !strings.HasSuffix(lines[0], "21.0s") {
				t.Errorf("wide %v: the build line lost its elapsed time: %q", wide, lines[0])
			}
		}
	}
}

func TestTTYFrameHeightCap(t *testing.T) {
	s := newTestTTY(io.Discard, true)
	for i := 1; i <= 6; i++ {
		feed(s, timed{0, app.StepStarted{ID: fmt.Sprint(i), Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: fmt.Sprintf("v%d", i)}}})
	}
	feed(s, timed{0, app.StepOutput{ID: "6", Data: []byte("a\nb\n")}})
	lines := s.frame(epoch.Add(secs(1)), 80, 5)
	if len(lines) != 4 || lines[3] != "… 3 more" || strings.Contains(strings.Join(lines, "\n"), "│") {
		t.Fatalf("height 5:\n%s", frameText(lines))
	}
	if lines := s.frame(epoch.Add(secs(1)), 80, 0); len(lines) != 8 {
		t.Fatalf("height 0:\n%s", frameText(lines))
	}
}

func TestTTYTailSanitizes(t *testing.T) {
	s := newTestTTY(io.Discard, true)
	feed(s,
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: odooCtr}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("\x1b[31mred\x1b[0m\tok\x1b[K\n")}},
	)
	lines := s.frame(epoch.Add(secs(1)), 80, 0)
	if len(lines) != 2 || lines[1] != "  │ red ok" {
		t.Fatalf("frame:\n%q", lines)
	}
}
