package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
)

const (
	OperationUp   Operation = "up"
	OperationDown Operation = "down"
)

var (
	// ErrConflict: an expected resource name is held by a resource odooboat
	// does not own, or an owned resource cannot be mutated (read-only or
	// malformed claim).
	ErrConflict = runtime.ErrConflict
	// ErrIncompatibleData: an existing data volume does not match the
	// configuration; reusing it could damage data (ADR 0001 §5).
	ErrIncompatibleData = errors.New("existing data is incompatible with the configuration")
	// ErrNotReady: a container did not become ready in time, or stopped.
	ErrNotReady = errors.New("environment did not become ready")
)

// Readiness deadlines and polling interval (ADR 0009 §5). Variables so tests
// do not wait.
var (
	postgresReadyTimeout = 60 * time.Second
	odooReadyTimeout     = 120 * time.Second
	readyInterval        = time.Second
)

// Stop grace periods: PostgreSQL gets time for a clean shutdown.
const (
	odooStopGrace     = 10 * time.Second
	postgresStopGrace = 30 * time.Second
	// failureLogLines is how many log lines are attached to a readiness error.
	failureLogLines = 50
)

type UpRequest struct {
	Selector Selector
	Wait     WaitOptions
	// Pull allows pulling missing images pinned by digest: the base and the
	// PostgreSQL server image.
	Pull     bool
	Progress io.Writer
}

// ResourceReport describes one resource and what an operation did with it.
type ResourceReport struct {
	Kind   runtime.ResourceType `yaml:"kind" json:"kind"`
	Role   runtime.Role         `yaml:"role" json:"role"`
	Name   string               `yaml:"name" json:"name"`
	Action string               `yaml:"action,omitempty" json:"action,omitempty"`
	State  string               `yaml:"state,omitempty" json:"state,omitempty"`
	Reason string               `yaml:"reason,omitempty" json:"reason,omitempty"`
}

type UpImage struct {
	Image    string `yaml:"image" json:"image"`
	ImageID  string `yaml:"imageId" json:"imageId"`
	Identity string `yaml:"identity,omitempty" json:"identity,omitempty"`
	Reused   bool   `yaml:"reused,omitempty" json:"reused,omitempty"`
	Pulled   bool   `yaml:"pulled,omitempty" json:"pulled,omitempty"`
}

type UpResult struct {
	WorkspaceID   string                     `yaml:"workspaceId" json:"workspaceId"`
	EnvironmentID string                     `yaml:"environmentId" json:"environmentId"`
	Environment   string                     `yaml:"environment" json:"environment"`
	URL           string                     `yaml:"url" json:"url"`
	Odoo          UpImage                    `yaml:"odooImage" json:"odooImage"`
	Postgres      UpImage                    `yaml:"postgresImage" json:"postgresImage"`
	Resources     []ResourceReport           `yaml:"resources" json:"resources"`
	Warnings      []string                   `yaml:"warnings" json:"warnings"`
	Preferences   config.ResolvedPreferences `yaml:"-" json:"-"`
}

// lifecycleInputs are resolved from configuration before binding or locking,
// and again under the lock.
type lifecycleInputs struct {
	postgresRef    string
	masterPassword string
	configured     bool
}

func resolveLifecycle(cfg config.ResolvedConfig) (lifecycleInputs, error) {
	if err := requireBuildable(cfg); err != nil {
		return lifecycleInputs{}, err
	}
	images, err := config.EmbeddedPostgresImages()
	if err != nil {
		return lifecycleInputs{}, err
	}
	ref, ok := images.Lookup(cfg.Postgres)
	if !ok {
		return lifecycleInputs{}, fmt.Errorf("%w: no PostgreSQL server image for major version %d; available: %s",
			config.ErrResolved, cfg.Postgres, joinInts(images.Majors()))
	}
	password, configured, err := cfg.Odoo.MasterPassword.Resolve(os.LookupEnv)
	if err != nil {
		return lifecycleInputs{}, fmt.Errorf("odoo.masterPassword: %w", err)
	}
	return lifecycleInputs{postgresRef: ref, masterPassword: password, configured: configured}, nil
}

