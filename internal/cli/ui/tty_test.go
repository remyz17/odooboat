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
	for i := 7; i <= 130; i++ {
		feed(s, timed{0, app.StepStarted{ID: fmt.Sprint(i), Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: "v"}}})
	}
	for _, width := range []int{3, 5, 10} {
		for _, l := range s.frame(epoch.Add(secs(1)), width, 5) {
			if w := ansi.StringWidth(l); w > width-1 {
				t.Errorf("width %d: %q is %d cells", width, l, w)
			}
		}
	}
}

func TestTTYStepNamesAreSanitized(t *testing.T) {
	v := newVT(t)
	s := ttyOn(v, RetainOnFailure)
	name := app.Subject{Kind: app.ResourceBuildStep, Name: "[2/7] RUN <<EOF\n\tset -e\u009b2J\nEOF"}
	feed(s,
		timed{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: envImage}},
		timed{0, app.StepStarted{ID: "2", Parent: "1", Kind: app.StepBuild, Subject: name}},
		timed{secs(1), Tick{}},
		timed{secs(2), app.StepFinished{ID: "2", Outcome: app.OutcomeFailed}},
		timed{secs(2), app.StepFinished{ID: "1", Outcome: app.OutcomeFailed}},
	)
	for _, l := range append(s.frame(epoch, 80, 0), v.Lines()...) {
		if strings.ContainsAny(l, "\t\n\u009b") {
			t.Errorf("control character in %q", l)
		}
	}
	if lines := v.Lines(); len(lines) != 2 {
		t.Errorf("a name with newlines took more than its line:\n%s", v.Text())
	}
}

func TestTTYTailSanitizes(t *testing.T) {
	s := newTestTTY(io.Discard, true)
	feed(s,
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: odooCtr}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("\x1b[31mred\x1b[0m\tok\x1b[K\u009b2J\u0085\n")}},
	)
	lines := s.frame(epoch.Add(secs(1)), 80, 0)
	// The C1 introducer goes; what followed it is harmless text.
	if len(lines) != 2 || lines[1] != "  │ red ok2J" {
		t.Fatalf("frame:\n%q", lines)
	}
}

func ttyOn(w io.Writer, retention Retention) *tty {
	return NewTTY(w, retention, View{Caps: Capabilities{Unicode: true}, Size: func() (int, int) { return 80, 24 }}).(*tty)
}

// upSequence is an up: a reused build, a pull, a network, and two
// containers started and waited for.
func upSequence() []timed {
	pg := app.Subject{Kind: app.ResourceContainer, Name: "ob-shop-dev-postgres-1a2b3c4d", Role: app.RolePostgres}
	net := app.Subject{Kind: app.ResourceNetwork, Name: "ob-shop-dev-1a2b3c4d"}
	return []timed{
		{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: envImage}},
		{secs(0.1), Tick{}},
		{secs(0.4), app.StepFinished{ID: "1", Outcome: app.OutcomeReused}},
		{secs(1), app.StepStarted{ID: "2", Kind: app.StepPull, Subject: pgImage}},
		{secs(1), app.StepStarted{ID: "3", Parent: "2", Kind: app.StepPull, Subject: layerName(1)}},
		{secs(1), app.StepStarted{ID: "4", Parent: "2", Kind: app.StepPull, Subject: layerName(2)}},
		{secs(1.5), app.Transfer{ID: "3", Current: 10_000_000, Total: 100_000_000}},
		{secs(1.5), Tick{}},
		{secs(2), app.StepFinished{ID: "3", Outcome: app.OutcomeDone}},
		{secs(2), app.StepFinished{ID: "4", Outcome: app.OutcomeKept}},
		{secs(3), app.StepFinished{ID: "2", Outcome: app.OutcomeDone}},
		{secs(3), app.StepStarted{ID: "5", Kind: app.StepCreate, Subject: net}},
		{secs(3.1), app.StepFinished{ID: "5", Outcome: app.OutcomeDone}},
		{secs(4), app.StepStarted{ID: "6", Kind: app.StepStart, Subject: pg}},
		{secs(4), app.StepStarted{ID: "7", Parent: "6", Kind: app.StepWait, Subject: pg}},
		{secs(4.5), Tick{}},
		{secs(6), app.StepFinished{ID: "7", Outcome: app.OutcomeDone}},
		{secs(6), app.StepFinished{ID: "6", Outcome: app.OutcomeDone}},
		{secs(6), app.StepStarted{ID: "8", Kind: app.StepStart, Subject: odooCtr}},
		{secs(6), app.StepStarted{ID: "9", Parent: "8", Kind: app.StepWait, Subject: odooCtr}},
		{secs(7), Tick{}},
		{secs(12), app.StepFinished{ID: "9", Outcome: app.OutcomeDone}},
		{secs(12), app.StepFinished{ID: "8", Outcome: app.OutcomeDone}},
		{secs(12), Finish{}},
	}
}

