package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/workspace"
)

// ErrNotRunning: the environment has no running container for the requested
// role. Logs, exec, shell, and psql act on what up created (ADR 0010 §1).
var ErrNotRunning = errors.New("environment is not running")

// observedBinding is the binding of the selected environment, read without
// the lock (ADR 0006 §7).
type observedBinding struct {
	cfg         config.ResolvedConfig
	state       *workspace.State
	binding     workspace.Binding
	workspaceID string
	bound       bool
}

func (s WorkspaceService) observeBinding(sel Selector) (observedBinding, error) {
	resolved, err := config.Resolve(sel.options())
	if err != nil {
		return observedBinding{}, err
	}
	o := observedBinding{cfg: resolved.Config}
	location, err := workspace.Locate(resolved.Sources.ProjectPath)
	if err != nil {
		return o, err
	}
	inspection, err := workspace.Inspect(s.store, location)
	if err != nil {
		return o, err
	}
	if inspection.State == nil {
		return o, nil
	}
	o.state, o.workspaceID = inspection.State, inspection.State.Workspace.ID
	o.binding, o.bound = inspection.State.Environments[o.cfg.EnvironmentName]
	return o, nil
}

// connectBinding connects to the bound runtime and checks the recorded
// engine identity: resources listed on another engine would be misleading
// (ADR 0004 A1).
func (s WorkspaceService) connectBinding(ctx context.Context, o observedBinding) (runtime.Engine, error) {
	b := o.binding
	engine, err := s.runtimes.Connect(ctx, runtime.Locator{Alias: b.Connection.Alias, Kind: b.Connection.Kind,
		Context: b.Connection.Context, Socket: b.Connection.Socket})
	if err != nil {
		return nil, err
	}
	info, err := engine.Info(ctx)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	if recorded := b.Engine; recorded != nil && !recorded.Unavailable && engine.Capabilities().Has(runtime.CapEngineIdentity) &&
		(recorded.Source != info.Identity.Source || recorded.Value != info.Identity.Value) {
		_ = engine.Close()
		return nil, fmt.Errorf("%w: environment %q was bound to engine %s=%s but the connection reaches %s=%s",
			workspace.ErrBindingConflict, o.cfg.EnvironmentName, recorded.Source, recorded.Value, info.Identity.Source, info.Identity.Value)
	}
	return engine, nil
}

// boundEnvironment is a connected, bound environment observed without the
// lock.
type boundEnvironment struct {
	observedBinding
	engine     runtime.Engine
	containers runtime.ContainerStore
}

// connectBound connects to the selected environment's runtime. An unbound
// environment has never been brought up.
func (s WorkspaceService) connectBound(ctx context.Context, sel Selector) (boundEnvironment, func(), error) {
	o, err := s.observeBinding(sel)
	if err != nil {
		return boundEnvironment{}, nil, err
	}
	if !o.bound {
		return boundEnvironment{}, nil, fmt.Errorf("%w: environment %q has never been started; run odooboat up",
			ErrNotRunning, o.cfg.EnvironmentName)
	}
	engine, err := s.connectBinding(ctx, o)
	if err != nil {
		return boundEnvironment{}, nil, err
	}
	containers, ok := engine.(runtime.ContainerStore)
	if !ok || !engine.Capabilities().Has(runtime.CapLabelsFilter) {
		_ = engine.Close()
		return boundEnvironment{}, nil, fmt.Errorf("%w: runtime connection %q does not list containers by labels",
			ErrRuntimeUnsupported, o.binding.Connection.Alias)
	}
	return boundEnvironment{observedBinding: o, engine: engine, containers: containers}, func() { _ = engine.Close() }, nil
}

// require refuses an operation the runtime cannot perform (ADR 0001 §4).
func (b boundEnvironment) require(caps ...runtime.Capability) error {
	var missing []string
	for _, c := range caps {
		if !b.engine.Capabilities().Has(c) {
			missing = append(missing, string(c))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: runtime connection %q lacks %v", ErrRuntimeUnsupported, b.binding.Connection.Alias, missing)
	}
	return nil
}

// container returns this environment's container of role, found by
// ownership labels, never by name (ADR 0005 §5).
func (b boundEnvironment) container(ctx context.Context, role runtime.Role) (runtime.ContainerInfo, error) {
	listed, err := b.containers.ListContainers(ctx, runtime.LabelFilter{
		runtime.LabelWorkspace: b.workspaceID, runtime.LabelEnvironment: b.binding.ID, runtime.LabelRole: string(role)})
	if err != nil {
		return runtime.ContainerInfo{}, err
	}
	for _, c := range listed {
		claim := runtime.DecodeOwnership(runtime.ResourceContainer, c.Labels)
		if claim.State == runtime.ClaimValid && claim.Ownership.WorkspaceID == b.workspaceID &&
			claim.Ownership.EnvironmentID == b.binding.ID && claim.Ownership.Role == role {
			return c, nil
		}
	}
	return runtime.ContainerInfo{}, fmt.Errorf("%w: environment %q has no %s container; run odooboat up",
		ErrNotRunning, b.cfg.EnvironmentName, role)
}

// running returns this environment's container of role, which must run.
func (b boundEnvironment) running(ctx context.Context, role runtime.Role) (runtime.ContainerInfo, error) {
	c, err := b.container(ctx, role)
	if err != nil {
		return c, err
	}
	if c.State != runtime.ContainerRunning {
		state := string(c.State)
		if c.State == runtime.ContainerExited {
			state += " with code " + strconv.Itoa(c.ExitCode)
		}
		return c, fmt.Errorf("%w: the %s container of environment %q has %s; run odooboat up (odooboat logs %s shows why)",
			ErrNotRunning, role, b.cfg.EnvironmentName, state, role)
	}
	return c, nil
}

// url is the environment's Odoo URL read from the published port, or "".
func odooURL(c runtime.ContainerInfo) string {
	for _, p := range c.Ports {
		if p.ContainerPort == envspec.HTTPPort {
			return "http://" + envspec.HostAddress + ":" + strconv.Itoa(p.HostPort)
		}
	}
	return ""
}

type LogsRequest struct {
	Selector Selector
	Role     runtime.Role
	Options  runtime.LogOptions
	Stdout   io.Writer
	Stderr   io.Writer
}

// Logs writes a container's logs. It does not take the lock and reads an
// exited container too (ADR 0010 §2).
func (s WorkspaceService) Logs(ctx context.Context, req LogsRequest) error {
	b, release, err := s.connectBound(ctx, req.Selector)
	if err != nil {
		return err
	}
	defer release()
	var caps []runtime.Capability
	if req.Options.Follow {
		caps = append(caps, runtime.CapLogsFollow)
	}
	if !req.Options.Since.IsZero() {
		caps = append(caps, runtime.CapLogsBoundedSince)
	}
	if err := b.require(caps...); err != nil {
		return err
	}
	c, err := b.container(ctx, req.Role)
	if err != nil {
		return err
	}
	return b.containers.ContainerLogs(ctx, c.ID, req.Options, req.Stdout, req.Stderr)
}
