package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/workspace"
)

// fakeAccess records requests and answers with result, err, or a panic.
type fakeAccess struct {
	result app.SessionResult
	err    error
	panics bool
	// selects is reported as the chosen database.
	selects string
	logs    []app.LogsRequest
	execs   []app.ExecRequest
	shells  []app.ShellRequest
	psqls   []app.PsqlRequest
	written string
}

func (f *fakeAccess) answer(s app.Session) (app.SessionResult, error) {
	if f.panics {
		panic("session exploded")
	}
	if f.selects != "" && s.Reporter != nil {
		s.Reporter.Report(app.DatabaseSelected{Name: f.selects})
	}
	if s.Stdout != nil && f.written != "" {
		fmt.Fprint(s.Stdout, f.written)
	}
	return f.result, f.err
}

func (f *fakeAccess) Logs(_ context.Context, req app.LogsRequest) error {
	f.logs = append(f.logs, req)
	if req.Stdout != nil {
		fmt.Fprint(req.Stdout, "out\n")
	}
	if req.Stderr != nil {
		fmt.Fprint(req.Stderr, "err\n")
	}
	return f.err
}

func (f *fakeAccess) Exec(_ context.Context, req app.ExecRequest) (app.SessionResult, error) {
	f.execs = append(f.execs, req)
	return f.answer(req.Session)
}

func (f *fakeAccess) Shell(_ context.Context, req app.ShellRequest) (app.SessionResult, error) {
	f.shells = append(f.shells, req)
	return f.answer(req.Session)
}

func (f *fakeAccess) Psql(_ context.Context, req app.PsqlRequest) (app.SessionResult, error) {
	f.psqls = append(f.psqls, req)
	return f.answer(req.Session)
}

// fakeTerminal is an interactive terminal counting raw-mode restores.
type fakeTerminal struct {
	raw, restored int
}

func (t *fakeTerminal) Interactive() bool      { return true }
func (t *fakeTerminal) StderrIsTerminal() bool { return false }
func (t *fakeTerminal) StdoutIsTerminal() bool { return false }
func (t *fakeTerminal) StderrSize() (int, int) { return 0, 0 }
func (t *fakeTerminal) Size() (app.TerminalSize, error) {
	return app.TerminalSize{Width: 120, Height: 40}, nil
}
func (t *fakeTerminal) MakeRaw() (func() error, error) {
	t.raw++
	return func() error { t.restored++; return nil }, nil
}
func (t *fakeTerminal) Resizes() (<-chan app.TerminalSize, func()) { return nil, func() {} }

type accessRun struct {
	code           int
	stdout, stderr string
}

func runAccess(t *testing.T, access *fakeAccess, terminal Terminal, args ...string) accessRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := Dependencies{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, WorkingDir: t.TempDir(),
		Access: access, Terminal: terminal}
	code := runRoot(context.Background(), NewRootCommand(deps), args, deps)
	return accessRun{code, stdout.String(), stderr.String()}
}

func TestExecFamilyExitCodes(t *testing.T) {
	for _, command := range [][]string{{"exec", "--", "true"}, {"shell", "-d", "db"}, {"psql", "-d", "db"}} {
		if r := runAccess(t, &fakeAccess{result: app.SessionResult{ExitCode: 7}}, nil, command...); r.code != 7 || r.stderr != "" {
			t.Errorf("%v remote 7: exit %d, stderr %q", command, r.code, r.stderr)
		}
		if r := runAccess(t, &fakeAccess{}, nil, command...); r.code != 0 {
			t.Errorf("%v remote 0: exit %d", command, r.code)
		}
		for _, err := range []error{
			fmt.Errorf("%w: never started; run odooboat up", app.ErrNotRunning),
			fmt.Errorf("%w: another engine", workspace.ErrBindingConflict),
			fmt.Errorf("%w: no TTY", app.ErrRuntimeUnsupported),
		} {
			if r := runAccess(t, &fakeAccess{err: err}, nil, command...); r.code != ExitToolFailure || !strings.Contains(r.stderr, err.Error()) {
				t.Errorf("%v failing with %v: exit %d, stderr %q", command, err, r.code, r.stderr)
			}
		}
		if r := runAccess(t, &fakeAccess{}, nil, append([]string{"--output", "json"}, command...)...); r.code != ExitToolFailure {
			t.Errorf("%v with --output: exit %d, want 125", command, r.code)
		}
	}
}

