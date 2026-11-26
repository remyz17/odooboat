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
