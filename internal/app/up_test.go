package app

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
)

// resourceEngine is an in-memory runtime with images, containers, networks,
// and volumes. It records mutating calls in order.
type resourceEngine struct {
	*imageEngine
	containers map[string]*runtime.ContainerInfo // by name
	networks   map[string]runtime.NetworkInfo
	volumes    map[string]runtime.VolumeInfo
	copied     map[string]map[string][]byte // container ID -> file -> content
	calls      []string
	nextID     int
	// probeExit is the exit code of readiness probes; exitOnStart makes a
	// container of that role stop as soon as it starts.
	probeExit   int
	exitOnStart runtime.Role
	logCalls    []logCall
	// sessionCalls records started exec sessions; nextSession is the session
	// the next StartExec returns, one exiting 0 when nil.
	sessionCalls []sessionCall
	nextSession  *fakeSession
	// execCalls records non-interactive execs other than readiness probes;
	// execOutput answers them, readiness probes answering probeExit when nil.
	execCalls  []sessionCall
	execOutput func(runtime.ExecRequest) (string, int)
}

type logCall struct {
	id   string
	opts runtime.LogOptions
}

var lifecycleCapabilities = runtime.Capabilities{
	runtime.CapEngineIdentity, runtime.CapImageBuild, runtime.CapImagePull, runtime.CapImageNoImplicitPull,
	runtime.CapLabelsContainer, runtime.CapLabelsVolume, runtime.CapLabelsNetwork, runtime.CapLabelsFilter,
	runtime.CapNetworkPrivate, runtime.CapNetworkNames, runtime.CapVolumePersistent, runtime.CapMountBind,
	runtime.CapContainerExitStatus, runtime.CapContainerCopy, runtime.CapPortPublish,
}

// accessCapabilities adds what logs and sessions need (ADR 0010 §8).
var accessCapabilities = append(slices.Clone(lifecycleCapabilities), runtime.CapLogsFollow, runtime.CapLogsFollowEnds,
	runtime.CapLogsBoundedSince, runtime.CapLogsSeparateStreams, runtime.CapLogsSourceTimes,
	runtime.CapExecTTY, runtime.CapExecResize, runtime.CapExecSignal, runtime.CapExecCancelConfirmed)

func postgresRef(t *testing.T, major int) string {
	t.Helper()
	images, err := config.EmbeddedPostgresImages()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := images.Lookup(major)
	if !ok {
		t.Fatalf("no postgres %d image", major)
	}
	return ref
}

func newResourceEngine(t *testing.T) *resourceEngine {
	t.Helper()
	images := newImageEngine()
	images.capabilities = accessCapabilities
	images.add(nil, runtime.ImageInfo{ID: localBaseID, Labels: baseLabels("3.12"), Arch: "arm64"})
	images.add([]string{postgresRef(t, 16)}, runtime.ImageInfo{ID: "sha256:" + strings.Repeat("5", 64), Arch: "arm64",
		Env: []string{"PATH=/usr/bin", "PG_MAJOR=16"}})
	return &resourceEngine{imageEngine: images, containers: map[string]*runtime.ContainerInfo{},
		networks: map[string]runtime.NetworkInfo{}, volumes: map[string]runtime.VolumeInfo{}, copied: map[string]map[string][]byte{}}
}

