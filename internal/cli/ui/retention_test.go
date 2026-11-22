package ui

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// feed sends events at epoch+offset through sink.
type timed struct {
	offset time.Duration
	event  any
}

func feed(sink Sink, events ...timed) {
	for _, e := range events {
		sink.Handle(Record{Time: epoch.Add(e.offset), Event: e.event})
	}
}

func outputLines(records []Record) []string {
	var out []string
	for _, r := range records {
		if o, ok := r.Event.(app.StepOutput); ok {
			out = append(out, string(o.Data))
		}
	}
	return out
}

func TestRetentionDiscardsOnSuccess(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1"}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("a\nb\n")}},
		timed{0, app.StepFinished{ID: "1", Outcome: app.OutcomeDone}},
	)
	want := []any{app.StepStarted{ID: "1"}, app.StepFinished{ID: "1", Outcome: app.OutcomeDone}}
	if got := sink.events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func sixtyLines(outcome app.Outcome) []timed {
	events := []timed{{0, app.StepStarted{ID: "1"}}}
	for i := 1; i <= 60; i++ {
		events = append(events, timed{0, app.StepOutput{ID: "1", Data: fmt.Appendf(nil, "line %d\n", i)}})
	}
	return append(events, timed{0, app.StepFinished{ID: "1", Outcome: outcome}})
}

func assertTail(t *testing.T, sink *sinkLog, outcome app.Outcome) {
	t.Helper()
	lines := outputLines(sink.records)
	if len(lines) != RetainedLines || lines[0] != "line 11\n" || lines[49] != "line 60\n" {
		t.Fatalf("emitted %d lines: first %q", len(lines), lines)
	}
	last := sink.records[len(sink.records)-1].Event
	if f, ok := last.(app.StepFinished); !ok || f.Outcome != outcome {
		t.Fatalf("last record = %#v, want the finish after the output", last)
	}
}

func TestRetentionEmitsTailOnFailure(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure), sixtyLines(app.OutcomeFailed)...)
	assertTail(t, sink, app.OutcomeFailed)
}

func TestRetentionEmitsOnInterrupt(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure), sixtyLines(app.OutcomeInterrupted)...)
	assertTail(t, sink, app.OutcomeInterrupted)
}

func TestRetentionSplitsLines(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1"}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("x\r\ny\rz")}},
		timed{0, app.StepFinished{ID: "1", Outcome: app.OutcomeFailed}},
		timed{0, app.StepStarted{ID: "2"}},
		timed{0, app.StepOutput{ID: "2", Data: []byte("ab")}},
		timed{0, app.StepOutput{ID: "2", Data: []byte("c\n")}},
		timed{0, app.StepFinished{ID: "2", Outcome: app.OutcomeFailed}},
	)
	want := []string{"x\n", "y\n", "z\n", "abc\n"}
	if got := outputLines(sink.records); !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestRetentionKeepsOriginalTimes(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1"}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("early\n")}},
		timed{5 * time.Second, app.StepFinished{ID: "1", Outcome: app.OutcomeFailed}},
	)
	for _, r := range sink.records {
		if _, ok := r.Event.(app.StepOutput); ok && !r.Time.Equal(epoch) {
			t.Fatalf("output time = %v, want %v", r.Time, epoch)
		}
	}
}

func TestRetainAllPassesThrough(t *testing.T) {
	sink := &sinkLog{}
	events := []timed{
		{0, app.StepStarted{ID: "1"}},
		{0, app.StepOutput{ID: "1", Data: []byte("a\nb")}},
		{0, app.StepFinished{ID: "1", Outcome: app.OutcomeDone}},
		{0, Tick{}},
	}
	feed(Retain(sink, RetainAll), events...)
	if len(sink.records) != len(events) {
		t.Fatalf("records = %#v", sink.records)
	}
	for i, e := range events {
		if !reflect.DeepEqual(sink.records[i].Event, e.event) {
			t.Fatalf("record %d = %#v, want %#v", i, sink.records[i].Event, e.event)
		}
	}
}

func TestRetentionKeepsBlankLines(t *testing.T) {
	sink := &sinkLog{}
	feed(Retain(sink, RetainOnFailure),
		timed{0, app.StepStarted{ID: "1"}},
		timed{0, app.StepOutput{ID: "1", Data: []byte("a\n\nb\n")}},
		timed{0, app.StepFinished{ID: "1", Outcome: app.OutcomeFailed}},
	)
	want := []string{"a\n", "\n", "b\n"}
	if got := outputLines(sink.records); !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}
