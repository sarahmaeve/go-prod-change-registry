// Command pcr queries and records production changes in PCR.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrcli"
)

var (
	version   = "devel"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	os.Exit(run())
}

// run returns the exit code rather than calling os.Exit itself so that the
// deferred stop runs: os.Exit skips defers, which would leave the SIGINT and
// SIGTERM handlers installed and the context unreleased.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return pcrcli.Run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr, pcrcli.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    buildDate,
	})
}
