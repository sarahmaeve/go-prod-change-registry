// These tests cover the process entry point: the exit code run reports and the
// signal-handler cleanup that the old os.Exit-inside-run arrangement skipped.
// run is unexported and a main package cannot be imported, so these tests live
// in package main rather than in an external test package.
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// pcrcli's exit codes are unexported and its export_test.go aliases are visible
// only to the pcrcli package's own tests, so these are the sysexits literals
// that pcrcli.Run returns: EX_OK and EX_USAGE.
const (
	exitOK    = 0
	exitUsage = 64
)

// mainHelperEnv puts the re-executed test binary into entry-point mode.
const mainHelperEnv = "PCR_TEST_RUN_MAIN"

// TestMain re-executes this binary as the real command when the helper
// variable is set, which is the only way to observe what main hands the OS:
// calling main in-process would terminate the test run.
func TestMain(m *testing.M) {
	if os.Getenv(mainHelperEnv) != "1" {
		os.Exit(m.Run())
	}
	os.Args[0] = "pcr"
	main()
}

// TestRunReportsExitCode pins the codes run returns for its callers. The
// subtests mutate process globals (os.Args, the standard streams and the
// environment), so neither they nor the parent may run in parallel.
//
//nolint:paralleltest // run reads os.Args and the standard streams, so these cases must stay serial.
func TestRunReportsExitCode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "version", args: []string{"version"}, want: exitOK},
		{name: "help", args: []string{"--help"}, want: exitOK},
		{name: "unknown flag", args: []string{"--no-such-flag"}, want: exitUsage},
		{name: "config without value", args: []string{"--config"}, want: exitUsage},
		{name: "command without subcommand", args: []string{"events"}, want: exitUsage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runWithArgs(t, test.args...); got != test.want {
				t.Errorf("run() with args %q = %d, want %d", test.args, got, test.want)
			}
		})
	}
}

// TestRunReleasesSignalContext is the behavioural evidence that the deferred
// stop executes. stop is not reachable from outside run, but the goroutine
// signal.NotifyContext starts blocks until either a signal arrives or stop
// cancels the context, so a goroutine that outlives run means the defer never
// ran -- exactly the leak os.Exit used to cause.
//
//nolint:paralleltest // run reads os.Args and the standard streams, so this test must stay serial.
func TestRunReleasesSignalContext(t *testing.T) {
	// The first signal.Notify of a process starts os/signal's watcher
	// goroutine, which never exits; warm it up before taking the baseline so
	// it is not mistaken for a leak.
	runWithArgs(t, "version")
	baseline := stableGoroutineCount(t)

	const calls = 5
	for range calls {
		runWithArgs(t, "version")
	}

	if got := waitForGoroutineCount(t, baseline); got > baseline {
		t.Errorf("run() x%d left the goroutine count at %d, want <= %d; the deferred stop did not run", calls, got, baseline)
	}
}

// TestMainPropagatesExitCode checks the one statement in main that cannot be
// exercised in-process: that the OS sees the code run returned.
//
//nolint:paralleltest // the subtests re-execute this binary and must stay serial with the tests that swap process globals.
func TestMainPropagatesExitCode(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "version", args: []string{"version"}, want: exitOK},
		{name: "version with global option", args: []string{"--output", "json", "version"}, want: exitOK},
		{name: "unknown flag", args: []string{"--no-such-flag"}, want: exitUsage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), binary, test.args...) //nolint:gosec // G204: this test binary and the table-owned arguments are trusted.
			command.Env = append(os.Environ(),
				mainHelperEnv+"=1",
				"PCR_CONFIG="+filepath.Join(t.TempDir(), "pcr.toml"),
			)
			output, err := command.CombinedOutput()
			got := command.ProcessState.ExitCode()
			if err != nil && got < 0 {
				t.Fatalf("main() with args %q did not exit cleanly: %v, output %q", test.args, err, output)
			}
			if got != test.want {
				t.Errorf("main() with args %q exited %d, want %d; output %q", test.args, got, test.want, output)
			}
		})
	}
}

