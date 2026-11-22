package app

import (
	"context"
	"strconv"
	"sync"

	"github.com/remyz17/odooboat/internal/runtime"
)

// Event is something that happens during an operation (ADR 0011 §4). Events
// carry no wording: presentation chooses the words. The set is closed.
type Event interface{ event() }

// Reporter receives events. It must be safe for concurrent use and return
// promptly, never waiting on rendering. A nil Reporter discards events.
type Reporter interface{ Report(Event) }

// ResourceKind names what a step acts on.
type ResourceKind string

const (
	ResourceImage     ResourceKind = "image"
	ResourceSource    ResourceKind = "source"
	ResourceContainer ResourceKind = "container"
	ResourceNetwork   ResourceKind = "network"
	ResourceVolume    ResourceKind = "volume"
	ResourceDatabase  ResourceKind = "database"
	ResourceLayer     ResourceKind = "layer"
	ResourceBuildStep ResourceKind = "build-step"
)

// StepKind names what a step does.
type StepKind string

const (
	StepPull    StepKind = "pull"
	StepFetch   StepKind = "fetch"
	StepBuild   StepKind = "build"
	StepCreate  StepKind = "create"
	StepStart   StepKind = "start"
	StepReplace StepKind = "replace"
	StepRemove  StepKind = "remove"
	StepWait    StepKind = "wait"
)

// Outcome is how a step finished. Interrupted is reported when the
// operation's context is cancelled, failed for any other error.
type Outcome string

const (
	OutcomeDone        Outcome = "done"
	OutcomeReused      Outcome = "reused"
	OutcomeKept        Outcome = "kept"
	OutcomeSkipped     Outcome = "skipped"
	OutcomeFailed      Outcome = "failed"
	OutcomeInterrupted Outcome = "interrupted"
)

// Stream names an output stream of a step.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Subject is the resource a step acts on.
type Subject struct {
	Kind ResourceKind
	// Name is an engine name, a reference, or a database name.
	Name string
	// Role is set when the resource has one.
	Role Role
}

// StepStarted opens a step. Steps nest through Parent.
type StepStarted struct {
	ID, Parent string
	Kind       StepKind
	Subject    Subject
}

// StepFinished closes a step; it follows every StepStarted exactly once.
type StepFinished struct {
	ID      string
	Outcome Outcome
	Err     error
}

// StepOutput is raw output of a step, not split into lines.
type StepOutput struct {
	ID     string
	Stream Stream
	Data   []byte
}

// Transfer is byte progress of a step; Total is zero when unknown.
type Transfer struct {
	ID             string
	Current, Total int64
}

// LockWaiting reports that the operation waits for the environment lock.
// Holder is nil when the holding process is unknown.
type LockWaiting struct{ Holder *LockHolder }

// DatabaseSelected reports the database a session chose by itself.
type DatabaseSelected struct{ Name string }

// SessionOperation names a session operation that may fail without ending
// the session.
type SessionOperation string

const (
	SessionResize SessionOperation = "resize"
	SessionSignal SessionOperation = "signal"
)

// SessionDegraded reports that a session operation failed; the session
// goes on.
type SessionDegraded struct {
	Operation SessionOperation
	Err       error
}

func (StepStarted) event()      {}
func (StepFinished) event()     {}
func (StepOutput) event()       {}
func (Transfer) event()         {}
func (LockWaiting) event()      {}
func (DatabaseSelected) event() {}
func (SessionDegraded) event()  {}

// Action is what an operation did to a resource, as results report it.
type Action string

const (
	ActionCreated  Action = "created"
	ActionKept     Action = "kept"
	ActionStarted  Action = "started"
	ActionReplaced Action = "replaced"
	ActionRemoved  Action = "removed"
)

// ActionOf derives a result's action from the step that produced it, so
// results and events never disagree; "" when the step did nothing reportable.
func ActionOf(kind StepKind, outcome Outcome) Action {
	if outcome == OutcomeKept {
		return ActionKept
	}
	if outcome != OutcomeDone {
		return ""
	}
	switch kind {
	case StepCreate:
		return ActionCreated
	case StepStart:
		return ActionStarted
	case StepReplace:
		return ActionReplaced
	case StepRemove:
		return ActionRemoved
	}
	return ""
}

