package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

func writeContext(t *testing.T, configDir, name, meta string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(digest[:])
	dir := filepath.Join(configDir, "contexts", "meta", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestResolveDockerDesktopContext(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://203.0.113.1:2375")
	t.Setenv("DOCKER_CONTEXT", "elsewhere")
	fixture, err := os.ReadFile(filepath.Join("testdata", "desktop-linux.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	writeContext(t, configDir, "desktop-linux", string(fixture))
	endpoint, err := ResolveEndpoint(runtime.Locator{Alias: "local", Context: "desktop-linux"}, configDir)
	if err != nil || endpoint != "unix:///Users/example/.docker/run/docker.sock" {
		t.Fatalf("endpoint = %q, %v", endpoint, err)
	}
}

func TestResolveDefaultContextAndSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://203.0.113.1:2375")
	missing := filepath.Join(t.TempDir(), "absent")
	endpoint, err := ResolveEndpoint(runtime.Locator{Context: "default"}, missing)
	if err != nil || endpoint != DefaultEndpoint {
		t.Fatalf("default = %q, %v", endpoint, err)
	}
	endpoint, err = ResolveEndpoint(runtime.Locator{Socket: "unix:///run/user/1000/../1000/docker.sock"}, missing)
	if err != nil || endpoint != "unix:///run/user/1000/docker.sock" {
		t.Fatalf("socket = %q, %v", endpoint, err)
	}
}

func TestResolveRejects(t *testing.T) {
	cases := []struct {
		name  string
		meta  string
		tls   bool
		loc   runtime.Locator
		class error
		text  string
	}{
		{name: "missing context", loc: runtime.Locator{Context: "nope"}, class: runtime.ErrUnavailable, text: "docker context ls"},
		{name: "invalid JSON", meta: `{`, class: runtime.ErrUnavailable, text: "unreadable"},
		{name: "name mismatch", meta: `{"Name":"other","Endpoints":{"docker":{"Host":"unix:///x.sock"}}}`, class: runtime.ErrUnavailable, text: "records name"},
		{name: "no docker endpoint", meta: `{"Name":"ctx","Endpoints":{}}`, class: runtime.ErrUnavailable, text: "no docker endpoint"},
		{name: "ssh endpoint", meta: `{"Name":"ctx","Endpoints":{"docker":{"Host":"ssh://me@host"}}}`, class: runtime.ErrUnsupported, text: "ssh://"},
		{name: "tcp endpoint", meta: `{"Name":"ctx","Endpoints":{"docker":{"Host":"tcp://host:2376"}}}`, class: runtime.ErrUnsupported, text: "tcp://"},
		{name: "tls material", meta: `{"Name":"ctx","Endpoints":{"docker":{"Host":"unix:///x.sock"}}}`, tls: true, class: runtime.ErrUnsupported, text: "TLS"},
		{name: "relative socket", loc: runtime.Locator{Socket: "unix://docker.sock"}, class: runtime.ErrUnavailable, text: "absolute"},
		{name: "tcp socket", loc: runtime.Locator{Socket: "tcp://host:2375"}, class: runtime.ErrUnsupported, text: "tcp://"},
		{name: "both locators", loc: runtime.Locator{Context: "ctx", Socket: "unix:///x.sock"}, class: runtime.ErrUnavailable, text: "both"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			loc := tc.loc
			if tc.meta != "" {
				id := writeContext(t, configDir, "ctx", tc.meta)
				loc = runtime.Locator{Context: "ctx"}
				if tc.tls {
					if err := os.MkdirAll(filepath.Join(configDir, "contexts", "tls", id, "docker"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			loc.Alias = "local"
			_, err := ResolveEndpoint(loc, configDir)
			if !errors.Is(err, tc.class) || !strings.Contains(err.Error(), tc.text) || !strings.Contains(err.Error(), `"local"`) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.class, tc.text)
			}
		})
	}
}

func TestDefaultConfigDir(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", "/custom/docker")
	if dir, err := DefaultConfigDir(); err != nil || dir != "/custom/docker" {
		t.Fatalf("dir = %q, %v", dir, err)
	}
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("HOME", "/home/dev")
	if dir, err := DefaultConfigDir(); err != nil || dir != "/home/dev/.docker" {
		t.Fatalf("dir = %q, %v", dir, err)
	}
}