// lifecycleEngine is the primitive set the lifecycle needs.
type lifecycleEngine struct {
	images     runtime.ImageStore
	containers runtime.ContainerStore
	networks   runtime.NetworkStore
	volumes    runtime.VolumeStore
}

func lifecycleStores(env LockedEnvironment, required ...runtime.Capability) (lifecycleEngine, error) {
	var e lifecycleEngine
	var ok [4]bool
	e.images, ok[0] = env.Engine.(runtime.ImageStore)
	e.containers, ok[1] = env.Engine.(runtime.ContainerStore)
	e.networks, ok[2] = env.Engine.(runtime.NetworkStore)
	e.volumes, ok[3] = env.Engine.(runtime.VolumeStore)
	if slices.Contains(ok[:], false) {
		return lifecycleEngine{}, fmt.Errorf("%w: runtime connection %q does not manage containers, networks, and volumes",
			ErrRuntimeUnsupported, env.Binding.Connection.Alias)
	}
	var missing []string
	for _, c := range required {
		if !env.Engine.Capabilities().Has(c) {
			missing = append(missing, string(c))
		}
	}
	if len(missing) > 0 {
		return lifecycleEngine{}, fmt.Errorf("%w: runtime connection %q lacks %s", ErrRuntimeUnsupported,
			env.Binding.Connection.Alias, strings.Join(missing, ", "))
	}
	return e, nil
}

// Up brings the environment to its desired state and waits until it is ready
// (ADR 0009 §6).
func (s WorkspaceService) Up(ctx context.Context, req UpRequest) (UpResult, error) {
	progress := req.Progress
	if progress == nil {
		progress = io.Discard
	}
	resolved, err := config.Resolve(req.Selector.options())
	if err != nil {
		return UpResult{}, err
	}
	if _, err := resolveLifecycle(resolved.Config); err != nil {
		return UpResult{}, err
	}
	var result UpResult
	err = s.withEnvironment(ctx, req.Selector, OperationUp, req.Wait, func(ctx context.Context, env LockedEnvironment) error {
		up, err := s.up(ctx, env, req.Pull, progress)
		up.WorkspaceID, up.EnvironmentID, up.Environment, up.Preferences = env.WorkspaceID, env.Binding.ID, env.Name, env.Preferences
		result = up
		return err
	})
	return result, err
}

