package runtimetest

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

// CheckProgress fails t when reports break the contract of runtime.Progress:
// each ID starts first, ends exactly once with a terminal state, reports
// nothing after that, and no ID is left open.
func CheckProgress(t testing.TB, reports []runtime.Progress) {
	t.Helper()
	open := map[string]bool{}
	closed := map[string]bool{}
	for i, p := range reports {
		if p.ID == "" {
			if p.State != "" && p.State != runtime.ProgressRunning {
				t.Errorf("report %d: empty ID with state %q", i, p.State)
			}
			continue
		}
		switch {
		case closed[p.ID]:
			t.Errorf("report %d: %s reported %q after its terminal state", i, p.ID, p.State)
		case !open[p.ID] && p.State != runtime.ProgressStarted:
			t.Errorf("report %d: %s reported %q before started", i, p.ID, p.State)
		case open[p.ID] && p.State == runtime.ProgressStarted:
			t.Errorf("report %d: %s started twice", i, p.ID)
		}
		open[p.ID] = true
		if p.State.Terminal() {
			closed[p.ID] = true
		}
	}
	for id := range open {
		if !closed[id] {
			t.Errorf("%s was never closed", id)
		}
	}
}

// progressLog records reports for the contract tests.
type progressLog struct {
	mu      sync.Mutex
	reports []runtime.Progress
}

func (l *progressLog) report(p runtime.Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reports = append(l.reports, p)
}

func (l *progressLog) all() []runtime.Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reports)
}

// String renders the reports for failure messages.
func (l *progressLog) String() string {
	var b strings.Builder
	for _, p := range l.all() {
		if p.ID == "" {
			b.Write(p.Output)
			continue
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", p.Kind, p.ID, p.Name, p.State)
		b.Write(p.Output)
	}
	return b.String()
}
