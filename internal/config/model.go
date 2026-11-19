package config

import (
	"fmt"
	"strconv"
	"strings"
)

const SchemaVersion = 1

// Runtime connection kinds.
const (
	RuntimeKindDocker = "docker"
	RuntimeKindPodman = "podman"
	RuntimeKindApple  = "apple"
)

// Output preferences.
const (
	OutputYAML = "yaml"
	OutputJSON = "json"
)

// FieldPath is the stable, user-facing spelling of a configuration field.
// Authored errors use the path as written in the document; resolved values and
// provenance use the resolved spelling.
type FieldPath string

const (
	FieldProjectName        FieldPath = "name"
	FieldOdooVersion        FieldPath = "odooVersion"
	FieldRuntimeConnection  FieldPath = "runtimeConnection"
	FieldAddons             FieldPath = "addons"
	FieldPythonPackages     FieldPath = "pythonPackages"
	FieldOdooMasterPassword FieldPath = "odoo.masterPassword"
	FieldOdooOptions        FieldPath = "odoo.options"
	FieldPreferencesOutput  FieldPath = "preferences.output"
	FieldPreferencesColor   FieldPath = "preferences.color"
	FieldPython             FieldPath = "python"
	FieldPostgres           FieldPath = "postgres"
	FieldAllowUnsupported   FieldPath = "allowUnsupported"
	FieldBaseImage          FieldPath = "baseImage"
	FieldOdooCommunity      FieldPath = "odoo.source.community"
	FieldOdooEnterprise     FieldPath = "odoo.source.enterprise"
	// FieldUnsupported is derived from the resolved combination, never authored.
	FieldUnsupported FieldPath = "unsupported"
)

// Odoo source presentation modes (ADR 0008 §9). Community and enterprise share
// one mode.
const (
	SourceModeMount    = "mount"
	SourceModeEmbedded = "embedded"
)

func AddonField(name, leaf string) FieldPath {
	return FieldPath(fmt.Sprintf("addons.%s.%s", name, leaf))
}

func OptionField(key string) FieldPath {
	return FieldPath(fmt.Sprintf("odoo.options.%s", key))
}

// --- Authored documents
//
// One type per owner. A document type exposes only the fields its owner may set;
// anything else is rejected as an unknown field by the decoder. Pointer fields
// distinguish an omitted value from an explicitly authored zero value.

type UserDocument struct {
	Schema             int                          `yaml:"schema"`
	RuntimeConnections map[string]RuntimeConnection `yaml:"runtimeConnections"`
	Preferences        UserPreferences              `yaml:"preferences"`
}

type RuntimeConnection struct {
	Kind    string `yaml:"kind"`
	Context string `yaml:"context"`
	Socket  string `yaml:"socket"`
}

type UserPreferences struct {
	Output *string `yaml:"output"`
	Color  *bool   `yaml:"color"`
}

type ProjectDocument struct {
	Schema       int                           `yaml:"schema"`
	Project      ProjectDefinition             `yaml:"project"`
	Environments map[string]EnvironmentOverlay `yaml:"environments"`
}

type ProjectDefinition struct {
	Name              string                 `yaml:"name"`
	OdooVersion       string                 `yaml:"odooVersion"`
	Python            *string                `yaml:"python"`
	Postgres          *int                   `yaml:"postgres"`
	AllowUnsupported  *[]string              `yaml:"allowUnsupported"`
	BaseImage         *string                `yaml:"baseImage"`
	RuntimeConnection string                 `yaml:"runtimeConnection"`
	Addons            map[string]AddonSource `yaml:"addons"`
	PythonPackages    *[]string              `yaml:"pythonPackages"`
	Odoo              OdooSettings           `yaml:"odoo"`
}

// AddonSource fields are optional so an overlay can override one field of an
// inherited entry. The project document must author Path.
type AddonSource struct {
	Path    *string `yaml:"path"`
	Enabled *bool   `yaml:"enabled"`
}

type OdooSettings struct {
	MasterPassword *SecretRef        `yaml:"masterPassword"`
	Options        map[string]string `yaml:"options"`
	Source         *OdooSource       `yaml:"source"`
}

// LocalOdooSettings exposes only the Odoo fields a local document owns: source
// locations are machine-specific, the other Odoo settings are shared intent.
type LocalOdooSettings struct {
	Source *OdooSource `yaml:"source"`
}

// OdooSource declares the Odoo code an environment runs, separately from
// addons (ADR 0008 §8). Each entry is replaced as a whole by a higher owner.
type OdooSource struct {
	Community  *SourceLocation `yaml:"community"`
	Enterprise *SourceLocation `yaml:"enterprise"`
}

