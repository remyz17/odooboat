package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envimage"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/source"
	"github.com/remyz17/odooboat/internal/source/git"
)

// Error classes of the environment image build. The CLI maps them onto exit
// codes.
var (
	ErrImageNotFound  = runtime.ErrImageNotFound
	ErrInvalidImage   = runtime.ErrInvalidImage
	ErrImageBuild     = runtime.ErrImageBuild
	ErrSourceMismatch = source.ErrSeriesMismatch
	ErrSourceFetch    = git.ErrFetch
	// ErrSourceInvalid: a source is not a usable Odoo tree. Its directory or
	// marker files are missing or unreadable, its series cannot be read, or
	// a file odooboat reads leads outside the tree.
	ErrSourceInvalid = errors.New("odoo source is not usable")
	// ErrSourceRequired: the operation needs an Odoo source the
	// configuration does not declare (ADR 0008 A1.2).
	ErrSourceRequired = errors.New("odoo source is required")
)

type EnvironmentBuildRequest struct {
	Selector Selector
	Wait     WaitOptions
	// Pull allows pulling a missing base image, pinned by digest. Without it
	// a missing base is an error: images are never pulled implicitly.
	Pull bool
	// Reporter receives the pull, fetch, and build steps; nil discards them.
	Reporter Reporter
}

type BuildBase struct {
	Reference string `yaml:"reference" json:"reference"`
	ID        string `yaml:"id" json:"id"`
	Arch      string `yaml:"arch" json:"arch"`
	Pulled    bool   `yaml:"pulled" json:"pulled"`
}

type BuildSource struct {
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Git    string `yaml:"git,omitempty" json:"git,omitempty"`
	Commit string `yaml:"commit,omitempty" json:"commit,omitempty"`
	Series string `yaml:"series" json:"series"`
}

type EnvironmentBuildResult struct {
	WorkspaceID    string                     `yaml:"workspaceId" json:"workspaceId"`
	EnvironmentID  string                     `yaml:"environmentId" json:"environmentId"`
	Environment    string                     `yaml:"environment" json:"environment"`
	Image          string                     `yaml:"image" json:"image"`
	ImageID        string                     `yaml:"imageId" json:"imageId"`
	Identity       string                     `yaml:"identity" json:"identity"`
	Reused         bool                       `yaml:"reused" json:"reused"`
	Base           BuildBase                  `yaml:"base" json:"base"`
	Series         string                     `yaml:"series" json:"series"`
	Python         string                     `yaml:"python" json:"python"`
	PostgresClient int                        `yaml:"postgresClient" json:"postgresClient"`
	Unsupported    []config.Dimension         `yaml:"unsupported" json:"unsupported"`
	SourceMode     string                     `yaml:"sourceMode" json:"sourceMode"`
	Community      BuildSource                `yaml:"community" json:"community"`
	Enterprise     *BuildSource               `yaml:"enterprise,omitempty" json:"enterprise,omitempty"`
	Preferences    config.ResolvedPreferences `yaml:"-" json:"-"`
}

// BuildEnvironment makes the environment image of an environment available in
// its runtime (ADR 0008 §11, amendment A3). An image whose identity already
// exists is reused without building.
func (s WorkspaceService) BuildEnvironment(ctx context.Context, req EnvironmentBuildRequest) (EnvironmentBuildResult, error) {
	// Configuration-level requirements are checked before the environment is
	// bound or locked, so a missing source changes nothing.
	resolved, err := config.Resolve(req.Selector.options())
	if err != nil {
		return EnvironmentBuildResult{}, err
	}
	if err := requireBuildable(resolved.Config); err != nil {
		return EnvironmentBuildResult{}, err
	}

	var result EnvironmentBuildResult
	err = s.withEnvironment(ctx, req.Selector, OperationBuild, req.Wait, req.Reporter, func(ctx context.Context, env LockedEnvironment) error {
		built, err := buildEnvironment(ctx, env, req.Pull, newTracker(req.Reporter))
		if err != nil {
			return err
		}
		built.WorkspaceID, built.EnvironmentID, built.Environment = env.WorkspaceID, env.Binding.ID, env.Name
		built.Preferences = env.Preferences
		result = built
		return nil
	})
	return result, err
}

