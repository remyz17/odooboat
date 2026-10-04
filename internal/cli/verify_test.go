package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/state"
)

type stubEngine struct{}

func (stubEngine) Capabilities() runtime.Capabilities {
	return runtime.Capabilities{runtime.CapEngineIdentity}
}
func (stubEngine) Close() error { return nil }
func (stubEngine) Info(context.Context) (runtime.EngineInfo, error) {
	return runtime.EngineInfo{Identity: runtime.EngineIdentity{Source: "stub.id", Value: "engine-1"}, Version: "1.0"}, nil
}

type stubConnector struct{}

func (stubConnector) Connect(context.Context, runtime.Locator) (runtime.Engine, error) {
	return stubEngine{}, nil
}

func TestEnvironmentVerifyCommand(t *testing.T) {
	root, base := cliStateProject(t)
	args := append(append([]string{}, base...), "environment", "verify", "--output", "json")
	first := executeWith(t, stubConnector{}, root, args...)
	if first.err != nil {
		t.Fatal(first.err)
	}
	var output struct {
		IdentityRecorded bool `json:"identityRecorded"`
		Engine           struct {
			Source string `json:"source"`
			Value  string `json:"value"`
		} `json:"engine"`
	}
	if err := json.Unmarshal([]byte(first.stdout), &output); err != nil || !output.IdentityRecorded || output.Engine.Value != "engine-1" {
		t.Fatalf("verify JSON = %s (%v)", first.stdout, err)
	}

	unsupported := execute(t, root, args...)
	if ExitCode(unsupported.err) != ExitRuntime {
		t.Fatalf("missing adapter: err = %v, exit %d", unsupported.err, ExitCode(unsupported.err))
	}
}

func TestRuntimeAndBusyExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{runtime.NewConnectionError(runtime.ErrUnavailable, "local", "", "down", ""), ExitRuntime},
		{fmt.Errorf("wrapped: %w", runtime.NewConnectionError(runtime.ErrUnsupported, "local", "", "ssh", "")), ExitRuntime},
		{&state.BusyError{EnvironmentID: "x"}, ExitBusy},
		{app.ErrState, ExitState},
	}
	for _, tc := range cases {
		if code := ExitCode(tc.err); code != tc.code {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, code, tc.code)
		}
	}
}
