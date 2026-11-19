package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// projectWith builds a minimal valid project document with extra lines under
// "project:" and an optional trailing section.
func projectWith(projectLines, tail string) string {
	return "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n" +
		projectLines + tail
}

func TestRuntimeCombinationValidation(t *testing.T) {
	tests := []struct {
		name    string
		project string
		class   error
		want    string // empty: resolution succeeds
	}{
		{name: "matrix defaults", project: projectWith("", "")},
		{name: "supported explicit versions", project: projectWith("  python: \"3.10\"\n  postgres: 13\n", "")},
		{
			name:    "unquoted python keeps its spelling",
			project: projectWith("  python: 3.10\n", ""),
		},
		{
			name:    "unsupported python",
			project: projectWith("  python: \"3.13\"\n", ""),
			class:   ErrResolved, want: `python 3.13 is not supported for Odoo 18.0`,
		},
		{
			name:    "unsupported postgres",
			project: projectWith("  postgres: 18\n", ""),
			class:   ErrResolved, want: `list "postgres" under allowUnsupported`,
		},
		{
			name:    "accepting one dimension does not relax another",
			project: projectWith("  python: \"3.13\"\n  postgres: 18\n  allowUnsupported: [postgres]\n", ""),
			class:   ErrResolved, want: "python 3.13 is not supported",
		},
		{
			name:    "series outside the matrix",
			project: strings.Replace(projectWith("", ""), `"18.0"`, `"15.0"`, 1),
			class:   ErrResolved, want: "supported series: 18.0",
		},
		{
			name:    "saas series are out of scope",
			project: strings.Replace(projectWith("", ""), `"18.0"`, `"saas-18.1"`, 1),
			class:   ErrAuthored, want: `odoo version must look like "18.0"`,
		},
		{
			name:    "python format",
			project: projectWith("  python: \"3\"\n", ""),
			class:   ErrAuthored, want: `python version must look like "3.12"`,
		},
		{
			name:    "postgres must be positive",
			project: projectWith("  postgres: 0\n", ""),
			class:   ErrAuthored, want: "postgres must be a positive major version",
		},
		{
			name:    "the series is never relaxable",
			project: projectWith("  allowUnsupported: [series]\n", ""),
			class:   ErrAuthored, want: "the Odoo series itself cannot be relaxed",
		},
		{
			name:    "dimension listed twice",
			project: projectWith("  allowUnsupported: [python, python]\n", ""),
			class:   ErrAuthored, want: "dimension is listed twice",
		},
		{
			name:    "base image tag without digest",
			project: projectWith("  baseImage: ghcr.io/acme/odoo:18.0\n", ""),
			class:   ErrAuthored, want: "base image must be pinned by digest",
		},
		{
			name:    "base image pinned by digest",
			project: projectWith("  baseImage: ghcr.io/acme/odoo:18.0@sha256:"+strings.Repeat("a", 64)+"\n", ""),
		},
		{
			name:    "local base image ID",
			project: projectWith("  baseImage: sha256:"+strings.Repeat("b", 64)+"\n", ""),
		},
		{
			name:    "short image ID",
			project: projectWith("  baseImage: sha256:"+strings.Repeat("b", 12)+"\n", ""),
			class:   ErrAuthored, want: "base image must be pinned by digest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDocuments(t, tc.project, validUserDocument)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.class) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.class, tc.want)
			}
		})
	}
}

func TestAcceptedUnsupportedDimensionIsReported(t *testing.T) {
	project := projectWith("  postgres: 18\n  allowUnsupported: [postgres]\n", "")
	result, err := resolveDocuments(t, project, validUserDocument)
	if err != nil {
		t.Fatal(err)
	}
	cfg := result.Config
	if !reflect.DeepEqual(cfg.Unsupported, []Dimension{DimensionPostgres}) {
		t.Fatalf("unsupported = %v, want [postgres]", cfg.Unsupported)
	}
	if value, _ := cfg.Lookup(FieldUnsupported); value != "[postgres]" {
		t.Fatalf("explain unsupported = %q", value)
	}
}

// Without authored values, python and postgres resolve to the matrix defaults
// of the resolved series.
func TestMatrixDefaultsFollowTheResolvedSeries(t *testing.T) {
	result, err := resolveDocuments(t, projectWith("", ""), validUserDocument)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := mustMatrix(t).Lookup("18.0")
	if result.Config.Python != entry.DefaultPython || result.Config.Postgres != entry.DefaultPostgres {
		t.Fatalf("python, postgres = %q, %d", result.Config.Python, result.Config.Postgres)
	}
}