func (s WorkspaceService) up(ctx context.Context, env LockedEnvironment, pull bool, progress io.Writer) (UpResult, error) {
	cfg := env.Config
	in, err := resolveLifecycle(cfg)
	if err != nil {
		return UpResult{}, err
	}
	// Every capability is checked before the first mutation, the image build
	// included (ADR 0001 §4).
	required := []runtime.Capability{
		runtime.CapImageBuild, runtime.CapImageNoImplicitPull, runtime.CapLabelsContainer, runtime.CapLabelsVolume,
		runtime.CapLabelsNetwork, runtime.CapNetworkPrivate, runtime.CapNetworkNames, runtime.CapVolumePersistent,
		runtime.CapContainerCopy, runtime.CapPortPublish, runtime.CapContainerExitStatus,
	}
	if needsBindMounts(cfg) {
		required = append(required, runtime.CapMountBind)
	}
	if pull {
		required = append(required, runtime.CapImagePull)
	}
	stores, err := lifecycleStores(env, required...)
	if err != nil {
		return UpResult{}, err
	}

	identity := envspec.Identity{
		WorkspaceID: env.WorkspaceID, EnvironmentID: env.Binding.ID, Project: cfg.ProjectName,
		Environment: env.Name, OperationID: env.OperationID, Version: s.version,
	}
	// Cheap refusals first: incompatible data, then a missing server image,
	// before anything is pulled or built.
	if err := guardData(ctx, stores, identity, cfg.Postgres); err != nil {
		return UpResult{}, err
	}
	var result UpResult
	if result.Postgres, err = preparePostgres(ctx, env, stores.images, in.postgresRef, cfg.Postgres, pull, progress); err != nil {
		return result, err
	}
	built, err := buildEnvironment(ctx, env, pull, progress)
	if err != nil {
		return result, err
	}
	result.Odoo = UpImage{Image: built.Image, ImageID: built.ImageID, Identity: built.Identity, Reused: built.Reused}

	desired, err := envspec.New(envspec.Inputs{
		Config: cfg, Identity: identity,
		Images:         envspec.Images{Odoo: built.ImageID, Postgres: result.Postgres.ImageID},
		MasterPassword: in.masterPassword, Configured: in.configured,
	})
	if err != nil {
		return result, err
	}
	result.Warnings = desired.Warnings

	observed, err := observe(ctx, stores, desired)
	if err != nil {
		return result, err
	}
	steps, err := planUp(desired, observed, identity)
	if err != nil {
		return result, err
	}
	stale, err := staleResources(ctx, stores, desired, identity)
	if err != nil {
		return result, err
	}
	for _, r := range stale {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%s %s is no longer part of the environment and was left untouched", r.Kind, r.Name))
	}

	for _, step := range steps {
		if err := applyStep(ctx, stores, step, progress); err != nil {
			return result, partial(err, result.Resources)
		}
		result.Resources = append(result.Resources, step.report())
		if step.container != nil {
			if err := waitReady(ctx, stores.containers, step.container, progress); err != nil {
				return result, partial(err, result.Resources)
			}
		}
	}

	odoo, err := stores.containers.InspectContainer(ctx, desired.Odoo.Spec.Name)
	if err != nil {
		return result, err
	}
	for _, p := range odoo.Ports {
		if p.ContainerPort == envspec.HTTPPort {
			result.URL = "http://" + envspec.HostAddress + ":" + strconv.Itoa(p.HostPort)
		}
	}
	return result, nil
}

func needsBindMounts(cfg config.ResolvedConfig) bool {
	if cfg.Odoo.Source.Mode == config.SourceModeMount {
		return true
	}
	return slices.ContainsFunc(cfg.Addons, func(a config.ResolvedAddon) bool { return a.Enabled })
}

// preparePostgres finds the pinned server image, pulling it only when allowed,
// and checks it claims no ownership and runs the configured major version.
func preparePostgres(ctx context.Context, env LockedEnvironment, store runtime.ImageStore, ref string, major int,
	pull bool, progress io.Writer) (UpImage, error) {
	image := UpImage{Image: ref}
	info, err := store.InspectImage(ctx, ref)
	switch {
	case err == nil:
	case !errors.Is(err, runtime.ErrImageNotFound):
		return image, err
	case !pull:
		return image, fmt.Errorf("%w: PostgreSQL image %s is not present in runtime %q; rerun with --pull to pull it",
			ErrImageNotFound, ref, env.Binding.Connection.Alias)
	default:
		fmt.Fprintf(progress, "pulling PostgreSQL image %s\n", ref)
		if err := store.PullImage(ctx, ref, progress); err != nil {
			return image, err
		}
		if info, err = store.InspectImage(ctx, ref); err != nil {
			return image, err
		}
		image.Pulled = true
	}
	image.ImageID = info.ID
	if err := runtime.CheckImageLabels(info.Labels); err != nil {
		return image, fmt.Errorf("PostgreSQL image %s: %w", ref, err)
	}
	if got := imageEnv(info.Env, "PG_MAJOR"); got != strconv.Itoa(major) {
		return image, fmt.Errorf("%w: PostgreSQL image %s runs major version %q, the environment uses %d",
			ErrInvalidImage, ref, got, major)
	}
	return image, nil
}

func imageEnv(env []string, key string) string {
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	return ""
}

// observed holds the resources found under the desired names; nil is absent.
type observed struct {
	network    *runtime.NetworkInfo
	volumes    map[string]*runtime.VolumeInfo
	containers map[string]*runtime.ContainerInfo
}