func requireBuildable(cfg config.ResolvedConfig) error {
	if cfg.Odoo.Source.Community == nil {
		return WithHint(fmt.Errorf("%w: environment %q declares no community source", ErrSourceRequired, cfg.EnvironmentName),
			"set project.odoo.source.community to {path: ...} or {git: ..., commit: ...}")
	}
	if cfg.BaseImage == "" {
		return WithHint(fmt.Errorf("%w: no published base image for Odoo %s with Python %s", config.ErrResolved, cfg.OdooVersion, cfg.Python),
			"set baseImage to an image pinned by digest or a local image ID")
	}
	return nil
}

// buildEnvironment reports a pull step when it pulls the base, a fetch step
// per embedded source, and a build step that is reused or nests the
// builder's steps.
func buildEnvironment(ctx context.Context, env LockedEnvironment, pull bool, t *tracker) (EnvironmentBuildResult, error) {
	cfg := env.Config
	// The configuration was resolved again under the lock; it may have
	// changed since the first check.
	if err := requireBuildable(cfg); err != nil {
		return EnvironmentBuildResult{}, err
	}
	store, ok := env.Engine.(runtime.ImageStore)
	builder, canBuild := env.Engine.(runtime.ImageBuilder)
	if !ok || !canBuild || !env.Engine.Capabilities().Has(runtime.CapImageBuild) {
		return EnvironmentBuildResult{}, fmt.Errorf("%w: runtime connection %q cannot build images",
			ErrRuntimeUnsupported, env.Binding.Connection.Alias)
	}

	base, from, err := prepareBase(ctx, env, store, pull, t)
	if err != nil {
		return EnvironmentBuildResult{}, err
	}

	result := EnvironmentBuildResult{
		Base: base, Series: cfg.OdooVersion, Python: cfg.Python, PostgresClient: cfg.Postgres,
		Unsupported: cfg.Unsupported, SourceMode: cfg.Odoo.Source.Mode,
	}
	work, err := os.MkdirTemp("", "odooboat-build-")
	if err != nil {
		return EnvironmentBuildResult{}, err
	}
	defer os.RemoveAll(work)

	spec := envimage.Spec{
		From: from, BaseID: base.ID, Arch: base.Arch, Series: cfg.OdooVersion, Python: cfg.Python,
		PostgresClient: cfg.Postgres, Requirements: map[string][]byte{}, Embedded: map[string]envimage.Embedded{},
	}

	src := cfg.Odoo.Source
	community, err := prepareSource(ctx, envimage.Community, *src.Community, cfg.OdooVersion, work, &spec, t)
	if err != nil {
		return EnvironmentBuildResult{}, err
	}
	result.Community = community
	if src.Enterprise != nil {
		enterprise, err := prepareSource(ctx, envimage.Enterprise, *src.Enterprise, cfg.OdooVersion, work, &spec, t)
		if err != nil {
			return EnvironmentBuildResult{}, err
		}
		result.Enterprise = &enterprise
	}

	plan, err := envimage.NewPlan(spec)
	if err != nil {
		return EnvironmentBuildResult{}, err
	}
	result.Identity, result.Image = plan.Identity, plan.Tag()

	step := t.start("", StepBuild, Subject{Kind: ResourceImage, Name: plan.Tag()})
	if existing, err := store.InspectImage(ctx, plan.Tag()); err == nil {
		if metadata, err := runtime.DecodeImageMetadata(existing.Labels); err == nil && metadata.Inputs == plan.Identity {
			step.finishAs(OutcomeReused)
			result.ImageID, result.Reused = existing.ID, true
			return result, nil
		}
		// A tag is only an index: an image whose label disagrees is rebuilt.
	} else if !errors.Is(err, runtime.ErrImageNotFound) {
		step.finish(ctx, err)
		return EnvironmentBuildResult{}, err
	}

	built, err := buildFromContext(plan.WriteContext, func(context io.Reader) (runtime.BuiltImage, error) {
		return builder.BuildImage(ctx, runtime.BuildRequest{
			Context: context, Tags: []string{plan.Tag()}, Labels: plan.Metadata(base.Reference).Labels(),
			Progress: t.runtimeProgress(ctx, step.id()),
		})
	})
	if err == nil {
		err = deliver(built, env)
	}
	step.finish(ctx, err)
	if err != nil {
		return EnvironmentBuildResult{}, err
	}
	result.ImageID = built.ID
	return result, nil
}