func (e *resourceEngine) record(format string, args ...any) {
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

func matches(labels map[string]string, filter runtime.LabelFilter) bool {
	for k, v := range filter {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (e *resourceEngine) byID(id string) *runtime.ContainerInfo {
	for _, c := range e.containers {
		if c.ID == id || c.Name == id {
			return c
		}
	}
	return nil
}

func (e *resourceEngine) ListContainers(_ context.Context, filter runtime.LabelFilter) ([]runtime.ContainerInfo, error) {
	var out []runtime.ContainerInfo
	for _, c := range e.containers {
		if matches(c.Labels, filter) {
			out = append(out, *c)
		}
	}
	slices.SortFunc(out, func(a, b runtime.ContainerInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (e *resourceEngine) InspectContainer(_ context.Context, nameOrID string) (runtime.ContainerInfo, error) {
	if c := e.byID(nameOrID); c != nil {
		return *c, nil
	}
	return runtime.ContainerInfo{}, fmt.Errorf("%w: container %s", runtime.ErrNotFound, nameOrID)
}

func (e *resourceEngine) CreateContainer(_ context.Context, spec runtime.ContainerSpec) (string, error) {
	if _, ok := e.containers[spec.Name]; ok {
		return "", fmt.Errorf("%w: container %s", runtime.ErrConflict, spec.Name)
	}
	if _, ok := e.images[spec.Image]; !ok {
		return "", fmt.Errorf("%w: %s", runtime.ErrImageNotFound, spec.Image)
	}
	e.nextID++
	id := fmt.Sprintf("c%d", e.nextID)
	var ports []runtime.PortBinding
	for _, p := range spec.Ports {
		p.HostPort = 40000 + e.nextID
		ports = append(ports, p)
	}
	e.containers[spec.Name] = &runtime.ContainerInfo{ID: id, Name: spec.Name, ImageID: spec.Image, Labels: spec.Labels,
		State: runtime.ContainerCreated, Ports: ports}
	e.record("create %s", spec.Name)
	return id, nil
}

func (e *resourceEngine) CopyToContainer(_ context.Context, id, dir string, archive io.Reader) error {
	tr := tar.NewReader(archive)
	files := map[string][]byte{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(tr)
		files[dir+"/"+header.Name] = data
	}
	e.copied[id] = files
	e.record("copy %s", e.byID(id).Name)
	return nil
}

func (e *resourceEngine) StartContainer(_ context.Context, id string) error {
	c := e.byID(id)
	if c == nil {
		return fmt.Errorf("%w: %s", runtime.ErrNotFound, id)
	}
	c.State = runtime.ContainerRunning
	if role := runtime.DecodeOwnership(runtime.ResourceContainer, c.Labels).Ownership.Role; role != "" && role == e.exitOnStart {
		c.State, c.ExitCode = runtime.ContainerExited, 1
	}
	e.record("start %s", c.Name)
	return nil
}

func (e *resourceEngine) StopContainer(_ context.Context, id string, _ time.Duration) error {
	c := e.byID(id)
	if c == nil {
		return fmt.Errorf("%w: %s", runtime.ErrNotFound, id)
	}
	c.State = runtime.ContainerExited
	e.record("stop %s", c.Name)
	return nil
}

func (e *resourceEngine) RemoveContainer(_ context.Context, id string) error {
	c := e.byID(id)
	if c == nil {
		return fmt.Errorf("%w: %s", runtime.ErrNotFound, id)
	}
	delete(e.containers, c.Name)
	e.record("remove %s", c.Name)
	return nil
}

func (e *resourceEngine) ExecContainer(_ context.Context, id string, req runtime.ExecRequest) (runtime.ExecResult, error) {
	if c := e.byID(id); c == nil || c.State != runtime.ContainerRunning {
		return runtime.ExecResult{}, fmt.Errorf("container %s is not running", id)
	}
	if e.execOutput == nil {
		return runtime.ExecResult{ExitCode: e.probeExit}, nil
	}
	out, code := e.execOutput(req)
	if !slices.Contains(req.Cmd, "pg_isready") && !slices.Contains(req.Cmd, "ready") {
		e.execCalls = append(e.execCalls, sessionCall{containerID: id, req: req})
	}
	w := req.Stdout
	if code != 0 {
		w = req.Stderr
	}
	if w != nil {
		_, _ = io.WriteString(w, out)
	}
	return runtime.ExecResult{ExitCode: code}, nil
}

func (e *resourceEngine) ContainerLogs(_ context.Context, id string, opts runtime.LogOptions, _, stderr io.Writer) error {
	if e.byID(id) == nil {
		return fmt.Errorf("%w: %s", runtime.ErrNotFound, id)
	}
	e.logCalls = append(e.logCalls, logCall{id: id, opts: opts})
	_, err := io.WriteString(stderr, "fatal: database is gone\n")
	return err
}

func (e *resourceEngine) ListNetworks(_ context.Context, filter runtime.LabelFilter) ([]runtime.NetworkInfo, error) {
	var out []runtime.NetworkInfo
	for _, n := range e.networks {
		if matches(n.Labels, filter) {
			out = append(out, n)
		}
	}
	return out, nil
}

func (e *resourceEngine) InspectNetwork(_ context.Context, name string) (runtime.NetworkInfo, error) {
	if n, ok := e.networks[name]; ok {
		return n, nil
	}
	return runtime.NetworkInfo{}, fmt.Errorf("%w: network %s", runtime.ErrNotFound, name)
}

func (e *resourceEngine) CreateNetwork(_ context.Context, name string, labels map[string]string) (string, error) {
	e.networks[name] = runtime.NetworkInfo{ID: "n-" + name, Name: name, Labels: labels}
	e.record("create network %s", name)
	return "n-" + name, nil
}

func (e *resourceEngine) RemoveNetwork(_ context.Context, id string) error {
	delete(e.networks, strings.TrimPrefix(id, "n-"))
	e.record("remove network %s", strings.TrimPrefix(id, "n-"))
	return nil
}

func (e *resourceEngine) ListVolumes(_ context.Context, filter runtime.LabelFilter) ([]runtime.VolumeInfo, error) {
	var out []runtime.VolumeInfo
	for _, v := range e.volumes {
		if matches(v.Labels, filter) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (e *resourceEngine) InspectVolume(_ context.Context, name string) (runtime.VolumeInfo, error) {
	if v, ok := e.volumes[name]; ok {
		return v, nil
	}
	return runtime.VolumeInfo{}, fmt.Errorf("%w: volume %s", runtime.ErrNotFound, name)
}

func (e *resourceEngine) CreateVolume(_ context.Context, name string, labels map[string]string) error {
	if _, ok := e.volumes[name]; ok {
		return fmt.Errorf("%w: volume %s", runtime.ErrConflict, name)
	}
	e.volumes[name] = runtime.VolumeInfo{Name: name, Labels: labels}
	e.record("create volume %s", name)
	return nil
}

func (e *resourceEngine) RemoveVolume(_ context.Context, name string) error {
	delete(e.volumes, name)
	e.record("remove volume %s", name)
	return nil
}

type lifecycleFixture struct {
	t        *testing.T
	engine   *resourceEngine
	service  WorkspaceService
	selector Selector
	root     string
}

func newLifecycle(t *testing.T, engine *resourceEngine, extra string) *lifecycleFixture {
	t.Helper()
	postgresReadyTimeout, odooReadyTimeout, readyInterval = 50*time.Millisecond, 50*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		postgresReadyTimeout, odooReadyTimeout, readyInterval = 60*time.Second, 120*time.Second, time.Second
	})
	service, selector, root := buildFixture(t, engine, communityLines(t, localBaseID, "community-18.0")+extra)
	return &lifecycleFixture{t: t, engine: engine, service: service, selector: selector, root: root}
}

func (f *lifecycleFixture) up(pull bool) (UpResult, error) {
	return f.service.Up(context.Background(), UpRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}, Pull: pull})
}

func (f *lifecycleFixture) mustUp() UpResult {
	f.t.Helper()
	result, err := f.up(false)
	if err != nil {
		f.t.Fatalf("up: %v", err)
	}
	return result
}

func (f *lifecycleFixture) name(result UpResult, role runtime.Role) string {
	f.t.Helper()
	name, err := runtime.ResourceName("demo", "default", role, result.EnvironmentID)
	if err != nil {
		f.t.Fatal(err)
	}
	return name
}

func actions(result UpResult) map[runtime.Role]string {
	out := map[runtime.Role]string{}
	for _, r := range result.Resources {
		out[r.Role] = r.Action
	}
	return out
}

func (f *lifecycleFixture) rewrite(extra string) {
	f.t.Helper()
	content := "schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n" +
		communityLines(f.t, localBaseID, "community-18.0") + extra
	if err := os.WriteFile(filepath.Join(f.root, config.ProjectFileName), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func TestUpCreatesWaitsThenKeeps(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	for role, action := range actions(first) {
		if action != actionCreated {
			t.Errorf("first up: %s %s", role, action)
		}
	}
	postgres, odoo := f.name(first, runtime.RolePostgres), f.name(first, runtime.RoleOdoo)
	want := []string{
		"create network " + f.name(first, runtime.RoleEnvironment),
		"create volume " + f.name(first, runtime.RolePostgresData),
		"create volume " + f.name(first, runtime.RoleOdooData),
		"create " + postgres, "start " + postgres,
		"create " + odoo, "copy " + odoo, "start " + odoo,
	}
	if !slices.Equal(f.engine.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.engine.calls, "\n"), strings.Join(want, "\n"))
	}
	conf := f.engine.copied[f.engine.containers[odoo].ID][envspec.RunDir+"/"+envspec.ConfFile]
	if !strings.Contains(string(conf), "db_host = "+postgres+"\n") {
		t.Fatalf("odoo.conf:\n%s", conf)
	}
	if first.URL != fmt.Sprintf("http://127.0.0.1:%d", f.engine.containers[odoo].Ports[0].HostPort) {
		t.Fatalf("url = %q", first.URL)
	}
	if len(first.Warnings) != 1 || !strings.Contains(first.Warnings[0], "masterPassword") {
		t.Fatalf("warnings = %q", first.Warnings)
	}

	f.engine.calls = nil
	second := f.mustUp()
	for role, action := range actions(second) {
		if action != actionKept {
			t.Errorf("second up: %s %s", role, action)
		}
	}
	if len(f.engine.calls) != 0 || f.engine.builds != 1 {
		t.Fatalf("second up mutated: %q (builds %d)", f.engine.calls, f.engine.builds)
	}

	// A stopped container is started, not recreated.
	f.engine.containers[odoo].State = runtime.ContainerExited
	if got := actions(f.mustUp())[runtime.RoleOdoo]; got != actionStarted {
		t.Fatalf("stopped odoo: %s", got)
	}
}

func TestUpReplacesOnlyWhatChanged(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	f.rewrite("    options:\n      limit_time_real: \"240\"\n")
	f.engine.calls = nil
	second := f.mustUp()
	if got := actions(second); got[runtime.RoleOdoo] != actionReplaced || got[runtime.RolePostgres] != actionKept {
		t.Fatalf("actions = %v", got)
	}
	odoo := f.name(first, runtime.RoleOdoo)
	if !slices.Equal(f.engine.calls[:2], []string{"stop " + odoo, "remove " + odoo}) {
		t.Fatalf("calls = %q", f.engine.calls)
	}
}

func TestUpRefusesBeforeAnyChange(t *testing.T) {
	ownedBy := func(result UpResult, role runtime.Role, mutate func(*runtime.Ownership)) map[string]string {
		o := runtime.Ownership{Scope: runtime.ScopeEnvironment, WorkspaceID: result.WorkspaceID, EnvironmentID: result.EnvironmentID,
			Role: role, OperationID: "33333333-3333-4333-8333-333333333333", DisplayProject: "demo", DisplayEnvironment: "default",
			CreatedBy: "odooboat/test"}
		if role == runtime.RolePostgresData {
			o.PostgresMajor = 16
		}
		mutate(&o)
		labels, err := o.Labels(runtime.ResourceVolume)
		if err != nil {
			t.Fatal(err)
		}
		return labels
	}
	cases := map[string]struct {
		prepare func(f *lifecycleFixture, result UpResult)
		want    error
		message string
	}{
		"other postgres major": {func(f *lifecycleFixture, r UpResult) {
			name := f.name(r, runtime.RolePostgresData)
			f.engine.volumes[name] = runtime.VolumeInfo{Name: name, Labels: ownedBy(r, runtime.RolePostgresData, func(o *runtime.Ownership) { o.PostgresMajor = 15 })}
		}, ErrIncompatibleData, "PostgreSQL 15 cluster"},
		// Ownership decoding requires the fact on postgres-data, so a volume
		// without it is a malformed claim: refused, never reused.
		"no postgres major": {func(f *lifecycleFixture, r UpResult) {
			name := f.name(r, runtime.RolePostgresData)
			labels := ownedBy(r, runtime.RolePostgresData, func(*runtime.Ownership) {})
			delete(labels, runtime.LabelPostgresMajor)
			f.engine.volumes[name] = runtime.VolumeInfo{Name: name, Labels: labels}
		}, ErrConflict, "malformed"},
		"unmanaged container": {func(f *lifecycleFixture, r UpResult) {
			name := f.name(r, runtime.RoleOdoo)
			f.engine.containers[name] = &runtime.ContainerInfo{ID: "foreign", Name: name, State: runtime.ContainerRunning}
		}, ErrConflict, "not managed by odooboat"},
		"other workspace's volume": {func(f *lifecycleFixture, r UpResult) {
			name := f.name(r, runtime.RoleOdooData)
			f.engine.volumes[name] = runtime.VolumeInfo{Name: name, Labels: ownedBy(r, runtime.RoleOdooData, func(o *runtime.Ownership) {
				o.WorkspaceID = "99999999-9999-4999-8999-999999999999"
			})}
		}, ErrConflict, "another workspace"},
		"newer schema network": {func(f *lifecycleFixture, r UpResult) {
			name := f.name(r, runtime.RoleEnvironment)
			f.engine.networks[name] = runtime.NetworkInfo{ID: "n-" + name, Name: name, Labels: map[string]string{runtime.LabelSchema: "2"}}
		}, ErrConflict, "newer odooboat"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLifecycle(t, newResourceEngine(t), "")
			first := f.mustUp()
			if _, err := f.service.Down(context.Background(), DownRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}}); err != nil {
				t.Fatal(err)
			}
			tc.prepare(f, first)
			f.engine.calls, f.engine.builds = nil, 0
			_, err := f.up(false)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.want, tc.message)
			}
			if len(f.engine.calls) != 0 {
				t.Fatalf("a refused up mutated: %q", f.engine.calls)
			}
		})
	}
}

