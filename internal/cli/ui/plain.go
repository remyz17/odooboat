package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// HeartbeatInterval is how often plain progress repeats a running step.
const HeartbeatInterval = 30 * time.Second

// ErrorReport is the error that ends an invocation; it is rendered last.
type ErrorReport struct {
	// Code is the stable error code; Usage is the command's usage text,
	// printed only by plain progress.
	Code, Message, Hint, Usage string
}

// zone is where plain progress shows wall-clock times.
var zone = time.Local

// plain writes one line per step start and finish, without control
// sequences or colour (ADR 0011 §7).
type plain struct {
	w     io.Writer
	steps map[string]*plainStep
	order []string
}

type plainStep struct {
	started  app.StepStarted
	at, told time.Time
	partial  []byte
	afterCR  bool
}

func NewPlain(w io.Writer) Sink { return &plain{w: w, steps: map[string]*plainStep{}} }

// hidden reports whether a step's start, success, and heartbeat are left
// out: layers and build steps are shown only when they fail.
func (s *plainStep) hidden() bool { return hiddenKind(s.started.Subject.Kind) }

func (s *plainStep) label(withRole bool) string { return stepLabel(s.started, withRole) }

func (p *plain) Handle(r Record) {
	switch e := r.Event.(type) {
	case app.StepStarted:
		s := &plainStep{started: e, at: r.Time, told: r.Time}
		p.steps[e.ID] = s
		p.order = append(p.order, e.ID)
		if !s.hidden() {
			fmt.Fprintln(p.w, s.label(true))
		}
	case app.StepFinished:
		s := p.step(e.ID)
		p.endLine(e.ID, s)
		delete(p.steps, e.ID)
		failed := e.Outcome == app.OutcomeFailed || e.Outcome == app.OutcomeInterrupted
		if !s.hidden() || failed {
			fmt.Fprintf(p.w, "%s: %s (%s)\n", s.label(true), e.Outcome, elapsed(r.Time.Sub(s.at)))
		}
	case app.StepOutput:
		s := p.step(e.ID)
		for _, b := range e.Data {
			if b == '\n' && s.afterCR {
				s.afterCR = false
				continue
			}
			s.afterCR = b == '\r'
			if b == '\n' || b == '\r' {
				p.writeLine(s)
				continue
			}
			s.partial = append(s.partial, b)
		}
	case Tick:
		for _, id := range p.order {
			s, ok := p.steps[id]
			if !ok || s.hidden() {
				continue
			}
			if r.Time.Sub(s.at) >= HeartbeatInterval && r.Time.Sub(s.told) >= HeartbeatInterval {
				s.told = r.Time
				fmt.Fprintf(p.w, "%s: running (%s)\n", s.label(false), elapsed(r.Time.Sub(s.at)))
			}
		}
		p.compact()
	default:
		if text, ok := messageText(r.Event); ok {
			fmt.Fprint(p.w, text)
		}
	}
}

// step returns the step id, or a stand-in named after the id when its start
// was never seen.
func (p *plain) step(id string) *plainStep {
	if s, ok := p.steps[id]; ok {
		return s
	}
	return &plainStep{started: app.StepStarted{ID: id, Subject: app.Subject{Name: id}}}
}

func (p *plain) writeLine(s *plainStep) {
	fmt.Fprintf(p.w, "  %s | %s\n", outputPrefix(s.started.Subject.Name), s.partial)
	s.partial = s.partial[:0]
}

// prefixLength bounds the subject shown before each output line.
const prefixLength = 40

// outputPrefix shortens a subject for output lines: a build step's name is
// its whole command, so only its "[stage n/m]" marker is kept; other long
// names are cut.
func outputPrefix(name string) string {
	if strings.HasPrefix(name, "[") {
		if end := strings.Index(name, "]"); end > 0 {
			return name[:end+1]
		}
	}
	if runes := []rune(name); len(runes) > prefixLength {
		return string(runes[:prefixLength-3]) + "..."
	}
	return name
}

// endLine writes an unterminated output line before the step's finish.
func (p *plain) endLine(id string, s *plainStep) {
	if len(s.partial) > 0 {
		p.writeLine(s)
	}
}

// compact drops finished steps from the heartbeat order.
func (p *plain) compact() {
	kept := p.order[:0]
	for _, id := range p.order {
		if _, ok := p.steps[id]; ok {
			kept = append(kept, id)
		}
	}
	p.order = kept
}

// elapsed is rounded to 0.1 s below a minute and to the second above.
func elapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
