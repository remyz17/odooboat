package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli/ui"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/spf13/cobra"
)

// TODO move it to build-time ldflags
var Version = "dev"

// Process exit codes.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitConfig  = 3
	ExitState   = 4
	ExitRuntime = 5
	ExitBusy    = 6
	// ExitImage: a base image is missing or invalid, or an image build failed.
	ExitImage = 7
	// ExitSource: an Odoo source could not be fetched, is not a usable Odoo
	// tree, or has the wrong series.
	ExitSource = 8
	// ExitConflict: a resource name is held by something odooboat does not
	// own, an owned resource is read-only, or existing data is incompatible.
	ExitConflict = 9
	// ExitNotReady: a container did not become ready, or stopped.
	ExitNotReady = 10
	// ExitNotRunning: the environment has no container to read or enter.
	ExitNotRunning = 11
	// ExitToolFailure: an exec-family command failed in Odooboat itself,
	// before or while running the remote command (ADR 0010 §6).
	ExitToolFailure = 125
)

// ExitStatus carries a remote command's exit code: Run exits with it and
// prints nothing, the command having already written its own output.
type ExitStatus struct{ Code int }

func (e *ExitStatus) Error() string { return fmt.Sprintf("remote command exited %d", e.Code) }

// annotationExecFamily marks exec, shell, and psql: the remote command's
// code is returned unchanged, so every Odooboat failure exits 125 instead
// of a code a remote command could also return (ADR 0010 §6).
const annotationExecFamily = "odooboat.exec-family"

func markExecFamily(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationExecFamily] = "true"
}

// ErrUsage marks a failure of CLI syntax, the only class that prints usage text.
var ErrUsage = errors.New("invalid usage")

type UsageError struct {
	Cmd *cobra.Command
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

func usageErrorf(cmd *cobra.Command, format string, args ...any) error {
	return &UsageError{Cmd: cmd, Err: fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))}
}

type Dependencies struct {
	Config     app.ConfigService
	Workspace  app.WorkspaceService
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	WorkingDir string
	// Terminal is the local terminal; nil means none (no TTY is allocated).
	Terminal Terminal
	// Interrupts routes signals to exec sessions; nil means they are not
	// forwarded and only cancel the context.
	Interrupts Interrupts
	// Access serves logs, exec, shell, and psql; nil means Workspace.
	Access AccessService
	// Getenv reads process controls such as TERM and CI; nil means os.Getenv.
	Getenv func(string) string
	// Now stamps progress events; nil means time.Now.
	Now func() time.Time
}

func (d Dependencies) getenv() func(string) string {
	if d.Getenv == nil {
		return os.Getenv
	}
	return d.Getenv
}

func (d Dependencies) now() func() time.Time {
	if d.Now == nil {
		return time.Now
	}
	return d.Now
}

// AccessService is the part of the workspace service that observes and
// enters a running environment (ADR 0010).
type AccessService interface {
	Logs(ctx context.Context, req app.LogsRequest) error
	Exec(ctx context.Context, req app.ExecRequest) (app.SessionResult, error)
	Shell(ctx context.Context, req app.ShellRequest) (app.SessionResult, error)
	Psql(ctx context.Context, req app.PsqlRequest) (app.SessionResult, error)
}

func (d Dependencies) access() AccessService {
	if d.Access == nil {
		return d.Workspace
	}
	return d.Access
}

func (d Dependencies) terminal() Terminal {
	if d.Terminal == nil {
		return noTerminal{}
	}
	return d.Terminal
}

type globalFlags struct {
	projectPath    string
	userConfigPath string
	environment    string
	output         string
	progress       string
	stepOutput     string
	// present is the invocation's presenter, set before any command runs.
	present *presenter
}

// reporter is where operations report their events; nil discards them.
func (g *globalFlags) reporter() app.Reporter {
	if g.present == nil {
		return nil
	}
	return g.present.Reporter()
}

