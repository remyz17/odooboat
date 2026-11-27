package ui

import (
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// Retention decides when step output reaches the renderer (ADR 0011 §6).
type Retention string

const (
	// RetainOnFailure holds each step's last lines and emits them only when
	// the step fails or is interrupted.
	RetainOnFailure Retention = "on-failure"
	// RetainAll emits output as it arrives.
	RetainAll Retention = "all"
)

// RetainedLines is how many lines of a step RetainOnFailure keeps.
const RetainedLines = 50

// Retain filters step output into next according to mode.
func Retain(next Sink, mode Retention) Sink {
	if mode == RetainAll {
		return next
	}
	return &retainer{next: next, steps: map[string]*heldOutput{}}
}

type retainer struct {
	next  Sink
	steps map[string]*heldOutput
}

type heldLine struct {
	time   time.Time
	stream app.Stream
	data   []byte
}

// heldOutput is a ring of a step's last lines and its unterminated line.
type heldOutput struct {
	lines   []heldLine
	partial *heldLine
	// afterCR: the last byte seen was '\r', so a leading '\n' ends nothing.
	afterCR bool
}

func (r *retainer) Handle(rec Record) {
	switch e := rec.Event.(type) {
	case app.StepOutput:
		h := r.steps[e.ID]
		if h == nil {
			h = &heldOutput{}
			r.steps[e.ID] = h
		}
		h.add(rec.Time, e.Stream, e.Data)
	case app.StepFinished:
		if h := r.steps[e.ID]; h != nil {
			delete(r.steps, e.ID)
			if e.Outcome == app.OutcomeFailed || e.Outcome == app.OutcomeInterrupted {
				h.end()
				for _, l := range h.lines {
					data := append(l.data, '\n')
					r.next.Handle(Record{Time: l.time, Event: app.StepOutput{ID: e.ID, Stream: l.stream, Data: data}})
				}
			}
		}
		r.next.Handle(rec)
	default:
		r.next.Handle(rec)
	}
}

// add splits data on "\n", "\r", and "\r\n" into held lines.
func (h *heldOutput) add(t time.Time, stream app.Stream, data []byte) {
	for _, b := range data {
		if b == '\n' && h.afterCR {
			h.afterCR = false
			continue
		}
		h.afterCR = b == '\r'
		if b == '\n' || b == '\r' {
			if h.partial == nil {
				h.push(heldLine{time: t, stream: stream}) // a blank line
			}
			h.end()
			continue
		}
		if h.partial == nil {
			h.partial = &heldLine{time: t, stream: stream}
		}
		h.partial.data = append(h.partial.data, b)
	}
}

// end turns the unterminated line, if any, into a held line.
func (h *heldOutput) end() {
	if h.partial != nil {
		h.push(*h.partial)
		h.partial = nil
	}
}

func (h *heldOutput) push(l heldLine) {
	if len(h.lines) == RetainedLines {
		copy(h.lines, h.lines[1:])
		h.lines = h.lines[:RetainedLines-1]
	}
	h.lines = append(h.lines, l)
}

// last returns up to n of the latest lines, the unterminated one included.
func (h *heldOutput) last(n int) []string {
	var out []string
	for _, l := range h.lines {
		out = append(out, string(l.data))
	}
	if h.partial != nil {
		out = append(out, string(h.partial.data))
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
