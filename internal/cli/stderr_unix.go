//go:build unix

package cli

import (
	"os"
	"syscall"
)

// ProgressStderr returns a duplicate of stderr for progress and errors. Go
// turns a write to a broken pipe on file descriptors 1 and 2 into a fatal
// SIGPIPE; on a duplicate the write fails with EPIPE instead, which
// ui.Writer drops, so `odooboat up 2>&1 | head` lets the operation finish
// (ADR 0011 §6). stdout keeps the usual SIGPIPE behaviour.
func ProgressStderr(stderr *os.File) *os.File {
	fd, err := syscall.Dup(int(stderr.Fd()))
	if err != nil {
		return stderr
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), stderr.Name())
}
