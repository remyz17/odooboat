package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// resolveDocuments writes a project document (and an optional user document) to a
// temporary directory and resolves them.
func resolveDocuments(t *testing.T, project, user string) (Result, error) {
	t.Helper()
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ProjectFileName)
	if err := os.WriteFile(projectPath, []byte(project), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	userPath := ""
	if user != "" {
		userPath = filepath.Join(dir, "user.yaml")
		if err := os.WriteFile(userPath, []byte(user), 0o600); err != nil {
			t.Fatalf("write user config: %v", err)
		}
	}
	return Resolve(Options{WorkingDir: dir, ProjectPath: projectPath, UserConfigPath: userPath})
}

const validUserDocument = "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    context: default\n"

func TestValidateAuthored(t *testing.T) {
	tests := []struct {
		name    string
		project string
		class   error
		want    string
	}{
		{
			name:    "protected odoo option",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  odoo:\n    options:\n      db_host: localhost\n",
			class:   ErrAuthored,
			want:    "this Odoo option is managed by odooboat",
		},
		{
			name:    "protected addons_path",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  odoo:\n    options:\n      addons_path: /opt\n",
			class:   ErrAuthored,
			want:    "use the addons field instead",
		},
		{
			name:    "environment overlay cannot author the project name",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\nenvironments:\n  dev:\n    name: other\n",
			class:   ErrDecode,
			want:    `unknown field "name"`,
		},
		{
			name:    "local overlay cannot author odoo options",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  odoo:\n    bogus: 1\n",
			class:   ErrDecode,
			want:    `unknown field "bogus"`,
		},
		{
			name:    "malformed odoo version",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: eighteen\n  runtimeConnection: local\n",
			class:   ErrAuthored,
			want:    "odoo version must look like",
		},
		{
			name:    "missing project name",
			project: "schema: 1\nproject:\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n",
			class:   ErrAuthored,
			want:    "project name is required",
		},
		{
			name:    "addon set without a path",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  addons:\n    core:\n      enabled: true\n",
			class:   ErrAuthored,
			want:    "addon path is required",
		},
		{
			name:    "secret with both env and literal",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  odoo:\n    masterPassword:\n      env: PW\n      literal: hunter2\n",
			class:   ErrAuthored,
			want:    "exactly one of",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDocuments(t, tc.project, validUserDocument)
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !errors.Is(err, tc.class) {
				t.Errorf("error %v is not of class %v", err, tc.class)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateResolved(t *testing.T) {
	tests := []struct {
		name    string
		project string
		user    string
		want    string
	}{
		{
			name:    "no runtime connection selected",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n",
			user:    validUserDocument,
			want:    "no runtime connection selected",
		},
		{
			name:    "runtime connection does not exist",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: missing\n",
			user:    validUserDocument,
			want:    "unknown runtime connection",
		},
		{
			name:    "addon path does not exist",
			project: "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  addons:\n    core:\n      path: ./nowhere\n",
			user:    validUserDocument,
			want:    "addon path does not exist",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDocuments(t, tc.project, tc.user)
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !errors.Is(err, ErrResolved) {
				t.Errorf("error %v is not ErrResolved", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateCollectsIndependentErrors(t *testing.T) {
	project := "schema: 1\nproject:\n  name: Demo\n  odooVersion: eighteen\n  odoo:\n    options:\n      db_host: localhost\n"
	_, err := resolveDocuments(t, project, validUserDocument)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	var aggregate *Errors
	if !errors.As(err, &aggregate) {
		t.Fatalf("error %v is not an aggregate", err)
	}
	if len(aggregate.Errs) < 3 {
		t.Errorf("collected %d errors, want at least 3:\n%v", len(aggregate.Errs), err)
	}
}

func TestRuntimeConnectionLocators(t *testing.T) {
	tests := []struct {
		name string
		user string
		want string
	}{
		{"docker requires a locator", "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n", "exactly one"},
		{"docker rejects two locators", "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    context: default\n    socket: /tmp/docker.sock\n", "exactly one"},
		{"podman requires a locator", "schema: 1\nruntimeConnections:\n  local:\n    kind: podman\n", "exactly one"},
		{"apple rejects a context", "schema: 1\nruntimeConnections:\n  local:\n    kind: apple\n    context: local\n", "local runtime"},
		{"apple rejects a socket", "schema: 1\nruntimeConnections:\n  local:\n    kind: apple\n    socket: /tmp/apple.sock\n", "local runtime"},
	}
	project := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDocuments(t, project, tc.user)
			if err == nil || !errors.Is(err, ErrAuthored) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want authored error containing %q", err, tc.want)
			}
		})
	}
}

func TestUnixSocketNormalization(t *testing.T) {
	project := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n"
	user := "schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    socket: unix:///var/run/../run/docker.sock\n"
	result, err := resolveDocuments(t, project, user)
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.RuntimeConnection.Socket != "unix:///var/run/docker.sock" {
		t.Fatalf("socket = %q", result.Config.RuntimeConnection.Socket)
	}
}

func TestSecretsAreNeverRendered(t *testing.T) {
	const secret = "s3cr3t-literal"
	project := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n  odoo:\n    masterPassword:\n      literal: " + secret + "\n"

	result, err := resolveDocuments(t, project, validUserDocument)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	asYAML, err := yaml.Marshal(result.Config)
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	asJSON, err := json.Marshal(result.Config)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	provYAML, err := yaml.Marshal(result.Provenance)
	if err != nil {
		t.Fatalf("marshal provenance: %v", err)
	}
	explained, _ := result.Config.Lookup(FieldOdooMasterPassword)

	surfaces := map[string]string{
		"yaml":       string(asYAML),
		"json":       string(asJSON),
		"provenance": string(provYAML),
		"explain":    explained,
	}
	for name, rendered := range surfaces {
		if strings.Contains(rendered, secret) {
			t.Errorf("%s output leaks the secret value", name)
		}
	}
	if explained != RedactedPlaceholder {
		t.Errorf("explain value = %q, want %q", explained, RedactedPlaceholder)
	}
	if value, ok := result.Config.Odoo.MasterPassword.Value(); !ok || value != secret {
		t.Error("the literal secret should still be reachable through Value()")
	}
}

func TestResolveDoesNotTouchTheFilesystem(t *testing.T) {
	root := fixtureRoot(t)
	before := snapshotModTimes(t, root)
	resolveFixture(t, "dev", InvocationPatch{})
	after := snapshotModTimes(t, root)

	for path, mod := range before {
		if after[path] != mod {
			t.Errorf("%s was modified during resolution", path)
		}
	}
	if len(before) != len(after) {
		t.Errorf("the fixture tree changed size: %d -> %d entries", len(before), len(after))
	}
}

func snapshotModTimes(t *testing.T, root string) map[string]int64 {
	t.Helper()
	times := map[string]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		times[path] = info.ModTime().UnixNano()
		return nil
	})
	if err != nil {
		t.Fatalf("walk fixtures: %v", err)
	}
	return times
}
