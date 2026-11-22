package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envimage"
	"github.com/remyz17/odooboat/internal/runtime"
	statefile "github.com/remyz17/odooboat/internal/state/file"
)

// imageEngine is an in-memory runtime with an image store and a builder.
type imageEngine struct {
	fakeEngine
	images   map[string]runtime.ImageInfo
	pullable map[string]runtime.ImageInfo
	builds   int
	pulls    int
	// builtIn overrides the engine reported by builds, to exercise delivery.
	builtIn *runtime.EngineIdentity
}

func newImageEngine() *imageEngine {
	return &imageEngine{
		fakeEngine: fakeEngine{identity: "engine-a",
			capabilities: runtime.Capabilities{runtime.CapEngineIdentity, runtime.CapImageBuild, runtime.CapImagePull}},
		images: map[string]runtime.ImageInfo{}, pullable: map[string]runtime.ImageInfo{},
	}
}

func (e *imageEngine) add(refs []string, info runtime.ImageInfo) {
	e.images[info.ID] = info
	for _, ref := range refs {
		e.images[ref] = info
	}
}

func (e *imageEngine) InspectImage(_ context.Context, ref string) (runtime.ImageInfo, error) {
	if info, ok := e.images[ref]; ok {
		return info, nil
	}
	return runtime.ImageInfo{}, fmt.Errorf("%w: %s", runtime.ErrImageNotFound, ref)
}

func (e *imageEngine) PullImage(_ context.Context, ref string, _ runtime.ProgressFunc) error {
	info, ok := e.pullable[ref]
	if !ok {
		return errors.New("not in the registry")
	}
	e.pulls++
	e.add([]string{ref}, info)
	return nil
}

func (e *imageEngine) TagImage(_ context.Context, source, target string) error {
	info, ok := e.images[source]
	if !ok {
		return fmt.Errorf("%w: %s", runtime.ErrImageNotFound, source)
	}
	e.images[target] = info
	return nil
}

func (e *imageEngine) BuildImage(_ context.Context, req runtime.BuildRequest) (runtime.BuiltImage, error) {
	if _, err := io.ReadAll(req.Context); err != nil {
		return runtime.BuiltImage{}, err
	}
	e.builds++
	info := runtime.ImageInfo{ID: fmt.Sprintf("sha256:built-%d", e.builds), Labels: req.Labels, Arch: "arm64"}
	e.add(req.Tags, info)
	identity := runtime.EngineIdentity{Source: "fake.id", Value: e.identity}
	if e.builtIn != nil {
		identity = *e.builtIn
	}
	return runtime.BuiltImage{ID: info.ID, Engine: identity}, nil
}

var (
	localBaseID = "sha256:" + strings.Repeat("a", 64)
	pinnedBase  = "ghcr.io/x/odooboat/odoo@sha256:" + strings.Repeat("b", 64)
)

func baseLabels(python string) map[string]string {
	return runtime.ImageMetadata{Contract: runtime.ImageContract, Series: "18.0", Python: python, Kind: runtime.ImageKindBase}.Labels()
}

func sourceFixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "source", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// buildFixture writes a project whose lines are appended under "project:".
func buildFixture(t *testing.T, engine runtime.Engine, projectLines string) (WorkspaceService, Selector, string) {
	t.Helper()
	selector, root := statefulSelector(t)
	content := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n" + projectLines
	if err := os.WriteFile(filepath.Join(root, config.ProjectFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store := statefile.New()
	return NewWorkspaceService(store, store, engineConnector{engine}, "test"), selector, root
}

type engineConnector struct{ engine runtime.Engine }

func (c engineConnector) Connect(context.Context, runtime.Locator) (runtime.Engine, error) {
	return c.engine, nil
}

func communityLines(t *testing.T, base, community string) string {
	return "  baseImage: " + base + "\n  odoo:\n    source:\n      community: {path: " + sourceFixture(t, community) + "}\n"
}

func build(service WorkspaceService, selector Selector, pull bool) (EnvironmentBuildResult, error) {
	return service.BuildEnvironment(context.Background(), EnvironmentBuildRequest{
		Selector: selector, Wait: WaitOptions{Policy: WaitFailFast}, Pull: pull,
	})
}

func TestBuildEnvironmentBuildsThenReuses(t *testing.T) {
	engine := newImageEngine()
	engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "arm64"})
	service, selector, _ := buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))

	first, err := build(service, selector, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || engine.builds != 1 || first.Base.ID != localBaseID || first.Base.Arch != "arm64" ||
		first.Community.Series != "18.0" || first.PostgresClient != 16 || first.SourceMode != config.SourceModeMount {
		t.Fatalf("first build = %+v (builds %d)", first, engine.builds)
	}
	if !strings.HasPrefix(first.Image, envimage.Repository+":") || first.Image != envimage.Repository+":"+strings.TrimPrefix(first.Identity, "sha256:") {
		t.Fatalf("image tag = %q, identity %q", first.Image, first.Identity)
	}
	// A local base is built FROM a tag derived from its ID.
	if _, ok := engine.images[envimage.LocalBaseTag(localBaseID)]; !ok {
		t.Fatal("local base was not tagged")
	}
	metadata, err := runtime.DecodeImageMetadata(engine.images[first.Image].Labels)
	if err != nil || metadata.Inputs != first.Identity || metadata.Base != localBaseID || metadata.Kind != runtime.ImageKindEnvironment {
		t.Fatalf("labels = %+v, %v", metadata, err)
	}

	second, err := build(service, selector, false)
	if err != nil || !second.Reused || engine.builds != 1 || second.Identity != first.Identity || second.ImageID != first.ImageID {
		t.Fatalf("second build = %+v (builds %d), %v", second, engine.builds, err)
	}
}