// errContextAbandoned ends the context writer once the builder returned.
var errContextAbandoned = errors.New("build context abandoned")

// buildFromContext streams the context that write produces into build. When
// writing fails, that failure is the cause: the builder only sees a truncated
// stream and would report it obscurely. The writer has returned before
// buildFromContext does, so nothing it reads can disappear under it.
func buildFromContext(write func(io.Writer) error, build func(io.Reader) (runtime.BuiltImage, error)) (runtime.BuiltImage, error) {
	reader, writer := io.Pipe()
	written := make(chan error, 1)
	go func() {
		err := write(writer)
		_ = writer.CloseWithError(err)
		written <- err
	}()
	built, err := build(reader)
	_ = reader.CloseWithError(errContextAbandoned)
	if writeErr := <-written; writeErr != nil && !errors.Is(writeErr, errContextAbandoned) {
		return runtime.BuiltImage{}, fmt.Errorf("write build context: %w", writeErr)
	}
	return built, err
}

// prepareBase finds the base image in the runtime, pulling it only when
// allowed, and checks that its metadata matches the configuration (ADR 0008
// §7). It returns the reference the Dockerfile builds FROM.
func prepareBase(ctx context.Context, env LockedEnvironment, store runtime.ImageStore, pull bool, t *tracker) (BuildBase, string, error) {
	cfg := env.Config
	ref := cfg.BaseImage
	local := strings.HasPrefix(ref, "sha256:")
	base := BuildBase{Reference: ref}

	info, err := store.InspectImage(ctx, ref)
	switch {
	case err == nil:
	case !errors.Is(err, runtime.ErrImageNotFound):
		return BuildBase{}, "", err
	case local:
		return BuildBase{}, "", fmt.Errorf("%w: local base image %s is not present in runtime %q", ErrImageNotFound, ref, env.Binding.Connection.Alias)
	case !pull:
		return BuildBase{}, "", WithHint(fmt.Errorf("%w: base image %s is not present in runtime %q",
			ErrImageNotFound, ref, env.Binding.Connection.Alias), "rerun with --pull")
	default:
		if !env.Engine.Capabilities().Has(runtime.CapImagePull) {
			return BuildBase{}, "", fmt.Errorf("%w: runtime connection %q cannot pull images", ErrRuntimeUnsupported, env.Binding.Connection.Alias)
		}
		if err := pullImage(ctx, store, ref, t); err != nil {
			return BuildBase{}, "", err
		}
		if info, err = store.InspectImage(ctx, ref); err != nil {
			return BuildBase{}, "", err
		}
		base.Pulled = true
	}
	base.ID, base.Arch = info.ID, info.Arch

	metadata, err := runtime.DecodeImageMetadata(info.Labels)
	if err != nil {
		return BuildBase{}, "", fmt.Errorf("base image %s: %w", ref, err)
	}
	switch {
	case metadata.Kind != runtime.ImageKindBase:
		return BuildBase{}, "", fmt.Errorf("%w: %s is a %s image, not a base image", ErrInvalidImage, ref, metadata.Kind)
	case metadata.Series != cfg.OdooVersion:
		return BuildBase{}, "", fmt.Errorf("%w: base image %s is for Odoo %s, the environment uses %s", ErrInvalidImage, ref, metadata.Series, cfg.OdooVersion)
	case metadata.Python != cfg.Python:
		return BuildBase{}, "", fmt.Errorf("%w: base image %s provides Python %s, the environment uses %s", ErrInvalidImage, ref, metadata.Python, cfg.Python)
	}
	// A base of another platform would build under emulation, slowly or not
	// at all. An engine that does not report its architecture is trusted.
	if engine := runtime.PlatformArch(env.EngineInfo.Arch); engine != "" && runtime.PlatformArch(info.Arch) != engine {
		return BuildBase{}, "", fmt.Errorf("%w: base image %s is for %s, runtime %q runs %s",
			ErrInvalidImage, ref, info.Arch, env.Binding.Connection.Alias, engine)
	}

	if !local {
		return base, ref, nil
	}
	// BuildKit cannot build FROM a bare image ID; a tag derived from the ID
	// names the same content.
	tag := envimage.LocalBaseTag(info.ID)
	if err := store.TagImage(ctx, info.ID, tag); err != nil {
		return BuildBase{}, "", err
	}
	return base, tag, nil
}

