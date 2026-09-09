package workspace

import (
	"fmt"
	"path/filepath"
	"strings"
)

func Inspect(store Reader, location Location) (Inspection, error) {
	current, exists, err := store.Load(location.StateFile)
	if err != nil {
		return Inspection{}, err
	}
	if !exists {
		return Inspection{Status: StatusUninitialized, Location: location}, nil
	}
	if err := Validate(current); err != nil {
		return Inspection{}, err
	}
	if !sameAnchor(current.Workspace, location) {
		return Inspection{}, fmt.Errorf("%w: state anchors %q, selected project is %q",
			ErrProjectConflict, current.Workspace.ProjectFile, location.ProjectFile)
	}

	inspection := Inspection{Status: StatusCurrent, Location: location, State: &current}
	if current.Workspace.Root == location.Root && current.Workspace.StateFile == location.StateFile {
		return inspection, nil
	}
	inspection.Status = StatusMoved
	inspection.PreviousRoot = current.Workspace.Root
	if current.Workspace.StateFile == location.StateFile {
		return inspection, nil
	}

	original, exists, err := store.Load(current.Workspace.StateFile)
	if err != nil {
		return Inspection{}, fmt.Errorf("%w: inspect former state location: %v", ErrState, err)
	}
	if exists {
		if err := Validate(original); err != nil {
			return Inspection{}, err
		}
		if original.Workspace.ID == current.Workspace.ID {
			inspection.Status = StatusDuplicate
			inspection.OriginalFile = current.Workspace.StateFile
		}
	}
	return inspection, nil
}

func Validate(value State) error {
	if value.Schema != SchemaVersion {
		return fmt.Errorf("%w: schema %d is not supported", ErrCorrupt, value.Schema)
	}
	if !ValidUUID(value.Workspace.ID) {
		return fmt.Errorf("%w: invalid workspace UUID", ErrCorrupt)
	}
	if value.Workspace.Root == "" || !filepath.IsAbs(value.Workspace.Root) ||
		value.Workspace.ProjectFile == "" || !filepath.IsAbs(value.Workspace.ProjectFile) ||
		value.Workspace.StateFile == "" || !filepath.IsAbs(value.Workspace.StateFile) {
		return fmt.Errorf("%w: workspace paths must be absolute", ErrCorrupt)
	}
	if filepath.Clean(value.Workspace.Root) != value.Workspace.Root ||
		filepath.Clean(value.Workspace.ProjectFile) != value.Workspace.ProjectFile ||
		filepath.Clean(value.Workspace.StateFile) != value.Workspace.StateFile {
		return fmt.Errorf("%w: workspace paths must be normalized", ErrCorrupt)
	}
	anchor, err := filepath.Rel(value.Workspace.Root, value.Workspace.ProjectFile)
	if err != nil || anchor == ".." || strings.HasPrefix(anchor, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: project anchor is outside the workspace", ErrCorrupt)
	}
	if value.Environments == nil {
		return fmt.Errorf("%w: environments must be an object", ErrCorrupt)
	}
	for name, binding := range value.Environments {
		if !environmentPattern.MatchString(name) || !ValidUUID(binding.ID) {
			return fmt.Errorf("%w: invalid environment binding %q", ErrCorrupt, name)
		}
		if binding.Connection.Alias == "" || binding.Connection.Kind == "" {
			return fmt.Errorf("%w: incomplete runtime binding for %q", ErrCorrupt, name)
		}
		switch binding.Connection.Kind {
		case "docker", "podman":
			if (binding.Connection.Context == "") == (binding.Connection.Socket == "") {
				return fmt.Errorf("%w: invalid runtime locator for %q", ErrCorrupt, name)
			}
		case "apple":
			if binding.Connection.Context != "" || binding.Connection.Socket != "" {
				return fmt.Errorf("%w: invalid Apple runtime locator for %q", ErrCorrupt, name)
			}
		default:
			return fmt.Errorf("%w: unknown runtime kind for %q", ErrCorrupt, name)
		}
	}
	return nil
}

func NewState(location Location) (State, error) {
	id, err := NewUUID()
	if err != nil {
		return State{}, err
	}
	return State{
		Schema:       SchemaVersion,
		Workspace:    Identity{ID: id, Root: location.Root, ProjectFile: location.ProjectFile, StateFile: location.StateFile},
		Environments: map[string]Binding{},
	}, nil
}

func RefreshLocation(value *State, location Location) {
	value.Workspace.Root = location.Root
	value.Workspace.ProjectFile = location.ProjectFile
	value.Workspace.StateFile = location.StateFile
}

func sameAnchor(identity Identity, location Location) bool {
	old, oldErr := filepath.Rel(identity.Root, identity.ProjectFile)
	current, currentErr := filepath.Rel(location.Root, location.ProjectFile)
	return oldErr == nil && currentErr == nil && filepath.Clean(old) == filepath.Clean(current)
}
