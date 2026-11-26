package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"gopkg.in/yaml.v3"
)

func TestLifecycleExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{fmt.Errorf("%w: container x is not managed", app.ErrConflict), ExitConflict},
		{fmt.Errorf("%w: volume holds PostgreSQL 16", app.ErrIncompatibleData), ExitConflict},
		{fmt.Errorf("%w: odoo stopped", app.ErrNotReady), ExitNotReady},
		{errors.Join(errors.New("completed: network created"), fmt.Errorf("%w", app.ErrNotReady)), ExitNotReady},
	}
	for _, tc := range cases {
		if code := ExitCode(tc.err); code != tc.code {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, code, tc.code)
		}
	}
}

// status reads local state only until a binding exists: an unbound
// environment needs no runtime.
func TestStatusOfAnUnboundEnvironment(t *testing.T) {
	root, base := cliStateProject(t)
	result := execute(t, root, append(base, "status", "-o", "yaml")...)
	var status struct {
		Environment string   `yaml:"environment"`
		Bound       bool     `yaml:"bound"`
		Warnings    []string `yaml:"warnings"`
	}
	if result.err != nil || yaml.Unmarshal([]byte(result.stdout), &status) != nil {
		t.Fatalf("status = %q, %v", result.stdout, result.err)
	}
	if status.Environment != "default" || status.Bound || len(status.Warnings) != 1 {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(filepath.Join(root, ".odooboat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status changed the filesystem: %v", err)
	}
}

// status renders as text when no format is asked for.
func TestStatusDefaultsToText(t *testing.T) {
	root, base := cliStateProject(t)
	result := execute(t, root, append(base, "status")...)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if first, _, _ := strings.Cut(result.stdout, "\n"); first != "default  - not bound" || strings.Contains(result.stdout, "environment:") {
		t.Fatalf("status = %q, want text", result.stdout)
	}
	if strings.Contains(result.stderr, "masterPassword") {
		t.Fatalf("text echoed its warning on stderr: %q", result.stderr)
	}
}

// up and down refuse before any mutation, with the class's exit code, and
// print nothing on stdout.
func TestLifecycleRefusals(t *testing.T) {
	engine := newImageStub()
	engine.images[stubLocalBase] = stubBaseInfo(stubLocalBase)
	cases := map[string]struct {
		args      []string
		community string
		connector imageStubConnector
		code      int
		message   string
	}{
		"up without source": {[]string{"up"}, "", imageStubConnector{engine}, ExitConfig, "declares no community source"},
		// The image stub builds but has no container primitives.
		"up on an image-only runtime":   {[]string{"up"}, "community-18.0", imageStubConnector{engine}, ExitRuntime, "does not manage containers"},
		"down on an image-only runtime": {[]string{"down"}, "community-18.0", imageStubConnector{engine}, ExitRuntime, "does not manage containers"},
	}
	for name, tc := range cases {
		root, base := buildProject(t, stubLocalBase, tc.community)
		result := executeWith(t, tc.connector, root, append(base, tc.args...)...)
		if code := ExitCode(result.err); code != tc.code || result.err == nil || !strings.Contains(result.err.Error(), tc.message) {
			t.Errorf("%s: exit %d, want %d; err = %v", name, code, tc.code, result.err)
		}
		if result.stdout != "" || engine.builds != 0 {
			t.Errorf("%s: stdout %q, builds %d", name, result.stdout, engine.builds)
		}
	}
}
