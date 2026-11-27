package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// TTYRefresh is how often the live region is redrawn.
const TTYRefresh = 100 * time.Millisecond

// View is what the tty renderer knows of its terminal: the capabilities of
// stderr and its size, read at every redraw. A nil Size, or a size of 0,
// means unknown: nothing is truncated or capped.
type View struct {
	Caps Capabilities
	Size func() (width, height int)
}

// tty redraws a live region of running steps below the scrollback (ADR 0011
// §7). Layers and build steps are collapsed onto their visible parent, and
// retention is applied here: output is shown live as decoration, and kept
// in the scrollback only as retention decides.
type tty struct {
	w         io.Writer
	retention Retention
	view      View

	steps map[string]*ttyStep
	roots []string // top-level steps, in start order
	seq   int
	ticks int

	drawn   int      // lines of the live region on screen
	hold    bool     // a session owns the terminal: nothing is drawn
	pending []string // output lines of --step-output=all, written at the next tick
}

type ttyStep struct {
	started  app.StepStarted
	seq      int
	at, end  time.Time
	finished bool
	outcome  app.Outcome
	transfer *app.Transfer
	output   heldOutput
	children []string
	parent   *ttyStep
	all      lineSplitter // completed lines for --step-output=all
}

// NewTTY returns the live renderer writing to w.
func NewTTY(w io.Writer, retention Retention, view View) Sink {
	return &tty{w: w, retention: retention, view: view, steps: map[string]*ttyStep{}}
}

func (t *tty) Handle(r Record) {
	switch e := r.Event.(type) {
	case app.StepStarted:
		t.start(e, r.Time)
	case app.StepFinished:
		s := t.step(e.ID, r.Time)
		s.finished, s.outcome, s.end = true, e.Outcome, r.Time
		if t.retention == RetainAll {
			if line, ok := s.all.rest(); ok {
				t.pending = append(t.pending, allLine(s, line))
			}
		}
		if s.parent == nil {
			t.paint(r.Time, t.commit(s), true)
		}
	case app.StepOutput:
		s := t.step(e.ID, r.Time)
		s.output.add(r.Time, e.Stream, e.Data)
		if t.retention == RetainAll {
			for _, line := range s.all.feed(e.Data) {
				t.pending = append(t.pending, allLine(s, line))
			}
		}
	case app.Transfer:
		transfer := e
		t.step(e.ID, r.Time).transfer = &transfer
	case Tick:
		if !t.hold {
			t.ticks++
			t.paint(r.Time, nil, true)
		}
	case Settle:
		t.paint(r.Time, nil, false)
		t.hold = e.Hold
	case Finish:
		var lines []string
		for _, id := range t.roots {
			t.walk(t.steps[id], 0, func(s *ttyStep, depth int) {
				lines = append(lines, strings.Repeat("  ", depth)+stepLabel(s.started, true)+"  "+elapsed(r.Time.Sub(s.at)))
			})
		}
		t.roots, t.steps = nil, map[string]*ttyStep{}
		t.paint(r.Time, lines, false)
	default:
		if text, ok := messageText(r.Event); ok {
			t.paint(r.Time, strings.SplitAfter(strings.TrimSuffix(text, "\n"), "\n"), true)
		}
	}
}

// paint erases the live region, writes the pending output lines and the
// committed lines to the scrollback, then draws the region again when draw
// is set and no session holds the terminal. It writes nothing when nothing
// changes on screen.
func (t *tty) paint(now time.Time, committed []string, draw bool) {
	var b strings.Builder
	if t.drawn > 0 {
		fmt.Fprintf(&b, "\r\x1b[%dA\x1b[J", t.drawn)
		t.drawn = 0
	}
	for _, l := range append(t.pending, committed...) {
		b.WriteString(strings.TrimSuffix(l, "\n") + "\n")
	}
	t.pending = nil
	if draw && !t.hold {
		width, height := 0, 0
		if t.view.Size != nil {
			width, height = t.view.Size()
		}
		for _, l := range t.frame(now, width, height) {
			b.WriteString(l + "\n")
			t.drawn++
		}
	}
	if b.Len() > 0 {
		io.WriteString(t.w, b.String())
	}
}

