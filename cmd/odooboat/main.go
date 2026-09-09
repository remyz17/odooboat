/*
Copyright © 2026 Remy Zulauff <remy.zulauff@icloud.com>
*/
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/remyz17/odooboat/internal/app"
	"github.com/remyz17/odooboat/internal/cli"
	statefile "github.com/remyz17/odooboat/internal/state/file"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workingDir, err := os.Getwd()
	if err != nil {
		_, _ = os.Stderr.WriteString("odooboat: " + err.Error() + "\n")
		os.Exit(cli.ExitError)
	}
	os.Exit(cli.Run(ctx, os.Args[1:], cli.Dependencies{
		Config: app.NewConfigService(), Workspace: app.NewWorkspaceService(statefile.New()),
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, WorkingDir: workingDir,
	}))
}
