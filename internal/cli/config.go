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
	cmd.Flags().String(flagRuntimeConnection, "", "override the runtime connection for this invocation")
}

func invocationPatch(cmd *cobra.Command) config.InvocationPatch {
	patch := config.InvocationPatch{}
	if value, ok := changedString(cmd, flagOdooVersion); ok {
		patch.OdooVersion = &value
	}
	if value, ok := changedString(cmd, flagRuntimeConnection); ok {
		patch.RuntimeConnection = &value
	}
	return patch
}

func changedString(cmd *cobra.Command, name string) (string, bool) {
	flag := cmd.Flags().Lookup(name)
	if flag == nil || !flag.Changed {
		return "", false
	}
	return flag.Value.String(), true
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
			format, err := global.format(result.Config)
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, validateOutput{Valid: true, Sources: result.Sources})
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
			format, err := global.format(result.Config)
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, showOutput{Sources: result.Sources, Config: result.Config})
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
			format, err := global.formatOf(result.Preferences)
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, result)
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
			format, err := global.formatOrDefault()
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, initOutput{Created: result.Path})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name (default: the directory name)")
	return cmd
}
