package cli

import (
	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/spf13/cobra"
)

// Flags that carry a per-invocation configuration override. Anything else on the
// command line is a CLI concern and stays out of InvocationPatch.
// Thoses are only examples for now, more will be added as the CLI matures.
const (
	flagOdooVersion       = "odoo-version"
	flagPython            = "python"
	flagPostgres          = "postgres"
	flagRuntimeConnection = "runtime-connection"
)

type validateOutput struct {
	Valid   bool           `yaml:"valid" json:"valid"`
	Sources config.Sources `yaml:"sources" json:"sources"`
}

type showOutput struct {
	Sources config.Sources        `yaml:"sources" json:"sources"`
	Config  config.ResolvedConfig `yaml:"config" json:"config"`
}

type initOutput struct {
	Created string `yaml:"created" json:"created"`
}

func addOverrideFlags(cmd *cobra.Command) {
	cmd.Flags().String(flagOdooVersion, "", "override the Odoo version for this invocation")
	cmd.Flags().String(flagPython, "", "override the Python version for this invocation")
	cmd.Flags().Int(flagPostgres, 0, "override the PostgreSQL major version for this invocation")
	cmd.Flags().String(flagRuntimeConnection, "", "override the runtime connection for this invocation")
}

func invocationPatch(cmd *cobra.Command) config.InvocationPatch {
	patch := config.InvocationPatch{}
	if value, ok := changedString(cmd, flagOdooVersion); ok {
		patch.OdooVersion = &value
	}
	if value, ok := changedString(cmd, flagPython); ok {
		patch.Python = &value
	}
	if changed(cmd, flagPostgres) {
		// The flag parsed as an int already; the error is impossible here.
		value, _ := cmd.Flags().GetInt(flagPostgres)
		patch.Postgres = &value
	}
	if value, ok := changedString(cmd, flagRuntimeConnection); ok {
		patch.RuntimeConnection = &value
	}
	return patch
}

func changedString(cmd *cobra.Command, name string) (string, bool) {
	if !changed(cmd, name) {
		return "", false
	}
	return cmd.Flags().Lookup(name).Value.String(), true
}

func changed(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed
}

func newConfigCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the resolved configuration",
		Args:  noSubcommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newConfigValidateCommand(deps, global),
		newConfigShowCommand(deps, global),
		newConfigExplainCommand(deps, global),
	)
	return cmd
}

func newConfigValidateCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Resolve and validate every applicable configuration source",
		Long:  "Resolves and validates every applicable configuration source without inspecting or mutating a runtime.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Config.Validate(cmd.Context(), app.ValidateRequest{
				Selector: global.selector(cmd, deps),
			})
			if err != nil {
				return err
			}
			return global.print(deps, validateOutput{Valid: true, Sources: result.Sources}, nil, nil)
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newConfigShowCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the resolved configuration with secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Config.Show(cmd.Context(), app.ShowRequest{
				Selector: global.selector(cmd, deps),
			})
			if err != nil {
				return err
			}
			return global.print(deps, showOutput{Sources: result.Sources, Config: result.Config}, nil, nil)
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newConfigExplainCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain FIELD",
		Short: "Report a field's effective value and where it came from",
		Long:  "Reports a field's effective value, its winning source and the sources it overrode. A secret field reports metadata and source but never its value.",
		Args:  exactArgs(1, "exactly one FIELD argument, for example \"odooVersion\""),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := app.ExplainRequest{
				Selector: global.selector(cmd, deps),
				Field:    config.FieldPath(args[0]),
			}
			result, err := deps.Config.Explain(cmd.Context(), request)
			if err != nil {
				return err
			}
			return global.print(deps, result, explainText(result), nil)
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newInitCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a minimal " + config.ProjectFileName + " in the current directory",
		Long:  "Creates a minimal schema-1 " + config.ProjectFileName + ". It never overwrites an existing project definition.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Config.Init(cmd.Context(), app.InitRequest{
				Dir:         deps.WorkingDir,
				ProjectName: name,
			})
			if err != nil {
				return err
			}
			return global.print(deps, initOutput{Created: result.Path}, nil, nil)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (default: the directory name)")
	return cmd
}
