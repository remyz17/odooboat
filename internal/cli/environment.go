package cli

import (
	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

func newEnvironmentCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "environment", Short: "Inspect and bind environment identity",
		Args: noSubcommand, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newEnvironmentShowCommand(deps, global), newEnvironmentBindCommand(deps, global))
	return cmd
}

func newEnvironmentShowCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "show", Short: "Compare the desired and bound runtime connections", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.ShowEnvironment(cmd.Context(), app.EnvironmentShowRequest{Selector: global.selector(cmd, deps)})
			if err != nil {
				return err
			}
			format, err := global.formatOf(result.Preferences)
			if err != nil {
				return err
			}
			if err := render(deps.Stdout, format, result); err != nil {
				return err
			}
			if result.WorkspaceStatus == app.WorkspaceStatusDuplicate {
				return app.ErrDuplicateWorkspace
			}
			return nil
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newEnvironmentBindCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "bind", Short: "Bind the environment to its exact resolved runtime connection", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.BindEnvironment(cmd.Context(), app.EnvironmentBindRequest{Selector: global.selector(cmd, deps)})
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