// The tag is an index, the label is the identity: a tag pointing at an image
// with other inputs is rebuilt.
func TestBuildEnvironmentRebuildsWhenTheTagDisagrees(t *testing.T) {
	engine := newImageEngine()
	engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "arm64"})
	service, selector, _ := buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))
	first, err := build(service, selector, false)
	if err != nil {
		t.Fatal(err)
	}
	stale := engine.images[first.Image]
	stale.Labels = map[string]string{}
	engine.images[first.Image] = stale
	if again, err := build(service, selector, false); err != nil || again.Reused || engine.builds != 2 {
		t.Fatalf("rebuild = %+v (builds %d), %v", again, engine.builds, err)
	}
}

func TestBuildEnvironmentPullsOnlyWhenAllowed(t *testing.T) {
	engine := newImageEngine()
	engine.pullable[pinnedBase] = runtime.ImageInfo{ID: "sha256:" + strings.Repeat("c", 64), Labels: baseLabels("3.12"), Arch: "arm64"}
	service, selector, _ := buildFixture(t, engine, communityLines(t, pinnedBase, "community-18.0"))

	_, err := build(service, selector, false)
	if !errors.Is(err, ErrImageNotFound) || !strings.Contains(err.Error(), "--pull") || engine.pulls != 0 || engine.builds != 0 {
		t.Fatalf("without --pull: err = %v, pulls %d, builds %d", err, engine.pulls, engine.builds)
	}
	result, err := build(service, selector, true)
	if err != nil || !result.Base.Pulled || engine.pulls != 1 || engine.builds != 1 || result.Base.Arch != "arm64" {
		t.Fatalf("with --pull = %+v, pulls %d, builds %d, %v", result, engine.pulls, engine.builds, err)
	}
	// A published base is built FROM its pinned reference, not retagged.
	if _, ok := engine.images[envimage.LocalBaseTag(result.Base.ID)]; ok {
		t.Fatal("a published base was retagged")
	}
}

func TestBuildEnvironmentRejectsMismatchedBases(t *testing.T) {
	cases := map[string]map[string]string{
		"other python":      baseLabels("3.10"),
		"environment image": envimage.Plan{}.Metadata("x").Labels(),
		"no metadata":       {},
	}
	for name, labels := range cases {
		engine := newImageEngine()
		engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: labels})
		service, selector, _ := buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))
		if _, err := build(service, selector, false); !errors.Is(err, ErrInvalidImage) || engine.builds != 0 {
			t.Errorf("%s: err = %v, builds %d", name, err, engine.builds)
		}
	}
	// The fake engine reports arm64, as Docker would report aarch64.
	engine := newImageEngine()
	engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "amd64"})
	service, selector, _ := buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))
	if _, err := build(service, selector, false); !errors.Is(err, ErrInvalidImage) || !strings.Contains(err.Error(), "amd64") || engine.builds != 0 {
		t.Errorf("other platform: err = %v, builds %d", err, engine.builds)
	}

	// A local image ID is never pulled.
	engine = newImageEngine()
	service, selector, _ = buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))
	if _, err := build(service, selector, true); !errors.Is(err, ErrImageNotFound) || engine.pulls != 0 {
		t.Fatalf("absent local base: err = %v, pulls %d", err, engine.pulls)
	}
}

