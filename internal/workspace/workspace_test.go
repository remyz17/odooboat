package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

type memoryStore map[string]State

func (m memoryStore) Load(path string) (State, bool, error) {
	value, ok := m[path]
	return value, ok, nil
}

func testState(t *testing.T, root, stateFile, project string) State {
	t.Helper()
	location := Location{Root: root, StateFile: stateFile, ProjectFile: project}
	value, err := NewState(location)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUUIDv4(t *testing.T) {
	first, err := NewUUID()
	if err != nil || !ValidUUID(first) {
		t.Fatalf("UUID = %q, %v", first, err)
	}
	second, _ := NewUUID()
	if first == second {
		t.Fatal("two generated UUIDs are equal")
	}
}

func TestConnectionEqualityIsStrict(t *testing.T) {
	base := Connection{Alias: "local", Kind: "docker", Context: "default"}
	if !base.Equal(base) {
		t.Fatal("identical snapshots should match")
	}
	variants := []Connection{
		{Alias: "other", Kind: "docker", Context: "default"},
		{Alias: "local", Kind: "podman", Context: "default"},
		{Alias: "local", Kind: "docker", Context: "other"},
		{Alias: "local", Kind: "docker", Socket: "/tmp/docker.sock"},
	}
	for _, variant := range variants {
		if base.Equal(variant) {
			t.Errorf("snapshot %+v unexpectedly matched %+v", base, variant)
		}
	}
}

func TestInspectClassifiesMoveAndCopy(t *testing.T) {
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	oldFile := filepath.Join(oldRoot, ".git", "odooboat", "state.json")
	newFile := filepath.Join(newRoot, ".git", "odooboat", "state.json")
	value := testState(t, oldRoot, oldFile, filepath.Join(oldRoot, "odooboat.yaml"))
	location := Location{Root: newRoot, StateFile: newFile, ProjectFile: filepath.Join(newRoot, "odooboat.yaml"), Git: true}

	moved, err := Inspect(memoryStore{newFile: value}, location)
	if err != nil || moved.Status != StatusMoved {
		t.Fatalf("moved inspection = %+v, %v", moved, err)
	}
	duplicate, err := Inspect(memoryStore{oldFile: value, newFile: value}, location)
	if err != nil || duplicate.Status != StatusDuplicate {
		t.Fatalf("duplicate inspection = %+v, %v", duplicate, err)
	}
}

func TestInspectRejectsSecondProjectAnchor(t *testing.T) {
	root := t.TempDir()
	stateFile := filepath.Join(root, ".git", "odooboat", "state.json")
	value := testState(t, root, stateFile, filepath.Join(root, "one", "odooboat.yaml"))
	_, err := Inspect(memoryStore{stateFile: value}, Location{
		Root: root, StateFile: stateFile, ProjectFile: filepath.Join(root, "two", "odooboat.yaml"), Git: true,
	})
	if !errors.Is(err, ErrProjectConflict) {
		t.Fatalf("error = %v, want ErrProjectConflict", err)
	}
}

func TestValidateRejectsUnsafeState(t *testing.T) {
	root := t.TempDir()
	value := testState(t, root, filepath.Join(root, "state.json"), filepath.Join(root, "odooboat.yaml"))
	value.Environments["dev"] = Binding{ID: value.Workspace.ID, Connection: Connection{Alias: "local", Kind: "docker"}}
	if err := Validate(value); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error = %v, want ErrCorrupt", err)
	}
}
