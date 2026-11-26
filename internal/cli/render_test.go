package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/runtime"
	statefile "github.com/remyz17/odooboat/internal/state/file"
	"github.com/spf13/cobra"
)

func userConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "user.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResultFormatResolution(t *testing.T) {
	deps := Dependencies{WorkingDir: t.TempDir()}
	for _, c := range []struct {
		name, output, user, want string
		color                    bool
	}{
		{"default", "", "schema: 1\n", "text", true},
		{"preference", "", "schema: 1\npreferences:\n  output: yaml\n", "yaml", true},
		{"flag wins", "json", "schema: 1\npreferences:\n  output: yaml\n", "json", true},
		{"colour off", "", "schema: 1\npreferences:\n  color: false\n", "text", false},
	} {
		format, color := presentation{output: c.output, userConfig: userConfig(t, c.user)}.result(deps)
		if format != c.want || color != c.color {
			t.Errorf("%s: %s, %v; want %s, %v", c.name, format, color, c.want, c.color)
		}
	}
}

func TestBrokenUserConfigFallsBackToText(t *testing.T) {
	broken := userConfig(t, "schema: [\n")
	if format, color := (presentation{userConfig: broken}).result(Dependencies{WorkingDir: t.TempDir()}); format != "text" || !color {
		t.Fatalf("result = %s, %v; want text, true", format, color)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--user-config", broken, "status"}, Dependencies{
		Config: app.NewConfigService(), Workspace: app.NewWorkspaceService(statefile.New(), statefile.New(), runtime.Connectors{}, "test"),
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, WorkingDir: t.TempDir(),
		Getenv: func(string) string { return "" },
	})
	if code != ExitConfig || strings.Count(stderr.String(), "error:") != 1 || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

type stubResult struct {
	Name     string   `yaml:"name" json:"name"`
	Warnings []string `yaml:"warnings" json:"warnings"`
}

// stubText renders a fixed line and the warnings it is given.
type stubText struct{ warnings []string }

func (s stubText) RenderText(p *ui.Printer) error {
	p.Headline("stub", "done", ui.ToneDone)
	p.Warnings(s.warnings)
	return p.Err()
}

func printed(t *testing.T, format string, value any, text textRenderer, warnings []string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	g := &globalFlags{resultFormat: format, colorPreference: true,
		present: newPresenter(&stderr, ui.FormatPlain, ui.RetainOnFailure, time.Now)}
	deps := Dependencies{Stdout: &stdout, Getenv: func(string) string { return "" }}
	if err := g.print(deps, value, text, warnings); err != nil {
		t.Fatal(err)
	}
	g.present.Close()
	return stdout.String(), stderr.String()
}

func TestTextFallsBackToYAML(t *testing.T) {
	value := stubResult{Name: "x", Warnings: []string{}}
	text, _ := printed(t, "text", value, nil, nil)
	yaml, _ := printed(t, "yaml", value, nil, nil)
	if text != yaml || !strings.Contains(text, "name: x") {
		t.Fatalf("text fallback %q, yaml %q", text, yaml)
	}
}

func TestTextWarningsStayOnStdout(t *testing.T) {
	warnings := []string{"w"}
	value := stubResult{Name: "x", Warnings: warnings}
	stdout, stderr := printed(t, "text", value, stubText{warnings}, warnings)
	if stderr != "" || strings.Count(stdout, "w\n") != 1 {
		t.Fatalf("text: stdout %q, stderr %q", stdout, stderr)
	}
	_, stderr = printed(t, "yaml", value, stubText{warnings}, warnings)
	if strings.Count(stderr, ": w\n") != 1 {
		t.Fatalf("yaml: stderr %q, want the warning once", stderr)
	}
}

func TestUnknownOutputIsUsageError(t *testing.T) {
	ok := &cobra.Command{Use: "ok", RunE: func(*cobra.Command, []string) error { return nil }}
	code, stderr := runCLI(t, Dependencies{}, []*cobra.Command{ok}, "-o", "toml", "ok")
	if code != ExitUsage || !strings.Contains(stderr, "expected text, yaml, or json") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestWorkspaceShowFollowsOutputPreference(t *testing.T) {
	root, _ := cliStateProject(t)
	user := userConfig(t, "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    context: default\npreferences:\n  output: json\n")
	show := execute(t, root, "--user-config", user, "workspace", "show")
	var out map[string]any
	if show.err != nil || json.Unmarshal([]byte(show.stdout), &out) != nil {
		t.Fatalf("workspace show = %q, %v; want JSON", show.stdout, show.err)
	}
}

// With 2>&1, progress reported before the result is written before it, in
// text as in yaml.
func TestTextResultFollowsProgress(t *testing.T) {
	var both lockedBuffer
	slow := writerFunc(func(p []byte) (int, error) {
		time.Sleep(20 * time.Millisecond)
		return both.Write(p)
	})
	g := &globalFlags{resultFormat: "text", present: newPresenter(slow, ui.FormatPlain, ui.RetainOnFailure, time.Now)}
	subject := app.Subject{Kind: app.ResourceContainer, Name: "odoo"}
	g.present.Reporter().Report(app.StepStarted{ID: "1", Kind: app.StepCreate, Subject: subject})
	g.present.Reporter().Report(app.StepFinished{ID: "1", Outcome: app.OutcomeDone})
	if err := g.print(Dependencies{Stdout: &both, Getenv: func(string) string { return "" }}, stubResult{}, stubText{}, nil); err != nil {
		t.Fatal(err)
	}
	g.present.Close()
	if out := both.String(); strings.HasPrefix(out, "stub") {
		t.Fatalf("result came before progress:\n%s", out)
	}
}
