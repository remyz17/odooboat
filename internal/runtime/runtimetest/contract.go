// Package runtimetest is the behavioural contract suite every runtime adapter
// runs (ADR 0005 §8). A divergence becomes a declared missing capability whose
// dependent tests assert the rejection; tests are never skipped silently.
package runtimetest

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/runtime"
)

// Factory connects a fresh engine for one test.
type Factory func(ctx context.Context, t *testing.T) runtime.Engine

// Hooks gives the suite adapter-specific means it does not test, such as
// removing the images it creates. Removal is not a contract primitive yet.
type Hooks struct {
	RemoveImage func(ctx context.Context, ref string) error
}

// PullRefEnv names a reference pinned by digest, of an image with a POSIX
// shell and busybox tools (alpine). The container tests run it and fail
// without it; the pull test pulls it and is skipped, with a message, when the
// variable is empty.
const PullRefEnv = "ODOOBOAT_TEST_PULL_REF"

// Run executes the contract suite against an adapter.
func Run(t *testing.T, connect Factory, hooks Hooks) {
	t.Run("engine identity", func(t *testing.T) { testEngineIdentity(t, connect) })
	t.Run("image inspect missing", func(t *testing.T) { testInspectMissing(t, connect) })
	t.Run("image build and tag", func(t *testing.T) { testBuildAndTag(t, connect, hooks) })
	t.Run("image build large context", func(t *testing.T) { testBuildLargeContext(t, connect, hooks) })
	t.Run("container lifecycle", func(t *testing.T) { testContainerLifecycle(t, connect) })
	t.Run("network and volume", func(t *testing.T) { testNetworkAndVolume(t, connect) })
	t.Run("image pull", func(t *testing.T) { testPull(t, connect, hooks) })
}

func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return prefix + ":" + hex.EncodeToString(b[:])
}

func imageStore(t *testing.T, engine runtime.Engine) runtime.ImageStore {
	t.Helper()
	store, ok := engine.(runtime.ImageStore)
	if !ok {
		t.Fatal("adapter does not implement runtime.ImageStore")
	}
	return store
}

func testInspectMissing(t *testing.T, connect Factory) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	ref := uniqueName(t, "odooboat-contract/absent")
	if _, err := imageStore(t, engine).InspectImage(ctx, ref); !errors.Is(err, runtime.ErrImageNotFound) {
		t.Fatalf("inspect %s: err = %v, want ErrImageNotFound", ref, err)
	}
}

// scratchContext is a build context whose only stage is FROM scratch, so the
// build needs no image and no network.
func scratchContext(t *testing.T, label string) *bytes.Buffer {
	t.Helper()
	files := map[string]string{
		"Dockerfile": "FROM scratch\nCOPY --chmod=0644 marker /marker\nLABEL odooboat.image.contract-test=" + label + "\n",
		"marker":     label,
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func testBuildAndTag(t *testing.T, connect Factory, hooks Hooks) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	if !engine.Capabilities().Has(runtime.CapImageBuild) {
		t.Log("image.build is not declared; nothing to verify")
		return
	}
	builder, ok := engine.(runtime.ImageBuilder)
	if !ok {
		t.Fatal("declared image.build but does not implement runtime.ImageBuilder")
	}
	store := imageStore(t, engine)
	tag, alias := uniqueName(t, "odooboat-contract/build"), uniqueName(t, "odooboat-contract/alias")
	cleanup(t, hooks, tag, alias)

	label := strings.TrimPrefix(tag, "odooboat-contract/build:")
	var progress bytes.Buffer
	built, err := builder.BuildImage(ctx, runtime.BuildRequest{
		Context: scratchContext(t, label), Tags: []string{tag}, Progress: &progress,
		Labels: map[string]string{"odooboat.image.request-label": label},
	})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, progress.String())
	}
	info, err := engine.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if built.ID == "" || built.Engine != info.Identity {
		t.Fatalf("built = %+v, engine %+v", built, info.Identity)
	}
	inspected, err := store.InspectImage(ctx, tag)
	if err != nil {
		t.Fatalf("inspect built image: %v", err)
	}
	if inspected.ID != built.ID || inspected.Labels["odooboat.image.contract-test"] != label ||
		inspected.Labels["odooboat.image.request-label"] != label {
		t.Fatalf("inspected = %+v, built %s", inspected, built.ID)
	}
	if progress.Len() == 0 {
		t.Error("build wrote no progress")
	}

	if err := store.TagImage(ctx, built.ID, alias); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if again, err := store.InspectImage(ctx, alias); err != nil || again.ID != built.ID {
		t.Fatalf("inspect alias = %+v, %v", again, err)
	}
	if err := store.TagImage(ctx, uniqueName(t, "odooboat-contract/absent"), alias); !errors.Is(err, runtime.ErrImageNotFound) {
		t.Fatalf("tag missing source: err = %v, want ErrImageNotFound", err)
	}

	failing := scratchContext(t, label)
	failing.Reset()
	tw := tar.NewWriter(failing)
	content := "FROM scratch\nCOPY missing-file /missing\n"
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
	if _, err := builder.BuildImage(ctx, runtime.BuildRequest{Context: failing}); !errors.Is(err, runtime.ErrImageBuild) {
		t.Fatalf("failing build: err = %v, want ErrImageBuild", err)
	}
}

func testPull(t *testing.T, connect Factory, hooks Hooks) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	engine := connect(ctx, t)
	defer engine.Close()
	if !engine.Capabilities().Has(runtime.CapImagePull) {
		t.Log("image.pull is not declared; nothing to verify")
		return
	}
	store := imageStore(t, engine)
	if err := store.PullImage(ctx, "busybox:latest", nil); err == nil {
		t.Fatal("pulled a reference that is not pinned by digest")
	}
	ref := os.Getenv(PullRefEnv)
	if ref == "" {
		t.Skipf("%s is empty; set it to a reference pinned by digest to exercise pulls", PullRefEnv)
	}
	cleanup(t, hooks, ref)
	var progress bytes.Buffer
	if err := store.PullImage(ctx, ref, &progress); err != nil {
		t.Fatalf("pull %s: %v\n%s", ref, err, progress.String())
	}
	if _, err := store.InspectImage(ctx, ref); err != nil {
		t.Fatalf("inspect pulled image: %v", err)
	}
}

func cleanup(t *testing.T, hooks Hooks, refs ...string) {
	if hooks.RemoveImage == nil {
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, ref := range refs {
			_ = hooks.RemoveImage(ctx, ref)
		}
	})
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
