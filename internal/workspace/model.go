package workspace

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

const SchemaVersion = 1

var (
	ErrState             = errors.New("workspace state operation failed")
	ErrCorrupt           = fmt.Errorf("%w: state is corrupt or unsupported", ErrState)
	ErrDuplicate         = fmt.Errorf("%w: workspace identity is duplicated", ErrState)
	ErrProjectConflict   = fmt.Errorf("%w: another project is already anchored in this workspace", ErrState)
	ErrBindingConflict   = fmt.Errorf("%w: environment runtime binding conflicts", ErrState)
	ErrRekeyNotPermitted = fmt.Errorf("%w: workspace rekey is only allowed for a detected copy", ErrState)
)

type Status string

const (
	StatusUninitialized Status = "uninitialized"
	StatusCurrent       Status = "current"
	StatusMoved         Status = "moved"
	StatusDuplicate     Status = "duplicate"
)

type Connection struct {
	Alias   string `json:"alias" yaml:"alias"`
	Kind    string `json:"kind" yaml:"kind"`
	Context string `json:"context,omitempty" yaml:"context,omitempty"`
	Socket  string `json:"socket,omitempty" yaml:"socket,omitempty"`
}

type Binding struct {
	ID         string     `json:"id"`
	Connection Connection `json:"runtimeConnection"`
}

type Identity struct {
	ID          string `json:"id"`
	Root        string `json:"root"`
	ProjectFile string `json:"projectFile"`
	StateFile   string `json:"stateFile"`
}

type State struct {
	Schema       int                `json:"schema"`
	Workspace    Identity           `json:"workspace"`
	Environments map[string]Binding `json:"environments"`
}

type Location struct {
	Root        string
	ProjectFile string
	StateFile   string
	Git         bool
}

type Inspection struct {
	Status       Status
	Location     Location
	State        *State
	PreviousRoot string
	OriginalFile string
}

type Reader interface {
	Load(path string) (State, bool, error)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var environmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func NewUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func ValidUUID(value string) bool { return uuidPattern.MatchString(value) }

func (c Connection) Equal(other Connection) bool { return c == other }