// tracker numbers the steps of one operation and reports their events. It
// guarantees the event contract: a finishing step first closes its open
// children, and a finished step reports nothing more.
type tracker struct {
	reporter Reporter
	mu       sync.Mutex
	last     int
	open     map[string]*stepRun
}

func newTracker(r Reporter) *tracker {
	return &tracker{reporter: r, open: map[string]*stepRun{}}
}

// stepRun is one started step.
type stepRun struct {
	t        *tracker
	stepID   string
	children []*stepRun
	finished bool
}

// emit reports e; callers hold t.mu, so events keep their order.
func (t *tracker) emit(e Event) {
	if t.reporter != nil {
		t.reporter.Report(e)
	}
}

// notify reports an event that belongs to no step.
func (t *tracker) notify(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emit(e)
}

// start opens a step under parent ("" for a top-level step).
func (t *tracker) start(parent string, kind StepKind, subject Subject) *stepRun {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last++
	s := &stepRun{t: t, stepID: strconv.Itoa(t.last)}
	if p, ok := t.open[parent]; ok {
		p.children = append(p.children, s)
	} else {
		parent = ""
	}
	t.open[s.stepID] = s
	t.emit(StepStarted{ID: s.stepID, Parent: parent, Kind: kind, Subject: subject})
	return s
}

func (s *stepRun) id() string { return s.stepID }

// output reports raw output of the step.
func (s *stepRun) output(stream Stream, data []byte) {
	if len(data) == 0 {
		return
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	if !s.finished {
		s.t.emit(StepOutput{ID: s.stepID, Stream: stream, Data: data})
	}
}

// transfer reports byte progress of the step.
func (s *stepRun) transfer(current, total int64) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	if !s.finished {
		s.t.emit(Transfer{ID: s.stepID, Current: current, Total: total})
	}
}

// finish closes the step: done when err is nil, interrupted when ctx is
// cancelled, failed otherwise. A second call does nothing.
func (s *stepRun) finish(ctx context.Context, err error) {
	switch {
	case err == nil:
		s.close(OutcomeDone, nil)
	case ctx.Err() != nil:
		s.close(OutcomeInterrupted, err)
	default:
		s.close(OutcomeFailed, err)
	}
}

// finishAs closes the step with an outcome that is not an error.
func (s *stepRun) finishAs(outcome Outcome) { s.close(outcome, nil) }

func (s *stepRun) close(outcome Outcome, err error) {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.closeLocked(outcome, err)
}

func (s *stepRun) closeLocked(outcome Outcome, err error) {
	if s.finished {
		return
	}
	for _, c := range s.children {
		c.closeLocked(outcome, nil)
	}
	s.finished = true
	delete(s.t.open, s.stepID)
	s.t.emit(StepFinished{ID: s.stepID, Outcome: outcome, Err: err})
}

// runtimeProgress maps runtime progress onto steps nested under parent.
// Runtime IDs are scoped to the returned func, so two pulls reporting the
// same layer never share a step.
func (t *tracker) runtimeProgress(ctx context.Context, parent string) runtime.ProgressFunc {
	var mu sync.Mutex
	steps := map[string]*stepRun{}
	return func(p runtime.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.ID == "" {
			t.outputTo(parent, p.Output)
			return
		}
		s, ok := steps[p.ID]
		if !ok {
			kind, subject := StepBuild, Subject{Kind: ResourceBuildStep, Name: p.Name}
			if p.Kind == runtime.ProgressLayer {
				kind, subject = StepPull, Subject{Kind: ResourceLayer, Name: p.Name}
			}
			s = t.start(parent, kind, subject)
			steps[p.ID] = s
		}
		if p.Current != 0 || p.Total != 0 {
			s.transfer(p.Current, p.Total)
		}
		s.output(StreamStdout, p.Output)
		switch p.State {
		case runtime.ProgressCached:
			s.finishAs(OutcomeKept)
		case runtime.ProgressDone:
			s.finishAs(OutcomeDone)
		case runtime.ProgressFailed:
			if ctx.Err() != nil {
				s.finishAs(OutcomeInterrupted)
			} else {
				s.finishAs(OutcomeFailed)
			}
		}
	}
}

// outputTo reports output on the open step id, if any.
func (t *tracker) outputTo(id string, data []byte) {
	t.mu.Lock()
	s, ok := t.open[id]
	t.mu.Unlock()
	if ok {
		s.output(StreamStdout, data)
	}
}