func observe(ctx context.Context, e lifecycleEngine, d envspec.Desired) (observed, error) {
	o := observed{volumes: map[string]*runtime.VolumeInfo{}, containers: map[string]*runtime.ContainerInfo{}}
	if n, err := e.networks.InspectNetwork(ctx, d.Network.Name); err == nil {
		o.network = &n
	} else if !errors.Is(err, runtime.ErrNotFound) {
		return o, err
	}
	for _, v := range d.Volumes {
		if info, err := e.volumes.InspectVolume(ctx, v.Name); err == nil {
			o.volumes[v.Name] = &info
		} else if !errors.Is(err, runtime.ErrNotFound) {
			return o, err
		}
	}
	for _, c := range d.Containers() {
		if info, err := e.containers.InspectContainer(ctx, c.Spec.Name); err == nil {
			o.containers[c.Spec.Name] = &info
		} else if !errors.Is(err, runtime.ErrNotFound) {
			return o, err
		}
	}
	return o, nil
}

// step is one planned action of up.
type step struct {
	kind   runtime.ResourceType
	role   runtime.Role
	name   string
	action string // created, kept, started, replaced
	// existing is the ID of the observed container, for start and replace.
	existing  string
	network   *envspec.Network
	volume    *envspec.Volume
	container *envspec.Container
}

func (s step) report() ResourceReport {
	return ResourceReport{Kind: s.kind, Role: s.role, Name: s.name, Action: s.action}
}

const (
	actionCreated  = "created"
	actionKept     = "kept"
	actionStarted  = "started"
	actionReplaced = "replaced"
)

// owned checks that a resource found under an expected name is this
// environment's, with the expected role, and can be mutated.
func owned(kind runtime.ResourceType, name string, labels map[string]string, role runtime.Role, id envspec.Identity) (runtime.Ownership, error) {
	claim := runtime.DecodeOwnership(kind, labels)
	switch claim.State {
	case runtime.ClaimValid:
	case runtime.ClaimUnmanaged:
		return runtime.Ownership{}, fmt.Errorf("%w: %s %s exists but is not managed by odooboat; rename or remove it", ErrConflict, kind, name)
	case runtime.ClaimNewerSchema, runtime.ClaimRequiresUnknown:
		return runtime.Ownership{}, fmt.Errorf("%w: %s %s was written by a newer odooboat and is read-only; upgrade odooboat", ErrConflict, kind, name)
	default:
		return runtime.Ownership{}, fmt.Errorf("%w: %s %s has malformed odooboat labels and is never mutated: %v", ErrConflict, kind, name, claim.Err)
	}
	o := claim.Ownership
	if o.WorkspaceID != id.WorkspaceID || o.EnvironmentID != id.EnvironmentID || o.Role != role {
		return runtime.Ownership{}, fmt.Errorf("%w: %s %s belongs to another workspace or environment (workspace %s, environment %s, role %s)",
			ErrConflict, kind, name, o.WorkspaceID, o.EnvironmentID, o.Role)
	}
	return o, nil
}

// checkVolume verifies an existing volume is this environment's and, for
// postgres-data, that its cluster has the configured major version (ADR 0009
// §6). An absent fact is unknown, never compatible (ADR 0005 A2.4).
func checkVolume(v envspec.Volume, labels map[string]string, id envspec.Identity) error {
	ownership, err := owned(runtime.ResourceVolume, v.Name, labels, v.Role, id)
	if err != nil || v.Role != runtime.RolePostgresData {
		return err
	}
	switch {
	case ownership.PostgresMajor == 0:
		return fmt.Errorf("%w: volume %s records no PostgreSQL major version, so its cluster cannot be checked against PostgreSQL %d",
			ErrIncompatibleData, v.Name, v.PostgresMajor)
	case ownership.PostgresMajor != v.PostgresMajor:
		return fmt.Errorf("%w: volume %s holds a PostgreSQL %d cluster and the environment uses PostgreSQL %d; "+
			"set postgres back to %d, or migrate the data first (not supported yet)",
			ErrIncompatibleData, v.Name, ownership.PostgresMajor, v.PostgresMajor, ownership.PostgresMajor)
	}
	return nil
}

