package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

func (f *lifecycleFixture) logs(role runtime.Role, opts runtime.LogOptions) (string, error) {
	var out bytes.Buffer
	err := f.service.Logs(context.Background(), LogsRequest{Selector: f.selector, Role: role, Options: opts, Stdout: &out, Stderr: &out})
	return out.String(), err
}

func TestLogsOfRunningEnvironment(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	opts := runtime.LogOptions{Follow: true, Tail: 20}
	out, err := f.logs(runtime.RoleOdoo, opts)
	if err != nil || out != "fatal: database is gone\n" {
		t.Fatalf("logs = %q, %v", out, err)
	}
	want := logCall{id: f.engine.containers[f.name(first, runtime.RoleOdoo)].ID, opts: opts}
	if len(f.engine.logCalls) == 0 || f.engine.logCalls[len(f.engine.logCalls)-1] != want {
		t.Fatalf("log calls = %+v, want last %+v", f.engine.logCalls, want)
	}
	if _, err := f.logs(runtime.RolePostgres, runtime.LogOptions{Tail: 1}); err != nil {
		t.Fatal(err)
	}
	if got := f.engine.logCalls[len(f.engine.logCalls)-1].id; got != f.engine.containers[f.name(first, runtime.RolePostgres)].ID {
		t.Fatalf("postgres logs read container %s", got)
	}
}

func TestAccessStoppedContainer(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	odoo := f.engine.containers[f.name(first, runtime.RoleOdoo)]
	odoo.State, odoo.ExitCode = runtime.ContainerExited, 3

	bound, release, err := f.service.connectBound(context.Background(), f.selector)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = bound.running(context.Background(), runtime.RoleOdoo)
	if !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "odooboat up") {
		t.Fatalf("running(odoo) = %v, want ErrNotRunning mentioning exited and odooboat up", err)
	}
	if _, err := f.logs(runtime.RoleOdoo, runtime.LogOptions{Tail: 5}); err != nil {
		t.Fatalf("logs of an exited container: %v", err)
	}
}

func TestAccessAfterDown(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	f.mustUp()
	if _, err := f.service.Down(context.Background(), DownRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.logs(runtime.RoleOdoo, runtime.LogOptions{Tail: 5}); !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "odooboat up") {
		t.Fatalf("logs after down = %v, want ErrNotRunning mentioning odooboat up", err)
	}
}

func TestAccessUnbound(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	if _, err := f.logs(runtime.RoleOdoo, runtime.LogOptions{Tail: 5}); !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "odooboat up") {
		t.Fatalf("logs before any up = %v, want ErrNotRunning mentioning odooboat up", err)
	}
}

func TestAccessIgnoresForeignContainers(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	// Another environment of the same workspace has its own odoo container,
	// listed first by name.
	other := runtime.Ownership{Scope: runtime.ScopeEnvironment, WorkspaceID: first.WorkspaceID,
		EnvironmentID: "88888888-8888-4888-8888-888888888888", Role: runtime.RoleOdoo, Spec: "sha256:" + strings.Repeat("0", 64),
		OperationID: "33333333-3333-4333-8333-333333333333", DisplayProject: "demo", DisplayEnvironment: "other", CreatedBy: "odooboat/test"}
	labels, err := other.Labels(runtime.ResourceContainer)
	if err != nil {
		t.Fatal(err)
	}
	f.engine.containers["aaa-other"] = &runtime.ContainerInfo{ID: "foreign", Name: "aaa-other", Labels: labels, State: runtime.ContainerRunning}

	bound, release, err := f.service.connectBound(context.Background(), f.selector)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	got, err := bound.running(context.Background(), runtime.RoleOdoo)
	if err != nil || got.ID != f.engine.containers[f.name(first, runtime.RoleOdoo)].ID {
		t.Fatalf("running(odoo) = %+v, %v", got, err)
	}
}

func TestLogsRequiresCapabilities(t *testing.T) {
	engine := newResourceEngine(t)
	f := newLifecycle(t, engine, "")
	f.mustUp()
	engine.capabilities = lifecycleCapabilities
	if _, err := f.logs(runtime.RoleOdoo, runtime.LogOptions{Follow: true}); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("follow without logs.follow = %v, want ErrRuntimeUnsupported", err)
	}
	if _, err := f.logs(runtime.RoleOdoo, runtime.LogOptions{Tail: 5}); err != nil {
		t.Fatalf("bounded read needs no capability: %v", err)
	}
}
