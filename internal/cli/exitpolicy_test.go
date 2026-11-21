package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

// runWith runs root with extra commands added, as Run does.
func runWith(t *testing.T, extra []*cobra.Command, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := Dependencies{Stdout: &stdout, Stderr: &stderr, WorkingDir: t.TempDir()}
	root := NewRootCommand(deps)
	root.AddCommand(extra...)
	return runRoot(context.Background(), root, args, deps), stderr.String()
}

func failing(name string, err error, execFamily bool) *cobra.Command {
	cmd := &cobra.Command{Use: name, RunE: func(*cobra.Command, []string) error { return err }}
	cmd.Flags().Bool("flag", false, "")
	if execFamily {
		markExecFamily(cmd)
	}
	return cmd
}

func TestExitStatusPassesThrough(t *testing.T) {
	code, stderr := runWith(t, []*cobra.Command{failing("remote", &ExitStatus{Code: 3}, true)}, "remote")
	if code != 3 || stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 3 and nothing printed", code, stderr)
	}
}

func TestExecFamilyMapsFailuresTo125(t *testing.T) {
	extra := []*cobra.Command{failing("family", app.ErrRuntimeUnavailable, true), failing("plain", app.ErrRuntimeUnavailable, false)}
	if code, stderr := runWith(t, extra, "family"); code != ExitToolFailure || stderr == "" {
		t.Fatalf("exec-family failure: exit %d, stderr %q", code, stderr)
	}
	if code, _ := runWith(t, extra, "family", "--nope"); code != ExitToolFailure {
		t.Fatalf("exec-family unknown flag: exit %d, want 125", code)
	}
	if code, _ := runWith(t, extra, "plain"); code != ExitRuntime {
		t.Fatalf("plain command: exit %d, want %d", code, ExitRuntime)
	}
	if code, _ := runWith(t, extra, "plain", "--nope"); code != ExitUsage {
		t.Fatalf("plain unknown flag: exit %d, want %d", code, ExitUsage)
	}
}

func TestNotRunningExitCode(t *testing.T) {
	if code := ExitCode(app.ErrNotRunning); code != ExitNotRunning {
		t.Fatalf("ExitCode(ErrNotRunning) = %d, want %d", code, ExitNotRunning)
	}
}
