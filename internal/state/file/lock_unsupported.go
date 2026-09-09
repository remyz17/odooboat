//go:build !darwin && !linux

package file

import "fmt"

type heldLock struct{}

func acquireLock(string) (*heldLock, error) {
	return nil, fmt.Errorf("state locking is unsupported on this operating system")
}

func (*heldLock) close() {}
