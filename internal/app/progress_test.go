package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

// recorder is a Reporter that keeps every event.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Report(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// started returns the StepStarted events, in order.
func started(events []Event) []StepStarted {
	var out []StepStarted
	for _, e := range events {
		if s, ok := e.(StepStarted); ok {
			out = append(out, s)
		}
	}
	return out
}

// finished returns the StepFinished event of id, or false.
func finished(events []Event, id string) (StepFinished, bool) {
	for _, e := range events {
		if f, ok := e.(StepFinished); ok && f.ID == id {
			return f, true
		}
	}
	return StepFinished{}, false
}

func eventID(e Event) string {
	switch e := e.(type) {
	case StepStarted:
		return e.ID
	case StepFinished:
		return e.ID
	case StepOutput:
		return e.ID
	case Transfer:
		return e.ID
	}
	return ""
}

// assertBalanced checks the event contract of ADR 0011 §4: every started
// step finishes exactly once, parents start before their children, and no
// event mentions a step after its finish.
func assertBalanced(t *testing.T, events []Event) {
	t.Helper()
	open := map[string]bool{}
	done := map[string]bool{}
	for i, e := range events {
		id := eventID(e)
		if id == "" {
			continue
		}
		if done[id] {
			t.Errorf("event %d (%T) mentions step %s after its finish", i, e, id)
			continue
		}
		switch e := e.(type) {
		case StepStarted:
			if open[id] {
				t.Errorf("event %d: step %s started twice", i, id)
			}
			if e.Parent != "" && !open[e.Parent] {
				t.Errorf("event %d: step %s starts under %s, which is not running", i, id, e.Parent)
			}
			open[id] = true
		case StepFinished:
			if !open[id] {
				t.Errorf("event %d: step %s finishes without starting", i, id)
			}
			delete(open, id)
			done[id] = true
		default:
			if !open[id] {
				t.Errorf("event %d (%T): step %s is not running", i, e, id)
			}
		}
	}
	for id := range open {
		t.Errorf("step %s never finished", id)
	}
}

func TestTrackerOutcomes(t *testing.T) {
	var r recorder
	tr := newTracker(&r)
	subject := Subject{Kind: ResourceImage, Name: "img"}

	ok := tr.start("", StepBuild, subject)
	ok.finish(context.Background(), nil)
	ok.finish(context.Background(), errors.New("late")) // a second finish is a no-op

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := tr.start("", StepBuild, subject)
	stopped.finish(cancelled, context.Canceled)

	boom := errors.New("x")
	failed := tr.start("", StepBuild, subject)
	failed.finish(context.Background(), boom)

	events := r.all()
	assertBalanced(t, events)
	for id, want := range map[string]Outcome{ok.id(): OutcomeDone, stopped.id(): OutcomeInterrupted, failed.id(): OutcomeFailed} {
		f, found := finished(events, id)
		if !found || f.Outcome != want {
			t.Errorf("step %s finished %+v, want %s", id, f, want)
		}
	}
	if f, _ := finished(events, failed.id()); !errors.Is(f.Err, boom) {
		t.Errorf("failed step error = %v", f.Err)
	}
	if len(events) != 6 {
		t.Errorf("events = %d, want 6 (a second finish emits nothing)", len(events))
	}
}

func TestTrackerNilReporter(t *testing.T) {
	tr := newTracker(nil)
	a := tr.start("", StepBuild, Subject{})
	b := tr.start(a.id(), StepPull, Subject{})
	if a.id() == b.id() {
		t.Fatalf("ids %q and %q collide", a.id(), b.id())
	}
	b.output(StreamStdout, []byte("x"))
	b.transfer(1, 2)
	tr.notify(DatabaseSelected{Name: "db"})
	a.finish(context.Background(), nil)
}

