package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/remyz17/odooboat/internal/runtime"
)

func TestParseDatabases(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want []string
	}{
		{"", nil},
		{"\n\n", nil},
		{"a\n", []string{"a"}},
		{"my db\nprod-2\n", []string{"my db", "prod-2"}},
	} {
		if got := parseDatabases(tc.out); !slices.Equal(got, tc.want) {
			t.Errorf("parseDatabases(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	want := []string{"odoo", "shell", "--config", "/opt/odooboat/run/odoo.conf", "--no-http", "-d", "db", "--log-level=warn"}
	if got := shellCommand("db", []string{"--log-level=warn"}); !slices.Equal(got, want) {
		t.Fatalf("shellCommand = %q", got)
	}
}

func TestPsqlCommand(t *testing.T) {
	want := []string{"psql", "-U", "odoo", "-v", "ON_ERROR_STOP=1", "-d", "db", "-c", "select 1"}
	if got := psqlCommand("db", []string{"-c", "select 1"}); !slices.Equal(got, want) {
		t.Fatalf("psqlCommand = %q", got)
	}
}

// listing makes the fake engine answer database listings with out.
func (f *lifecycleFixture) listing(out string) {
	f.engine.execOutput = func(req runtime.ExecRequest) (string, int) {
		if slices.Contains(req.Cmd, "-AtX") {
			return out, 0
		}
		return "", f.engine.probeExit
	}
}

func (f *lifecycleFixture) shell(db string) (string, error) {
	var notice bytes.Buffer
	_, err := f.service.Shell(context.Background(), ShellRequest{Selector: f.selector, Database: db, Session: Session{Notice: &notice}})
	return notice.String(), err
}

func (f *lifecycleFixture) psql(db string) (string, error) {
	var notice bytes.Buffer
	_, err := f.service.Psql(context.Background(), PsqlRequest{Selector: f.selector, Database: db, Session: Session{Notice: &notice}})
	return notice.String(), err
}

func (f *lifecycleFixture) lastSession() sessionCall {
	f.t.Helper()
	if len(f.engine.sessionCalls) == 0 {
		f.t.Fatal("no session started")
	}
	return f.engine.sessionCalls[len(f.engine.sessionCalls)-1]
}

func TestDatabaseSelection(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	postgresID := f.engine.containers[f.name(first, runtime.RolePostgres)].ID

	t.Run("explicit", func(t *testing.T) {
		f.engine.execCalls = nil
		f.listing("other\n")
		if _, err := f.shell("x"); err != nil {
			t.Fatal(err)
		}
		if len(f.engine.execCalls) != 0 {
			t.Fatalf("an explicit database was listed: %+v", f.engine.execCalls)
		}
		if cmd := f.lastSession().req.Cmd; !slices.Contains(cmd, "x") {
			t.Fatalf("shell command = %q", cmd)
		}
	})
	t.Run("one", func(t *testing.T) {
		f.engine.execCalls = nil
		f.listing("prod\n")
		notice, err := f.shell("")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(notice, `using database "prod"`) {
			t.Fatalf("notice = %q", notice)
		}
		if cmd := f.lastSession().req.Cmd; !slices.Equal(cmd[len(cmd)-2:], []string{"-d", "prod"}) {
			t.Fatalf("shell command = %q", cmd)
		}
		call := f.engine.execCalls[0]
		query := strings.Join(call.req.Cmd, " ")
		if call.containerID != postgresID || call.req.User != "postgres" || !strings.Contains(query, "NOT datistemplate") ||
			!strings.Contains(query, "datname <> 'postgres'") {
			t.Fatalf("listing exec = %+v", call)
		}
	})
	t.Run("none", func(t *testing.T) {
		f.listing("")
		_, err := f.shell("")
		if !errors.Is(err, ErrDatabase) || !strings.Contains(Hint(err), "database manager") || !strings.Contains(Hint(err), first.URL) {
			t.Fatalf("shell without databases = %v, want ErrDatabase naming the manager at %s", err, first.URL)
		}
		if _, err := f.psql(""); err != nil {
			t.Fatal(err)
		}
		if cmd := f.lastSession().req.Cmd; !slices.Equal(cmd[5:7], []string{"-d", "postgres"}) {
			t.Fatalf("psql command = %q", cmd)
		}
	})
	t.Run("several", func(t *testing.T) {
		f.listing("a\nb\n")
		for _, run := range []func(string) (string, error){f.shell, f.psql} {
			if _, err := run(""); !errors.Is(err, ErrDatabase) || !strings.Contains(err.Error(), `"a"`) || !strings.Contains(err.Error(), `"b"`) {
				t.Fatalf("several databases = %v, want ErrDatabase listing a and b", err)
			}
		}
	})
	t.Run("listing fails", func(t *testing.T) {
		f.engine.execOutput = func(runtime.ExecRequest) (string, int) { return "psql: error: boom\n", 2 }
		if _, err := f.shell(""); !errors.Is(err, ErrDatabase) || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("failing listing = %v", err)
		}
	})
}

func TestShellRunsInOdooAsImageUser(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	if _, err := f.shell("db"); err != nil {
		t.Fatal(err)
	}
	call := f.lastSession()
	if call.containerID != f.engine.containers[f.name(first, runtime.RoleOdoo)].ID || call.req.User != "" {
		t.Fatalf("shell session = %+v", call)
	}
}

func TestPsqlRunsInPostgresAsPostgres(t *testing.T) {
	f := newLifecycle(t, newResourceEngine(t), "")
	first := f.mustUp()
	if _, err := f.psql("db"); err != nil {
		t.Fatal(err)
	}
	call := f.lastSession()
	if call.containerID != f.engine.containers[f.name(first, runtime.RolePostgres)].ID || call.req.User != "postgres" {
		t.Fatalf("psql session = %+v", call)
	}
}
