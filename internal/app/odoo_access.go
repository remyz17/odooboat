package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/remyz17/odooboat/internal/envspec"
	"github.com/remyz17/odooboat/internal/runtime"
)

// ErrDatabase: no database could be selected for shell or psql.
var ErrDatabase = errors.New("database selection failed")

// maintenanceDatabase is PostgreSQL's default database. It is never an Odoo
// database, and psql falls back to it when there is none (ADR 0010 §4).
const maintenanceDatabase = "postgres"

// postgresSystemUser runs psql in the server container, over its local
// socket (ADR 0010 §2).
const postgresSystemUser = "postgres"

const databasesQuery = "SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY datname"

type ShellRequest struct {
	Selector Selector
	Database string
	Args     []string
	Session  Session
}

type PsqlRequest struct {
	Selector Selector
	Database string
	Args     []string
	Session  Session
}

// shellCommand never serves HTTP: with workers above 0, Odoo's shell would
// otherwise start a prefork server on the port the running Odoo uses.
func shellCommand(db string, args []string) []string {
	return append([]string{"odoo", "shell", "--config", path.Join(envspec.RunDir, envspec.ConfFile), "--no-http", "-d", db}, args...)
}

// psqlCommand stops a script at its first error, so a failing script exits
// non-zero; later arguments can override it.
func psqlCommand(db string, args []string) []string {
	return append([]string{"psql", "-U", envspec.PostgresUser, "-v", "ON_ERROR_STOP=1", "-d", db}, args...)
}

func parseDatabases(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// Shell opens Odoo's shell on a database, in the running Odoo container.
func (s WorkspaceService) Shell(ctx context.Context, req ShellRequest) (SessionResult, error) {
	b, release, err := s.connectBound(ctx, req.Selector)
	if err != nil {
		return SessionResult{}, err
	}
	defer release()
	db, err := b.database(ctx, req.Database, false, req.Session)
	if err != nil {
		return SessionResult{}, err
	}
	return b.exec(ctx, runtime.RoleOdoo, runtime.ExecRequest{Cmd: shellCommand(db, req.Args)}, req.Session)
}

// Psql opens psql on a database, in the running PostgreSQL container.
func (s WorkspaceService) Psql(ctx context.Context, req PsqlRequest) (SessionResult, error) {
	b, release, err := s.connectBound(ctx, req.Selector)
	if err != nil {
		return SessionResult{}, err
	}
	defer release()
	db, err := b.database(ctx, req.Database, true, req.Session)
	if err != nil {
		return SessionResult{}, err
	}
	return b.exec(ctx, runtime.RolePostgres, runtime.ExecRequest{Cmd: psqlCommand(db, req.Args), User: postgresSystemUser}, req.Session)
}

// database applies the selection rule of ADR 0010 §4: the requested
// database, else the only one, else psql's maintenance database.
func (b boundEnvironment) database(ctx context.Context, requested string, maintenanceOK bool, s Session) (string, error) {
	if requested != "" {
		return requested, nil
	}
	pg, err := b.running(ctx, runtime.RolePostgres)
	if err != nil {
		return "", err
	}
	var out, errOut bytes.Buffer
	result, err := b.containers.ExecContainer(ctx, pg.ID, runtime.ExecRequest{User: postgresSystemUser, Stdout: &out, Stderr: &errOut,
		Cmd: []string{"psql", "-U", envspec.PostgresUser, "-d", maintenanceDatabase, "-AtX", "-c", databasesQuery}})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("%w: listing databases exited %d: %s", ErrDatabase, result.ExitCode, strings.TrimSpace(errOut.String()))
	}
	names := parseDatabases(out.String())
	switch {
	case len(names) == 1:
		if s.Notice != nil {
			fmt.Fprintf(s.Notice, "odooboat: using database %q\n", names[0])
		}
		return names[0], nil
	case len(names) > 1:
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		return "", fmt.Errorf("%w: environment %q has several databases (%s); choose one with -d",
			ErrDatabase, b.cfg.EnvironmentName, strings.Join(quoted, ", "))
	case maintenanceOK:
		return maintenanceDatabase, nil
	}
	where := "the database manager at /web/database/manager"
	if odoo, err := b.running(ctx, runtime.RoleOdoo); err == nil && odooURL(odoo) != "" {
		where = "the database manager at " + odooURL(odoo) + "/web/database/manager"
	}
	return "", fmt.Errorf("%w: environment %q has no database yet; create one with %s", ErrDatabase, b.cfg.EnvironmentName, where)
}