func TestSessionRestoresTerminalOnEveryPath(t *testing.T) {
	for name, access := range map[string]*fakeAccess{
		"success":          {},
		"remote failure":   {result: app.SessionResult{ExitCode: 3}},
		"odooboat failure": {err: app.ErrNotRunning},
		"panic":            {panics: true},
	} {
		terminal := &fakeTerminal{}
		func() {
			defer func() { _ = recover() }()
			runAccess(t, access, terminal, "exec", "--", "sh")
		}()
		if terminal.raw != 1 || terminal.restored != 1 {
			t.Errorf("%s: raw %d, restored %d; want 1 and 1", name, terminal.raw, terminal.restored)
		}
	}
	// --no-tty never touches the terminal.
	terminal := &fakeTerminal{}
	access := &fakeAccess{}
	runAccess(t, access, terminal, "exec", "--no-tty", "--", "sh")
	if terminal.raw != 0 || access.execs[0].Session.TTY {
		t.Fatalf("--no-tty: raw %d, TTY %v", terminal.raw, access.execs[0].Session.TTY)
	}
}

func TestExecArguments(t *testing.T) {
	access := &fakeAccess{}
	if r := runAccess(t, access, nil, "exec", "--", "ls", "-la"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	req := access.execs[0]
	if !slices.Equal(req.Cmd, []string{"ls", "-la"}) || req.Role != app.RoleOdoo || req.Session.TTY {
		t.Fatalf("exec request = %+v", req)
	}
	runAccess(t, access, nil, "exec", "--service", "postgres", "--user", "postgres", "--", "id")
	if req := access.execs[1]; req.Role != app.RolePostgres || req.User != "postgres" {
		t.Fatalf("exec request = %+v", req)
	}
	if r := runAccess(t, &fakeAccess{}, nil, "exec"); r.code != ExitToolFailure {
		t.Fatalf("exec without a command: exit %d", r.code)
	}
	if r := runAccess(t, &fakeAccess{}, nil, "exec", "--service", "web", "--", "true"); r.code != ExitToolFailure {
		t.Fatalf("unknown service: exit %d", r.code)
	}
}

func TestShellArguments(t *testing.T) {
	access := &fakeAccess{}
	if r := runAccess(t, access, nil, "shell", "-d", "prod", "--", "--log-level=warn"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if req := access.shells[0]; req.Database != "prod" || !slices.Equal(req.Args, []string{"--log-level=warn"}) {
		t.Fatalf("shell request = %+v", req)
	}
	if r := runAccess(t, &fakeAccess{}, nil, "shell", "extra"); r.code != ExitToolFailure {
		t.Fatalf("positional argument before --: exit %d", r.code)
	}
	runAccess(t, access, nil, "psql", "--", "-c", "select 1")
	if req := access.psqls[0]; req.Database != "" || !slices.Equal(req.Args, []string{"-c", "select 1"}) {
		t.Fatalf("psql request = %+v", req)
	}
}

func TestLogsFlags(t *testing.T) {
	access := &fakeAccess{}
	r := runAccess(t, access, nil, "logs")
	if r.code != 0 || r.stdout != "out\nerr\n" {
		t.Fatalf("logs: exit %d, stdout %q", r.code, r.stdout)
	}
	if req := access.logs[0]; req.Role != app.RoleOdoo || req.Options.Tail != 100 || req.Options.Follow {
		t.Fatalf("default logs request = %+v", req)
	}
	runAccess(t, access, nil, "logs", "postgres", "-f", "--tail", "all", "--timestamps")
	if req := access.logs[1]; req.Role != app.RolePostgres || req.Options.Tail != -1 || !req.Options.Follow || !req.Options.Timestamps {
		t.Fatalf("logs request = %+v", req)
	}
	before := time.Now()
	runAccess(t, access, nil, "logs", "--since", "10m")
	if since := access.logs[2].Options.Since; since.Before(before.Add(-10*time.Minute-time.Second)) || since.After(time.Now().Add(-10*time.Minute)) {
		t.Fatalf("--since 10m gave %v", since)
	}
	runAccess(t, access, nil, "logs", "--since", "2026-11-21T10:00:00Z")
	if since := access.logs[3].Options.Since; !since.Equal(time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("--since RFC 3339 gave %v", since)
	}
	for _, bad := range [][]string{{"logs", "--tail", "x"}, {"logs", "--since", "yesterday"}, {"logs", "web"}} {
		if r := runAccess(t, &fakeAccess{}, nil, bad...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", bad, r.code, ExitUsage)
		}
	}
	if r := runAccess(t, &fakeAccess{err: context.Canceled}, nil, "logs", "-f"); r.code != 0 {
		t.Fatalf("interrupted follow: exit %d, stderr %q", r.code, r.stderr)
	}
	if r := runAccess(t, &fakeAccess{err: fmt.Errorf("%w: run odooboat up", app.ErrNotRunning)}, nil, "logs"); r.code != ExitNotRunning {
		t.Fatalf("logs after down: exit %d", r.code)
	}
	if r := runAccess(t, &fakeAccess{err: errors.New("boom")}, nil, "logs"); r.code != ExitError {
		t.Fatalf("logs failure: exit %d", r.code)
	}
}

func TestShellHelpDocumentsRollback(t *testing.T) {
	r := runAccess(t, &fakeAccess{}, nil, "shell", "--help")
	if !strings.Contains(r.stdout, "env.cr.commit()") || !strings.Contains(r.stdout, "rolled back") {
		t.Fatalf("shell help = %q", r.stdout)
	}
}

func TestRawSessionNoticeUsesCRLF(t *testing.T) {
	access := &fakeAccess{selects: "db", err: app.ErrDatabase}
	r := runAccess(t, access, &fakeTerminal{}, "--progress=plain", "shell")
	if !strings.Contains(r.stderr, "using database \"db\"\r\n") {
		t.Fatalf("stderr %q lacks the notice in raw mode", r.stderr)
	}
	if !strings.HasSuffix(r.stderr, "selection failed\n") || strings.HasSuffix(r.stderr, "\r\n") {
		t.Fatalf("stderr %q: the error after the session must end with a plain newline", r.stderr)
	}
}

// fakeInterrupts counts holds of the second-interrupt exit.
type fakeInterrupts struct{ held, released int }

func (f *fakeInterrupts) Capture() (<-chan app.Signal, func()) { return nil, func() {} }
func (f *fakeInterrupts) Hold() func() {
	f.held++
	return func() { f.released++ }
}

func TestTTYSessionHoldsSecondInterruptExit(t *testing.T) {
	for name, access := range map[string]*fakeAccess{"success": {}, "failure": {err: app.ErrNotRunning}} {
		interrupts := &fakeInterrupts{}
		var stderr bytes.Buffer
		deps := Dependencies{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, WorkingDir: t.TempDir(),
			Access: access, Terminal: &fakeTerminal{}, Interrupts: interrupts}
		runRoot(context.Background(), NewRootCommand(deps), []string{"exec", "--", "sh"}, deps)
		if interrupts.held != 1 || interrupts.released != 1 {
			t.Errorf("%s: held %d, released %d; want 1 and 1", name, interrupts.held, interrupts.released)
		}
	}
}
