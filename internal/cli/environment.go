package cli

import (
	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

func newEnvironmentCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "environment", Short: "Inspect, bind, verify, and build environments",
		Args: noSubcommand, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newEnvironmentShowCommand(deps, global), newEnvironmentBindCommand(deps, global),
		newEnvironmentVerifyCommand(deps, global), newEnvironmentBuildCommand(deps, global))
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
			if err := global.print(deps, result, environmentShowText(result), nil); err != nil {
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
			return global.print(deps, result, nil, nil)
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newEnvironmentVerifyCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "verify", Short: "Reach the bound runtime and record or compare its engine identity", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selector := global.selector(cmd, deps)
			result, err := deps.Workspace.VerifyEnvironment(cmd.Context(), app.EnvironmentVerifyRequest{
				Selector: selector,
				Wait:     lockWait, Reporter: global.reporter(),
			})
			if err != nil {
				return err
			}
			return global.print(deps, result, nil, nil)
		},
	}
	addOverrideFlags(cmd)
	return cmd
}

func newEnvironmentBuildCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var pull bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the environment image, or reuse it when its inputs are unchanged",
		Long: "Build the environment image from the base image, the PostgreSQL client of the\n" +
			"environment's server version, the Odoo requirements, and embedded sources.\n" +
			"An image with the same inputs is reused. Progress is written to stderr.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.BuildEnvironment(cmd.Context(), app.EnvironmentBuildRequest{
				Selector: global.selector(cmd, deps),
				Wait:     lockWait,
				Pull:     pull, Reporter: global.reporter(),
			})
			if err != nil {
				return err
			}
			return global.print(deps, result, buildText(result), nil)
		},
	}
	cmd.Flags().BoolVar(&pull, "pull", false, "pull the base image, pinned by digest, if it is missing")
	addOverrideFlags(cmd)
	return cmd
}
