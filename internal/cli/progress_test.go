package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
	statefile "github.com/remyz17/odooboat/internal/state/file"
	"github.com/spf13/cobra"
)

// runCLI runs the root command with extra commands, as Run does, and
// returns the exit code and stderr.
func runCLI(t *testing.T, deps Dependencies, extra []*cobra.Command, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	if deps.Stderr == nil {
		deps.Stderr = &stderr
	}
	if deps.Stdout == nil {
		deps.Stdout = io.Discard
	}
	if deps.WorkingDir == "" {
		deps.WorkingDir = t.TempDir()
	}
	if deps.Getenv == nil {
		deps.Getenv = func(string) string { return "" }
	}
	root := NewRootCommand(deps)
	root.AddCommand(extra...)
	return runRoot(context.Background(), root, args, deps), stderr.String()
}

// emitting is a command that drives the invocation's presenter as an
// operation would.
func emitting(name string, run func(p *presenter) error) *cobra.Command {
	return &cobra.Command{Use: name, Args: exactArgs(0, "no arguments"), RunE: func(cmd *cobra.Command, _ []string) error {
		return run(presenterOf(cmd))
	}}
}

func jsonLines(t *testing.T, stderr string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("stderr line is not JSON: %q\nstderr:\n%s", line, stderr)
		}
		events = append(events, event)
	}
	return events
}

