//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const pipeHelperEnv = "ODOOBOAT_TEST_PIPE_HELPER"

// TestPipeHelper runs only as a subprocess of TestClosedStderrPipeDoesNotKill:
// it writes to a stderr whose reader is gone, then records that it finished.
func TestPipeHelper(t *testing.T) {
	done := os.Getenv(pipeHelperEnv)
	if done == "" {
		t.Skip("subprocess helper")
	}
	w := ProgressStderr(os.Stderr)
	for i := range 100 {
		fmt.Fprintf(w, "line %d\n", i)
	}
	if err := os.WriteFile(done, []byte("completed"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A closed stderr pipe must fail writes, not kill the process with SIGPIPE,
// so an operation piped through head finishes (ADR 0011 §6).
func TestClosedStderrPipeDoesNotKill(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	done := filepath.Join(t.TempDir(), "done")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPipeHelper$")
	cmd.Env = append(os.Environ(), pipeHelperEnv+"="+done)
	cmd.Stderr = w
	runErr := cmd.Run()
	w.Close()
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the process did not finish (%v): %v", runErr, err)
	}
}
