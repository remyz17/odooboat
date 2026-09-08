package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/spf13/cobra"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "config", "valid"))
	if err != nil {
		t.Fatalf("locate fixtures: %v", err)
	}
	return root
}

type run struct {
	stdout string
	stderr string
	err    error
}

func execute(t *testing.T, workingDir string, args ...string) run {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := NewRootCommand(Dependencies{
		Config:     app.NewConfigService(),
		Stdin:      strings.NewReader(""),
		Stdout:     &stdout,
		Stderr:     &stderr,
		WorkingDir: workingDir,
	})
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return run{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func fixtureArgs(t *testing.T, args ...string) []string {
	t.Helper()
	return append([]string{"--user-config", filepath.Join(fixtureRoot(t), "user.yaml")}, args...)
}

func TestInvocationPatchOnlyCarriesChangedFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addOverrideFlags(cmd)

	if patch := invocationPatch(cmd); patch.OdooVersion != nil || patch.RuntimeConnection != nil {
		t.Fatalf("unparsed command produced %+v, want an empty patch", patch)
	}

	if err := cmd.ParseFlags([]string{"--odoo-version", "16.0"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	patch := invocationPatch(cmd)
	if patch.OdooVersion == nil || *patch.OdooVersion != "16.0" {
		t.Errorf("odooVersion = %v, want 16.0", patch.OdooVersion)
	}
	if patch.RuntimeConnection != nil {
		t.Errorf("runtimeConnection = %v, want nil for an unchanged flag", *patch.RuntimeConnection)
	}
}

func TestPresentationFlagsStayOutOfThePatch(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	addOverrideFlags(cmd)
	cmd.Flags().String("output", "", "")
	cmd.Flags().String("env", "", "")

	if err := cmd.ParseFlags([]string{"--output", "json", "--env", "dev"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if patch := invocationPatch(cmd); patch != (config.InvocationPatch{}) {
		t.Errorf("patch = %+v, want empty", patch)
	}
}

func TestConfigShowJSON(t *testing.T) {
	projectDir := filepath.Join(fixtureRoot(t), "project")
	result := execute(t, projectDir, fixtureArgs(t, "config", "show", "--env", "dev", "-o", "json")...)
	if result.err != nil {
		t.Fatalf("show: %v (%s)", result.err, result.stderr)
	}

	var decoded struct {
		Config struct {
			OdooVersion string `json:"odooVersion"`
			Odoo        struct {
				MasterPassword map[string]string `json:"masterPassword"`
			} `json:"odoo"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &decoded); err != nil {
		t.Fatalf("decode json output: %v\n%s", err, result.stdout)
	}
	if decoded.Config.OdooVersion != "17.0" {
		t.Errorf("odooVersion = %q, want 17.0", decoded.Config.OdooVersion)
	}
	if decoded.Config.Odoo.MasterPassword["source"] != "env" {
		t.Errorf("masterPassword = %v, want an env reference", decoded.Config.Odoo.MasterPassword)
	}
}

func TestConfigShowIsDeterministic(t *testing.T) {
	projectDir := filepath.Join(fixtureRoot(t), "project")
	first := execute(t, projectDir, fixtureArgs(t, "config", "show", "--env", "dev")...)
	second := execute(t, projectDir, fixtureArgs(t, "config", "show", "--env", "dev")...)
	if first.err != nil || second.err != nil {
		t.Fatalf("show failed: %v / %v", first.err, second.err)
	}
	if first.stdout != second.stdout {
		t.Error("two identical invocations produced different output")
	}
}

func TestRootCommandsAreIndependent(t *testing.T) {
	projectDir := filepath.Join(fixtureRoot(t), "project")
	dev := execute(t, projectDir, fixtureArgs(t, "config", "explain", "odooVersion", "--env", "dev")...)
	base := execute(t, projectDir, fixtureArgs(t, "config", "explain", "odooVersion")...)

	if dev.err != nil || base.err != nil {
		t.Fatalf("explain failed: %v / %v", dev.err, base.err)
	}
	if !strings.Contains(dev.stdout, `"17.0"`) {
		t.Errorf("dev output does not report 17.0:\n%s", dev.stdout)
	}
	if !strings.Contains(base.stdout, `"18.0"`) {
		t.Errorf("default output does not report 18.0:\n%s", base.stdout)
	}
}

func TestExitCodes(t *testing.T) {
	root := fixtureRoot(t)
	projectDir := filepath.Join(root, "project")

	tests := []struct {
		name       string
		workingDir string
		args       []string
		want       int
	}{
		{
			name:       "valid configuration",
			workingDir: projectDir,
			args:       fixtureArgs(t, "config", "validate"),
			want:       ExitOK,
		},
		{
			name:       "unknown command",
			workingDir: projectDir,
			args:       []string{"bogus"},
			want:       ExitUsage,
		},
		{
			name:       "unknown flag",
			workingDir: projectDir,
			args:       []string{"config", "show", "--nope"},
			want:       ExitUsage,
		},
		{
			name:       "explain without an argument",
			workingDir: projectDir,
			args:       fixtureArgs(t, "config", "explain"),
			want:       ExitUsage,
		},
		{
			name:       "unknown output format",
			workingDir: projectDir,
			args:       fixtureArgs(t, "config", "show", "-o", "toml"),
			want:       ExitUsage,
		},
		{
			name:       "missing project",
			workingDir: t.TempDir(),
			args:       fixtureArgs(t, "config", "validate"),
			want:       ExitConfig,
		},
		{
			name:       "missing explicit project file",
			workingDir: projectDir,
			args:       fixtureArgs(t, "config", "validate", "--project", filepath.Join(root, "nope.yaml")),
			want:       ExitConfig,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := execute(t, tc.workingDir, tc.args...)
			if got := ExitCode(result.err); got != tc.want {
				t.Errorf("exit code = %d, want %d (err: %v)", got, tc.want, result.err)
			}
		})
	}
}

func TestConfigurationErrorsDoNotPrintUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), fixtureArgs(t, "config", "validate", "--project", filepath.Join(t.TempDir(), "nope.yaml")), strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfig {
		t.Errorf("exit code = %d, want %d", code, ExitConfig)
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("a configuration failure printed usage text:\n%s", stderr.String())
	}
}

func TestUsageErrorsPrintUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("a usage failure did not print usage text:\n%s", stderr.String())
	}
}