func TestUpPostgresImage(t *testing.T) {
	engine := newResourceEngine(t)
	ref := postgresRef(t, 16)
	pulled := engine.images[ref]
	delete(engine.images, ref)
	engine.pullable[ref] = pulled
	f := newLifecycle(t, engine, "")
	if _, err := f.up(false); !errors.Is(err, ErrImageNotFound) || Hint(err) != "rerun with --pull" || engine.builds != 0 {
		t.Fatalf("without --pull: %v (builds %d)", err, engine.builds)
	}
	result, err := f.up(true)
	if err != nil || !result.Postgres.Pulled || result.Postgres.ImageID != pulled.ID {
		t.Fatalf("with --pull: %+v, %v", result.Postgres, err)
	}

	wrong := newResourceEngine(t)
	info := wrong.images[ref]
	info.Env = []string{"PG_MAJOR=15"}
	wrong.images[ref] = info
	if _, err := newLifecycle(t, wrong, "").up(false); !errors.Is(err, ErrInvalidImage) || wrong.builds != 0 {
		t.Fatalf("other major: %v (builds %d)", err, wrong.builds)
	}
}

func TestUpReportsNotReadyWithLogs(t *testing.T) {
	engine := newResourceEngine(t)
	engine.exitOnStart = runtime.RoleOdoo
	f := newLifecycle(t, engine, "")
	_, err := f.up(false)
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "database is gone") ||
		!strings.Contains(err.Error(), "completed before the failure") {
		t.Fatalf("stopped odoo: %v", err)
	}

	engine = newResourceEngine(t)
	engine.probeExit = 1
	if _, err := newLifecycle(t, engine, "").up(false); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "not ready after") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestUpRequiresCapabilitiesBeforeBuilding(t *testing.T) {
	engine := newResourceEngine(t)
	engine.capabilities = slices.DeleteFunc(slices.Clone(lifecycleCapabilities), func(c runtime.Capability) bool { return c == runtime.CapContainerCopy })
	_, err := newLifecycle(t, engine, "").up(false)
	if !errors.Is(err, ErrRuntimeUnsupported) || !strings.Contains(err.Error(), "container.copy") || engine.builds != 0 {
		t.Fatalf("err = %v (builds %d)", err, engine.builds)
	}
}