func TestTrackerFinishClosesOpenChildren(t *testing.T) {
	var r recorder
	tr := newTracker(&r)
	parent := tr.start("", StepBuild, Subject{Kind: ResourceImage, Name: "img"})
	child := tr.start(parent.id(), StepBuild, Subject{Kind: ResourceBuildStep, Name: "RUN make"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	parent.finish(ctx, context.Canceled)

	events := r.all()
	assertBalanced(t, events)
	f, _ := finished(events, child.id())
	if f.Outcome != OutcomeInterrupted {
		t.Fatalf("child finished %+v, want interrupted", f)
	}
	if last := events[len(events)-1]; eventID(last) != parent.id() {
		t.Fatalf("last event = %+v, want the parent's finish", last)
	}
}

func TestTrackerRuntimeProgress(t *testing.T) {
	var r recorder
	tr := newTracker(&r)
	parent := tr.start("", StepPull, Subject{Kind: ResourceImage, Name: "postgres"})
	progress := tr.runtimeProgress(context.Background(), parent.id())
	layer := runtime.Progress{ID: "a1b2c3d4e5f6", Kind: runtime.ProgressLayer, Name: "a1b2c3d4e5f6"}
	for _, p := range []runtime.Progress{
		withState(layer, runtime.ProgressStarted, 0, 0),
		withState(layer, runtime.ProgressRunning, 10, 100),
		withState(layer, runtime.ProgressDone, 0, 0),
	} {
		progress(p)
	}
	vertex := runtime.Progress{ID: "sha256:v", Kind: runtime.ProgressVertex, Name: "RUN make"}
	progress(withState(vertex, runtime.ProgressStarted, 0, 0))
	log := withState(vertex, runtime.ProgressRunning, 0, 0)
	log.Output = []byte("raw\n")
	progress(log)
	progress(withState(vertex, runtime.ProgressCached, 0, 0))
	progress(runtime.Progress{Output: []byte("whole build\n")})
	parent.finish(context.Background(), nil)

	events := r.all()
	assertBalanced(t, events)
	want := []Event{
		StepStarted{ID: "1", Kind: StepPull, Subject: Subject{Kind: ResourceImage, Name: "postgres"}},
		StepStarted{ID: "2", Parent: "1", Kind: StepPull, Subject: Subject{Kind: ResourceLayer, Name: "a1b2c3d4e5f6"}},
		Transfer{ID: "2", Current: 10, Total: 100},
		StepFinished{ID: "2", Outcome: OutcomeDone},
		StepStarted{ID: "3", Parent: "1", Kind: StepBuild, Subject: Subject{Kind: ResourceBuildStep, Name: "RUN make"}},
		StepOutput{ID: "3", Stream: StreamStdout, Data: []byte("raw\n")},
		StepFinished{ID: "3", Outcome: OutcomeKept},
		StepOutput{ID: "1", Stream: StreamStdout, Data: []byte("whole build\n")},
		StepFinished{ID: "1", Outcome: OutcomeDone},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events:\n%#v\nwant:\n%#v", events, want)
	}
}

func TestTrackerRuntimeFailureWhileCancelledIsInterrupted(t *testing.T) {
	var r recorder
	tr := newTracker(&r)
	parent := tr.start("", StepBuild, Subject{Kind: ResourceImage, Name: "img"})
	ctx, cancel := context.WithCancel(context.Background())
	progress := tr.runtimeProgress(ctx, parent.id())
	vertex := runtime.Progress{ID: "sha256:v", Kind: runtime.ProgressVertex, Name: "RUN sleep"}
	progress(withState(vertex, runtime.ProgressStarted, 0, 0))
	cancel()
	progress(withState(vertex, runtime.ProgressFailed, 0, 0))
	parent.finish(ctx, context.Canceled)

	events := r.all()
	assertBalanced(t, events)
	if f, _ := finished(events, "2"); f.Outcome != OutcomeInterrupted {
		t.Fatalf("vertex finished %+v, want interrupted", f)
	}
}

func withState(p runtime.Progress, state runtime.ProgressState, current, total int64) runtime.Progress {
	p.State, p.Current, p.Total = state, current, total
	return p
}

func TestActionOf(t *testing.T) {
	for _, c := range []struct {
		kind    StepKind
		outcome Outcome
		want    Action
	}{
		{StepCreate, OutcomeDone, ActionCreated},
		{StepStart, OutcomeDone, ActionStarted},
		{StepReplace, OutcomeDone, ActionReplaced},
		{StepRemove, OutcomeDone, ActionRemoved},
		{StepCreate, OutcomeKept, ActionKept},
		{StepStart, OutcomeKept, ActionKept},
		{StepCreate, OutcomeFailed, ""},
		{StepWait, OutcomeDone, ""},
	} {
		if got := ActionOf(c.kind, c.outcome); got != c.want {
			t.Errorf("ActionOf(%s, %s) = %q, want %q", c.kind, c.outcome, got, c.want)
		}
	}
}
