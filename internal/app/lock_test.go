package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/state"
	statefile "github.com/remyz17/odooboat/internal/state/file"
	"github.com/remyz17/odooboat/internal/workspace"
)

type fakeEngine struct {
	capabilities runtime.Capabilities
	identity     string
}

func (e *fakeEngine) Capabilities() runtime.Capabilities { return e.capabilities }
func (e *fakeEngine) Close() error                       { return nil }
func (e *fakeEngine) Info(context.Context) (runtime.EngineInfo, error) {
	return runtime.EngineInfo{
		Identity: runtime.EngineIdentity{Source: "fake.id", Value: e.identity},
		Version:  "1.0", OS: "linux", Arch: "arm64",
	}, nil
}

type fakeConnector struct {
	engine *fakeEngine
	err    error
	calls  int
}

func (c *fakeConnector) Connect(context.Context, runtime.Locator) (runtime.Engine, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.engine, nil
}

func identityEngine(id string) *fakeEngine {
	return &fakeEngine{capabilities: runtime.Capabilities{runtime.CapEngineIdentity}, identity: id}
}

func verifyFixture(t *testing.T, connector *fakeConnector) (WorkspaceService, Selector, string) {
	t.Helper()
	selector, root := statefulSelector(t)
	store := statefile.New()
	return NewWorkspaceService(store, store, connector, "test"), selector, filepath.Join(root, ".odooboat", "state.json")
}

func failFast(selector Selector) EnvironmentVerifyRequest {
	return EnvironmentVerifyRequest{Selector: selector, Wait: WaitOptions{Policy: WaitFailFast}}
}

func TestVerifyRecordsThenComparesEngineIdentity(t *testing.T) {
	connector := &fakeConnector{engine: identityEngine("engine-a")}
	service, selector, stateFile := verifyFixture(t, connector)
	ctx := context.Background()

	first, err := service.VerifyEnvironment(ctx, failFast(selector))
	if err != nil || !first.BindingCreated || !first.IdentityRecorded || first.Engine.Value != "engine-a" || first.Engine.Source != "fake.id" {
		t.Fatalf("first verify = %+v, %v", first, err)
	}
	value, _, err := statefile.New().Load(stateFile)
	if err != nil || value.Schema != workspace.SchemaVersion {
		t.Fatalf("state = %+v, %v", value, err)
	}
	if engine := value.Environments["default"].Engine; engine == nil || engine.Value != "engine-a" {
		t.Fatalf("recorded engine = %+v", engine)
	}

	second, err := service.VerifyEnvironment(ctx, failFast(selector))
	if err != nil || second.BindingCreated || second.IdentityRecorded || second.EnvironmentID != first.EnvironmentID {
		t.Fatalf("second verify = %+v, %v", second, err)
	}

	show, err := service.ShowEnvironment(ctx, EnvironmentShowRequest{Selector: selector})
	if err != nil || show.Engine == nil || show.Engine.Value != "engine-a" {
		t.Fatalf("show = %+v, %v", show, err)
	}
}

