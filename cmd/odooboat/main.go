/*
Copyright © 2026 Remy Zulauff <remy.zulauff@icloud.com>
*/
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/remyz17/odooboat/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
