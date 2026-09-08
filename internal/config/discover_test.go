package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "fixtures", "config", "valid"))
	if err != nil {
		t.Fatalf("locate fixtures: %v", err)
	}
	return dir
}

func TestDiscover(t *testing.T) {
	root := fixtureRoot(t)
	projectDir := filepath.Join(root, "project")
	projectFile := filepath.Join(projectDir, ProjectFileName)
	localFile := filepath.Join(projectDir, LocalFileName)
	userFile := filepath.Join(root, "user.yaml")

	tests := []struct {
		name string
		opts DiscoverOptions
		want Sources
	}{
		{
			name: "from the project directory",
			opts: DiscoverOptions{WorkingDir: projectDir, UserConfigPath: userFile, RequireProject: true},
			want: Sources{UserPath: userFile, ProjectPath: projectFile, LocalPath: localFile, EnvironmentName: DefaultEnvironment},
		},
		{
			name: "from a nested directory",
			opts: DiscoverOptions{WorkingDir: filepath.Join(projectDir, "nested", "deep"), UserConfigPath: userFile, RequireProject: true},
			want: Sources{UserPath: userFile, ProjectPath: projectFile, LocalPath: localFile, EnvironmentName: DefaultEnvironment},
		},
		{
			name: "explicit project selection",
			opts: DiscoverOptions{WorkingDir: t.TempDir(), ProjectPath: projectFile, EnvironmentName: "dev"},
			want: Sources{ProjectPath: projectFile, LocalPath: localFile, EnvironmentName: "dev"},
		},
		{
			name: "absent user and local files are not an error",
			opts: DiscoverOptions{WorkingDir: projectDir, UserConfigPath: "", RequireProject: true},
			want: Sources{ProjectPath: projectFile, LocalPath: localFile, EnvironmentName: DefaultEnvironment},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Discover(tc.opts)
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			// The default user config location depends on the host; ignore it
			// when the case does not select one explicitly.
			if tc.opts.UserConfigPath == "" {
				got.UserPath = ""
			}
			if got != tc.want {
				t.Errorf("sources = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDiscoverWithoutProject(t *testing.T) {
	got, err := Discover(DiscoverOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got.ProjectPath != "" {
		t.Errorf("project was discovered although it was not required: %s", got.ProjectPath)
	}
	if got.EnvironmentName != DefaultEnvironment {
		t.Errorf("environment = %q, want %q", got.EnvironmentName, DefaultEnvironment)
	}
}

func TestDiscoverFailures(t *testing.T) {
	root := fixtureRoot(t)
	empty := t.TempDir()

	tests := []struct {
		name string
		opts DiscoverOptions
		want string
	}{
		{
			name: "missing explicit project",
			opts: DiscoverOptions{WorkingDir: empty, ProjectPath: filepath.Join(empty, "nope.yaml")},
			want: "project definition does not exist",
		},
		{
			name: "project selector points at a directory",
			opts: DiscoverOptions{WorkingDir: empty, ProjectPath: filepath.Join(root, "project")},
			want: "--project selects a file, not a directory",
		},
		{
			name: "missing explicit user config",
			opts: DiscoverOptions{WorkingDir: empty, UserConfigPath: filepath.Join(empty, "nope.yaml"), RequireProject: true},
			want: "user configuration file does not exist",
		},
		{
			name: "no project in any parent",
			opts: DiscoverOptions{WorkingDir: empty, RequireProject: true},
			want: "no odooboat.yaml found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Discover(tc.opts)
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("error %v is not ErrNotFound", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
