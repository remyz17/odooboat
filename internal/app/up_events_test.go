package app

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/state"
	statefile "github.com/remyz17/odooboat/internal/state/file"
	"github.com/remyz17/odooboat/internal/workspace"
)

func (f *lifecycleFixture) upReporting(ctx context.Context, r Reporter) (UpResult, error) {
	return f.service.Up(ctx, UpRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}, Reporter: r})
}

// containerSteps returns the top-level steps on containers, by role.
func resourceSteps(events []Event) map[Role]StepStarted {
	out := map[Role]StepStarted{}
	for _, s := range topLevel(events) {
		switch s.Subject.Kind {
		case ResourceContainer, ResourceNetwork, ResourceVolume:
			out[s.Subject.Role] = s
		}
	}
	return out
}

func TestUpReportsStepPerResource(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	for _, want := range []struct {
		outcome Outcome
		action  Action
	}{{OutcomeDone, ActionCreated}, {OutcomeKept, ActionKept}} {
		var r recorder
		result, err := f.upReporting(context.Background(), &r)
		if err != nil {
			t.Fatal(err)
		}
		events := r.all()
		assertBalanced(t, events)
		steps := resourceSteps(events)
		for _, role := range []Role{runtime.RoleEnvironment, runtime.RolePostgresData, runtime.RoleOdooData, RolePostgres, RoleOdoo} {
			s, ok := steps[role]
			if !ok || s.Kind != StepCreate {
				t.Fatalf("%s: step %+v, want create", role, s)
			}
			if f, _ := finished(events, s.ID); f.Outcome != want.outcome {
				t.Errorf("%s finished %+v, want %s", role, f, want.outcome)
			}
			waits := childrenOf(events, s.ID)
			if container := role == RoleOdoo || role == RolePostgres; container != (len(waits) == 1 && waits[0].Kind == StepWait) {
				t.Errorf("%s: children %+v", role, waits)
			}
		}
		for _, report := range result.Resources {
			if report.Action != want.action {
				t.Errorf("%s action = %q, want %q", report.Name, report.Action, want.action)
			}
		}
	}
}

// failingCalls are the fake engine calls up makes while applying its steps.
var failingCalls = []string{"create network", "create volume", "create container", "copy", "start", "exec", "inspect running"}

func TestUpFailureLeavesNoOpenStep(t *testing.T) {
	for _, call := range failingCalls {
		t.Run(call, func(t *testing.T) {
			engine := newResourceEngine(t)
			engine.failures = map[string]error{call: errors.New("injected " + call)}
			var r recorder
			_, err := newLifecycle(t, engine, "").upReporting(context.Background(), &r)
			if err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("err = %v", err)
			}
			events := r.all()
			assertBalanced(t, events)
			failed := 0
			for _, s := range topLevel(events) {
				if f, _ := finished(events, s.ID); f.Outcome == OutcomeFailed {
					failed++
				}
			}
			if failed != 1 {
				t.Fatalf("%d top-level steps failed, want 1", failed)
			}
		})
	}
}

func TestUpCancelledIsInterrupted(t *testing.T) {
	engine := newResourceEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.execOutput = func(runtime.ExecRequest) (string, int) {
		cancel()
		return "", 1
	}
	var r recorder
	if _, err := newLifecycle(t, engine, "").upReporting(ctx, &r); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	events := r.all()
	assertBalanced(t, events)
	var wait StepStarted
	for _, s := range started(events) {
		if s.Kind == StepWait {
			wait = s
		}
	}
	for _, id := range []string{wait.ID, wait.Parent} {
		if f, _ := finished(events, id); f.Outcome != OutcomeInterrupted {
			t.Errorf("step %s finished %+v, want interrupted", id, f)
		}
	}
}

func TestUpNotReadyReportsLogsAsWaitOutput(t *testing.T) {
	engine := newResourceEngine(t)
	engine.exitOnStart = runtime.RoleOdoo
	var r recorder
	_, err := newLifecycle(t, engine, "").upReporting(context.Background(), &r)
	if !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "database is gone") {
		t.Fatalf("err = %v, want ErrNotReady without log lines", err)
	}
	if hint := Hint(err); !strings.Contains(hint, "odooboat logs odoo") {
		t.Fatalf("hint = %q", hint)
	}
	events := r.all()
	assertBalanced(t, events)
	var wait StepStarted
	for _, s := range started(events) {
		if s.Kind == StepWait && s.Subject.Role == RoleOdoo {
			wait = s
		}
	}
	var output []byte
	for _, e := range events {
		if o, ok := e.(StepOutput); ok && o.ID == wait.ID {
			output = append(output, o.Data...)
		}
	}
	if !strings.Contains(string(output), "database is gone") {
		t.Fatalf("wait output = %q", output)
	}
}

func TestDownReportsRemoveSteps(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	var r recorder
	if _, err := f.service.Down(context.Background(), DownRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}, Reporter: &r}); err != nil {
		t.Fatal(err)
	}
	events := r.all()
	assertBalanced(t, events)
	var got []Role
	for _, s := range started(events) {
		if s.Kind != StepRemove {
			t.Fatalf("step %+v, want remove", s)
		}
		got = append(got, s.Subject.Role)
	}
	if want := []Role{RoleOdoo, RolePostgres, runtime.RoleEnvironment}; !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
}

// waitingReporter releases a held lock once the wait is reported.
type waitingReporter struct {
	recorder
	release func()
}

func (w *waitingReporter) Report(e Event) {
	w.recorder.Report(e)
	if _, ok := e.(LockWaiting); ok {
		w.release()
	}
}

func TestWithEnvironmentReportsLockWaiting(t *testing.T) {
	connector := &fakeConnector{engine: identityEngine("engine-a")}
	service, selector, stateFile := verifyFixture(t, connector)
	bound, err := service.BindEnvironment(context.Background(), EnvironmentBindRequest{Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := statefile.New().LockEnvironment(context.Background(), workspace.Location{StateFile: stateFile}, bound.EnvironmentID,
		state.LockRequest{Policy: state.WaitFailFast, Holder: state.Holder{PID: 1, Operation: "up"}})
	if err != nil {
		t.Fatal(err)
	}
	r := &waitingReporter{release: func() { _ = lock.Release() }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := service.VerifyEnvironment(ctx, EnvironmentVerifyRequest{Selector: selector, Wait: WaitOptions{Policy: Wait}, Reporter: r}); err != nil {
		t.Fatal(err)
	}
	events := r.all()
	if len(events) == 0 {
		t.Fatal("no event reported")
	}
	waiting, ok := events[0].(LockWaiting)
	if !ok || waiting.Holder == nil || waiting.Holder.Operation != "up" {
		t.Fatalf("first event = %#v, want LockWaiting held by up", events[0])
	}
}

// wordingFields are field names that may not be io.Writers in app.
var wordingFields = []string{"Progress"}

// TestNoProgressWriterInApp keeps progress and notice wording out of the
// application: no io.Writer field may be named Progress or Notice.
func TestNoProgressWriterInApp(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok {
				return true
			}
			sel, ok := field.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Writer" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "io" {
				return true
			}
			for _, n := range field.Names {
				if slices.Contains(wordingFields, n.Name) {
					t.Errorf("%s: field %s io.Writer carries wording; report events instead", fset.Position(field.Pos()), n.Name)
				}
			}
			return true
		})
	}
}