func TestTTYScrollbackGolden(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure), upSequence()...)
	golden(t, "tty_scrollback.golden", []byte(v.Text()))
}

func TestTTYCommittedTextIsClean(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure), upSequence()...)
	text := v.Text()
	for _, bad := range append([]string{"\x1b", "layer"}, spinnerUnicode...) {
		if strings.Contains(text, bad) {
			t.Errorf("scrollback contains %q:\n%s", bad, text)
		}
	}
}

func TestTTYRedrawOnlyOnTick(t *testing.T) {
	v := newVT(t)
	s := ttyOn(v, RetainOnFailure)
	feed(s, timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: odooCtr}})
	for range 20 {
		feed(s, timed{0, app.StepOutput{ID: "1", Data: []byte("x\n")}})
	}
	if v.written != 0 {
		t.Fatalf("wrote %d bytes before a tick", v.written)
	}
	feed(s, timed{secs(1), Tick{}})
	if lines := v.Lines(); len(lines) != 9 || !strings.Contains(lines[0], "create container") {
		t.Fatalf("after a tick:\n%s", v.Text())
	}
}

func TestTTYCommitIsImmediate(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: "v1"}}},
		timed{0, Tick{}},
		timed{secs(1), app.StepFinished{ID: "1", Outcome: app.OutcomeDone}},
	)
	if got := v.Text(); got != "✓ create volume v1  1.0s\n" {
		t.Fatalf("scrollback %q", got)
	}
}

func failingBuild(lines int) []timed {
	var out strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&out, "out %d\n", i)
	}
	return []timed{
		{0, app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: envImage}},
		{0, app.StepStarted{ID: "2", Parent: "1", Kind: app.StepBuild, Subject: pipStep}},
		{secs(1), app.StepOutput{ID: "2", Data: []byte(out.String())}},
		{secs(1), Tick{}},
		{secs(2), app.StepFinished{ID: "2", Outcome: app.OutcomeFailed}},
		{secs(2), app.StepFinished{ID: "1", Outcome: app.OutcomeFailed}},
		{secs(2), Finish{}},
	}
}

func TestTTYFailedBuildStep(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure), failingBuild(60)...)
	lines := v.Lines()
	if len(lines) != 52 || !strings.HasPrefix(lines[0], "✗ build image") ||
		lines[1] != "  ✗ build build-step [3/7] RUN pip install -r requirements.txt  failed  2.0s" ||
		lines[2] != "    │ out 11" || lines[51] != "    │ out 60" {
		t.Fatalf("on-failure:\n%s", v.Text())
	}

	v = newVT(t)
	feed(ttyOn(v, RetainAll), failingBuild(60)...)
	lines = v.Lines()
	if len(lines) != 62 || lines[0] != "  [3/7] | out 1" || lines[59] != "  [3/7] | out 60" ||
		!strings.HasPrefix(lines[60], "✗ build image") || strings.Contains(v.Text(), "│") {
		t.Fatalf("all:\n%s", v.Text())
	}
}

