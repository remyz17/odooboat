package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/state"
	"github.com/remyz17/odooboat/internal/workspace"
)

// Operation names a stateful environment workflow in lock holder records.
type Operation string

const OperationVerify Operation = "environment verify"

type WaitPolicy string

const (
	// WaitFailFast returns ErrEnvironmentBusy immediately; used by MCP.
	WaitFailFast WaitPolicy = WaitPolicy(state.WaitFailFast)
	// Wait blocks until the lock is free or the context is cancelled; used by the CLI.
	Wait WaitPolicy = WaitPolicy(state.Wait)
)

type LockHolder = state.Holder

type WaitOptions struct {
	Policy WaitPolicy
	// OnWait reports the holder once while waiting; nil when it is unknown.
	OnWait func(*LockHolder)
}

// LockedEnvironment is what a stateful workflow may rely on while the
// environment lock is held and re-verification has succeeded.
type LockedEnvironment struct {
	Location         workspace.Location
	WorkspaceID      string
	Name             string
	OperationID      string
	Binding          workspace.Binding
	Engine           runtime.Engine
	EngineInfo       runtime.EngineInfo
	BindingCreated   bool
	IdentityRecorded bool
	Preferences      config.ResolvedPreferences
}

// withEnvironment is the single entry point of every stateful environment
// operation (ADR 0006 §4): bind, lock, re-verify, connect, check the engine
// identity, run fn, release.
func (s WorkspaceService) withEnvironment(ctx context.Context, sel Selector, op Operation, wait WaitOptions,
	fn func(context.Context, LockedEnvironment) error) (err error) {
	resolved, err := config.Resolve(sel.options())
	if err != nil {
		return err
	}
	location, err := workspace.Locate(resolved.Sources.ProjectPath)
	if err != nil {
		return err
	}
	name := resolved.Config.EnvironmentName
	desired := connectionSnapshot(resolved.Config.RuntimeConnection)
	bound, binding, created, err := s.ensureBinding(location, name, desired)
	if err != nil {
		return err
	}
	operationID, err := workspace.NewUUID()
	if err != nil {
		return fmt.Errorf("%w: %v", workspace.ErrState, err)
	}

	lock, err := s.locker.LockEnvironment(ctx, location, binding.ID, state.LockRequest{
		Holder: s.holder(op, operationID), Policy: state.WaitPolicy(wait.Policy), OnWait: wait.OnWait,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()

	inspection, err := workspace.Inspect(s.store, location)
	if err != nil {
		return err
	}
	if binding, err = verifyBinding(inspection, name, binding.ID, desired); err != nil {
		return err
	}

	engine, err := s.runtimes.Connect(ctx, runtime.Locator{
		Alias: binding.Connection.Alias, Kind: binding.Connection.Kind,
		Context: binding.Connection.Context, Socket: binding.Connection.Socket,
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close()) }()
	info, err := engine.Info(ctx)
	if err != nil {
		return err
	}
	binding, recorded, err := s.checkEngine(location, name, binding, engine.Capabilities(), info)
	if err != nil {
		return err
	}

	return fn(ctx, LockedEnvironment{
		Location: location, WorkspaceID: bound.Workspace.ID, Name: name, OperationID: operationID,
		Binding: binding, Engine: engine, EngineInfo: info,
		BindingCreated: created, IdentityRecorded: recorded, Preferences: resolved.Config.Preferences,
	})
}

func (s WorkspaceService) holder(op Operation, operationID string) state.Holder {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return state.Holder{
		PID: os.Getpid(), Host: host, Operation: string(op), OperationID: operationID,
		StartedAt: time.Now().UTC().Truncate(time.Second), Version: "odooboat/" + s.version,
	}
}

// verifyBinding repeats the ADR 0004 guards on state reloaded under the
// environment lock. Inspect has already checked the project anchor.
func verifyBinding(inspection workspace.Inspection, name, id string, desired workspace.Connection) (workspace.Binding, error) {
	if inspection.Status == workspace.StatusDuplicate {
		return workspace.Binding{}, workspace.ErrDuplicate
	}
	if inspection.State == nil {
		return workspace.Binding{}, fmt.Errorf("%w: workspace state disappeared before the environment was locked; retry",
			workspace.ErrBindingConflict)
	}
	binding, ok := inspection.State.Environments[name]
	switch {
	case !ok:
		return workspace.Binding{}, fmt.Errorf("%w: environment %q was unbound before it was locked; retry",
			workspace.ErrBindingConflict, name)
	case binding.ID != id:
		return workspace.Binding{}, fmt.Errorf("%w: environment %q changed identity (%s -> %s) before it was locked; retry",
			workspace.ErrBindingConflict, name, id, binding.ID)
	case !binding.Connection.Equal(desired):
		return workspace.Binding{}, fmt.Errorf("%w: environment %q is bound to %q, desired %q",
			workspace.ErrBindingConflict, name, binding.Connection.Alias, desired.Alias)
	}
	return binding, nil
}

// checkEngine compares the connected engine with the recorded identity, or
// records it on first contact (ADR 0004 A1). A mismatch is never accepted.
func (s WorkspaceService) checkEngine(location workspace.Location, name string, binding workspace.Binding,
	capabilities runtime.Capabilities, info runtime.EngineInfo) (workspace.Binding, bool, error) {
	observed := workspace.EngineRecord{Unavailable: true}
	if capabilities.Has(runtime.CapEngineIdentity) {
		observed = workspace.EngineRecord{Source: info.Identity.Source, Value: info.Identity.Value}
	}
	if recorded := binding.Engine; recorded != nil {
		switch {
		case recorded.Unavailable:
			return binding, false, nil
		case observed.Unavailable:
			return workspace.Binding{}, false, fmt.Errorf("%w: environment %q recorded engine %s=%s, which this runtime adapter cannot verify",
				workspace.ErrBindingConflict, name, recorded.Source, recorded.Value)
		case *recorded != observed:
			return workspace.Binding{}, false, fmt.Errorf("%w: environment %q was bound to engine %s=%s but the connection reaches %s=%s; "+
				"the engine may have been reset or the context repointed",
				workspace.ErrBindingConflict, name, recorded.Source, recorded.Value, observed.Source, observed.Value)
		}
		return binding, false, nil
	}

	value, err := s.store.Mutate(location, func(current workspace.State, exists bool) (workspace.State, bool, error) {
		stored, ok := current.Environments[name]
		if !exists || !ok || stored.ID != binding.ID {
			return workspace.State{}, false, fmt.Errorf("%w: environment %q changed while locked; retry",
				workspace.ErrBindingConflict, name)
		}
		stored.Engine = &observed
		current.Environments[name] = stored
		return current, true, nil
	})
	if err != nil {
		return workspace.Binding{}, false, err
	}
	return value.Environments[name], true, nil
}