func TestVerifyRejectsAnotherEngine(t *testing.T) {
	connector := &fakeConnector{engine: identityEngine("engine-a")}
	service, selector, stateFile := verifyFixture(t, connector)
	if _, err := service.VerifyEnvironment(context.Background(), failFast(selector)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(stateFile)
	connector.engine = identityEngine("engine-b")
	_, err := service.VerifyEnvironment(context.Background(), failFast(selector))
	if !errors.Is(err, workspace.ErrBindingConflict) {
		t.Fatalf("err = %v, want ErrBindingConflict", err)
	}
	after, _ := os.ReadFile(stateFile)
	if string(before) != string(after) {
		t.Fatal("a mismatch changed the state")
	}
}

func TestVerifyRecordsUnavailableIdentity(t *testing.T) {
	connector := &fakeConnector{engine: &fakeEngine{identity: "ignored"}}
	service, selector, _ := verifyFixture(t, connector)
	result, err := service.VerifyEnvironment(context.Background(), failFast(selector))
	if err != nil || !result.Engine.Unavailable || result.Engine.Value != "" || !result.IdentityRecorded {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	connector.engine = identityEngine("engine-a")
	if result, err := service.VerifyEnvironment(context.Background(), failFast(selector)); err != nil || !result.Engine.Unavailable {
		t.Fatalf("an unavailable record was replaced: %+v, %v", result, err)
	}
}

func TestVerifyRejectsUnverifiableRecordedIdentity(t *testing.T) {
	connector := &fakeConnector{engine: identityEngine("engine-a")}
	service, selector, _ := verifyFixture(t, connector)
	if _, err := service.VerifyEnvironment(context.Background(), failFast(selector)); err != nil {
		t.Fatal(err)
	}
	connector.engine = &fakeEngine{identity: "engine-a"}
	if _, err := service.VerifyEnvironment(context.Background(), failFast(selector)); !errors.Is(err, workspace.ErrBindingConflict) {
		t.Fatalf("err = %v, want ErrBindingConflict", err)
	}
}

func TestVerifyRuntimeUnavailableRecordsNothing(t *testing.T) {
	connector := &fakeConnector{err: runtime.NewConnectionError(runtime.ErrUnavailable, "local", "unix:///x.sock", "down", "")}
	service, selector, stateFile := verifyFixture(t, connector)
	_, err := service.VerifyEnvironment(context.Background(), failFast(selector))
	if !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("err = %v, want ErrRuntimeUnavailable", err)
	}
	value, _, _ := statefile.New().Load(stateFile)
	if binding, ok := value.Environments["default"]; !ok || binding.Engine != nil {
		t.Fatalf("binding = %+v, %v", binding, ok)
	}
}

func TestVerifyFailsFastWhenEnvironmentIsLocked(t *testing.T) {
	connector := &fakeConnector{engine: identityEngine("engine-a")}
	service, selector, stateFile := verifyFixture(t, connector)
	bound, err := service.BindEnvironment(context.Background(), EnvironmentBindRequest{Selector: selector})
	if err != nil {
		t.Fatal(err)
	}
	location := workspace.Location{StateFile: stateFile}
	lock, err := statefile.New().LockEnvironment(context.Background(), location, bound.EnvironmentID,
		state.LockRequest{Policy: state.WaitFailFast, Holder: state.Holder{PID: 1, Operation: "up"}})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	_, err = service.VerifyEnvironment(context.Background(), failFast(selector))
	var busy *state.BusyError
	if !errors.Is(err, ErrEnvironmentBusy) || !errors.As(err, &busy) || busy.Holder == nil || busy.Holder.Operation != "up" {
		t.Fatalf("err = %v, want busy held by up", err)
	}
	if connector.calls != 0 {
		t.Fatal("the runtime was contacted without the environment lock")
	}
}

func TestVerifyBindingUnderLock(t *testing.T) {
	const id = "3f9a2c1e-2222-4222-8222-222222222222"
	desired := workspace.Connection{Alias: "local", Kind: "docker", Context: "default"}
	stateWith := func(binding workspace.Binding) *workspace.State {
		return &workspace.State{Environments: map[string]workspace.Binding{"default": binding}}
	}
	cases := []struct {
		name       string
		inspection workspace.Inspection
		want       error
	}{
		{"unchanged", workspace.Inspection{Status: workspace.StatusCurrent, State: stateWith(workspace.Binding{ID: id, Connection: desired})}, nil},
		{"duplicate", workspace.Inspection{Status: workspace.StatusDuplicate, State: stateWith(workspace.Binding{ID: id, Connection: desired})}, workspace.ErrDuplicate},
		{"state removed", workspace.Inspection{Status: workspace.StatusUninitialized}, workspace.ErrBindingConflict},
		{"binding removed", workspace.Inspection{Status: workspace.StatusCurrent, State: &workspace.State{Environments: map[string]workspace.Binding{}}}, workspace.ErrBindingConflict},
		{"rekeyed", workspace.Inspection{Status: workspace.StatusCurrent, State: stateWith(workspace.Binding{ID: "4f9a2c1e-2222-4222-8222-222222222222", Connection: desired})}, workspace.ErrBindingConflict},
		{"rebound", workspace.Inspection{Status: workspace.StatusCurrent, State: stateWith(workspace.Binding{ID: id, Connection: workspace.Connection{Alias: "other", Kind: "docker", Context: "default"}})}, workspace.ErrBindingConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verifyBinding(tc.inspection, "default", id, desired)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
