package file

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/state"
	"github.com/remyz17/odooboat/internal/workspace"
)

const (
	lockEnvA = "aaaaaaaa-1111-4111-8111-111111111111"
	lockEnvB = "bbbbbbbb-2222-4222-8222-222222222222"
)

func lockRequest(policy state.WaitPolicy) state.LockRequest {
	return state.LockRequest{Policy: policy, Holder: state.Holder{
		PID: os.Getpid(), Host: "test", Operation: "test", OperationID: "op", StartedAt: time.Now().UTC(), Version: "odooboat/test",
	}}
}

// TestEnvironmentLockHelper runs only as a subprocess: it holds the lock until
// its stdin is closed, or until it is killed.
func TestEnvironmentLockHelper(t *testing.T) {
	if os.Getenv("ODOOBOAT_ENV_LOCK_HELPER") != "1" {
		return
	}
	location := testLocation(os.Getenv("ODOOBOAT_LOCK_ROOT"))
	lock, err := New().LockEnvironment(context.Background(), location, os.Getenv("ODOOBOAT_LOCK_ENV"), lockRequest(state.WaitFailFast))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

type lockHolder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startHolder(t *testing.T, root, environmentID string) *lockHolder {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestEnvironmentLockHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ODOOBOAT_ENV_LOCK_HELPER=1", "ODOOBOAT_LOCK_ROOT="+root, "ODOOBOAT_LOCK_ENV="+environmentID)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper did not lock: %q, %v", line, err)
	}
	return &lockHolder{cmd: cmd, stdin: stdin}
}

func (h *lockHolder) release(t *testing.T) {
	t.Helper()
	_ = h.stdin.Close()
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
}

func TestEnvironmentLockFailFastReportsHolder(t *testing.T) {
	root := t.TempDir()
	holder := startHolder(t, root, lockEnvA)
	_, err := New().LockEnvironment(context.Background(), testLocation(root), lockEnvA, lockRequest(state.WaitFailFast))
	var busy *state.BusyError
	if !errors.As(err, &busy) || !errors.Is(err, state.ErrEnvironmentBusy) {
		t.Fatalf("err = %v, want BusyError", err)
	}
	if busy.Holder == nil || busy.Holder.PID != holder.cmd.Process.Pid || busy.Holder.Operation != "test" {
		t.Fatalf("holder = %+v, want pid %d", busy.Holder, holder.cmd.Process.Pid)
	}

	other, err := New().LockEnvironment(context.Background(), testLocation(root), lockEnvB, lockRequest(state.WaitFailFast))
	if err != nil {
		t.Fatalf("distinct environment contended: %v", err)
	}
	if err := other.Release(); err != nil {
		t.Fatal(err)
	}
	holder.release(t)
}

func TestEnvironmentLockReleasedWhenHolderIsKilled(t *testing.T) {
	root := t.TempDir()
	holder := startHolder(t, root, lockEnvA)
	if err := holder.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.cmd.Wait()
	lock, err := New().LockEnvironment(context.Background(), testLocation(root), lockEnvA, lockRequest(state.WaitFailFast))
	if err != nil {
		t.Fatalf("lock after holder death: %v", err)
	}
	_ = lock.Release()
}

func TestEnvironmentLockWaitAcquiresAfterRelease(t *testing.T) {
	root := t.TempDir()
	holder := startHolder(t, root, lockEnvA)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = holder.stdin.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lock, err := New().LockEnvironment(ctx, testLocation(root), lockEnvA, lockRequest(state.Wait))
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	_ = holder.cmd.Wait()
}

func TestEnvironmentLockWaitStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	holder := startHolder(t, root, lockEnvA)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var reported *state.Holder
	req := lockRequest(state.Wait)
	req.OnWait = func(h *state.Holder) { reported = h }
	_, err := New().LockEnvironment(ctx, testLocation(root), lockEnvA, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if reported == nil || reported.PID != holder.cmd.Process.Pid {
		t.Fatalf("OnWait reported %+v", reported)
	}
	holder.release(t)
}

func TestStateMutationWhileEnvironmentLocked(t *testing.T) {
	root := t.TempDir()
	location := testLocation(root)
	holder := startHolder(t, root, lockEnvA)
	_, err := New().Mutate(location, func(workspace.State, bool) (workspace.State, bool, error) {
		value, err := workspace.NewState(location)
		return value, true, err
	})
	if err != nil {
		t.Fatalf("state mutation blocked by environment lock: %v", err)
	}
	holder.release(t)
}

func TestEnvironmentLockFileIsKeptAndProtected(t *testing.T) {
	root := t.TempDir()
	location := testLocation(root)
	lock, err := New().LockEnvironment(context.Background(), location, lockEnvA, lockRequest(state.WaitFailFast))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(location.StateFile), "locks")
	info, err := os.Stat(filepath.Join(dir, "env-"+lockEnvA+".lock"))
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		t.Fatalf("lock file after release = %v, %v", info, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("lock directory = %v, %v", info, err)
	}
	if _, err := New().LockEnvironment(context.Background(), location, "not-a-uuid", lockRequest(state.WaitFailFast)); !errors.Is(err, workspace.ErrState) {
		t.Fatalf("invalid UUID: err = %v", err)
	}
}
