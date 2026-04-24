// Command pcr-smoke runs end-to-end smoke / integration checks against a
// pcr-server instance. Two modes:
//
//	# Spawn an ephemeral local server, build it if necessary, run, kill.
//	pcr-smoke --start-local
//
//	# Hit an already-running server (e.g. docker-compose up).
//	pcr-smoke --base-url=http://localhost:8080 --token=changeme
//
// Exits non-zero on any failed case. Each case prints a single PASS / FAIL
// line, with the failing reason and (in --verbose) the request/response.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Default port differs from the server's default (:8080) so --start-local
// doesn't collide with a developer's already-running compose instance.
const defaultLocalAddr = ":18080"

func main() {
	os.Exit(run())
}

// run owns all deferred cleanup. main() is just an os.Exit driver so
// deferred local-server shutdown actually fires before the process ends.
//
// Exit codes:
//
//	0 - all checks passed
//	1 - one or more checks failed
//	2 - usage error or local server failed to start
func run() int {
	var (
		baseURL    = flag.String("base-url", "", "Base URL of the server (default: http://127.0.0.1<addr> in --start-local; required otherwise)")
		token      = flag.String("token", "smoke-token-abc", "Bearer token (must be in PCR_API_TOKENS on the target server)")
		startLocal = flag.Bool("start-local", false, "Build and spawn a local pcr-server against an ephemeral DB; kill it on exit")
		binary     = flag.String("binary", "./bin/pcr-server", "Path to pcr-server binary (built automatically if missing in --start-local)")
		addr       = flag.String("addr", defaultLocalAddr, "Listen address for --start-local mode")
		keepData   = flag.Bool("keep-data", false, "Don't delete the temp DB on exit (--start-local debug aid)")
		verbose    = flag.Bool("v", false, "Verbose: print each HTTP request and response preview")
	)
	flag.Parse()

	resolvedURL := *baseURL
	if resolvedURL == "" {
		if *startLocal {
			resolvedURL = "http://127.0.0.1" + *addr
		} else {
			_, _ = fmt.Fprintln(os.Stderr, "error: --base-url is required when --start-local is not set")
			return 2
		}
	}

	// Ctrl-C cleans up the local server via the deferred srv.stop below.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var srv *localServer
	if *startLocal {
		srv = newLocalServer(*binary, *addr, *token, *keepData)
		fmt.Printf("starting local server (binary=%s, addr=%s)\n", *binary, *addr)
		if err := srv.start(ctx); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "fatal: start local server: %v\n", err)
			return 2
		}
		defer srv.stop()
		if err := srv.waitReady(ctx, resolvedURL); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
			srv.dumpLog(os.Stderr)
			return 2
		}
	}

	c, err := newClient(resolvedURL, *token, *verbose)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fatal: build client: %v\n", err)
		return 2
	}

	fmt.Printf("smoke checks against %s\n", resolvedURL)
	failed := runCases(ctx, c)

	if failed > 0 && srv != nil {
		_, _ = fmt.Fprintln(os.Stderr)
		srv.dumpLog(os.Stderr)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// runCases executes every case in order, printing a PASS/FAIL line for
// each. Returns the number of failed cases. Cases share a fixture so a
// later case can reference an event created earlier.
func runCases(ctx context.Context, c *client) int {
	cases := allCases()
	f := &fixture{}

	failed := 0
	start := time.Now()

	for i, tc := range cases {
		// Per-case timeout so a hanging server doesn't stall the whole run.
		caseCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := tc.Run(caseCtx, c, f)
		cancel()

		idx := fmt.Sprintf("[%2d/%d]", i+1, len(cases))
		if err != nil {
			fmt.Printf("  %s FAIL  %s\n        %v\n", idx, tc.Name, err)
			failed++
		} else {
			fmt.Printf("  %s PASS  %s\n", idx, tc.Name)
		}
	}

	elapsed := time.Since(start).Round(time.Millisecond)
	fmt.Println()
	if failed == 0 {
		fmt.Printf("all %d checks passed in %s\n", len(cases), elapsed)
	} else {
		fmt.Printf("%d/%d checks failed in %s\n", failed, len(cases), elapsed)
	}
	return failed
}