func TestTTYInterrupted(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1", Kind: app.StepPull, Subject: pgImage}},
		timed{0, app.StepStarted{ID: "2", Parent: "1", Kind: app.StepPull, Subject: layerName(1)}},
		timed{secs(1), Tick{}},
		timed{secs(2), app.StepFinished{ID: "2", Outcome: app.OutcomeInterrupted}},
		timed{secs(2), app.StepFinished{ID: "1", Outcome: app.OutcomeInterrupted}},
		timed{secs(2), ErrorReport{Message: "interrupted"}},
		timed{secs(2), Finish{}},
	)
	want := "✗ pull image postgres:16 (postgres)  interrupted  2.0s\n" +
		"  ✗ pull layer a1b2c3d4e5f1  interrupted  2.0s\n" +
		"error: interrupted\n"
	if got := v.Text(); got != want {
		t.Fatalf("scrollback:\n%s\nwant:\n%s", got, want)
	}
}

func TestTTYMessagesAboveRegion(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: "v1"}}},
		timed{0, Tick{}},
		timed{secs(1), Warning{Message: "disk"}},
	)
	if lines := v.Lines(); len(lines) != 2 || lines[0] != "warning: disk" || !strings.Contains(lines[1], "create volume v1") {
		t.Fatalf("screen:\n%s", v.Text())
	}
}

func TestTTYSettleAndHold(t *testing.T) {
	v := newVT(t)
	s := ttyOn(v, RetainOnFailure)
	feed(s,
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: "v1"}}},
		timed{0, Tick{}},
		timed{0, Settle{}},
	)
	if got := v.Text(); got != "" {
		t.Fatalf("after Settle: %q", got)
	}
	feed(s, timed{0, Settle{Hold: true}})
	before := v.written
	feed(s, timed{secs(1), Tick{}}, timed{secs(1), app.StepOutput{ID: "1", Data: []byte("x\n")}}, timed{secs(2), Tick{}},
		timed{secs(2), Settle{Hold: false}})
	if v.written != before {
		t.Fatalf("held: wrote %d bytes", v.written-before)
	}
	feed(s, timed{secs(3), Tick{}})
	if lines := v.Lines(); len(lines) == 0 || !strings.Contains(lines[0], "create volume v1") {
		t.Fatalf("after release:\n%s", v.Text())
	}
}

func TestTTYUnknownStep(t *testing.T) {
	// Output and transfers of a step never started are not drawn, as plain
	// prints nothing for them.
	v := newVT(t)
	s := ttyOn(v, RetainOnFailure)
	feed(s,
		timed{0, app.StepOutput{ID: "9", Data: []byte("x\n")}},
		timed{0, app.Transfer{ID: "8", Current: 5}},
		timed{secs(1), Tick{}},
	)
	if v.written != 0 {
		t.Fatalf("drew an unknown step:\n%s", v.Text())
	}
	// Its failure is committed with the output held for it, as plain does.
	feed(s,
		timed{secs(1), app.StepFinished{ID: "9", Outcome: app.OutcomeFailed}},
		timed{secs(2), Finish{}},
	)
	if text := v.Text(); !strings.Contains(text, "9  failed") || !strings.Contains(text, "│ x") || strings.Contains(text, "8") {
		t.Fatalf("scrollback:\n%s", text)
	}
}

func TestTTYFinishCommitsOpenSteps(t *testing.T) {
	v := newVT(t)
	feed(ttyOn(v, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: app.Subject{Kind: app.ResourceVolume, Name: "v1"}}},
		timed{0, Tick{}},
		timed{secs(2), Finish{}},
	)
	if got := v.Text(); got != "create volume v1  2.0s\n" {
		t.Fatalf("scrollback %q", got)
	}
}
