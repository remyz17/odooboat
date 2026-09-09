package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func Locate(projectFile string) (Location, error) {
	projectFile, err := canonical(projectFile)
	if err != nil {
		return Location{}, fmt.Errorf("%w: resolve project file: %v", ErrState, err)
	}
	projectDir := filepath.Dir(projectFile)

	root, gitDir, ok := gitLocation(projectDir)
	if ok {
		root, err = canonical(root)
		if err != nil {
			return Location{}, fmt.Errorf("%w: resolve Git worktree: %v", ErrState, err)
		}
		gitDir, err = canonical(gitDir)
		if err != nil {
			return Location{}, fmt.Errorf("%w: resolve Git directory: %v", ErrState, err)
		}
		return Location{
			Root: root, ProjectFile: projectFile,
			StateFile: filepath.Join(gitDir, "odooboat", "state.json"), Git: true,
		}, nil
	}

	return Location{
		Root: projectDir, ProjectFile: projectFile,
		StateFile: filepath.Join(projectDir, ".odooboat", "state.json"),
	}, nil
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

func gitLocation(dir string) (root, gitDir string, ok bool) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte{'\n'})
	if len(lines) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(string(lines[0])), strings.TrimSpace(string(lines[1])), true
}
