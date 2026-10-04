package app

import (
	"context"
	"fmt"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/workspace"
)

type EnvironmentShowRequest struct{ Selector Selector }

type EnvironmentShowResult struct {
	WorkspaceID       string                     `yaml:"workspaceId,omitempty" json:"workspaceId,omitempty"`
	WorkspaceStatus   WorkspaceStatus            `yaml:"workspaceStatus" json:"workspaceStatus"`
	EnvironmentID     string                     `yaml:"environmentId,omitempty" json:"environmentId,omitempty"`
	Environment       string                     `yaml:"environment" json:"environment"`
	Bound             bool                       `yaml:"bound" json:"bound"`
	DesiredConnection workspace.Connection       `yaml:"desiredConnection" json:"desiredConnection"`
	BoundConnection   *workspace.Connection      `yaml:"boundConnection,omitempty" json:"boundConnection,omitempty"`
	Matches           *bool                      `yaml:"matches,omitempty" json:"matches,omitempty"`
	Engine            *workspace.EngineRecord    `yaml:"engine,omitempty" json:"engine,omitempty"`
	Preferences       config.ResolvedPreferences `yaml:"-" json:"-"`
}

func (s WorkspaceService) ShowEnvironment(_ context.Context, req EnvironmentShowRequest) (EnvironmentShowResult, error) {
	resolved, err := config.Resolve(req.Selector.options())
	if err != nil {
		return EnvironmentShowResult{}, err
	}
	location, err := workspace.Locate(resolved.Sources.ProjectPath)
	if err != nil {
		return EnvironmentShowResult{}, err
	}
	inspection, err := workspace.Inspect(s.store, location)
	if err != nil {
		return EnvironmentShowResult{}, err
	}
	desired := connectionSnapshot(resolved.Config.RuntimeConnection)
	result := EnvironmentShowResult{
		WorkspaceStatus: WorkspaceStatus(inspection.Status), Environment: resolved.Config.EnvironmentName,
		DesiredConnection: desired, Preferences: resolved.Config.Preferences,
	}
	if inspection.State == nil {
		return result, nil
	}
	result.WorkspaceID = inspection.State.Workspace.ID
	if binding, ok := inspection.State.Environments[result.Environment]; ok {
		matches := binding.Connection.Equal(desired)
		bound := binding.Connection
		result.Bound = true
		result.EnvironmentID = binding.ID
		result.BoundConnection = &bound
		result.Matches = &matches
		result.Engine = binding.Engine
	}
	return result, nil
}

type EnvironmentBindRequest struct{ Selector Selector }

type EnvironmentBindResult struct {
	WorkspaceID       string                     `yaml:"workspaceId" json:"workspaceId"`
	EnvironmentID     string                     `yaml:"environmentId" json:"environmentId"`
	Environment       string                     `yaml:"environment" json:"environment"`
	RuntimeConnection workspace.Connection       `yaml:"runtimeConnection" json:"runtimeConnection"`
	Created           bool                       `yaml:"created" json:"created"`
	Preferences       config.ResolvedPreferences `yaml:"-" json:"-"`
}

func (s WorkspaceService) BindEnvironment(_ context.Context, req EnvironmentBindRequest) (EnvironmentBindResult, error) {
	resolved, err := config.Resolve(req.Selector.options())
	if err != nil {
		return EnvironmentBindResult{}, err
	}
	location, err := workspace.Locate(resolved.Sources.ProjectPath)
	if err != nil {
		return EnvironmentBindResult{}, err
	}
	name := resolved.Config.EnvironmentName
	value, binding, created, err := s.ensureBinding(location, name, connectionSnapshot(resolved.Config.RuntimeConnection))
	if err != nil {
		return EnvironmentBindResult{}, err
	}
	return EnvironmentBindResult{
		WorkspaceID: value.Workspace.ID, EnvironmentID: binding.ID,
		Environment: name, RuntimeConnection: binding.Connection,
		Created: created, Preferences: resolved.Config.Preferences,
	}, nil
}

