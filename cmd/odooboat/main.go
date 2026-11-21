/*
Copyright © 2026 Remy Zulauff <remy.zulauff@icloud.com>
*/
package main

import (
	"context"
	"os"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli"
	"github.com/remyz17/odooboat/internal/config"
	"github.com/remyz17/odooboat/internal/runtime"
	"github.com/remyz17/odooboat/internal/runtime/docker"
	statefile "github.com/remyz17/odooboat/internal/state/file"
)

func main() {
	// Signals cancel the command, unless an exec session captures them to
	// forward them (ADR 0010 §7).
	ctx, interrupts, stop := cli.WatchSignals(context.Background())
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		_, _ = os.Stderr.WriteString("odooboat: " + err.Error() + "\n")
		os.Exit(cli.ExitError)
	}
	store := statefile.New()
	runtimes := runtime.Connectors{config.RuntimeKindDocker: docker.Connector{}}
	os.Exit(cli.Run(ctx, os.Args[1:], cli.Dependencies{
		Config: app.NewConfigService(), Workspace: app.NewWorkspaceService(store, store, runtimes, cli.Version),
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, WorkingDir: workingDir,
		Terminal: cli.NewTerminal(os.Stdin, os.Stdout), Interrupts: interrupts,
	}))
}