func TestDownKeepsVolumesAndStatusObserves(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	status, err := f.service.Status(context.Background(), StatusRequest{Selector: f.selector})
	if err != nil || status.Bound {
		t.Fatalf("status before up = %+v, %v", status, err)
	}
	first := f.mustUp()
	status, err = f.service.Status(context.Background(), StatusRequest{Selector: f.selector})
	if err != nil || !status.Running || status.URL != first.URL || len(status.Resources) != 5 || len(status.Orphans) != 0 {
		t.Fatalf("status after up = %+v, %v", status, err)
	}

	// A resource of this workspace whose environment is unknown is an orphan.
	orphan := runtime.Ownership{Scope: runtime.ScopeEnvironment, WorkspaceID: first.WorkspaceID,
		EnvironmentID: "88888888-8888-4888-8888-888888888888", Role: runtime.RoleOdooData,
		OperationID: "33333333-3333-4333-8333-333333333333", DisplayProject: "demo", DisplayEnvironment: "gone", CreatedBy: "odooboat/test"}
	labels, _ := orphan.Labels(runtime.ResourceVolume)
	f.engine.volumes["orphan"] = runtime.VolumeInfo{Name: "orphan", Labels: labels}
	if status, _ = f.service.Status(context.Background(), StatusRequest{Selector: f.selector}); len(status.Orphans) != 1 {
		t.Fatalf("orphans = %+v", status.Orphans)
	}

	f.engine.calls = nil
	down, err := f.service.Down(context.Background(), DownRequest{Selector: f.selector, Wait: WaitOptions{Policy: WaitFailFast}})
	if err != nil {
		t.Fatal(err)
	}
	odoo, postgres := f.name(first, runtime.RoleOdoo), f.name(first, runtime.RolePostgres)
	want := []string{"stop " + odoo, "remove " + odoo, "stop " + postgres, "remove " + postgres,
		"remove network " + f.name(first, runtime.RoleEnvironment)}
	if !slices.Equal(f.engine.calls, want) {
		t.Fatalf("down calls = %q", f.engine.calls)
	}
	if len(down.Kept) != 2 || len(f.engine.volumes) != 3 {
		t.Fatalf("kept = %+v, volumes %d", down.Kept, len(f.engine.volumes))
	}
	if status, _ = f.service.Status(context.Background(), StatusRequest{Selector: f.selector}); status.Running || status.URL != "" {
		t.Fatalf("status after down = %+v", status)
	}

	// Data survives: the next up keeps the volumes.
	if got := actions(f.mustUp()); got[runtime.RolePostgresData] != actionKept || got[runtime.RoleOdoo] != actionCreated {
		t.Fatalf("up after down = %v", got)
	}
}

