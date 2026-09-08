package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func resolveFixture(t *testing.T, env string, patch InvocationPatch) Result {
	t.Helper()
	root := fixtureRoot(t)
	result, err := Resolve(Options{
		WorkingDir:      filepath.Join(root, "project"),
		UserConfigPath:  filepath.Join(root, "user.yaml"),
		EnvironmentName: env,
		Patch:           patch,
	})
	if err != nil {
		t.Fatalf("resolve %q: %v", env, err)
	}
	return result
}

func addonPath(t *testing.T, cfg ResolvedConfig, name string) string {
	t.Helper()
	for _, addon := range cfg.Addons {
		if addon.Name == name {
			return addon.Path
		}
	}
	t.Fatalf("addon %q not found in %+v", name, cfg.Addons)
	return ""
}

func addonEnabled(t *testing.T, cfg ResolvedConfig, name string) bool {
	t.Helper()
	for _, addon := range cfg.Addons {
		if addon.Name == name {
			return addon.Enabled
		}
	}
	t.Fatalf("addon %q not found", name)
	return false
}

func TestResolveDefaultEnvironment(t *testing.T) {
	root := fixtureRoot(t)
	cfg := resolveFixture(t, "", InvocationPatch{}).Config

	if cfg.EnvironmentName != DefaultEnvironment {
		t.Errorf("environment = %q, want %q", cfg.EnvironmentName, DefaultEnvironment)
	}
	if cfg.OdooVersion != "18.0" {
		t.Errorf("odooVersion = %q, want 18.0", cfg.OdooVersion)
	}
	// The local document overrides the project-level connection.
	if cfg.RuntimeConnection.Name != "local" || cfg.RuntimeConnection.Kind != RuntimeKindDocker {
		t.Errorf("runtimeConnection = %+v, want local/docker", cfg.RuntimeConnection)
	}
	// The user document authors the socket, so it resolves against the user
	// document's directory, not the project's.
	if want := filepath.Join(root, "sockets", "docker.sock"); cfg.RuntimeConnection.Socket != want {
		t.Errorf("socket = %q, want %q", cfg.RuntimeConnection.Socket, want)
	}
	if want := []string{"requests", "pandas"}; !reflect.DeepEqual(cfg.PythonPackages, want) {
		t.Errorf("pythonPackages = %v, want %v", cfg.PythonPackages, want)
	}
	if addonEnabled(t, cfg, "extra") {
		t.Error("addon extra should be disabled in the default environment")
	}
	if got, want := cfg.Odoo.Options["workers"], "2"; got != want {
		t.Errorf("workers = %q, want %q", got, want)
	}
	if cfg.Odoo.MasterPassword.Kind != SecretKindEnv || cfg.Odoo.MasterPassword.Ref != "ODOOBOAT_MASTER_PASSWORD" {
		t.Errorf("masterPassword = %+v, want an env reference", cfg.Odoo.MasterPassword)
	}
	if cfg.Preferences.Output != OutputYAML || cfg.Preferences.Color {
		t.Errorf("preferences = %+v, want yaml output and color disabled", cfg.Preferences)
	}
}

func TestResolveEnvironmentOverlay(t *testing.T) {
	root := fixtureRoot(t)
	cfg := resolveFixture(t, "dev", InvocationPatch{}).Config

	if cfg.OdooVersion != "17.0" {
		t.Errorf("odooVersion = %q, want the environment overlay value 17.0", cfg.OdooVersion)
	}
	// The local environment overlay wins over the project environment overlay.
	if cfg.RuntimeConnection.Name != "remote" || cfg.RuntimeConnection.Kind != RuntimeKindPodman {
		t.Errorf("runtimeConnection = %+v, want remote/podman", cfg.RuntimeConnection)
	}
	if want := []string{"requests"}; !reflect.DeepEqual(cfg.PythonPackages, want) {
		t.Errorf("pythonPackages = %v, want the replaced list %v", cfg.PythonPackages, want)
	}
	// Keyed merge: local overrides one field of an inherited entry, the
	// environment overlay re-enables another entry.
	if want := filepath.Join(root, "local-core"); addonPath(t, cfg, "core") != want {
		t.Errorf("addons.core.path = %q, want %q", addonPath(t, cfg, "core"), want)
	}
	if !addonEnabled(t, cfg, "extra") {
		t.Error("addon extra should be re-enabled by the dev overlay")
	}
	if want := filepath.Join(root, "project", "addons", "extra"); addonPath(t, cfg, "extra") != want {
		t.Errorf("addons.extra.path = %q, want the inherited %q", addonPath(t, cfg, "extra"), want)
	}
	if got, want := cfg.Odoo.Options["workers"], "0"; got != want {
		t.Errorf("workers = %q, want the overlay value %q", got, want)
	}
	if got, want := cfg.Odoo.Options["limit_time_cpu"], "120"; got != want {
		t.Errorf("limit_time_cpu = %q, want the inherited %q", got, want)
	}
}

func TestResolveEmptyListClearsInheritedList(t *testing.T) {
	cfg := resolveFixture(t, "ci", InvocationPatch{}).Config
	if len(cfg.PythonPackages) != 0 {
		t.Errorf("pythonPackages = %v, want the list cleared by the ci overlay", cfg.PythonPackages)
	}
	if cfg.PythonPackages == nil {
		t.Error("pythonPackages should be an empty slice, not nil")
	}
}

func TestResolveInvocationPatchWins(t *testing.T) {
	version := "16.0"
	connection := "remote"
	cfg := resolveFixture(t, "dev", InvocationPatch{OdooVersion: &version, RuntimeConnection: &connection}).Config

	if cfg.OdooVersion != version {
		t.Errorf("odooVersion = %q, want the invocation value %q", cfg.OdooVersion, version)
	}
	if cfg.RuntimeConnection.Name != connection {
		t.Errorf("runtimeConnection = %q, want %q", cfg.RuntimeConnection.Name, connection)
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	first := resolveFixture(t, "dev", InvocationPatch{})
	second := resolveFixture(t, "dev", InvocationPatch{})

	if !reflect.DeepEqual(first.Config, second.Config) {
		t.Errorf("resolved config differs between runs:\n%+v\n%+v", first.Config, second.Config)
	}
	if !reflect.DeepEqual(first.Provenance, second.Provenance) {
		t.Error("provenance differs between runs")
	}
}

func TestResolveRelativePathsUseAuthoringDocument(t *testing.T) {
	base := filepath.Join("/tmp", "docs")
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "relative", path: "./addons", want: filepath.Join(base, "addons")},
		{name: "parent relative", path: "../shared", want: "/tmp/shared"},
		{name: "absolute is preserved", path: "/opt/addons", want: "/opt/addons"},
		{name: "empty stays empty", path: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRelative(base, tc.path); got != tc.want {
				t.Errorf("resolveRelative(%q, %q) = %q, want %q", base, tc.path, got, tc.want)
			}
		})
	}
}