// TestRunConfinesExitToMain guards the structure the gocritic exitAfterDefer
// fix established. The leak the defer prevents is only observable inside the
// process, so moving os.Exit back into run would kill a test rather than fail
// an assertion; parsing main.go turns both regressions into a plain failure.
//
//nolint:paralleltest // kept serial with the rest of the file, which swaps process globals.
func TestRunConfinesExitToMain(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parser.ParseFile(main.go) error = %v", err)
	}
	functions := map[string]*ast.FuncDecl{}
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil {
			functions[function.Name.Name] = function
		}
	}

	runFunction, ok := functions["run"]
	if !ok {
		t.Fatalf("main.go declares no run function, want one that returns the exit code to main")
	}
	if position, found := findCall(runFunction, "os", "Exit"); found {
		t.Errorf("run() calls os.Exit at %s, want the exit confined to main() so run's defers execute", fileSet.Position(position))
	}
	if mainFunction, found := functions["main"]; !found {
		t.Errorf("main.go declares no main function, want one")
	} else if _, callsExit := findCall(mainFunction, "os", "Exit"); !callsExit {
		t.Errorf("main() does not call os.Exit, want it to report run's exit code to the OS")
	}

	stop, found := signalContextStopName(runFunction)
	if !found {
		t.Fatalf("run() does not call signal.NotifyContext, want it to install the SIGINT and SIGTERM handlers")
	}
	if !defersCall(runFunction, stop) {
		t.Errorf("run() does not defer %s(), want the signal handlers released and the context cancelled before returning", stop)
	}
}

// runWithArgs calls run with the given argv, isolating the process globals run
// reads and restoring them before returning so a failure message from the
// caller still reaches the real stdout.
func runWithArgs(t *testing.T, args ...string) int {
	t.Helper()
	// version does not read the file, but pointing at a temp path keeps the
	// developer's real configuration out of the test.
	t.Setenv("PCR_CONFIG", filepath.Join(t.TempDir(), "pcr.toml"))

	originalArgs, originalStdout, originalStderr := os.Args, os.Stdout, os.Stderr
	defer func() {
		os.Args, os.Stdout, os.Stderr = originalArgs, originalStdout, originalStderr
	}()

	os.Args = append([]string{"pcr"}, args...)
	os.Stdout = discardFile(t, "stdout")
	os.Stderr = discardFile(t, "stderr")
	return run()
}

// discardFile returns a writable file in the test's temp directory. A file
// rather than a pipe keeps the command's output from blocking on a full pipe
// buffer with no reader.
func discardFile(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), name)) //nolint:gosec // G304: the path is a test-owned temporary directory, not user input.
	if err != nil {
		t.Fatalf("os.Create(%s) error = %v", name, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// stableGoroutineCount returns a goroutine count that has stopped moving, so a
// goroutine still winding down from earlier work is not counted as a leak.
func stableGoroutineCount(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	previous := runtime.NumGoroutine()
	for repeats := 0; repeats < 3 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
		current := runtime.NumGoroutine()
		if current == previous {
			repeats++
			continue
		}
		previous, repeats = current, 0
	}
	return previous
}

// waitForGoroutineCount polls until the live goroutine count falls to want or
// the deadline passes, and returns the last reading. Polling is required
// because the signal.NotifyContext goroutine observes the cancelled context
// asynchronously.
func waitForGoroutineCount(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	got := runtime.NumGoroutine()
	for got > want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		got = runtime.NumGoroutine()
	}
	return got
}

// findCall reports the position of the first pkg.name call in the function.
func findCall(function *ast.FuncDecl, pkg, name string) (token.Pos, bool) {
	var position token.Pos
	found := false
	ast.Inspect(function, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == name {
			if identifier, ok := selector.X.(*ast.Ident); ok && identifier.Name == pkg {
				position, found = call.Pos(), true
				return false
			}
		}
		return true
	})
	return position, found
}

// signalContextStopName returns the name bound to the stop function returned
// by signal.NotifyContext.
func signalContextStopName(function *ast.FuncDecl) (string, bool) {
	stop := ""
	ast.Inspect(function, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) != 2 {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "NotifyContext" {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || identifier.Name != "signal" {
			return true
		}
		if name, ok := assignment.Lhs[1].(*ast.Ident); ok {
			stop = name.Name
			return false
		}
		return true
	})
	return stop, stop != ""
}

// defersCall reports whether the function defers a call to the named function.
func defersCall(function *ast.FuncDecl, name string) bool {
	deferred := false
	ast.Inspect(function, func(node ast.Node) bool {
		statement, ok := node.(*ast.DeferStmt)
		if !ok {
			return true
		}
		if identifier, ok := statement.Call.Fun.(*ast.Ident); ok && identifier.Name == name {
			deferred = true
			return false
		}
		return true
	})
	return deferred
}
