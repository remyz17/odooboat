package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

// The access commands observe and enter a running environment without the
// lock (ADR 0010). Their output is the remote bytes, never a rendered result.

var services = map[string]app.Role{"odoo": app.RoleOdoo, "postgres": app.RolePostgres}

func service(cmd *cobra.Command, name string) (app.Role, error) {
	role, ok := services[name]
	if !ok {
		return "", usageErrorf(cmd, "unknown service %q: expected odoo or postgres", name)
	}
	return role, nil
}

// rejectOutput refuses --output: these commands print the remote bytes.
func rejectOutput(cmd *cobra.Command, global *globalFlags) error {
	if global.output != "" {
		return usageErrorf(cmd, "--output does not apply to %s: it prints the command's own output", cmd.Name())
	}
	return nil
}

// afterDash returns the arguments after "--", refusing any before it.
func afterDash(cmd *cobra.Command, args []string) ([]string, error) {
	dash := cmd.ArgsLenAtDash()
	if (dash < 0 && len(args) > 0) || dash > 0 {
		return nil, usageErrorf(cmd, "arguments for the remote command go after --")
	}
	return args, nil
}

// crlf turns "\n" into "\r\n" for Odooboat's own messages while the local
// terminal is in raw mode.
type crlf struct{ w io.Writer }

func (c crlf) Write(p []byte) (int, error) {
	if _, err := c.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// newSession prepares the local side of a session. With a TTY the terminal
// is switched to raw mode; cleanup restores it and must run on every path.
// Without one, signals are captured for forwarding (ADR 0010 §3, §7).
func newSession(deps Dependencies, noTTY bool) (app.Session, func(), error) {
	s := app.Session{Stdin: deps.Stdin, Stdout: deps.Stdout, Stderr: deps.Stderr, Notice: deps.Stderr}
	terminal := deps.terminal()
	if noTTY || !terminal.Interactive() {
		if deps.Interrupts == nil {
			return s, func() {}, nil
		}
		signals, release := deps.Interrupts.Capture()
		s.Signals = signals
		return s, release, nil
	}
	size, err := terminal.Size()
	if err != nil {
		return s, nil, fmt.Errorf("read terminal size: %w", err)
	}
	restore, err := terminal.MakeRaw()
	if err != nil {
		return s, nil, fmt.Errorf("switch terminal to raw mode: %w", err)
	}
	resizes, stopResizes := terminal.Resizes()
	s.TTY, s.Size, s.Resizes, s.Notice = true, size, resizes, crlf{deps.Stderr}
	return s, func() {
		stopResizes()
		_ = restore()
	}, nil
}

// session runs one access command with its local session; a remote
// non-zero exit becomes the process's exit code.
func session(deps Dependencies, noTTY bool, run func(app.Session) (app.SessionResult, error)) error {
	s, cleanup, err := newSession(deps, noTTY)
	if err != nil {
		return err
	}
	defer cleanup()
	result, err := run(s)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return &ExitStatus{Code: result.ExitCode}
	}
	return nil
}

func newLogsCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var follow, timestamps bool
	var tail, since string
	cmd := &cobra.Command{
		Use:   "logs [odoo|postgres]",
		Short: "Show the logs of the environment's Odoo or PostgreSQL container",
		Long: "Show a container's logs, both streams on stdout. Following ends with Ctrl-C or\n" +
			"when the container stops. The default shows the last 100 lines; --tail all shows\n" +
			"everything.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageErrorf(cmd, "expected at most one service")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectOutput(cmd, global); err != nil {
				return err
			}
			name := "odoo"
			if len(args) == 1 {
				name = args[0]
			}
			role, err := service(cmd, name)
			if err != nil {
				return err
			}
			opts := app.LogOptions{Follow: follow, Timestamps: timestamps, Tail: -1}
			if tail != "all" {
				if opts.Tail, err = strconv.Atoi(tail); err != nil || opts.Tail < 0 {
					return usageErrorf(cmd, "--tail expects a number of lines or all, not %q", tail)
				}
			}
			if since != "" {
				if opts.Since, err = parseSince(since, time.Now()); err != nil {
					return usageErrorf(cmd, "%v", err)
				}
			}
			err = deps.access().Logs(cmd.Context(), app.LogsRequest{Selector: global.selector(cmd, deps), Role: role,
				Options: opts, Stdout: deps.Stdout, Stderr: deps.Stdout})
			if errors.Is(err, context.Canceled) {
				// Ctrl-C ends observation; nothing failed.
				return nil
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep showing new lines until Ctrl-C or the container stops")
	cmd.Flags().StringVar(&tail, "tail", "100", "number of last lines to show, or all")
	cmd.Flags().StringVar(&since, "since", "", "show lines newer than a duration (10m) or an RFC 3339 time")
	cmd.Flags().BoolVar(&timestamps, "timestamps", false, "prefix each line with the engine's timestamp")
	return cmd
}