// commit returns the final lines of a finished top-level step and its
// tree, and forgets them: visible steps with their outcome, hidden steps
// only when they failed, and the retained output of failed steps.
func (t *tty) commit(root *ttyStep) []string {
	var lines []string
	var visit func(s *ttyStep, depth int)
	visit = func(s *ttyStep, depth int) {
		failed := s.outcome == app.OutcomeFailed || s.outcome == app.OutcomeInterrupted
		if !s.hidden() || failed {
			lines = append(lines, t.finalLine(s, depth))
			if failed && t.retention != RetainAll {
				s.output.end()
				for _, l := range s.output.lines {
					lines = append(lines, strings.Repeat("  ", depth)+"  "+t.bar()+sanitize(string(l.data)))
				}
			}
		}
		next := depth
		if !s.hidden() {
			next++
		}
		for _, id := range s.children {
			if c := t.steps[id]; c != nil {
				visit(c, next)
			}
		}
		delete(t.steps, s.started.ID)
	}
	visit(root, 0)
	for i, id := range t.roots {
		if id == root.started.ID {
			t.roots = append(t.roots[:i:i], t.roots[i+1:]...)
			break
		}
	}
	return lines
}

// finalLine is a step's line in the scrollback: no spinner, no truncation.
func (t *tty) finalLine(s *ttyStep, depth int) string {
	indent := strings.Repeat("  ", depth)
	if s.running() {
		return indent + stepLabel(s.started, true)
	}
	parts := []string{stepLabel(s.started, true)}
	if s.outcome != app.OutcomeDone {
		parts = append(parts, string(s.outcome))
	}
	parts = append(parts, elapsed(s.end.Sub(s.at)))
	return indent + t.symbol(outcomeTones[s.outcome]) + " " + strings.Join(parts, "  ")
}

// allLine words an output line of --step-output=all as plain does.
func allLine(s *ttyStep, line string) string {
	return "  " + outputPrefix(s.started.Subject.Name) + " | " + sanitize(line)
}

func (t *tty) start(e app.StepStarted, at time.Time) *ttyStep {
	t.seq++
	s := &ttyStep{started: e, seq: t.seq, at: at}
	t.steps[e.ID] = s
	if parent, ok := t.steps[e.Parent]; ok && e.Parent != "" {
		s.parent = parent
		parent.children = append(parent.children, e.ID)
	} else {
		t.roots = append(t.roots, e.ID)
	}
	return s
}

// step returns the step id, or a top-level stand-in named after the id
// when its start was never seen, as plain does.
func (t *tty) step(id string, at time.Time) *ttyStep {
	if s, ok := t.steps[id]; ok {
		return s
	}
	return t.start(app.StepStarted{ID: id, Subject: app.Subject{Name: id}}, at)
}

func (s *ttyStep) hidden() bool { return hiddenKind(s.started.Subject.Kind) }

func (s *ttyStep) running() bool { return !s.finished }

// lineSplitter splits output into lines on "\n", "\r", and "\r\n".
type lineSplitter struct {
	partial []byte
	started bool
	afterCR bool
}

func (l *lineSplitter) feed(data []byte) []string {
	var out []string
	for _, b := range data {
		if b == '\n' && l.afterCR {
			l.afterCR = false
			continue
		}
		l.afterCR = b == '\r'
		if b == '\n' || b == '\r' {
			out = append(out, string(l.partial))
			l.partial, l.started = l.partial[:0], false
			continue
		}
		l.partial = append(l.partial, b)
		l.started = true
	}
	return out
}

// rest returns the unterminated last line, if any.
func (l *lineSplitter) rest() (string, bool) {
	if !l.started {
		return "", false
	}
	line := string(l.partial)
	l.partial, l.started = l.partial[:0], false
	return line, true
}
