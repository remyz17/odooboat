package cli

import (
	"fmt"
	"time"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

func newEnvironmentCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "environment", Short: "Inspect, bind, and verify environment identity",
		Args: noSubcommand, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newEnvironmentShowCommand(deps, global), newEnvironmentBindCommand(deps, global),
		newEnvironmentVerifyCommand(deps, global))
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

func newEnvironmentVerifyCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "verify", Short: "Reach the bound runtime and record or compare its engine identity", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selector := global.selector(cmd, deps)
			result, err := deps.Workspace.VerifyEnvironment(cmd.Context(), app.EnvironmentVerifyRequest{
				Selector: selector,
				Wait: app.WaitOptions{Policy: app.Wait, OnWait: func(holder *app.LockHolder) {
					fmt.Fprintln(deps.Stderr, waitingMessage(holder))
				}},
			})
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

func waitingMessage(holder *app.LockHolder) string {
	if holder == nil {
		return "odooboat: waiting for the environment lock held by an unknown process"
	}
	return fmt.Sprintf("odooboat: waiting for the environment lock held by %q (pid %d on %s since %s)",
		holder.Operation, holder.PID, holder.Host, holder.StartedAt.Local().Format(time.TimeOnly))
}