// guardData applies checkVolume to the existing data volumes before anything
// is pulled or built: an incompatible cluster must refuse up first.
func guardData(ctx context.Context, e lifecycleEngine, id envspec.Identity, postgresMajor int) error {
	for _, v := range []envspec.Volume{{Role: runtime.RolePostgresData, PostgresMajor: postgresMajor}, {Role: runtime.RoleOdooData}} {
		name, err := runtime.ResourceName(id.Project, id.Environment, v.Role, id.EnvironmentID)
		if err != nil {
			return err
		}
		v.Name = name
		info, err := e.volumes.InspectVolume(ctx, name)
		if errors.Is(err, runtime.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := checkVolume(v, info.Labels, id); err != nil {
			return err
		}
	}
	return nil
}

// planUp computes the ordered actions bringing the observed resources to the
// desired ones (ADR 0009 §6). It refuses before any change when a name is
// held by a resource this environment does not own, or when existing data
// is incompatible.
func planUp(d envspec.Desired, o observed, id envspec.Identity) ([]step, error) {
	var steps []step
	network := step{kind: runtime.ResourceNetwork, role: runtime.RoleEnvironment, name: d.Network.Name, action: actionKept}
	if o.network == nil {
		network.action, network.network = actionCreated, &d.Network
	} else if _, err := owned(runtime.ResourceNetwork, d.Network.Name, o.network.Labels, runtime.RoleEnvironment, id); err != nil {
		return nil, err
	}
	steps = append(steps, network)

	for i := range d.Volumes {
		v := &d.Volumes[i]
		s := step{kind: runtime.ResourceVolume, role: v.Role, name: v.Name, action: actionKept}
		existing := o.volumes[v.Name]
		if existing == nil {
			s.action, s.volume = actionCreated, v
			steps = append(steps, s)
			continue
		}
		if err := checkVolume(*v, existing.Labels, id); err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}

	for _, c := range d.Containers() {
		c := c
		s := step{kind: runtime.ResourceContainer, role: c.Role, name: c.Spec.Name, container: &c}
		existing := o.containers[c.Spec.Name]
		if existing == nil {
			s.action = actionCreated
			steps = append(steps, s)
			continue
		}
		ownership, err := owned(runtime.ResourceContainer, c.Spec.Name, existing.Labels, c.Role, id)
		if err != nil {
			return nil, err
		}
		s.existing = existing.ID
		switch {
		case ownership.Spec != c.Hash || existing.State == runtime.ContainerOther:
			s.action = actionReplaced
		case existing.State == runtime.ContainerRunning:
			s.action = actionKept
		default:
			s.action = actionStarted
		}
		steps = append(steps, s)
	}
	return steps, nil
}

// staleResources lists owned resources whose names are not desired anymore,
// such as those left by a renamed project. They are reported, never touched
// by up; down removes stale containers.
func staleResources(ctx context.Context, e lifecycleEngine, d envspec.Desired, id envspec.Identity) ([]ResourceReport, error) {
	filter := runtime.LabelFilter{runtime.LabelWorkspace: id.WorkspaceID, runtime.LabelEnvironment: id.EnvironmentID}
	desired := map[string]bool{d.Network.Name: true, d.Postgres.Spec.Name: true, d.Odoo.Spec.Name: true}
	for _, v := range d.Volumes {
		desired[v.Name] = true
	}
	var stale []ResourceReport
	containers, err := e.containers.ListContainers(ctx, filter)
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		if !desired[c.Name] {
			stale = append(stale, ResourceReport{Kind: runtime.ResourceContainer, Name: c.Name})
		}
	}
	volumes, err := e.volumes.ListVolumes(ctx, filter)
	if err != nil {
		return nil, err
	}
	for _, v := range volumes {
		if !desired[v.Name] {
			stale = append(stale, ResourceReport{Kind: runtime.ResourceVolume, Name: v.Name})
		}
	}
	return stale, nil
}

