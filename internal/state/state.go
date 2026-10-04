package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/remyz17/odooboat/internal/workspace"
)

// Store is the local metadata persistence and cross-process mutation boundary.
// Implementations must reload while holding their mutation lock and must not
// write when the callback returns an error or reports no change.
type Store interface {
	workspace.Reader
	Mutate(location workspace.Location, fn func(workspace.State, bool) (workspace.State, bool, error)) (workspace.State, error)
}

// ErrEnvironmentBusy is distinct from workspace.ErrState so callers can retry.
var ErrEnvironmentBusy = errors.New("environment is busy")

// Holder describes the process holding an environment lock. It is
// informational only; the file lock is the sole source of truth (ADR 0006 §5).
type Holder struct {
	PID         int       `json:"pid" yaml:"pid"`
	Host        string    `json:"host" yaml:"host"`
	Operation   string    `json:"operation" yaml:"operation"`
	OperationID string    `json:"operationId" yaml:"operationId"`
	StartedAt   time.Time `json:"startedAt" yaml:"startedAt"`
	Version     string    `json:"version" yaml:"version"`
}

type WaitPolicy string

const (
	// WaitFailFast returns ErrEnvironmentBusy immediately (MCP).
	WaitFailFast WaitPolicy = "fail-fast"
	// Wait blocks until the lock is acquired or the context is cancelled (CLI).
	Wait WaitPolicy = "wait"
)

type LockRequest struct {
	Holder Holder
	Policy WaitPolicy
	// OnWait is called at most once, after a short delay, while waiting. The
	// holder is nil when its record cannot be read.
	OnWait func(*Holder)
}

// Lock is a held environment lock.
type Lock interface {
	Release() error
}

// Locker serializes stateful operations on one environment across processes.
type Locker interface {
	LockEnvironment(ctx context.Context, location workspace.Location, environmentID string, req LockRequest) (Lock, error)
}

// BusyError reports a lock held by another process.
type BusyError struct {
	EnvironmentID string
	Holder        *Holder
}

func (e *BusyError) Error() string {
	if e.Holder == nil {
		return fmt.Sprintf("environment %s is busy: held by an unknown process", e.EnvironmentID)
	}
	return fmt.Sprintf("environment %s is busy: %s", e.EnvironmentID, e.Holder.Describe())
}

func (e *BusyError) Unwrap() error { return ErrEnvironmentBusy }

// Describe renders the holder for human messages.
func (h *Holder) Describe() string {
	return fmt.Sprintf("held by %q (pid %d on %s since %s, operation %s)",
		h.Operation, h.PID, h.Host, h.StartedAt.Format(time.RFC3339), h.OperationID)
}
