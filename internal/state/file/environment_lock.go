package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/remyz17/odooboat/internal/state"
	"github.com/remyz17/odooboat/internal/workspace"
)

var _ state.Locker = Store{}

const (
	// onWaitDelay avoids reporting a holder for contention that resolves quickly.
	onWaitDelay = time.Second
)

// LockEnvironment takes the lock <state dir>/locks/env-<uuid>.lock (ADR 0006).
// A blocking flock cannot observe a context, so waiting polls a non-blocking
// acquisition. Lock files are never deleted: deleting a locked file would let
// another process lock a new inode at the same path.
func (Store) LockEnvironment(ctx context.Context, location workspace.Location, environmentID string, req state.LockRequest) (state.Lock, error) {
	if !workspace.ValidUUID(environmentID) {
		return nil, fmt.Errorf("%w: invalid environment UUID %q", workspace.ErrState, environmentID)
	}
	dir := filepath.Join(filepath.Dir(location.StateFile), "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create lock directory: %v", workspace.ErrState, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: protect lock directory: %v", workspace.ErrState, err)
	}
	path := filepath.Join(dir, "env-"+environmentID+".lock")

	start := time.Now()
	delay := nextDelay(0)
	notified := false
	for {
		held, err := tryLock(path)
		if err != nil {
			return nil, fmt.Errorf("%w: acquire environment lock: %v", workspace.ErrState, err)
		}
		if held != nil {
			if err := writeHolder(held.file, req.Holder); err != nil {
				held.close()
				return nil, fmt.Errorf("%w: record environment lock holder: %v", workspace.ErrState, err)
			}
			return &environmentLock{held: held}, nil
		}
		if req.Policy != state.Wait {
			return nil, &state.BusyError{EnvironmentID: environmentID, Holder: readHolder(path)}
		}
		if !notified && req.OnWait != nil && time.Since(start) >= onWaitDelay {
			notified = true
			req.OnWait(readHolder(path))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("wait for environment %s: %w", environmentID, ctx.Err())
		case <-timer.C:
		}
		delay = nextDelay(delay)
	}
}

// nextDelay returns the pause before the next acquisition attempt, given the
// previous one. It trades resumption latency against polling cost.
func nextDelay(previous time.Duration) time.Duration {
	const (
		initial = 25 * time.Millisecond
		maximum = 500 * time.Millisecond
	)

	if previous <= 0 {
		return initial
	}

	return min(previous*2, maximum)
}

type environmentLock struct{ held *heldLock }

// Release clears the holder record before unlocking so a later reader does
// not attribute the free lock to a finished process.
func (l *environmentLock) Release() error {
	err := l.held.file.Truncate(0)
	l.held.close()
	if err != nil {
		return fmt.Errorf("%w: clear environment lock holder: %v", workspace.ErrState, err)
	}
	return nil
}

func writeHolder(file *os.File, holder state.Holder) error {
	data, err := json.Marshal(holder)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err = file.WriteAt(append(data, '\n'), 0)
	return err
}

// readHolder reads the record without locking; partial or missing content is
// an unknown holder.
func readHolder(path string) *state.Holder {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var holder state.Holder
	if err := json.Unmarshal(data, &holder); err != nil || holder.PID <= 0 {
		return nil
	}
	return &holder
}