// setRaw tells the presenter whether the terminal is in raw mode.
func (g *globalFlags) setRaw(raw bool) {
	if g.present != nil {
		g.present.SetRaw(raw)
	}
}

// warn echoes result warnings through the presenter.
func (g *globalFlags) warn(messages []string) {
	if g.present != nil {
		g.present.Warn(messages)
	}
}

func (g *globalFlags) selector(cmd *cobra.Command, deps Dependencies) app.Selector {
	return app.Selector{
		WorkingDir:     deps.WorkingDir,
		UserConfigPath: g.userConfigPath,
		ProjectPath:    g.projectPath,
		Environment:    g.environment,
		Patch:          invocationPatch(cmd),
	}
}

func NewRootCommand(deps Dependencies) *cobra.Command {
	global := &globalFlags{}

	root := &cobra.Command{
		Use:           "odooboat",
		Short:         "Manage Odoo development environments",
		Long:          "Odooboat manages reproducible Odoo development environments from a committed project definition.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noSubcommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetIn(deps.Stdin)
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &UsageError{Cmd: cmd, Err: fmt.Errorf("%w: %w", ErrUsage, err)}
	})
	// Every command reports through one presenter, chosen before it runs
	// (ADR 0011 §7).
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		s := presentation{progress: global.progress, stepOutput: global.stepOutput, output: global.output, userConfig: global.userConfigPath}
		if err := s.validate(cmd); err != nil {
			return err
		}
		global.present = s.presenter(deps)
		cmd.SetContext(withPresenter(cmd.Context(), global.present))
		return nil
	}

	flags := root.PersistentFlags()
	flags.StringVar(&global.projectPath, "project", "", "path to the "+config.ProjectFileName+" project definition file")
	flags.StringVar(&global.userConfigPath, "user-config", "", "path to the user configuration file")
	flags.StringVar(&global.environment, "env", "", "environment to resolve (default \""+config.DefaultEnvironment+"\")")
	flags.StringVarP(&global.output, "output", "o", "", "output format: yaml or json")
	flags.StringVar(&global.progress, "progress", string(ui.FormatAuto), "progress on stderr: auto, plain, or json")
	flags.StringVar(&global.stepOutput, "step-output", string(ui.RetainOnFailure),
		"output of finished steps: on-failure or all (on-failure shows the last 50 lines of a failed or interrupted step; output held by a process killed with SIGKILL is lost)")

	root.AddCommand(newConfigCommand(deps, global))
	root.AddCommand(newInitCommand(deps, global))
	root.AddCommand(newWorkspaceCommand(deps, global))
	root.AddCommand(newEnvironmentCommand(deps, global))
	root.AddCommand(newUpCommand(deps, global), newDownCommand(deps, global), newStatusCommand(deps, global))
	root.AddCommand(newLogsCommand(deps, global), newExecCommand(deps, global), newShellCommand(deps, global), newPsqlCommand(deps, global))
	return root
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	return runRoot(ctx, NewRootCommand(deps), args, deps)
}

func runRoot(ctx context.Context, root *cobra.Command, args []string, deps Dependencies) int {
	root.SetArgs(args)
	cmd, err := root.ExecuteContextC(ctx)
	present := presenterOf(cmd)
	var status *ExitStatus
	if err == nil || errors.As(err, &status) {
		if present != nil {
			present.Close()
		}
		if err == nil {
			return ExitOK
		}
		return status.Code
	}
	if present == nil {
		// The failure came before the command ran, often while parsing flags.
		present = scanPresentation(args).presenter(deps)
	}
	var usage *UsageError
	usageText := ""
	if errors.As(err, &usage) && usage.Cmd != nil {
		usageText = usage.Cmd.UsageString()
	}
	present.Fail(err, usageText)
	if cmd != nil && cmd.Annotations[annotationExecFamily] != "" {
		return ExitToolFailure
	}
	return ExitCode(err)
}

func noSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageErrorf(cmd, "unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

func exactArgs(n int, hint string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageErrorf(cmd, "expected %s", hint)
		}
		return nil
	}
}