func parseSince(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since expects a duration such as 10m or an RFC 3339 time, not %q", value)
}

func newExecCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var name, user string
	var noTTY bool
	cmd := &cobra.Command{
		Use:   "exec [--service odoo|postgres] [--user USER] -- COMMAND [ARG...]",
		Short: "Run a command in the environment's running Odoo or PostgreSQL container",
		Long: "Run a command in a running container. A terminal is allocated when stdin and\n" +
			"stdout are terminals; stdin is always forwarded. The command's exit code is\n" +
			"returned unchanged; 125 means odooboat itself failed.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageErrorf(cmd, "expected a command after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectOutput(cmd, global); err != nil {
				return err
			}
			role, err := service(cmd, name)
			if err != nil {
				return err
			}
			return session(deps, noTTY, func(s app.Session) (app.SessionResult, error) {
				return deps.access().Exec(cmd.Context(), app.ExecRequest{Selector: global.selector(cmd, deps), Role: role,
					User: user, Cmd: args, Session: s})
			})
		},
	}
	// Flags after the command belong to it.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&name, "service", "odoo", "container to run in: odoo or postgres")
	cmd.Flags().StringVar(&user, "user", "", "user to run as instead of the image's user")
	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "never allocate a terminal")
	markExecFamily(cmd)
	return cmd
}

func newShellCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var database string
	var noTTY bool
	cmd := &cobra.Command{
		Use:   "shell [-d DATABASE] [-- ODOO_ARGS...]",
		Short: "Open Odoo's shell on a database of the running environment",
		Long: "Open Odoo's shell in the running Odoo container. Without -d, the only database\n" +
			"is used. A script piped on stdin runs without a terminal, then its transaction\n" +
			"is rolled back: call env.cr.commit() to keep its changes. An uncaught exception\n" +
			"exits 1. The shell never serves HTTP, so it works with workers above 0.\n" +
			"Arguments after -- go to Odoo, for example --log-level=warn.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectOutput(cmd, global); err != nil {
				return err
			}
			odooArgs, err := afterDash(cmd, args)
			if err != nil {
				return err
			}
			return session(deps, noTTY, func(s app.Session) (app.SessionResult, error) {
				return deps.access().Shell(cmd.Context(), app.ShellRequest{Selector: global.selector(cmd, deps),
					Database: database, Args: odooArgs, Session: s})
			})
		},
	}
	cmd.Flags().StringVarP(&database, "database", "d", "", "database to open; default: the only one")
	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "never allocate a terminal")
	markExecFamily(cmd)
	return cmd
}

func newPsqlCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var database string
	var noTTY bool
	cmd := &cobra.Command{
		Use:   "psql [-d DATABASE] [-- PSQL_ARGS...]",
		Short: "Open psql on a database of the running environment",
		Long: "Open psql in the running PostgreSQL container. Without -d, the only database is\n" +
			"used, or postgres when there is none. A script stops at its first error\n" +
			"(ON_ERROR_STOP=1) and exits non-zero. Arguments after -- go to psql.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectOutput(cmd, global); err != nil {
				return err
			}
			psqlArgs, err := afterDash(cmd, args)
			if err != nil {
				return err
			}
			return session(deps, noTTY, func(s app.Session) (app.SessionResult, error) {
				return deps.access().Psql(cmd.Context(), app.PsqlRequest{Selector: global.selector(cmd, deps),
					Database: database, Args: psqlArgs, Session: s})
			})
		},
	}
	cmd.Flags().StringVarP(&database, "database", "d", "", "database to open; default: the only one, else postgres")
	cmd.Flags().BoolVar(&noTTY, "no-tty", false, "never allocate a terminal")
	markExecFamily(cmd)
	return cmd
}
