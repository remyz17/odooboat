package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/workspace"
)

func cliStateProject(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	project := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n"
	user := "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    context: default\n  other:\n    kind: podman\n    context: machine\n"
	if err := os.WriteFile(filepath.Join(root, "odooboat.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(root, "user.yaml")
	if err := os.WriteFile(userPath, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, []string{"--user-config", userPath}
}

func TestWorkspaceAndEnvironmentCommands(t *testing.T) {
	root, base := cliStateProject(t)
	showArgs := append(append([]string{}, base...), "workspace", "show", "--output", "json")
	show := execute(t, root, showArgs...)
	if show.err != nil {
		t.Fatal(show.err)
	}
	var workspaceOutput struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(show.stdout), &workspaceOutput); err != nil || workspaceOutput.Status != "uninitialized" {
		t.Fatalf("workspace JSON = %s (%v)", show.stdout, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".odooboat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace show changed filesystem: %v", err)
	}

	environmentShow := execute(t, root, append(append([]string{}, base...), "environment", "show")...)
	if environmentShow.err != nil || !strings.Contains(environmentShow.stdout, "bound: false") {
		t.Fatalf("environment show = %q, %v", environmentShow.stdout, environmentShow.err)
	}
	if _, err := os.Stat(filepath.Join(root, ".odooboat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment show changed filesystem: %v", err)
	}

	bindArgs := append(append([]string{}, base...), "environment", "bind", "--output", "json")
	first := execute(t, root, bindArgs...)
	if first.err != nil || !strings.Contains(first.stdout, `"created": true`) {
		t.Fatalf("first bind = %s, %v", first.stdout, first.err)
	}
	second := execute(t, root, bindArgs...)
	if second.err != nil || !strings.Contains(second.stdout, `"created": false`) {
		t.Fatalf("second bind = %s, %v", second.stdout, second.err)
	}

	conflictArgs := append(append([]string{}, base...), "environment", "bind", "--runtime-connection", "other")
	conflict := execute(t, root, conflictArgs...)
	if !errors.Is(conflict.err, workspace.ErrBindingConflict) || ExitCode(conflict.err) != ExitState {
		t.Fatalf("conflict = %v, exit %d", conflict.err, ExitCode(conflict.err))
	}
}

func TestWorkspaceCommandsSupportYAMLAndJSON(t *testing.T) {
	root, base := cliStateProject(t)
	for _, format := range []string{"yaml", "json"} {
		result := execute(t, root, append(append([]string{}, base...), "workspace", "show", "--output", format)...)
		if result.err != nil || strings.TrimSpace(result.stdout) == "" {
			t.Fatalf("%s output = %q, %v", format, result.stdout, result.err)
		}
	}
	for _, format := range []string{"yaml", "json"} {
		result := execute(t, root, append(append([]string{}, base...), "environment", "show", "--output", format)...)
		if result.err != nil || strings.TrimSpace(result.stdout) == "" {
			t.Fatalf("environment show %s output = %q, %v", format, result.stdout, result.err)
		}
	}
	for _, format := range []string{"yaml", "json"} {
		result := execute(t, root, append(append([]string{}, base...), "environment", "bind", "--output", format)...)
		if result.err != nil || strings.TrimSpace(result.stdout) == "" {
			t.Fatalf("environment bind %s output = %q, %v", format, result.stdout, result.err)
		}
	}
	for _, format := range []string{"yaml", "json"} {
		copyRoot := filepath.Join(t.TempDir(), "copy")
		copyTreeCLI(t, root, copyRoot)
		copyArgs := []string{"--user-config", filepath.Join(copyRoot, "user.yaml")}
		duplicate := execute(t, copyRoot, append(copyArgs, "workspace", "show", "--output", format)...)
		if ExitCode(duplicate.err) != ExitState || !strings.Contains(duplicate.stdout, "duplicate") {
			t.Fatalf("duplicate %s show = %q, %v", format, duplicate.stdout, duplicate.err)
		}
		rekey := execute(t, copyRoot, append(copyArgs, "workspace", "rekey", "--output", format)...)
		if rekey.err != nil || strings.TrimSpace(rekey.stdout) == "" {
			t.Fatalf("rekey %s output = %q, %v", format, rekey.stdout, rekey.err)
		}
	}
}

func copyTreeCLI(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if info.IsDir() {
			return os.MkdirAll(dest, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}
