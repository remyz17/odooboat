package ui

import (
	"io"
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
	case app.StepOutput:
		t.step(e.ID, r.Time).output.add(r.Time, e.Stream, e.Data)
	case app.Transfer:
		transfer := e
		t.step(e.ID, r.Time).transfer = &transfer
	case Tick:
		t.ticks++
	}
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
