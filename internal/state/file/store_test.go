package file

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/remyz17/odooboat/internal/workspace"
)

func TestMutationWritesRestrictiveStateAndIgnore(t *testing.T) {
	root := t.TempDir()
	location := workspace.Location{
		Root: root, ProjectFile: filepath.Join(root, "odooboat.yaml"),
		StateFile: filepath.Join(root, ".odooboat", "state.json"),
	}
	value, err := New().Mutate(location, func(_ workspace.State, exists bool) (workspace.State, bool, error) {
		if exists {
			t.Fatal("state unexpectedly exists")
		}
		value, err := workspace.NewState(location)
		return value, true, err
	})
	if err != nil || !workspace.ValidUUID(value.Workspace.ID) {
		t.Fatalf("mutate = %+v, %v", value, err)
	}
	info, err := os.Stat(location.StateFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %v, %v", info.Mode().Perm(), err)
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".odooboat", ".gitignore"))
	if err != nil || string(ignore) != "*\n!.gitignore\n" {
		t.Fatalf("gitignore = %q, %v", ignore, err)
	}
}

func TestConcurrentFirstMutationAcrossProcesses(t *testing.T) {
	if os.Getenv("ODOOBOAT_LOCK_HELPER") == "1" {
		concurrentMutationHelper(t)
		return
	}
	root := t.TempDir()
	const processes = 8
	errCh := make(chan error, processes)
	var wait sync.WaitGroup
	for range processes {
		wait.Add(1)
		go func() {
			defer wait.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestConcurrentFirstMutationAcrossProcesses", "-test.count=1")
			cmd.Env = append(os.Environ(), "ODOOBOAT_LOCK_HELPER=1", "ODOOBOAT_LOCK_ROOT="+root)
			if output, err := cmd.CombinedOutput(); err != nil {
				errCh <- errors.New(err.Error() + ": " + string(output))
			}
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	location := testLocation(root)
	value, exists, err := New().Load(location.StateFile)
	if err != nil || !exists {
		t.Fatalf("load final state: exists=%v err=%v", exists, err)
	}
	if !workspace.ValidUUID(value.Workspace.ID) || len(value.Environments) != 1 || !workspace.ValidUUID(value.Environments["default"].ID) {
		t.Fatalf("final state = %+v", value)
	}
}

func concurrentMutationHelper(t *testing.T) {
	root := os.Getenv("ODOOBOAT_LOCK_ROOT")
	name := "default"
	location := testLocation(root)
	_, err := New().Mutate(location, func(value workspace.State, exists bool) (workspace.State, bool, error) {
		if !exists {
			var err error
			value, err = workspace.NewState(location)
			if err != nil {
				return workspace.State{}, false, err
			}
		}
		if _, exists := value.Environments[name]; exists {
			return value, false, nil
		}
		id, err := workspace.NewUUID()
		if err != nil {
			return workspace.State{}, false, err
		}
		value.Environments[name] = workspace.Binding{ID: id, Connection: workspace.Connection{Alias: "local", Kind: "docker", Context: "default"}}
		return value, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testLocation(root string) workspace.Location {
	return workspace.Location{Root: root, ProjectFile: filepath.Join(root, "odooboat.yaml"), StateFile: filepath.Join(root, ".odooboat", "state.json")}
}

func TestCorruptAndUnknownStateAreRejectedWithoutReplacement(t *testing.T) {
	for _, data := range []string{"{", `{"schema":1,"unknown":true}`, `{"schema":99,"workspace":{},"environments":{}}`} {
		t.Run(data, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := New().Load(path)
			if !errors.Is(err, workspace.ErrCorrupt) {
				t.Fatalf("load error = %v", err)
			}
			current, _ := os.ReadFile(path)
			if string(current) != data {
				t.Fatal("corrupt state was replaced")
			}
		})
	}
}

func TestFailedMutationDoesNotReplaceValidState(t *testing.T) {
	root := t.TempDir()
	location := workspace.Location{Root: root, ProjectFile: filepath.Join(root, "odooboat.yaml"), StateFile: filepath.Join(root, ".odooboat", "state.json")}
	store := New()
	_, err := store.Mutate(location, func(workspace.State, bool) (workspace.State, bool, error) {
		value, err := workspace.NewState(location)
		return value, true, err
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(location.StateFile)
	sentinel := errors.New("stop")
	_, err = store.Mutate(location, func(value workspace.State, _ bool) (workspace.State, bool, error) {
		value.Workspace.ID = "changed"
		return value, true, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("mutation error = %v", err)
	}
	after, _ := os.ReadFile(location.StateFile)
	if string(before) != string(after) {
		t.Fatal("failed mutation replaced valid state")
	}
}