func TestErrorTable(t *testing.T) {
	for _, c := range errorClasses {
		for _, sentinel := range c.sentinels {
			err := fmt.Errorf("x: %w", sentinel)
			if got := ExitCode(err); got != c.exit {
				t.Errorf("ExitCode(%v) = %d, want %d", sentinel, got, c.exit)
			}
			if got := ErrorCode(err); got != c.code {
				t.Errorf("ErrorCode(%v) = %q, want %q", sentinel, got, c.code)
			}
		}
	}
	for err, want := range map[error]string{
		context.Canceled:            "interrupted",
		errors.New("anything else"): "error",
		app.ErrImageNotFound:        "not-found",
		ErrUsage:                    "usage",
	} {
		if got := ErrorCode(err); got != want {
			t.Errorf("ErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
	if ExitCode(errors.New("anything else")) != ExitError || ExitCode(nil) != ExitOK {
		t.Error("default exit codes changed")
	}
}

func TestJSONProgressStderrIsOnlyJSON(t *testing.T) {
	extra := func() []*cobra.Command {
		return []*cobra.Command{emitting("warns", func(p *presenter) error {
			p.Warn([]string{"no masterPassword is configured"})
			return nil
		}),
			emitting("waits", func(p *presenter) error {
				p.Reporter().Report(app.LockWaiting{Holder: &app.LockHolder{Operation: "up", PID: 1, Host: "h"}})
				return app.WithHint(fmt.Errorf("%w: held", app.ErrEnvironmentBusy), "retry later")
			})}
	}
	cases := []struct {
		args []string
		code int
		last string // error code of the last event; "" when none
	}{
		{[]string{"warns", "extra-arg"}, ExitUsage, "usage"},
		{[]string{"warns"}, ExitOK, ""},
		{[]string{"waits"}, ExitBusy, "locked"},
		{[]string{"warns", "--nope"}, ExitUsage, "usage"},
		{[]string{"--nope", "warns"}, ExitUsage, "usage"},
		{[]string{"--progress", "bogus", "warns"}, ExitUsage, "usage"},
	}
	for _, c := range cases {
		args := append([]string{"--progress=json"}, c.args...)
		if c.args[0] == "--progress" {
			args = c.args
		}
		code, stderr := runCLI(t, Dependencies{}, extra(), args...)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d", args, code, c.code)
		}
		if c.last == "" {
			if stderr != "" {
				t.Errorf("%v: stderr %q, want nothing", args, stderr)
			}
			continue
		}
		if c.args[0] == "--progress" {
			// An invalid --progress value cannot select json.
			if !strings.Contains(stderr, "error: ") {
				t.Errorf("%v: stderr %q", args, stderr)
			}
			continue
		}
		events := jsonLines(t, stderr)
		if last := events[len(events)-1]; last["type"] != "error" || last["code"] != c.last {
			t.Errorf("%v: last event %v, want error %s", args, last, c.last)
		}
	}
}

func TestStatusWarningsFollowProgressFormat(t *testing.T) {
	root, base := cliStateProject(t)
	deps := Dependencies{WorkingDir: root, Config: app.NewConfigService(),
		Workspace: app.NewWorkspaceService(statefile.New(), statefile.New(), runtime.Connectors{}, "test")}
	code, stderr := runCLI(t, deps, nil, append(base, "status", "--progress=plain")...)
	if code != ExitOK || !strings.HasPrefix(stderr, "warning: no masterPassword") {
		t.Fatalf("plain: exit %d, stderr %q", code, stderr)
	}
	code, stderr = runCLI(t, deps, nil, append(base, "status", "--progress=json")...)
	if code != ExitOK || stderr != "" {
		t.Fatalf("json: exit %d, stderr %q", code, stderr)
	}
}

type ttyStderr struct {
	noTerminal
	tty bool
}

func (t ttyStderr) StderrIsTerminal() bool { return t.tty }

func TestAutoProgressJSONWithOutputJSON(t *testing.T) {
	fails := func() []*cobra.Command {
		return []*cobra.Command{emitting("fails", func(*presenter) error { return app.ErrNotRunning })}
	}
	_, stderr := runCLI(t, Dependencies{}, fails(), "-o", "json", "fails")
	if events := jsonLines(t, stderr); events[0]["code"] != "not-running" {
		t.Fatalf("not a terminal: %v", events)
	}
	_, stderr = runCLI(t, Dependencies{Terminal: ttyStderr{tty: true}}, fails(), "-o", "json", "fails")
	if !strings.HasPrefix(stderr, "error: ") {
		t.Fatalf("terminal: stderr %q, want plain", stderr)
	}
	_, stderr = runCLI(t, Dependencies{}, fails(), "fails")
	if !strings.HasPrefix(stderr, "error: ") {
		t.Fatalf("yaml result: stderr %q, want plain", stderr)
	}
}

func TestAutoProgressReadsOutputPreference(t *testing.T) {
	dir := t.TempDir()
	user := dir + "/user.yaml"
	if err := os.WriteFile(user, []byte("schema: 1\npreferences:\n  output: json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fails := []*cobra.Command{emitting("fails", func(*presenter) error { return app.ErrNotRunning })}
	_, stderr := runCLI(t, Dependencies{WorkingDir: dir}, fails, "--user-config", user, "fails")
	if events := jsonLines(t, stderr); events[0]["type"] != "error" {
		t.Fatalf("stderr %q, want json", stderr)
	}
}

func TestPlainErrorWithHint(t *testing.T) {
	fails := []*cobra.Command{emitting("fails", func(*presenter) error {
		return app.WithHint(fmt.Errorf("%w: environment has never been started", app.ErrNotRunning), "run odooboat up")
	})}
	code, stderr := runCLI(t, Dependencies{}, fails, "fails")
	want := "error: environment is not running: environment has never been started\nhint: run odooboat up\n"
	if code != ExitNotRunning || stderr != want {
		t.Fatalf("exit %d, stderr %q, want %q", code, stderr, want)
	}
}

func TestInterruptedOutputPrecedesError(t *testing.T) {
	build := []*cobra.Command{emitting("build", func(p *presenter) error {
		r := p.Reporter()
		r.Report(app.StepStarted{ID: "1", Kind: app.StepBuild, Subject: app.Subject{Kind: app.ResourceImage, Name: "img"}})
		r.Report(app.StepOutput{ID: "1", Stream: app.StreamStdout, Data: []byte("compiling\n")})
		r.Report(app.StepFinished{ID: "1", Outcome: app.OutcomeInterrupted, Err: context.Canceled})
		return context.Canceled
	})}
	code, stderr := runCLI(t, Dependencies{}, build, "--progress=plain", "build")
	output, failure := strings.Index(stderr, "img | compiling"), strings.Index(stderr, "error: ")
	if code != ExitError || output < 0 || failure < output {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestClosedStderrDoesNotBlockExit(t *testing.T) {
	noisy := []*cobra.Command{emitting("noisy", func(p *presenter) error {
		for range 1000 {
			p.Reporter().Report(app.StepOutput{ID: "1", Data: []byte("x\n")})
		}
		return app.ErrNotReady
	})}
	done := make(chan int, 1)
	go func() {
		code, _ := runCLI(t, Dependencies{Stderr: brokenWriter{}}, noisy, "--step-output=all", "noisy")
		done <- code
	}()
	select {
	case code := <-done:
		if code != ExitNotReady {
			t.Fatalf("exit %d, want %d", code, ExitNotReady)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return with a closed stderr")
	}
}

func TestResolveFormatWiring(t *testing.T) {
	if got := ui.ResolveFormat(ui.FormatAuto, false, true); got != ui.FormatJSON {
		t.Fatalf("auto without a terminal and a json result = %s", got)
	}
	if config.OutputJSON != "json" {
		t.Fatal("result format name changed")
	}
}

func TestInterruptedErrorMessage(t *testing.T) {
	cancelled := func(err error) []*cobra.Command {
		return []*cobra.Command{emitting("stops", func(*presenter) error { return err })}
	}
	if _, stderr := runCLI(t, Dependencies{}, cancelled(context.Canceled), "stops"); stderr != "error: interrupted\n" {
		t.Fatalf("bare cancellation: stderr %q", stderr)
	}
	wrapped := fmt.Errorf("pull x: %w", context.Canceled)
	if _, stderr := runCLI(t, Dependencies{}, cancelled(wrapped), "stops"); stderr != "error: pull x: context canceled\n" {
		t.Fatalf("wrapped cancellation keeps its context: stderr %q", stderr)
	}
}
