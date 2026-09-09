package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/remyz17/odooboat/internal/config"
	statefile "github.com/remyz17/odooboat/internal/state/file"
	"github.com/remyz17/odooboat/internal/workspace"
)

func statefulSelector(t *testing.T) (Selector, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, config.ProjectFileName)
	user := filepath.Join(root, "user.yaml")
	if err := os.WriteFile(project, []byte("schema: 1\nproject:\n  name: demo\n  odooVersion: \"18.0\"\n  runtimeConnection: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("schema: 1\nruntimeConnections:\n  local:\n    kind: docker\n    context: default\n  alternate:\n    kind: podman\n    context: machine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Selector{WorkingDir: root, UserConfigPath: user}, root
}

func TestWorkspaceAndEnvironmentLifecycle(t *testing.T) {
	selector, root := statefulSelector(t)
	service := NewWorkspaceService(statefile.New())
	ctx := context.Background()

	show, err := service.Show(ctx, WorkspaceShowRequest{Selector: selector})
	if err != nil || show.Status != WorkspaceStatusUninitialized {
		t.Fatalf("initial show = %+v, %v", show, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".odooboat")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created state: %v", err)
	}
	environment, err := service.ShowEnvironment(ctx, EnvironmentShowRequest{Selector: selector})
	if err != nil || environment.Bound {
		t.Fatalf("unbound environment = %+v, %v", environment, err)
	}

	first, err := service.BindEnvironment(ctx, EnvironmentBindRequest{Selector: selector})
	if err != nil || !first.Created || !workspace.ValidUUID(first.WorkspaceID) || !workspace.ValidUUID(first.EnvironmentID) {
		t.Fatalf("first bind = %+v, %v", first, err)
	}
	second, err := service.BindEnvironment(ctx, EnvironmentBindRequest{Selector: selector})
	if err != nil || second.Created || second.WorkspaceID != first.WorkspaceID || second.EnvironmentID != first.EnvironmentID {
		t.Fatalf("second bind = %+v, %v", second, err)
	}

	alternate := "alternate"
	conflicting := selector
	conflicting.Patch.RuntimeConnection = &alternate
	_, err = service.BindEnvironment(ctx, EnvironmentBindRequest{Selector: conflicting})
	if !errors.Is(err, workspace.ErrBindingConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	after, err := service.ShowEnvironment(ctx, EnvironmentShowRequest{Selector: conflicting})
	if err != nil || !after.Bound || after.Matches == nil || *after.Matches {
		t.Fatalf("conflicting inspection = %+v, %v", after, err)
	}
}

func TestMovePreservesIdentityAndCopyRequiresRekey(t *testing.T) {
	selector, oldRoot := statefulSelector(t)
	service := NewWorkspaceService(statefile.New())
	first, err := service.BindEnvironment(context.Background(), EnvironmentBindRequest{Selector: selector})
	if err != nil {
		t.Fatal(err)
	}

	parent := t.TempDir()
	movedRoot := filepath.Join(parent, "moved")
	if err := os.Rename(oldRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	moved := Selector{WorkingDir: movedRoot, UserConfigPath: filepath.Join(movedRoot, "user.yaml")}
	status, err := service.Show(context.Background(), WorkspaceShowRequest{Selector: moved})
	if err != nil || status.Status != WorkspaceStatusMoved || status.WorkspaceID != first.WorkspaceID {
		t.Fatalf("moved show = %+v, %v", status, err)
	}
	rebound, err := service.BindEnvironment(context.Background(), EnvironmentBindRequest{Selector: moved})
	if err != nil || rebound.WorkspaceID != first.WorkspaceID || rebound.EnvironmentID != first.EnvironmentID {
		t.Fatalf("bind after move = %+v, %v", rebound, err)
	}

	copyRoot := filepath.Join(parent, "copy")
	copyTree(t, movedRoot, copyRoot)
	copySelector := Selector{WorkingDir: copyRoot, UserConfigPath: filepath.Join(copyRoot, "user.yaml")}
	copyStatus, err := service.Show(context.Background(), WorkspaceShowRequest{Selector: copySelector})
	if err != nil || copyStatus.Status != WorkspaceStatusDuplicate {
		t.Fatalf("copy show = %+v, %v", copyStatus, err)
	}
	if _, err := service.BindEnvironment(context.Background(), EnvironmentBindRequest{Selector: copySelector}); !errors.Is(err, workspace.ErrDuplicate) {
		t.Fatalf("copy bind error = %v", err)
	}
	rekeyed, err := service.Rekey(context.Background(), WorkspaceRekeyRequest{Selector: copySelector})
	if err != nil || rekeyed.WorkspaceID == first.WorkspaceID || rekeyed.ClearedBindings != 1 {
		t.Fatalf("rekey = %+v, %v", rekeyed, err)
	}
	original, err := service.Show(context.Background(), WorkspaceShowRequest{Selector: moved})
	if err != nil || original.WorkspaceID != first.WorkspaceID || original.Status != WorkspaceStatusCurrent {
		t.Fatalf("original after rekey = %+v, %v", original, err)
	}
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if info.IsDir() {
			return os.MkdirAll(dest, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}