func TestBuildEnvironmentRejectsSourceProblems(t *testing.T) {
	engine := newImageEngine()
	engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "arm64"})

	// Without a community source nothing is bound or locked.
	service, selector, root := buildFixture(t, engine, "  baseImage: "+localBaseID+"\n")
	if _, err := build(service, selector, false); !errors.Is(err, ErrSourceRequired) {
		t.Fatalf("no source: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".odooboat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected build created state: %v", err)
	}

	service, selector, _ = buildFixture(t, engine, communityLines(t, localBaseID, "community-17.0"))
	if _, err := build(service, selector, false); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("community 17.0: err = %v", err)
	}
	pair := communityLines(t, localBaseID, "community-18.0") + "      enterprise: {path: " + sourceFixture(t, "enterprise-19.0") + "}\n"
	service, selector, _ = buildFixture(t, engine, pair)
	if _, err := build(service, selector, false); !errors.Is(err, ErrSourceMismatch) || !strings.Contains(err.Error(), "enterprise") {
		t.Fatalf("enterprise 19.0: err = %v", err)
	}

	// An unusable tree is not reported as a series mismatch.
	// An absent path is a configuration error, caught before; an existing
	// directory that is not Odoo reaches the build.
	lines := "  baseImage: " + localBaseID + "\n  odoo:\n    source:\n      community: {path: " + t.TempDir() + "}\n"
	service, selector, _ = buildFixture(t, engine, lines)
	if _, err := build(service, selector, false); !errors.Is(err, ErrSourceInvalid) || errors.Is(err, ErrSourceMismatch) ||
		!strings.Contains(err.Error(), "release.py is missing") {
		t.Fatalf("not an Odoo tree: err = %v", err)
	}
	noRequirements := t.TempDir()
	if err := os.MkdirAll(filepath.Join(noRequirements, "odoo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noRequirements, "odoo", "release.py"), []byte("version_info = (18, 0, 0, FINAL, 0, '')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines = "  baseImage: " + localBaseID + "\n  odoo:\n    source:\n      community: {path: " + noRequirements + "}\n"
	service, selector, _ = buildFixture(t, engine, lines)
	if _, err := build(service, selector, false); !errors.Is(err, ErrSourceInvalid) || !strings.Contains(err.Error(), "requirements.txt") {
		t.Fatalf("community without requirements: err = %v", err)
	}
	if engine.builds != 0 {
		t.Fatalf("builds = %d", engine.builds)
	}
}

func TestBuildEnvironmentNeedsAnImageCapableRuntime(t *testing.T) {
	service, selector, _ := buildFixture(t, identityEngine("engine-a"), communityLines(t, localBaseID, "community-18.0"))
	if _, err := build(service, selector, false); !errors.Is(err, ErrRuntimeUnsupported) {
		t.Fatalf("err = %v, want ErrRuntimeUnsupported", err)
	}

	engine := newImageEngine()
	engine.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "arm64"})
	engine.builtIn = &runtime.EngineIdentity{Source: "fake.id", Value: "builder-elsewhere"}
	service, selector, _ = buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0"))
	if _, err := build(service, selector, false); !errors.Is(err, ErrRuntimeUnsupported) || !strings.Contains(err.Error(), "delivery") {
		t.Fatalf("delivery: err = %v", err)
	}
}

// A failing context writer is the cause reported, not the builder's view of a
// truncated stream; a builder that stops reading does not block the writer.
func TestBuildFromContext(t *testing.T) {
	broken := errors.New("unsupported file type")
	_, err := buildFromContext(func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return broken
	}, func(r io.Reader) (runtime.BuiltImage, error) {
		_, _ = io.ReadAll(r)
		return runtime.BuiltImage{}, fmt.Errorf("%w: unexpected EOF", runtime.ErrImageBuild)
	})
	if !errors.Is(err, broken) || errors.Is(err, runtime.ErrImageBuild) {
		t.Fatalf("writer failure: err = %v", err)
	}

	refused := errors.New("daemon refused the request")
	_, err = buildFromContext(func(w io.Writer) error {
		_, err := w.Write(make([]byte, 1<<20))
		return err
	}, func(io.Reader) (runtime.BuiltImage, error) { return runtime.BuiltImage{}, refused })
	if !errors.Is(err, refused) {
		t.Fatalf("builder failure: err = %v", err)
	}

	built, err := buildFromContext(func(w io.Writer) error {
		_, err := w.Write([]byte("context"))
		return err
	}, func(r io.Reader) (runtime.BuiltImage, error) {
		data, err := io.ReadAll(r)
		return runtime.BuiltImage{ID: string(data)}, err
	})
	if err != nil || built.ID != "context" {
		t.Fatalf("success: %+v, %v", built, err)
	}
}
