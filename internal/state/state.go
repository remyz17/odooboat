package state

import "github.com/remyz17/odooboat/internal/workspace"

// Store is the local metadata persistence and cross-process mutation boundary.
// Implementations must reload while holding their mutation lock and must not
// write when the callback returns an error or reports no change.
type Store interface {
	workspace.Reader
	Mutate(location workspace.Location, fn func(workspace.State, bool) (workspace.State, bool, error)) (workspace.State, error)
}