// ensureBinding creates or verifies the environment binding under the short
// state-file lock. It never takes the environment lock (ADR 0006 §3).
func (s WorkspaceService) ensureBinding(location workspace.Location, name string, desired workspace.Connection) (workspace.State, workspace.Binding, bool, error) {
	created := false
	value, err := s.store.Mutate(location, func(current workspace.State, exists bool) (workspace.State, bool, error) {
		var err error
		changed := false
		if !exists {
			current, err = workspace.NewState(location)
			if err != nil {
				return workspace.State{}, false, fmt.Errorf("%w: %v", workspace.ErrState, err)
			}
			changed = true
		} else {
			inspection, err := workspace.Inspect(s.store, location)
			if err != nil {
				return workspace.State{}, false, err
			}
			switch inspection.Status {
			case workspace.StatusDuplicate:
				return workspace.State{}, false, workspace.ErrDuplicate
			case workspace.StatusMoved:
				workspace.RefreshLocation(&current, location)
				changed = true
			}
		}

		if binding, ok := current.Environments[name]; ok {
			if !binding.Connection.Equal(desired) {
				return workspace.State{}, false, fmt.Errorf("%w: environment %q is bound to %q, desired %q",
					workspace.ErrBindingConflict, name, binding.Connection.Alias, desired.Alias)
			}
			return current, changed, nil
		}
		id, err := workspace.NewUUID()
		if err != nil {
			return workspace.State{}, false, fmt.Errorf("%w: %v", workspace.ErrState, err)
		}
		current.Environments[name] = workspace.Binding{ID: id, Connection: desired}
		created = true
		return current, true, nil
	})
	if err != nil {
		return workspace.State{}, workspace.Binding{}, false, err
	}
	return value, value.Environments[name], created, nil
}

func connectionSnapshot(value config.ResolvedRuntimeConnection) workspace.Connection {
	return workspace.Connection{Alias: value.Name, Kind: value.Kind, Context: value.Context, Socket: value.Socket}
}

type EnvironmentVerifyRequest struct {
	Selector Selector
	Wait     WaitOptions
}

type EngineReport struct {
	Source      string `yaml:"source,omitempty" json:"source,omitempty"`
	Value       string `yaml:"value,omitempty" json:"value,omitempty"`
	Unavailable bool   `yaml:"unavailable,omitempty" json:"unavailable,omitempty"`
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	OS          string `yaml:"os,omitempty" json:"os,omitempty"`
	Arch        string `yaml:"arch,omitempty" json:"arch,omitempty"`
}

type EnvironmentVerifyResult struct {
	WorkspaceID       string                     `yaml:"workspaceId" json:"workspaceId"`
	EnvironmentID     string                     `yaml:"environmentId" json:"environmentId"`
	Environment       string                     `yaml:"environment" json:"environment"`
	RuntimeConnection workspace.Connection       `yaml:"runtimeConnection" json:"runtimeConnection"`
	Engine            EngineReport               `yaml:"engine" json:"engine"`
	BindingCreated    bool                       `yaml:"bindingCreated" json:"bindingCreated"`
	IdentityRecorded  bool                       `yaml:"identityRecorded" json:"identityRecorded"`
	Preferences       config.ResolvedPreferences `yaml:"-" json:"-"`
}

// VerifyEnvironment binds the environment if needed, then under the
// environment lock reaches its runtime and records or compares the engine
// identity.
func (s WorkspaceService) VerifyEnvironment(ctx context.Context, req EnvironmentVerifyRequest) (EnvironmentVerifyResult, error) {
	var result EnvironmentVerifyResult
	err := s.withEnvironment(ctx, req.Selector, OperationVerify, req.Wait, func(_ context.Context, env LockedEnvironment) error {
		engine := EngineReport{Version: env.EngineInfo.Version, OS: env.EngineInfo.OS, Arch: env.EngineInfo.Arch}
		if record := env.Binding.Engine; record != nil {
			engine.Source, engine.Value, engine.Unavailable = record.Source, record.Value, record.Unavailable
		}
		result = EnvironmentVerifyResult{
			WorkspaceID: env.WorkspaceID, EnvironmentID: env.Binding.ID, Environment: env.Name,
			RuntimeConnection: env.Binding.Connection, Engine: engine,
			BindingCreated: env.BindingCreated, IdentityRecorded: env.IdentityRecorded, Preferences: env.Preferences,
		}
		return nil
	})
	return result, err
}
