//go:build !unix

package cli

import "os"

// ProgressStderr returns stderr unchanged: only Unix raises SIGPIPE.
func ProgressStderr(stderr *os.File) *os.File { return stderr }