// SourceLocation is either a local path presented as a bind mount, or a Git
// repository pinned to a commit and embedded in the environment image.
type SourceLocation struct {
	Path   string `yaml:"path"`
	Git    string `yaml:"git"`
	Commit string `yaml:"commit"`
}

// SecretRef carries exactly one of Env or Literal.
type SecretRef struct {
	Env     string `yaml:"env"`
	Literal string `yaml:"literal"`
}

type EnvironmentOverlay struct {
	OdooVersion       *string                `yaml:"odooVersion"`
	Python            *string                `yaml:"python"`
	Postgres          *int                   `yaml:"postgres"`
	AllowUnsupported  *[]string              `yaml:"allowUnsupported"`
	BaseImage         *string                `yaml:"baseImage"`
	RuntimeConnection *string                `yaml:"runtimeConnection"`
	Addons            map[string]AddonSource `yaml:"addons"`
	PythonPackages    *[]string              `yaml:"pythonPackages"`
	Odoo              *OdooSettings          `yaml:"odoo"`
}

type LocalDocument struct {
	Schema       int                                `yaml:"schema"`
	Project      LocalProjectOverlay                `yaml:"project"`
	Environments map[string]LocalEnvironmentOverlay `yaml:"environments"`
}

type LocalProjectOverlay struct {
	RuntimeConnection *string                `yaml:"runtimeConnection"`
	AllowUnsupported  *[]string              `yaml:"allowUnsupported"`
	BaseImage         *string                `yaml:"baseImage"`
	Addons            map[string]AddonSource `yaml:"addons"`
	Odoo              *LocalOdooSettings     `yaml:"odoo"`
}

type LocalEnvironmentOverlay struct {
	OdooVersion       *string                `yaml:"odooVersion"`
	Python            *string                `yaml:"python"`
	Postgres          *int                   `yaml:"postgres"`
	AllowUnsupported  *[]string              `yaml:"allowUnsupported"`
	BaseImage         *string                `yaml:"baseImage"`
	RuntimeConnection *string                `yaml:"runtimeConnection"`
	Addons            map[string]AddonSource `yaml:"addons"`
	Odoo              *LocalOdooSettings     `yaml:"odoo"`
}

// InvocationPatch holds only values explicitly provided for one invocation.
// The CLI must populate a field only when its flag was actually changed.
// allowUnsupported is deliberately absent: accepting an unsupported combination
// is durable intent, never a one-off override (ADR 0008 §10).
type InvocationPatch struct {
	OdooVersion       *string
	Python            *string
	Postgres          *int
	RuntimeConnection *string
}

// --- Resolved configuration
//
// Concrete values only. Optionality used to express authored presence must not
// reach application operations.

type ResolvedConfig struct {
	ProjectName      string      `yaml:"projectName" json:"projectName"`
	ProjectFile      string      `yaml:"projectFile" json:"projectFile"`
	EnvironmentName  string      `yaml:"environment" json:"environment"`
	OdooVersion      string      `yaml:"odooVersion" json:"odooVersion"`
	Python           string      `yaml:"python" json:"python"`
	Postgres         int         `yaml:"postgres" json:"postgres"`
	AllowUnsupported []Dimension `yaml:"allowUnsupported" json:"allowUnsupported"`
	// Unsupported lists the accepted dimensions that fall outside the matrix.
	// Inspection commands always report it (ADR 0008 §10).
	Unsupported       []Dimension               `yaml:"unsupported" json:"unsupported"`
	BaseImage         string                    `yaml:"baseImage,omitempty" json:"baseImage,omitempty"`
	RuntimeConnection ResolvedRuntimeConnection `yaml:"runtimeConnection" json:"runtimeConnection"`
	Addons            []ResolvedAddon           `yaml:"addons" json:"addons"`
	PythonPackages    []string                  `yaml:"pythonPackages" json:"pythonPackages"`
	Odoo              ResolvedOdoo              `yaml:"odoo" json:"odoo"`
	Preferences       ResolvedPreferences       `yaml:"preferences" json:"preferences"`
}

type ResolvedRuntimeConnection struct {
	Name    string `yaml:"name" json:"name"`
	Kind    string `yaml:"kind" json:"kind"`
	Context string `yaml:"context,omitempty" json:"context,omitempty"`
	Socket  string `yaml:"socket,omitempty" json:"socket,omitempty"`
}