func applyStep(ctx context.Context, e lifecycleEngine, s step, progress io.Writer) error {
	fmt.Fprintf(progress, "%s %s: %s\n", s.kind, s.name, s.action)
	switch {
	case s.network != nil:
		_, err := e.networks.CreateNetwork(ctx, s.network.Name, s.network.Labels)
		return err
	case s.volume != nil:
		return e.volumes.CreateVolume(ctx, s.volume.Name, s.volume.Labels)
	case s.container == nil:
		return nil
	}
	switch s.action {
	case actionKept:
		return nil
	case actionStarted:
		return e.containers.StartContainer(ctx, s.existing)
	case actionReplaced:
		grace := odooStopGrace
		if s.role == runtime.RolePostgres {
			grace = postgresStopGrace
		}
		if err := e.containers.StopContainer(ctx, s.existing, grace); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
		if err := e.containers.RemoveContainer(ctx, s.existing); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return err
		}
	}
	id, err := e.containers.CreateContainer(ctx, s.container.Spec)
	if err != nil {
		return err
	}
	if len(s.container.Files) > 0 {
		archive, err := envspec.Archive(s.container.Files)
		if err != nil {
			return err
		}
		if err := e.containers.CopyToContainer(ctx, id, s.container.Files[0].Dir, bytes.NewReader(archive)); err != nil {
			return err
		}
	}
	return e.containers.StartContainer(ctx, id)
}

// readinessProbe is the command executed in a container to test readiness.
func readinessProbe(role runtime.Role) []string {
	if role == runtime.RolePostgres {
		// TCP, not the socket: during initialization the image runs a
		// temporary server listening on its socket only.
		return []string{"pg_isready", "-q", "-h", "127.0.0.1", "-p", strconv.Itoa(envspec.PostgresPort), "-U", envspec.PostgresUser}
	}
	return []string{"python", "-I", envspec.HelperPath, "ready", "--url",
		"http://127.0.0.1:" + strconv.Itoa(envspec.HTTPPort) + "/web/health"}
}

// waitReady polls the container's readiness probe until it succeeds, the
// container stops, or the deadline passes (ADR 0009 §5).
func waitReady(ctx context.Context, containers runtime.ContainerStore, c *envspec.Container, progress io.Writer) error {
	timeout := odooReadyTimeout
	if c.Role == runtime.RolePostgres {
		timeout = postgresReadyTimeout
	}
	fmt.Fprintf(progress, "waiting for %s to be ready\n", c.Role)
	deadline := time.Now().Add(timeout)
	probe := readinessProbe(c.Role)
	for {
		info, err := containers.InspectContainer(ctx, c.Spec.Name)
		if err != nil {
			return err
		}
		if info.State != runtime.ContainerRunning {
			return notReady(ctx, containers, info.ID, fmt.Sprintf("%s container %s stopped (state %s, exit code %d)",
				c.Role, c.Spec.Name, info.State, info.ExitCode))
		}
		result, err := containers.ExecContainer(ctx, info.ID, runtime.ExecRequest{Cmd: probe})
		if err != nil {
			return err
		}
		if result.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return notReady(ctx, containers, info.ID, fmt.Sprintf("%s container %s was not ready after %s", c.Role, c.Spec.Name, timeout))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyInterval):
		}
	}
}

func notReady(ctx context.Context, containers runtime.ContainerStore, id, reason string) error {
	var logs bytes.Buffer
	if err := containers.ContainerLogs(ctx, id, failureLogLines, &logs); err != nil {
		return fmt.Errorf("%w: %s (logs unavailable: %v)", ErrNotReady, reason, err)
	}
	return fmt.Errorf("%w: %s; last log lines:\n%s", ErrNotReady, reason, strings.TrimRight(logs.String(), "\n"))
}

// partial reports what was completed before a failure: nothing is rolled
// back, and the next up converges from what it observes (ADR 0001 §5).
func partial(err error, done []ResourceReport) error {
	if len(done) == 0 {
		return err
	}
	parts := make([]string, len(done))
	for i, r := range done {
		parts[i] = r.Name + " " + r.Action
	}
	return fmt.Errorf("%w\ncompleted before the failure: %s; run up again once the cause is fixed", err, strings.Join(parts, ", "))
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}
