// Package runtimetest is the behavioural contract suite every runtime adapter
// runs (ADR 0005 §8). A divergence becomes a declared missing capability whose
// dependent tests assert the rejection; tests are never skipped silently.
package runtimetest

import (
	"context"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

// Factory connects a fresh engine for one test.
type Factory func(ctx context.Context, t *testing.T) runtime.Engine

// Run executes the contract suite against an adapter.
func Run(t *testing.T, connect Factory) {
	t.Run("engine identity", func(t *testing.T) { testEngineIdentity(t, connect) })
}

func testEngineIdentity(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()

	if !engine.Capabilities().Has(runtime.CapEngineIdentity) {
		t.Log("engine.identity is not declared; nothing to verify")
		return
	}
	first, err := engine.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if first.Identity.Source == "" || first.Identity.Value == "" {
		t.Fatalf("declared engine.identity but reported %+v", first.Identity)
	}
	t.Logf("engine %s %s/%s identity %s=%s", first.Version, first.OS, first.Arch,
		first.Identity.Source, first.Identity.Value)

	second := connect(ctx, t)
	defer second.Close()
	again, err := second.Info(ctx)
	if err != nil {
		t.Fatalf("info on second connection: %v", err)
	}
	if again.Identity != first.Identity {
		t.Fatalf("identity changed between connections: %+v != %+v", first.Identity, again.Identity)
	}
}