type ResolvedAddon struct {
	Name    string `yaml:"name" json:"name"`
	Path    string `yaml:"path" json:"path"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type ResolvedOdoo struct {
	MasterPassword ResolvedSecret     `yaml:"masterPassword" json:"masterPassword"`
	Options        map[string]string  `yaml:"options" json:"options"`
	Source         ResolvedOdooSource `yaml:"source" json:"source"`
}

// ResolvedOdooSource is empty when no source is declared. Configuration
// commands accept that; operations that run Odoo require a community source.
type ResolvedOdooSource struct {
	Mode       string                  `yaml:"mode,omitempty" json:"mode,omitempty"`
	Community  *ResolvedSourceLocation `yaml:"community,omitempty" json:"community,omitempty"`
	Enterprise *ResolvedSourceLocation `yaml:"enterprise,omitempty" json:"enterprise,omitempty"`
}

// Declared reports whether a community source is declared.
func (s ResolvedOdooSource) Declared() bool { return s.Community != nil }

type ResolvedSourceLocation struct {
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	Git    string `yaml:"git,omitempty" json:"git,omitempty"`
	Commit string `yaml:"commit,omitempty" json:"commit,omitempty"`
}

// Mode returns the presentation mode the location implies.
func (l ResolvedSourceLocation) Mode() string {
	if l.Path != "" {
		return SourceModeMount
	}
	return SourceModeEmbedded
}

func (l *ResolvedSourceLocation) describe() string {
	switch {
	case l == nil:
		return ""
	case l.Path != "":
		return l.Path
	default:
		return l.Git + "@" + l.Commit
	}
}

type ResolvedPreferences struct {
	Output string `yaml:"output" json:"output"`
	Color  bool   `yaml:"color" json:"color"`
}

// Lookup renders the effective value of a field for inspection commands. Secret
// fields report metadata only.
func (c ResolvedConfig) Lookup(field FieldPath) (string, bool) {
	switch field {
	case FieldProjectName:
		return c.ProjectName, true
	case FieldOdooVersion:
		return c.OdooVersion, true
	case FieldPython:
		return c.Python, true
	case FieldPostgres:
		return strconv.Itoa(c.Postgres), true
	case FieldAllowUnsupported:
		return joinDimensions(c.AllowUnsupported), true
	case FieldUnsupported:
		return joinDimensions(c.Unsupported), true
	case FieldBaseImage:
		return c.BaseImage, true
	case FieldOdooCommunity:
		return c.Odoo.Source.Community.describe(), true
	case FieldOdooEnterprise:
		return c.Odoo.Source.Enterprise.describe(), true
	case FieldRuntimeConnection:
		return c.RuntimeConnection.Name, true
	case FieldPythonPackages:
		return "[" + strings.Join(c.PythonPackages, ", ") + "]", true
	case FieldAddons:
		names := make([]string, 0, len(c.Addons))
		for _, addon := range c.Addons {
			names = append(names, addon.Name)
		}
		return "[" + strings.Join(names, ", ") + "]", true
	case FieldOdooOptions:
		return "[" + strings.Join(sortedKeys(c.Odoo.Options), ", ") + "]", true
	case FieldOdooMasterPassword:
		return describeSecret(c.Odoo.MasterPassword), true
	case FieldPreferencesOutput:
		return c.Preferences.Output, true
	case FieldPreferencesColor:
		return strconv.FormatBool(c.Preferences.Color), true
	}

	if key, ok := strings.CutPrefix(string(field), "odoo.options."); ok {
		value, found := c.Odoo.Options[key]
		return value, found
	}
	if rest, ok := strings.CutPrefix(string(field), "addons."); ok {
		name, leaf, found := strings.Cut(rest, ".")
		if !found {
			return "", false
		}
		for _, addon := range c.Addons {
			if addon.Name != name {
				continue
			}
			switch leaf {
			case "path":
				return addon.Path, true
			case "enabled":
				return strconv.FormatBool(addon.Enabled), true
			}
		}
	}
	return "", false
}

// EnabledAddons returns the addon sets that participate in an environment.
func (c ResolvedConfig) EnabledAddons() []ResolvedAddon {
	out := make([]ResolvedAddon, 0, len(c.Addons))
	for _, addon := range c.Addons {
		if addon.Enabled {
			out = append(out, addon)
		}
	}
	return out
}

func joinDimensions(values []Dimension) string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

func describeSecret(s ResolvedSecret) string {
	switch s.Kind {
	case SecretKindEnv:
		return "env:" + s.Ref
	case SecretKindLiteral:
		return RedactedPlaceholder
	default:
		return ""
	}
}
