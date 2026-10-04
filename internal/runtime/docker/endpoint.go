// Package docker is the Docker Engine runtime adapter.
package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/remyz17/odooboat/internal/runtime"
)

// DefaultEndpoint is the platform default Docker endpoint used by the
// unstored "default" context.
const DefaultEndpoint = "unix:///var/run/docker.sock"

const defaultContext = "default"

// DefaultConfigDir returns $DOCKER_CONFIG or ~/.docker. It is the only
// environment variable consulted: DOCKER_HOST and DOCKER_CONTEXT never
// redirect a bound environment (ADR 0005 §7).
func DefaultConfigDir() (string, error) {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate Docker configuration directory: %w", err)
	}
	return filepath.Join(home, ".docker"), nil
}

// contextMeta is the subset of the Docker CLI context store format we read.
// Unknown fields are ignored: the format belongs to the Docker CLI.
type contextMeta struct {
	Name      string `json:"Name"`
	Endpoints struct {
		Docker *struct {
			Host string `json:"Host"`
		} `json:"docker"`
	} `json:"Endpoints"`
}

// ResolveEndpoint turns a locator into a unix:// endpoint by reading the
// Docker CLI context store under configDir. It never falls back.
func ResolveEndpoint(loc runtime.Locator, configDir string) (string, error) {
	switch {
	case loc.Socket != "" && loc.Context != "":
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "",
			"connection sets both context and socket", "")
	case loc.Socket != "":
		return checkEndpoint(loc.Alias, loc.Socket)
	case loc.Context == defaultContext:
		return DefaultEndpoint, nil
	case loc.Context == "":
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "",
			"connection sets neither context nor socket", "")
	}

	digest := sha256.Sum256([]byte(loc.Context))
	id := hex.EncodeToString(digest[:])
	metaPath := filepath.Join(configDir, "contexts", "meta", id, "meta.json")
	data, err := os.ReadFile(metaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "",
			fmt.Sprintf("Docker context %q does not exist in %s", loc.Context, configDir),
			"list contexts with `docker context ls`")
	}
	if err != nil {
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "",
			fmt.Sprintf("read Docker context %q: %v", loc.Context, err), "")
	}
	var meta contextMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", malformedContext(loc, metaPath, err.Error())
	}
	if meta.Name != loc.Context {
		return "", malformedContext(loc, metaPath, fmt.Sprintf("records name %q", meta.Name))
	}
	if meta.Endpoints.Docker == nil || meta.Endpoints.Docker.Host == "" {
		return "", malformedContext(loc, metaPath, "has no docker endpoint")
	}
	tlsDir := filepath.Join(configDir, "contexts", "tls", id, "docker")
	if _, err := os.Stat(tlsDir); err == nil {
		return "", runtime.NewConnectionError(runtime.ErrUnsupported, loc.Alias, meta.Endpoints.Docker.Host,
			fmt.Sprintf("Docker context %q uses TLS material, which is not supported", loc.Context), "")
	}
	return checkEndpoint(loc.Alias, meta.Endpoints.Docker.Host)
}

// checkEndpoint accepts only unix:// endpoints with an absolute path.
func checkEndpoint(alias, endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, alias, endpoint, "endpoint is not a valid URL", "")
	}
	if parsed.Scheme != "unix" {
		return "", runtime.NewConnectionError(runtime.ErrUnsupported, alias, endpoint,
			fmt.Sprintf("%s:// endpoints are not supported", parsed.Scheme), "only unix:// sockets are supported")
	}
	if parsed.Host != "" || !filepath.IsAbs(parsed.Path) {
		return "", runtime.NewConnectionError(runtime.ErrUnavailable, alias, endpoint,
			"unix endpoint must be an absolute socket path", "")
	}
	return "unix://" + filepath.Clean(parsed.Path), nil
}

func malformedContext(loc runtime.Locator, path, problem string) error {
	return runtime.NewConnectionError(runtime.ErrUnavailable, loc.Alias, "",
		fmt.Sprintf("Docker context %q is unreadable: %s %s", loc.Context, path, problem),
		"recreate it with `docker context create` or select another connection")
}
