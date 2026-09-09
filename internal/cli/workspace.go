package cli

import (
	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

func newWorkspaceCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "workspace", Short: "Inspect and recover local workspace identity",
		Args: noSubcommand, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newWorkspaceShowCommand(deps, global), newWorkspaceRekeyCommand(deps, global))
	return cmd
}

func newWorkspaceShowCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "show", Short: "Show workspace identity status without changing state", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.Show(cmd.Context(), app.WorkspaceShowRequest{Selector: global.selector(cmd, deps)})
			if err != nil {
				return err
			}
			format, err := global.formatOrDefault()
			if err != nil {
				return err
			}
			if err := render(deps.Stdout, format, result); err != nil {
				return err
			}
			if result.Status == app.WorkspaceStatusDuplicate {
				return app.ErrDuplicateWorkspace
			}
			return nil
		},
	}
}

func newWorkspaceRekeyCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use: "rekey", Short: "Assign a new identity to a detected workspace copy", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.Rekey(cmd.Context(), app.WorkspaceRekeyRequest{Selector: global.selector(cmd, deps)})
			if err != nil {
				return err
			}
			format, err := global.formatOrDefault()
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, result)
		},
	}
}
