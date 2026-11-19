//go:build integration

package docker

import (
	"context"
	"os"
	"testing"

	"github.com/moby/moby/client"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/runtime/runtimetest"
)

// Run with: ODOOBOAT_TEST_RUNTIME=docker go test -tags integration ./internal/runtime/docker
// ODOOBOAT_TEST_DOCKER_CONTEXT selects the context (default: default).
func TestContract(t *testing.T) {
	if selected := os.Getenv("ODOOBOAT_TEST_RUNTIME"); selected != "docker" {
		t.Skipf("ODOOBOAT_TEST_RUNTIME=%q; the Docker contract runs only with docker", selected)
	}
	loc := runtime.Locator{Alias: "contract", Context: os.Getenv("ODOOBOAT_TEST_DOCKER_CONTEXT")}
	if loc.Context == "" {
		loc.Context = "default"
	}
	configDir, err := DefaultConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	connect := func(ctx context.Context, t *testing.T) *Engine {
		engine, err := Connect(ctx, loc, configDir)
		if err != nil {
			t.Fatal(err)
		}
		return engine
	}
	runtimetest.Run(t, func(ctx context.Context, t *testing.T) runtime.Engine { return connect(ctx, t) },
		runtimetest.Hooks{RemoveImage: func(ctx context.Context, ref string) error {
			engine := connect(ctx, t)
			defer engine.Close()
			_, err := engine.client.ImageRemove(ctx, ref, client.ImageRemoveOptions{PruneChildren: true})
			return err
		}})
}
