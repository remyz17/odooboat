package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

// Error classes of resource primitives (ADR 0005 §1). Adapters map backend
// errors onto them from structured error types, never from messages.
var (
	ErrNotFound = errors.New("runtime resource not found")
	ErrConflict = errors.New("runtime resource conflicts")
)

type MountKind string

const (
	MountVolume MountKind = "volume"
	MountBind   MountKind = "bind"
)

// Mount attaches a named volume or a host path to a container.
type Mount struct {
	Kind     MountKind `json:"kind"`
	Source   string    `json:"source"`
	Target   string    `json:"target"`
	ReadOnly bool      `json:"readOnly"`
}

// PortBinding publishes a container TCP port on a host address. HostPort 0
// lets the engine choose; inspection reports the chosen port.
type PortBinding struct {
	ContainerPort int    `json:"containerPort"`
	HostIP        string `json:"hostIP"`
	HostPort      int    `json:"hostPort"`
}

// ContainerSpec is everything a container is created with. Creation never
// pulls a missing image (capability image.no-implicit-pull).
type ContainerSpec struct {
	Name    string
	Image   string
	User    string
	Labels  map[string]string
	Env     []string
	Cmd     []string
	Network string
	Mounts  []Mount
	Ports   []PortBinding
}

type ContainerState string

const (
	ContainerCreated ContainerState = "created"
	ContainerRunning ContainerState = "running"
	ContainerExited  ContainerState = "exited"
	// ContainerOther covers transitional states (restarting, paused, removing,
	// dead): the container is neither usable nor cleanly stopped.
	ContainerOther ContainerState = "other"
)

type ContainerInfo struct {
	ID      string
	Name    string
	ImageID string
	Labels  map[string]string
	State   ContainerState
	// ExitCode is meaningful when State is exited (container.exit-status).
	ExitCode int
	// Ports lists the published bindings as the engine applied them.
	Ports []PortBinding
}

// LabelFilter selects resources carrying every listed label with its value.
type LabelFilter map[string]string

// ExecRequest runs a command in a running container without a terminal.
// Output is written to the caller's writers as it arrives.
type ExecRequest struct {
	Cmd    []string
	User   string
	Env    []string
	Stdout io.Writer
	Stderr io.Writer
}

// ExecResult is the outcome of a command that ran. A non-zero exit code is a
// result, never an error (ADR 0005 §2).
type ExecResult struct {
	ExitCode int
}

// LogOptions bounds a log read (ADR 0010 §8).
type LogOptions struct {
	// Follow keeps streaming new lines (logs.follow).
	Follow bool
	// Tail is the number of last lines to start from; negative means all.
	Tail int
	// Since drops lines older than this time; zero means unbounded
	// (logs.bounded-since).
	Since time.Time
	// Timestamps prefixes each line with the engine's RFC 3339 timestamp
	// (logs.source-timestamps on Docker).
	Timestamps bool
}

// ContainerStore is the container primitive set (ADR 0005 §2). It decides
// nothing: replacement policy belongs to the application (ADR 0001 §4).
type ContainerStore interface {
	ListContainers(ctx context.Context, filter LabelFilter) ([]ContainerInfo, error)
	// InspectContainer returns ErrNotFound for an absent container.
	InspectContainer(ctx context.Context, nameOrID string) (ContainerInfo, error)
	// CreateContainer returns the new container's ID, ErrConflict when the
	// name is taken, and ErrImageNotFound when the image is absent.
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, error)
	// CopyToContainer extracts a tar archive into dir, which must exist in
	// the container's image (capability container.copy).
	CopyToContainer(ctx context.Context, id, dir string, archive io.Reader) error
	StartContainer(ctx context.Context, id string) error
	// StopContainer sends the stop signal, then kills after grace.
	StopContainer(ctx context.Context, id string, grace time.Duration) error
	// RemoveContainer removes a stopped container. It never removes volumes.
	RemoveContainer(ctx context.Context, id string) error
	ExecContainer(ctx context.Context, id string, req ExecRequest) (ExecResult, error)
	// ContainerLogs writes a container's log streams, bounded by opts.
	// Following ends with nil when the container stops (logs.follow-ends)
	// and with the context's error when it is cancelled: cancelling stops
	// observation only (ADR 0005 §2).
	ContainerLogs(ctx context.Context, id string, opts LogOptions, stdout, stderr io.Writer) error
}

type NetworkInfo struct {
	ID     string
	Name   string
	Labels map[string]string
}

// NetworkStore manages private networks on which containers resolve each
// other by name (network.private, network.name-resolution).
type NetworkStore interface {
	ListNetworks(ctx context.Context, filter LabelFilter) ([]NetworkInfo, error)
	InspectNetwork(ctx context.Context, nameOrID string) (NetworkInfo, error)
	CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error)
	RemoveNetwork(ctx context.Context, nameOrID string) error
}

type VolumeInfo struct {
	Name   string
	Labels map[string]string
}

// VolumeStore manages named volumes that persist across container
// replacement (volume.named-persistent).
type VolumeStore interface {
	ListVolumes(ctx context.Context, filter LabelFilter) ([]VolumeInfo, error)
	InspectVolume(ctx context.Context, name string) (VolumeInfo, error)
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	// RemoveVolume deletes data. The lifecycle never calls it; it exists for
	// explicit destructive operations and test cleanup.
	RemoveVolume(ctx context.Context, name string) error
}
