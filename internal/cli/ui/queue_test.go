package ui

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// sinkLog records what a sink handles; release, when set, blocks Handle
// until it is closed.
type sinkLog struct {
	mu      sync.Mutex
	records []Record
	release chan struct{}
}

func (s *sinkLog) Handle(r Record) {
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
}

func (s *sinkLog) events() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]any, len(s.records))
	for i, r := range s.records {
		out[i] = r.Event
	}
	return out
}

var epoch = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

func fixedClock() func() time.Time { return func() time.Time { return epoch } }

func TestQueueDoesNotBlockOnSlowSink(t *testing.T) {
	sink := &sinkLog{release: make(chan struct{})}
	q := NewQueue(sink, fixedClock())
	done := make(chan struct{})
	go func() {
		for i := range 10000 {
			q.Report(app.StepOutput{ID: "1", Data: []byte{byte(i)}})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Report blocked on a slow sink")
	}
	close(sink.release)
	q.Close()
	events := sink.events()
	if len(events) != 10000 {
		t.Fatalf("delivered %d events, want 10000", len(events))
	}
	for i, e := range events {
		if e.(app.StepOutput).Data[0] != byte(i) {
			t.Fatalf("event %d out of order", i)
		}
	}
}

func TestQueueCoalescesTailTransfer(t *testing.T) {
	sink := &sinkLog{release: make(chan struct{})}
	q := NewQueue(sink, fixedClock())
	// The first record occupies the drainer, blocked in Handle, so the
	// next ones stay queued.
	q.Report(app.StepStarted{ID: "A"})
	waitFor(t, func() bool { return q.pending() == 0 })
	q.Report(app.Transfer{ID: "A", Current: 1})
	q.Report(app.Transfer{ID: "A", Current: 2})
	q.Report(app.StepOutput{ID: "A"})
	q.Report(app.Transfer{ID: "A", Current: 3})
	q.Report(app.Transfer{ID: "B", Current: 1})
	close(sink.release)
	q.Close()
	want := []any{
		app.StepStarted{ID: "A"},
		app.Transfer{ID: "A", Current: 2},
		app.StepOutput{ID: "A"},
		app.Transfer{ID: "A", Current: 3},
		app.Transfer{ID: "B", Current: 1},
	}
	if got := sink.events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %#v, want %#v", got, want)
	}
}

func TestQueueFlush(t *testing.T) {
	sink := &sinkLog{}
	q := NewQueue(sink, fixedClock())
	defer q.Close()
	for range 100 {
		q.Report(app.StepOutput{ID: "1"})
	}
	q.Post(Record{Time: epoch, Event: Tick{}})
	q.Flush()
	if n := len(sink.events()); n != 101 {
		t.Fatalf("after Flush the sink handled %d records, want 101", n)
	}
}

func TestQueueReportAfterCloseIsNoop(t *testing.T) {
	sink := &sinkLog{}
	q := NewQueue(sink, fixedClock())
	q.Close()
	q.Report(app.StepOutput{ID: "1"})
	q.Flush()
	q.Close()
	if n := len(sink.events()); n != 0 {
		t.Fatalf("handled %d records after Close", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
