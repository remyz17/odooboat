package cli

import (
	"github.com/remyz17/odooboat/internal/app"
	"github.com/spf13/cobra"
)

// The lifecycle commands act on the selected environment, the default one
// unless --env says otherwise (ADR 0001 §2). Progress goes to stderr and the
// result to stdout.

func newUpCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	var pull bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the environment: build its image, create its resources, and wait until Odoo is ready",
		Long: "Bring the environment to its desired state: build or reuse its image, create the\n" +
			"network and volumes, start PostgreSQL and Odoo, and wait until both are ready.\n" +
			"Running it again changes only what differs. Odoo is published on 127.0.0.1 on a\n" +
			"port chosen by the runtime; the URL is in the result.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.Up(cmd.Context(), app.UpRequest{
				Selector: global.selector(cmd, deps), Wait: lockWait, Pull: pull, Reporter: global.reporter(),
			})
			if err != nil {
				return err
			}
			global.warn(result.Warnings)
			format, err := global.formatOf(result.Preferences)
			if err != nil {
				return err
			}
			return render(deps.Stdout, format, result)
		},
	}
	cmd.Flags().BoolVar(&pull, "pull", false, "pull missing base and PostgreSQL images, pinned by digest")
	addOverrideFlags(cmd)
	return cmd
}

func newDownCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop the environment and remove its containers and network; volumes are kept",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.Down(cmd.Context(), app.DownRequest{
				Selector: global.selector(cmd, deps), Wait: lockWait, Reporter: global.reporter(),
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

func newStatusCommand(deps Dependencies, global *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the environment's resources in its runtime, without changing anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := deps.Workspace.Status(cmd.Context(), app.StatusRequest{Selector: global.selector(cmd, deps)})
			if err != nil {
				return err
			}
			global.warn(result.Warnings)
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

// lockWait waits for the environment lock; the wait is reported as an event.
var lockWait = app.WaitOptions{Policy: app.Wait}
