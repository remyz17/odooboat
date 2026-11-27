package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/runtime"
	"gopkg.in/yaml.v3"
)

// imageStub is an image-capable runtime: an image store, a registry of
// pullable references, and a builder that writes one progress line.
type imageStub struct {
	stubEngine
	images   map[string]runtime.ImageInfo
	registry map[string]runtime.ImageInfo
	builds   int
	pulls    int
}

func newImageStub() *imageStub {
	return &imageStub{images: map[string]runtime.ImageInfo{}, registry: map[string]runtime.ImageInfo{}}
}

func (*imageStub) Capabilities() runtime.Capabilities {
	return runtime.Capabilities{runtime.CapEngineIdentity, runtime.CapImageBuild, runtime.CapImagePull}
}

// Info reports the kernel's name for the architecture, as Docker does.
func (*imageStub) Info(context.Context) (runtime.EngineInfo, error) {
	return runtime.EngineInfo{Identity: runtime.EngineIdentity{Source: "stub.id", Value: "engine-1"}, Version: "1.0", OS: "linux", Arch: "aarch64"}, nil
}

func (s *imageStub) InspectImage(_ context.Context, ref string) (runtime.ImageInfo, error) {
	if info, ok := s.images[ref]; ok {
		return info, nil
	}
	return runtime.ImageInfo{}, fmt.Errorf("%w: %s", runtime.ErrImageNotFound, ref)
}

func (s *imageStub) PullImage(_ context.Context, ref string, progress runtime.ProgressFunc) error {
	info, ok := s.registry[ref]
	if !ok {
		return errors.New("not in the registry")
	}
	s.pulls++
	layer := runtime.Progress{ID: "0123456789ab", Kind: runtime.ProgressLayer, Name: "0123456789ab"}
	for _, state := range []runtime.ProgressState{runtime.ProgressStarted, runtime.ProgressDone} {
		layer.State = state
		progress.Report(layer)
	}
	s.images[ref] = info
	return nil
}

func (s *imageStub) TagImage(_ context.Context, source, target string) error {
	info, ok := s.images[source]
	if !ok {
		return fmt.Errorf("%w: %s", runtime.ErrImageNotFound, source)
	}
	s.images[target] = info
	return nil
}

func (s *imageStub) BuildImage(_ context.Context, req runtime.BuildRequest) (runtime.BuiltImage, error) {
	if _, err := io.Copy(io.Discard, req.Context); err != nil {
		return runtime.BuiltImage{}, err
	}
	s.builds++
	req.Progress.Report(runtime.Progress{Output: []byte("#1 stub build step\n")})
	info := runtime.ImageInfo{ID: fmt.Sprintf("sha256:built-%d", s.builds), Labels: req.Labels, Arch: "arm64"}
	for _, tag := range req.Tags {
		s.images[tag] = info
	}
	return runtime.BuiltImage{ID: info.ID, Engine: runtime.EngineIdentity{Source: "stub.id", Value: "engine-1"}}, nil
}

type imageStubConnector struct{ engine *imageStub }

func (c imageStubConnector) Connect(context.Context, runtime.Locator) (runtime.Engine, error) {
	return c.engine, nil
}

var (
	stubLocalBase  = "sha256:" + strings.Repeat("a", 64)
	stubPinnedBase = "ghcr.io/x/odooboat/odoo@sha256:" + strings.Repeat("b", 64)
)

func stubBaseInfo(id string) runtime.ImageInfo {
	labels := runtime.ImageMetadata{Contract: runtime.ImageContract, Series: "18.0", Python: "3.12", Kind: runtime.ImageKindBase}.Labels()
	return runtime.ImageInfo{ID: id, Labels: labels, Arch: "arm64"}
}