func TestPlanUpTable(t *testing.T) {
	id := envspec.Identity{WorkspaceID: "11111111-1111-4111-8111-111111111111", EnvironmentID: "22222222-2222-4222-8222-222222222222",
		Project: "demo", Environment: "default", OperationID: "33333333-3333-4333-8333-333333333333", Version: "test"}
	cfg := config.ResolvedConfig{Postgres: 16, Odoo: config.ResolvedOdoo{Source: config.ResolvedOdooSource{
		Mode: config.SourceModeMount, Community: &config.ResolvedSourceLocation{Path: "/src"}}}}
	desired, err := envspec.New(envspec.Inputs{Config: cfg, Identity: id, Images: envspec.Images{Odoo: "sha256:o", Postgres: "sha256:p"}})
	if err != nil {
		t.Fatal(err)
	}
	empty := observed{volumes: map[string]*runtime.VolumeInfo{}, containers: map[string]*runtime.ContainerInfo{}}
	steps, err := planUp(desired, empty, id)
	if err != nil || len(steps) != 5 {
		t.Fatalf("empty: %d steps, %v", len(steps), err)
	}
	odoo := desired.Odoo
	for state, want := range map[runtime.ContainerState]string{
		runtime.ContainerRunning: actionKept, runtime.ContainerExited: actionStarted,
		runtime.ContainerCreated: actionStarted, runtime.ContainerOther: actionReplaced,
	} {
		o := empty
		o.containers = map[string]*runtime.ContainerInfo{odoo.Spec.Name: {ID: "x", Name: odoo.Spec.Name, Labels: odoo.Spec.Labels, State: state}}
		steps, err := planUp(desired, o, id)
		if err != nil || steps[4].action != want {
			t.Errorf("%s: %v, %v", state, steps[4].action, err)
		}
	}
	stale := map[string]string{}
	for k, v := range odoo.Spec.Labels {
		stale[k] = v
	}
	stale[runtime.LabelSpec] = "sha256:" + strings.Repeat("f", 64)
	o := empty
	o.containers = map[string]*runtime.ContainerInfo{odoo.Spec.Name: {ID: "x", Name: odoo.Spec.Name, Labels: stale, State: runtime.ContainerRunning}}
	if steps, err := planUp(desired, o, id); err != nil || steps[4].action != actionReplaced {
		t.Errorf("changed spec: %+v, %v", steps, err)
	}
}

// Incompatible data refuses up before anything is pulled or built.
func TestUpGuardsDataBeforePulling(t *testing.T) {
	engine := newResourceEngine(t)
	engine.pullable[postgresRef(t, 15)] = runtime.ImageInfo{ID: "sha256:" + strings.Repeat("6", 64), Env: []string{"PG_MAJOR=15"}}
	f := newLifecycle(t, engine, "")
	f.mustUp()
	f.rewrite("")
	content, err := os.ReadFile(filepath.Join(f.root, config.ProjectFileName))
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), "  odooVersion: \"18.0\"\n", "  odooVersion: \"18.0\"\n  postgres: 15\n", 1)
	if err := os.WriteFile(filepath.Join(f.root, config.ProjectFileName), []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	engine.calls, engine.builds = nil, 0
	_, err = f.up(true)
	if !errors.Is(err, ErrIncompatibleData) || engine.pulls != 0 || engine.builds != 0 || len(engine.calls) != 0 {
		t.Fatalf("err = %v, pulls %d, builds %d, calls %q", err, engine.pulls, engine.builds, engine.calls)
	}
}
