package app

import (
	"context"
	"fmt"

	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/state"
	"github.com/remyz17/odooboat/internal/workspace"
)

type WorkspaceService struct {
	store state.Store
}

var (
	ErrState              = workspace.ErrState
	ErrDuplicateWorkspace = workspace.ErrDuplicate
)

type WorkspaceStatus string

const (
	WorkspaceStatusUninitialized WorkspaceStatus = "uninitialized"
	WorkspaceStatusCurrent       WorkspaceStatus = "current"
	WorkspaceStatusMoved         WorkspaceStatus = "moved"
	WorkspaceStatusDuplicate     WorkspaceStatus = "duplicate"
)

func NewWorkspaceService(store state.Store) WorkspaceService {
	return WorkspaceService{store: store}
}

type WorkspaceShowRequest struct{ Selector Selector }

type WorkspaceShowResult struct {
	Status        WorkspaceStatus `yaml:"status" json:"status"`
	WorkspaceID   string          `yaml:"workspaceId,omitempty" json:"workspaceId,omitempty"`
	WorkspaceRoot string          `yaml:"workspaceRoot" json:"workspaceRoot"`
	ProjectFile   string          `yaml:"projectFile" json:"projectFile"`
	StateFile     string          `yaml:"stateFile" json:"stateFile"`
	PreviousRoot  string          `yaml:"previousRoot,omitempty" json:"previousRoot,omitempty"`
	OriginalFile  string          `yaml:"originalStateFile,omitempty" json:"originalStateFile,omitempty"`
}

func (s WorkspaceService) Show(_ context.Context, req WorkspaceShowRequest) (WorkspaceShowResult, error) {
	location, err := locateSelector(req.Selector)
	if err != nil {
		return WorkspaceShowResult{}, err
	}
	inspection, err := workspace.Inspect(s.store, location)
	if err != nil {
		return WorkspaceShowResult{}, err
	}
	return workspaceShowResult(inspection), nil
}

type WorkspaceRekeyRequest struct{ Selector Selector }

type WorkspaceRekeyResult struct {
	Status          WorkspaceStatus `yaml:"status" json:"status"`
	PreviousID      string          `yaml:"previousWorkspaceId" json:"previousWorkspaceId"`
	WorkspaceID     string          `yaml:"workspaceId" json:"workspaceId"`
	ClearedBindings int             `yaml:"clearedBindings" json:"clearedBindings"`
	ProjectFile     string          `yaml:"projectFile" json:"projectFile"`
	StateFile       string          `yaml:"stateFile" json:"stateFile"`
}

func (s WorkspaceService) Rekey(_ context.Context, req WorkspaceRekeyRequest) (WorkspaceRekeyResult, error) {
	location, err := locateSelector(req.Selector)
	if err != nil {
		return WorkspaceRekeyResult{}, err
	}
	var previousID string
	var cleared int
	value, err := s.store.Mutate(location, func(current workspace.State, exists bool) (workspace.State, bool, error) {
		if !exists {
			return workspace.State{}, false, workspace.ErrRekeyNotPermitted
		}
		inspection, err := workspace.Inspect(s.store, location)
		if err != nil {
			return workspace.State{}, false, err
		}
		if inspection.Status != workspace.StatusDuplicate {
			return workspace.State{}, false, workspace.ErrRekeyNotPermitted
		}
		previousID = current.Workspace.ID
		cleared = len(current.Environments)
		id, err := workspace.NewUUID()
		if err != nil {
			return workspace.State{}, false, fmt.Errorf("%w: %v", workspace.ErrState, err)
		}
		current.Workspace.ID = id
		workspace.RefreshLocation(&current, location)
		current.Environments = map[string]workspace.Binding{}
		return current, true, nil
	})
	if err != nil {
		return WorkspaceRekeyResult{}, err
	}
	return WorkspaceRekeyResult{
		Status: WorkspaceStatusCurrent, PreviousID: previousID, WorkspaceID: value.Workspace.ID,
		ClearedBindings: cleared, ProjectFile: location.ProjectFile, StateFile: location.StateFile,
	}, nil
}

func locateSelector(selector Selector) (workspace.Location, error) {
	sources, err := config.Discover(config.DiscoverOptions{
		WorkingDir: selector.WorkingDir, ProjectPath: selector.ProjectPath,
		UserConfigPath: selector.UserConfigPath, EnvironmentName: selector.Environment,
		RequireProject: true,
	})
	if err != nil {
		return workspace.Location{}, err
	}
	return workspace.Locate(sources.ProjectPath)
}

func workspaceShowResult(inspection workspace.Inspection) WorkspaceShowResult {
	result := WorkspaceShowResult{
		Status: WorkspaceStatus(inspection.Status), WorkspaceRoot: inspection.Location.Root,
		ProjectFile: inspection.Location.ProjectFile, StateFile: inspection.Location.StateFile,
		PreviousRoot: inspection.PreviousRoot, OriginalFile: inspection.OriginalFile,
	}
	if inspection.State != nil {
		result.WorkspaceID = inspection.State.Workspace.ID
	}
	return result
}
