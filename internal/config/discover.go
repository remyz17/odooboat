package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	ProjectFileName    = "odooboat.yaml"
	LocalFileName      = "odooboat.local.yaml"
	UserFileName       = "config.yaml"
	AppDirName         = "odooboat"
	DefaultEnvironment = "default"
)

// maxAncestorWalk bounds the upward search so a pathological filesystem cannot
// loop indefinitely.
const maxAncestorWalk = 128

type DiscoverOptions struct {
	WorkingDir      string
	UserConfigPath  string
	ProjectPath     string
	EnvironmentName string
	RequireProject  bool
}

// Sources holds the absolute paths selected for one invocation. An empty path
// means the optional source is absent.
type Sources struct {
	UserPath        string `yaml:"user,omitempty" json:"user,omitempty"`
	ProjectPath     string `yaml:"project,omitempty" json:"project,omitempty"`
	LocalPath       string `yaml:"local,omitempty" json:"local,omitempty"`
	EnvironmentName string `yaml:"environment" json:"environment"`
}

func Discover(opts DiscoverOptions) (Sources, error) {
	src := Sources{EnvironmentName: opts.EnvironmentName}
	if src.EnvironmentName == "" {
		src.EnvironmentName = DefaultEnvironment
	}

	workingDir, err := workingDirectory(opts.WorkingDir)
	if err != nil {
		return Sources{}, fileErr(SourceProject, opts.WorkingDir, ErrNotFound, err.Error(), "")
	}

	userPath, err := discoverUser(workingDir, opts.UserConfigPath)
	if err != nil {
		return Sources{}, err
	}
	src.UserPath = userPath

	if !opts.RequireProject && opts.ProjectPath == "" {
		return src, nil
	}

	projectPath, err := discoverProject(workingDir, opts.ProjectPath)
	if err != nil {
		return Sources{}, err
	}
	src.ProjectPath = projectPath

	local := filepath.Join(filepath.Dir(projectPath), LocalFileName)
	if isRegularFile(local) {
		src.LocalPath = local
	}
	return src, nil
}

func discoverUser(workingDir, explicit string) (string, error) {
	if explicit != "" {
		path := absClean(workingDir, explicit)
		if !isRegularFile(path) {
			return "", fileErr(SourceUser, path, ErrNotFound,
				"user configuration file does not exist", "remove --user-config to use the default location")
		}
		return path, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		// No discoverable user config location; user settings are optional.
		return "", nil
	}
	path := filepath.Join(dir, AppDirName, UserFileName)
	if !isRegularFile(path) {
		return "", nil
	}
	return path, nil
}

func discoverProject(workingDir, explicit string) (string, error) {
	if explicit != "" {
		path := absClean(workingDir, explicit)
		info, err := os.Stat(path)
		if err != nil {
			return "", fileErr(SourceProject, path, ErrNotFound,
				"project definition does not exist", "--project selects an "+ProjectFileName+" file")
		}
		if info.IsDir() {
			return "", fileErr(SourceProject, path, ErrNotFound,
				"--project selects a file, not a directory",
				"use --project "+filepath.Join(path, ProjectFileName))
		}
		return path, nil
	}

	dir := workingDir
	for range maxAncestorWalk {
		candidate := filepath.Join(dir, ProjectFileName)
		if isRegularFile(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fileErr(SourceProject, workingDir, ErrNotFound,
		fmt.Sprintf("no %s found in this directory or any parent", ProjectFileName),
		"run \"odooboat init\" or pass --project PATH")
}

func workingDirectory(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	return filepath.Abs(dir)
}

// absClean interprets a selector path against the caller's working directory.
func absClean(workingDir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return filepath.Clean(path)
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
