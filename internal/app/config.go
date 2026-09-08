package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/remyz17/odooboat/internal/config"
)

// ErrProjectExists reports that init refused to replace an existing definition.
var ErrProjectExists = errors.New("project definition already exists")

// Selector carries the discovery inputs and the per-invocation overrides. Every
// adapter builds one; none of its fields depend on a CLI framework.
type Selector struct {
	WorkingDir     string
	UserConfigPath string
	ProjectPath    string
	Environment    string
	Patch          config.InvocationPatch
}

func (s Selector) options() config.Options {
	return config.Options{
		WorkingDir:      s.WorkingDir,
		UserConfigPath:  s.UserConfigPath,
		ProjectPath:     s.ProjectPath,
		EnvironmentName: s.Environment,
		Patch:           s.Patch,
	}
}

type ConfigService struct{}

func NewConfigService() ConfigService { return ConfigService{} }

type ValidateRequest struct {
	Selector Selector
}

type ValidateResult struct {
	Sources config.Sources
	Config  config.ResolvedConfig
}

// Validate resolves every applicable source without inspecting or mutating a runtime.
func (ConfigService) Validate(_ context.Context, req ValidateRequest) (ValidateResult, error) {
	result, err := config.Resolve(req.Selector.options())
	if err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{Sources: result.Sources, Config: result.Config}, nil
}

type ShowRequest struct {
	Selector Selector
}

type ShowResult struct {
	Sources    config.Sources
	Config     config.ResolvedConfig
	Provenance config.Provenance
}

func (ConfigService) Show(_ context.Context, req ShowRequest) (ShowResult, error) {
	result, err := config.Resolve(req.Selector.options())
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{Sources: result.Sources, Config: result.Config, Provenance: result.Provenance}, nil
}

type ExplainRequest struct {
	Selector Selector
	Field    config.FieldPath
}

type ExplainResult struct {
	Field        config.FieldPath   `yaml:"field" json:"field"`
	Value        string             `yaml:"value" json:"value"`
	Secret       bool               `yaml:"secret,omitempty" json:"secret,omitempty"`
	Winner       config.SourceRef   `yaml:"winner" json:"winner"`
	Overridden   []config.SourceRef `yaml:"overridden,omitempty" json:"overridden,omitempty"`
	ListReplaced bool               `yaml:"listReplaced,omitempty" json:"listReplaced,omitempty"`

	// Preferences let an adapter render this result the way the user asked for;
	// they are not part of the reported field.
	Preferences config.ResolvedPreferences `yaml:"-" json:"-"`
}

// Explain reports the effective value of one field and where it came from. A
// secret field reports metadata and source but never its resolved value.
func (ConfigService) Explain(_ context.Context, req ExplainRequest) (ExplainResult, error) {
	result, err := config.Resolve(req.Selector.options())
	if err != nil {
		return ExplainResult{}, err
	}

	entry, known := result.Provenance.Lookup(req.Field)
	value, found := result.Config.Lookup(req.Field)
	if !known && !found {
		return ExplainResult{}, fmt.Errorf("%w: unknown field %q; run \"odooboat config show\" to list the resolved fields",
			config.ErrResolved, req.Field)
	}

	return ExplainResult{
		Field:        req.Field,
		Value:        value,
		Secret:       entry.Secret,
		Winner:       entry.Winner,
		Overridden:   entry.Overridden,
		ListReplaced: entry.ListReplaced,
		Preferences:  result.Config.Preferences,
	}, nil
}

type InitRequest struct {
	Dir         string
	ProjectName string
}

type InitResult struct {
	Path string
}

// Init writes a minimal authored starting point. It never overwrites an existing
// project definition.
func (ConfigService) Init(_ context.Context, req InitRequest) (InitResult, error) {
	dir := req.Dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return InitResult{}, err
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return InitResult{}, err
	}

	name := req.ProjectName
	if name == "" {
		name = filepath.Base(dir)
	}

	path := filepath.Join(dir, config.ProjectFileName)
	if _, err := os.Stat(path); err == nil {
		return InitResult{}, fmt.Errorf("%w: %s", ErrProjectExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return InitResult{}, err
	}

	if err := writeFileAtomic(path, projectTemplate(name)); err != nil {
		return InitResult{}, err
	}
	return InitResult{Path: path}, nil
}

func projectTemplate(name string) []byte {
	return fmt.Appendf(nil, `schema: %d

project:
  name: %s
  odooVersion: "18.0"
  # runtimeConnection must name an entry in your user configuration.
  runtimeConnection: local
`, config.SchemaVersion, name)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".odooboat-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
