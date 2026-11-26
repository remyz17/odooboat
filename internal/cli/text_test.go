package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// testHome is the home directory text renderers see in tests.
const testHome = "/home/dev"

func renderText(t *testing.T, view textRenderer, caps ui.Capabilities) string {
	t.Helper()
	var b bytes.Buffer
	if err := view.RenderText(ui.NewPrinter(&b, caps, testHome)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func goldenText(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "text", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs:\n%s\nwant:\n%s", name, got, want)
	}
}

var unicodeCaps = ui.Capabilities{Unicode: true}

const masterPasswordWarning = "no masterPassword is configured: Odoo's database manager uses its default master password"

func container(role runtime.Role, name, state string) app.ResourceReport {
	return app.ResourceReport{Kind: runtime.ResourceContainer, Role: role, Name: name, State: state}
}

func volume(role runtime.Role, name string) app.ResourceReport {
	return app.ResourceReport{Kind: runtime.ResourceVolume, Role: role, Name: name}
}

func runningStatus() app.StatusResult {
	return app.StatusResult{
		Environment: "dev", Bound: true, EnvironmentID: "1a2b3c4d-0000", Running: true, URL: "http://127.0.0.1:54321",
		Resources: []app.ResourceReport{
			volume(runtime.RoleOdooData, "ob-myproject-dev-odoo-data-1a2b3c4d"),
			container(runtime.RolePostgres, "ob-myproject-dev-postgres-1a2b3c4d", "running"),
			{Kind: runtime.ResourceNetwork, Role: runtime.RoleEnvironment, Name: "ob-myproject-dev-environment-1a2b3c4d"},
			container(runtime.RoleOdoo, "ob-myproject-dev-odoo-1a2b3c4d", "running"),
			volume(runtime.RolePostgresData, "ob-myproject-dev-postgres-data-1a2b3c4d"),
		},
		Orphans:  []app.ResourceReport{},
		Warnings: []string{masterPasswordWarning},
	}
}

// adrStatus is the example of ADR 0011 §8.
const adrStatus = `dev  ● running
  url        http://127.0.0.1:54321
  odoo       running   ob-myproject-dev-odoo-1a2b3c4d
  postgres   running   ob-myproject-dev-postgres-1a2b3c4d
  volumes    ob-myproject-dev-postgres-data-1a2b3c4d, ob-myproject-dev-odoo-data-1a2b3c4d
  ⚠ no masterPassword is configured: Odoo's database manager uses its default master password
`

func TestStatusText(t *testing.T) {
	running := runningStatus()
	if got := renderText(t, statusText(running), unicodeCaps); got != adrStatus {
		t.Fatalf("running status:\n%s\nwant the ADR 0011 §8 example:\n%s", got, adrStatus)
	}

	partial := runningStatus()
	partial.Running, partial.URL = false, ""
	partial.Resources[1].State = "exited (1)"
	partial.Orphans = []app.ResourceReport{container(runtime.RoleOdoo, "ob-myproject-old-odoo-9f8e7d6c", "exited (0)")}

	stopped := runningStatus()
	stopped.Running, stopped.URL = false, ""
	stopped.Resources[1].State, stopped.Resources[3].State = "exited (0)", "exited (0)"

	down := runningStatus()
	down.Running, down.URL = false, ""
	down.Resources = []app.ResourceReport{down.Resources[0], down.Resources[4]}

	notBound := app.StatusResult{Environment: "dev", Resources: []app.ResourceReport{}, Orphans: []app.ResourceReport{},
		Warnings: []string{masterPasswordWarning}}

	for name, result := range map[string]app.StatusResult{
		"status_running": running, "status_partial": partial, "status_stopped": stopped,
		"status_down": down, "status_not_bound": notBound,
	} {
		t.Run(name, func(t *testing.T) {
			got := renderText(t, statusText(result), unicodeCaps)
			goldenText(t, name, got)
			if result.URL == "" && (strings.Contains(got, "url") || strings.Contains(got, "running\n")) {
				t.Fatalf("%s claims a running environment:\n%s", name, got)
			}
		})
	}
}

var ansiSequence = regexp.MustCompile("\x1b\\[[0-9;]*m")

// asciiSymbols maps each Unicode state symbol to its ASCII form.
var asciiSymbols = strings.NewReplacer("●", "*", "✓", "+", "○", "-", "⚠", "!", "✗", "x")

func TestTextLayoutIgnoresCapabilities(t *testing.T) {
	want := renderText(t, statusText(runningStatus()), ui.Capabilities{})
	for _, caps := range []ui.Capabilities{{Color: true}, {Unicode: true}, {Color: true, Unicode: true}} {
		got := asciiSymbols.Replace(ansiSequence.ReplaceAllString(renderText(t, statusText(runningStatus()), caps), ""))
		if got != want {
			t.Errorf("%+v:\n%s\nwant:\n%s", caps, got, want)
		}
	}
}

func withAction(r app.ResourceReport, action app.Action) app.ResourceReport {
	r.Action, r.State = action, ""
	return r
}

const (
	envImage      = "odooboat/environment:7c1e5f0a9b3d"
	postgresImage = "docker.io/library/postgres:16-bookworm@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func upResult(action app.Action, reused, pulled bool, warnings []string) app.UpResult {
	var resources []app.ResourceReport
	for _, r := range runningStatus().Resources {
		resources = append(resources, withAction(r, action))
	}
	return app.UpResult{
		Environment: "dev", URL: "http://127.0.0.1:54321",
		Odoo:      app.UpImage{Image: envImage, ImageID: "sha256:feedface", Reused: reused},
		Postgres:  app.UpImage{Image: postgresImage, Pulled: pulled},
		Resources: resources, Warnings: warnings,
	}
}

func TestUpText(t *testing.T) {
	for name, result := range map[string]app.UpResult{
		"up_created": upResult(app.ActionCreated, false, true, []string{masterPasswordWarning}),
		"up_kept":    upResult(app.ActionKept, true, false, nil),
	} {
		t.Run(name, func(t *testing.T) {
			got := renderText(t, upText(result), unicodeCaps)
			goldenText(t, name, got)
			if lines := strings.Split(got, "\n"); !strings.HasPrefix(lines[1], "  url ") {
				t.Fatalf("first row %q, want the URL", lines[1])
			}
		})
	}
}

func TestDownText(t *testing.T) {
	status := runningStatus().Resources
	down := app.DownResult{Environment: "dev",
		Removed: []app.ResourceReport{withAction(status[3], app.ActionRemoved), withAction(status[1], app.ActionRemoved), withAction(status[2], app.ActionRemoved)},
		Kept:    []app.ResourceReport{volume(runtime.RolePostgresData, status[4].Name), volume(runtime.RoleOdooData, status[0].Name)},
		Skipped: []app.ResourceReport{},
	}
	skipped := down
	skipped.Removed = down.Removed[1:]
	skipped.Skipped = []app.ResourceReport{{Kind: runtime.ResourceContainer, Role: runtime.RoleOdoo, Name: status[3].Name, Reason: "claim read-only"}}
	for name, result := range map[string]app.DownResult{"down": down, "down_skipped": skipped} {
		t.Run(name, func(t *testing.T) {
			goldenText(t, name, renderText(t, downText(result), unicodeCaps))
		})
	}
}

func buildResult() app.EnvironmentBuildResult {
	return app.EnvironmentBuildResult{
		Environment: "dev", Image: envImage, ImageID: "sha256:0123456789abcdef0123456789abcdef", Reused: true,
		Base:   app.BuildBase{Reference: "ghcr.io/remyz17/odooboat-base:18.0-py3.12@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"},
		Series: "18.0", Python: "3.12", PostgresClient: 16, Unsupported: []config.Dimension{}, SourceMode: "mount",
		Community:  app.BuildSource{Path: testHome + "/src/odoo", Series: "18.0"},
		Enterprise: &app.BuildSource{Path: testHome + "/src/enterprise", Series: "18.0"},
	}
}

func TestBuildText(t *testing.T) {
	built := buildResult()
	built.Reused, built.Base.Pulled, built.SourceMode = false, true, "embedded"
	built.Python, built.Unsupported = "3.13", []config.Dimension{config.DimensionPython}
	built.Community = app.BuildSource{Git: "https://github.com/odoo/odoo.git", Commit: "0123456789abcdef0123456789abcdef01234567", Series: "18.0"}
	for name, result := range map[string]app.EnvironmentBuildResult{"build_reused": buildResult(), "build_built": built} {
		t.Run(name, func(t *testing.T) {
			goldenText(t, name, renderText(t, buildText(result), unicodeCaps))
		})
	}
}

func TestTextIsASCIIWithoutUnicode(t *testing.T) {
	for name, view := range map[string]textRenderer{
		"status": statusText(runningStatus()), "up": upText(upResult(app.ActionCreated, false, true, []string{"w"})),
		"build": buildText(buildResult()),
	} {
		for _, r := range renderText(t, view, ui.Capabilities{}) {
			if r > 127 {
				t.Errorf("%s: non-ASCII %q without Unicode", name, r)
				break
			}
		}
	}
}
