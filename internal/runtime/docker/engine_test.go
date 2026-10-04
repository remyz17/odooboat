package docker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

func TestConnectUnavailableSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	socket := "unix://" + filepath.Join(t.TempDir(), "absent.sock")
	start := time.Now()
	_, err := Connect(context.Background(), runtime.Locator{Alias: "local", Socket: socket}, t.TempDir())
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), `"local"`) || !strings.Contains(err.Error(), socket) {
		t.Fatalf("error does not name alias and endpoint: %v", err)
	}
	if elapsed := time.Since(start); elapsed > pingTimeout {
		t.Fatalf("connect took %s", elapsed)
	}
}

func TestConnectResolutionErrorKeepsClass(t *testing.T) {
	_, err := Connect(context.Background(), runtime.Locator{Alias: "remote", Socket: "tcp://host:2375"}, t.TempDir())
	if !errors.Is(err, runtime.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