func mustMatrix(t *testing.T) Matrix {
	t.Helper()
	m, err := EmbeddedMatrix()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func sourceSection(community, enterprise string) string {
	out := "  odoo:\n    source:\n"
	if community != "" {
		out += "      community: " + community + "\n"
	}
	if enterprise != "" {
		out += "      enterprise: " + enterprise + "\n"
	}
	return out
}

func TestOdooSourceValidation(t *testing.T) {
	embedded := "{git: https://github.com/odoo/odoo.git, commit: " + testCommit + "}"
	tests := []struct {
		name                  string
		community, enterprise string
		class                 error
		want                  string
	}{
		{name: "mount pair", community: "{path: .}", enterprise: "{path: .}"},
		{name: "embedded pair", community: embedded, enterprise: "{git: git@github.com:odoo/enterprise.git, commit: " + testCommit + "}"},
		{name: "community alone", community: "{path: .}"},
		{
			name: "path and git together", community: "{path: ., git: https://x/y.git, commit: " + testCommit + "}",
			class: ErrAuthored, want: `exactly one of "path"`,
		},
		{name: "empty entry", community: "{}", class: ErrAuthored, want: `exactly one of "path"`},
		{name: "git without commit", community: "{git: https://x/y.git}", class: ErrAuthored, want: "full 40-character"},
		{name: "short commit", community: "{git: https://x/y.git, commit: abc123}", class: ErrAuthored, want: "full 40-character"},
		{name: "commit without git", community: "{commit: " + testCommit + "}", class: ErrAuthored, want: "git repository URL is required"},
		{
			name: "mixed modes", community: "{path: .}", enterprise: embedded,
			class: ErrResolved, want: "enterprise is embedded but community is mount",
		},
		{name: "enterprise without community", enterprise: "{path: .}", class: ErrResolved, want: "requires a community source"},
		{name: "missing mount path", community: "{path: ./missing}", class: ErrResolved, want: "source path does not exist"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveDocuments(t, projectWith(sourceSection(tc.community, tc.enterprise), ""), validUserDocument)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.class) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.class, tc.want)
			}
		})
	}
}

// Credentials never enter configuration, and the rejection never echoes them.
func TestGitCredentialsAreRejectedWithoutEcho(t *testing.T) {
	for _, url := range []string{
		"https://user:s3cr3t-token@github.com/odoo/enterprise.git",
		"https://s3cr3t-token@github.com/odoo/enterprise.git",
	} {
		community := "{git: " + url + ", commit: " + testCommit + "}"
		_, err := resolveDocuments(t, projectWith(sourceSection(community, ""), ""), validUserDocument)
		if !errors.Is(err, ErrAuthored) || !strings.Contains(err.Error(), "must not contain credentials") {
			t.Fatalf("%s: err = %v", url, err)
		}
		if strings.Contains(err.Error(), "s3cr3t-token") {
			t.Fatalf("error leaks the credential: %v", err)
		}
	}
}

func TestSourceEntriesAreReplacedAsAWhole(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"community", "enterprise"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	embedded := "{git: https://github.com/odoo/odoo.git, commit: " + testCommit + "}"
	project := projectWith(sourceSection(embedded, ""), "environments:\n  dev:\n    odoo:\n      source:\n        community: {path: ./community}\n")
	local := "schema: 1\nproject:\n  odoo:\n    source:\n      enterprise: {path: ./enterprise}\n"
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	projectPath := write(ProjectFileName, project)
	write(LocalFileName, local)
	userPath := write("user.yaml", validUserDocument)

	result, err := Resolve(Options{WorkingDir: dir, ProjectPath: projectPath, UserConfigPath: userPath, EnvironmentName: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	source := result.Config.Odoo.Source
	// The dev path replaces the project's git entry entirely: no commit leaks
	// into a mount entry.
	want := &ResolvedSourceLocation{Path: filepath.Join(dir, "community")}
	if source.Mode != SourceModeMount || !reflect.DeepEqual(source.Community, want) {
		t.Fatalf("community = %+v, mode %q", source.Community, source.Mode)
	}
	if source.Enterprise == nil || source.Enterprise.Path != filepath.Join(dir, "enterprise") {
		t.Fatalf("enterprise = %+v", source.Enterprise)
	}
}

// A local document owns source locations, not shared Odoo settings.
func TestLocalDocumentCannotAuthorSharedOdooSettings(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, ProjectFileName)
	if err := os.WriteFile(projectPath, []byte(projectWith("", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	local := "schema: 1\nproject:\n  odoo:\n    options:\n      workers: \"4\"\n"
	if err := os.WriteFile(filepath.Join(dir, LocalFileName), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Options{WorkingDir: dir, ProjectPath: projectPath})
	if !errors.Is(err, ErrDecode) || !strings.Contains(err.Error(), `unknown field "options"`) {
		t.Fatalf("err = %v", err)
	}
}
