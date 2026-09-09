package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/spf13/cobra"
)

// TODO move it to build-time ldflags
var Version = "dev"

// Process exit codes.
const (
	ExitOK     = 0
	ExitError  = 1
	ExitUsage  = 2
	ExitConfig = 3
	ExitState  = 4
)

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
}

type globalFlags struct {
	projectPath    string
	userConfigPath string
	environment    string
	output         string
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

	flags := root.PersistentFlags()
	flags.StringVar(&global.projectPath, "project", "", "path to the "+config.ProjectFileName+" project definition file")
	flags.StringVar(&global.userConfigPath, "user-config", "", "path to the user configuration file")
	flags.StringVar(&global.environment, "env", "", "environment to resolve (default \""+config.DefaultEnvironment+"\")")
	flags.StringVarP(&global.output, "output", "o", "", "output format: yaml or json")

	root.AddCommand(newConfigCommand(deps, global))
	root.AddCommand(newInitCommand(deps, global))
	root.AddCommand(newWorkspaceCommand(deps, global))
	root.AddCommand(newEnvironmentCommand(deps, global))
	return root
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	root := NewRootCommand(deps)
	root.SetArgs(args)

	if err := root.ExecuteContext(ctx); err != nil {
		var usage *UsageError
		if errors.As(err, &usage) && usage.Cmd != nil {
			fmt.Fprint(deps.Stderr, usage.Cmd.UsageString())
		}
		fmt.Fprintln(deps.Stderr, "odooboat:", err)
		return ExitCode(err)
	}
	return ExitOK
}

func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrUsage):
		return ExitUsage
	case errors.Is(err, config.ErrNotFound),
		errors.Is(err, config.ErrDecode),
		errors.Is(err, config.ErrAuthored),
		errors.Is(err, config.ErrResolved):
		return ExitConfig
	case errors.Is(err, app.ErrState):
		return ExitState
	default:
		return ExitError
	}
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
