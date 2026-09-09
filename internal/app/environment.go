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
	desired := connectionSnapshot(resolved.Config.RuntimeConnection)
	created := false
	value, err := s.store.Mutate(location, func(current workspace.State, exists bool) (workspace.State, bool, error) {
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

		name := resolved.Config.EnvironmentName
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
		return EnvironmentBindResult{}, err
	}
	binding := value.Environments[resolved.Config.EnvironmentName]
	return EnvironmentBindResult{
		WorkspaceID: value.Workspace.ID, EnvironmentID: binding.ID,
		Environment: resolved.Config.EnvironmentName, RuntimeConnection: binding.Connection,
		Created: created, Preferences: resolved.Config.Preferences,
	}, nil
}

func connectionSnapshot(value config.ResolvedRuntimeConnection) workspace.Connection {
	return workspace.Connection{Alias: value.Name, Kind: value.Kind, Context: value.Context, Socket: value.Socket}
}