// buildProject writes a project built on base with the given community
// fixture, or none when community is empty, and returns its root and the
// global arguments.
func buildProject(t *testing.T, base, community string) (string, []string) {
	t.Helper()
	root, args := cliStateProject(t)
	project := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  python: \"3.12\"\n  runtimeConnection: local\n  baseImage: " + base + "\n"
	if community != "" {
		dir, err := filepath.Abs(filepath.Join("..", "source", "testdata", community))
		if err != nil {
			t.Fatal(err)
		}
		project += "  odoo:\n    source:\n      community: {path: " + dir + "}\n"
	}
	if err := os.WriteFile(filepath.Join(root, "odooboat.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, args
}

type buildOutput struct {
	Image    string `json:"image" yaml:"image"`
	Identity string `json:"identity" yaml:"identity"`
	Reused   bool   `json:"reused" yaml:"reused"`
	Base     struct {
		ID     string `json:"id" yaml:"id"`
		Arch   string `json:"arch" yaml:"arch"`
		Pulled bool   `json:"pulled" yaml:"pulled"`
	} `json:"base" yaml:"base"`
	PostgresClient int `json:"postgresClient" yaml:"postgresClient"`
	Community      struct {
		Series string `json:"series" yaml:"series"`
	} `json:"community" yaml:"community"`
}

// The result goes to stdout in the selected format and progress to stderr,
// so the output stays parseable; a second run reuses the image.
func TestEnvironmentBuildCommand(t *testing.T) {
	engine := newImageStub()
	engine.images[stubLocalBase] = stubBaseInfo(stubLocalBase)
	root, base := buildProject(t, stubLocalBase, "community-18.0")
	args := append(append([]string{}, base...), "environment", "build", "--output", "json", "--progress", "plain")

	first := executeWith(t, imageStubConnector{engine}, root, args...)
	if first.err != nil {
		t.Fatalf("%v\nstderr: %s", first.err, first.stderr)
	}
	var built buildOutput
	if err := json.Unmarshal([]byte(first.stdout), &built); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, first.stdout)
	}
	if built.Reused || built.Base.ID != stubLocalBase || built.Base.Arch != "arm64" || built.PostgresClient != 16 ||
		built.Community.Series != "18.0" || !strings.HasPrefix(built.Image, "odooboat/environment:") || engine.builds != 1 {
		t.Fatalf("first build = %+v (builds %d)", built, engine.builds)
	}
	// Progress shows the identity tag cut to 12 characters.
	short := built.Image[:len("odooboat/environment:")+12]
	if want := "build image " + short + "\n"; !strings.HasPrefix(first.stderr, want) ||
		!strings.Contains(first.stderr, "build image "+short+": done (") {
		t.Errorf("stderr lacks the build step:\n%s", first.stderr)
	}
	// A successful step's output is retained, then discarded.
	if strings.Contains(first.stderr+first.stdout, "stub build step") {
		t.Errorf("successful build output reached the output:\n%s", first.stderr)
	}

	// The same result in YAML.
	second := executeWith(t, imageStubConnector{engine}, root, append(append([]string{}, base...), "environment", "build", "-o", "yaml")...)
	var reused buildOutput
	if second.err != nil || yaml.Unmarshal([]byte(second.stdout), &reused) != nil {
		t.Fatalf("second build = %q, %v", second.stdout, second.err)
	}
	if !reused.Reused || reused.Identity != built.Identity || engine.builds != 1 || !strings.Contains(second.stderr, ": reused (") {
		t.Fatalf("second build = %+v (builds %d), stderr %q", reused, engine.builds, second.stderr)
	}
}

func TestEnvironmentBuildPullFlag(t *testing.T) {
	engine := newImageStub()
	engine.registry[stubPinnedBase] = stubBaseInfo("sha256:" + strings.Repeat("c", 64))
	root, base := buildProject(t, stubPinnedBase, "community-18.0")
	args := append(append([]string{}, base...), "environment", "build", "--output", "json")

	refused := executeWith(t, imageStubConnector{engine}, root, args...)
	if ExitCode(refused.err) != ExitImage || app.Hint(refused.err) != "rerun with --pull" || engine.pulls != 0 || refused.stdout != "" {
		t.Fatalf("without --pull: err = %v (exit %d), pulls %d, stdout %q", refused.err, ExitCode(refused.err), engine.pulls, refused.stdout)
	}

	pulled := executeWith(t, imageStubConnector{engine}, root, append(args, "--pull")...)
	var built buildOutput
	if pulled.err != nil || json.Unmarshal([]byte(pulled.stdout), &built) != nil {
		t.Fatalf("with --pull = %q, %v", pulled.stdout, pulled.err)
	}
	if !built.Base.Pulled || engine.pulls != 1 || engine.builds != 1 ||
		!strings.Contains(pulled.stderr, `"subject":{"kind":"layer","name":"0123456789ab"}`) {
		t.Fatalf("with --pull = %+v (pulls %d, builds %d), stderr %q", built, engine.pulls, engine.builds, pulled.stderr)
	}
}

// Each failure class of the build reaches its own exit code.
func TestEnvironmentBuildExitCodes(t *testing.T) {
	cases := map[string]struct {
		base, community string
		connector       runtime.Connector
		code            int
		message         string
	}{
		"no community source":   {stubLocalBase, "", nil, ExitConfig, "declares no community source"},
		"base absent":           {stubLocalBase + "0", "community-18.0", nil, ExitConfig, "baseImage"},
		"runtime cannot build":  {stubLocalBase, "community-18.0", stubConnector{}, ExitRuntime, "cannot build images"},
		"series mismatch":       {stubLocalBase, "community-17.0", nil, ExitSource, "is Odoo 17.0"},
		"not an odoo tree":      {stubLocalBase, "enterprise-18.0", nil, ExitSource, "release.py is missing"},
		"local base not loaded": {"sha256:" + strings.Repeat("d", 64), "community-18.0", nil, ExitImage, "not present"},
	}
	for name, tc := range cases {
		engine := newImageStub()
		engine.images[stubLocalBase] = stubBaseInfo(stubLocalBase)
		connector := tc.connector
		if connector == nil {
			connector = imageStubConnector{engine}
		}
		root, base := buildProject(t, tc.base, tc.community)
		result := executeWith(t, connector, root, append(append([]string{}, base...), "environment", "build")...)
		if code := ExitCode(result.err); code != tc.code || result.err == nil || !strings.Contains(result.err.Error(), tc.message) {
			t.Errorf("%s: exit %d, want %d; err = %v", name, code, tc.code, result.err)
		}
		if tc.code == ExitSource && strings.Contains(tc.message, "missing") && strings.Contains(result.err.Error(), "does not match") {
			t.Errorf("%s: an unusable tree is reported as a mismatch: %v", name, result.err)
		}
		if engine.builds != 0 || result.stdout != "" {
			t.Errorf("%s: builds %d, stdout %q", name, engine.builds, result.stdout)
		}
	}
}
