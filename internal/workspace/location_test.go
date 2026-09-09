package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitClonesAndLinkedWorktreesHaveDistinctLocations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	parent := t.TempDir()
	repository := filepath.Join(parent, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.email", "test@example.invalid")
	runGit(t, repository, "config", "user.name", "Odooboat Test")
	if err := os.WriteFile(filepath.Join(repository, "odooboat.yaml"), []byte("schema: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "odooboat.yaml")
	runGit(t, repository, "commit", "-q", "-m", "initial")

	linked := filepath.Join(parent, "linked")
	runGit(t, repository, "worktree", "add", "-q", "-b", "linked-test", linked)
	clone := filepath.Join(parent, "clone")
	runGit(t, parent, "clone", "-q", repository, clone)

	locations := make([]Location, 0, 3)
	for _, root := range []string{repository, linked, clone} {
		location, err := Locate(filepath.Join(root, "odooboat.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !location.Git {
			t.Fatalf("%s was not detected as Git", root)
		}
		locations = append(locations, location)
	}
	for i := range locations {
		for j := i + 1; j < len(locations); j++ {
			if locations[i].StateFile == locations[j].StateFile {
				t.Fatalf("locations %d and %d share %s", i, j, locations[i].StateFile)
			}
			left, _ := NewState(locations[i])
			right, _ := NewState(locations[j])
			if left.Workspace.ID == right.Workspace.ID {
				t.Fatal("independent workspaces received the same identity")
			}
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