// prepareSource checks one source's series and records its requirements in
// the spec; an embedded source is exported first (ADR 0008 §8 and §9).
func prepareSource(ctx context.Context, edition string, location config.ResolvedSourceLocation, series, work string,
	spec *envimage.Spec, t *tracker) (BuildSource, error) {
	dir := location.Path
	report := BuildSource{Path: location.Path, Git: location.Git, Commit: location.Commit}
	if location.Mode() == config.SourceModeEmbedded {
		dir = filepath.Join(work, edition)
		step := t.start("", StepFetch, Subject{Kind: ResourceSource, Name: edition})
		err := git.Export(ctx, location.Git, location.Commit, dir)
		step.finish(ctx, err)
		if err != nil {
			return BuildSource{}, fmt.Errorf("%s source: %w", edition, err)
		}
		spec.Embedded[edition] = envimage.Embedded{Git: location.Git, Commit: location.Commit, Dir: dir}
	}

	read := source.ReadSeries
	if edition == envimage.Enterprise {
		read = source.ReadEnterpriseSeries
	}
	detected, err := read(dir)
	if err != nil {
		return BuildSource{}, fmt.Errorf("%w: %s source: %w", ErrSourceInvalid, edition, err)
	}
	if detected != series {
		return BuildSource{}, fmt.Errorf("%w: %s source is Odoo %s, the environment uses %s", ErrSourceMismatch, edition, detected, series)
	}
	report.Series = detected

	requirements, err := source.ReadRequirements(dir)
	switch {
	case err == nil:
		spec.Requirements[edition] = requirements
	case edition == envimage.Enterprise && errors.Is(err, source.ErrNotOdooSource):
		// Enterprise declares no requirements.txt of its own.
	default:
		return BuildSource{}, fmt.Errorf("%w: %s source: %w", ErrSourceInvalid, edition, err)
	}
	return report, nil
}

// deliver makes a build result available to the runtime that runs it (ADR
// 0001 §4). Docker builds in the engine that runs, so there is nothing to
// transfer; a builder elsewhere needs a transfer this slice does not provide.
func deliver(built runtime.BuiltImage, env LockedEnvironment) error {
	if built.Engine != env.EngineInfo.Identity {
		return fmt.Errorf("%w: image %s was built in engine %s=%s, not in the environment's engine; delivery between engines is not supported yet",
			ErrRuntimeUnsupported, built.ID, built.Engine.Source, built.Engine.Value)
	}
	return nil
}

// pullImage pulls ref as a step whose layers are nested steps.
func pullImage(ctx context.Context, store runtime.ImageStore, ref string, t *tracker) error {
	step := t.start("", StepPull, Subject{Kind: ResourceImage, Name: ref})
	err := store.PullImage(ctx, ref, t.runtimeProgress(ctx, step.id()))
	step.finish(ctx, err)
	return err
}
