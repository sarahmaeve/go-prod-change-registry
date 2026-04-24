package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// localServer manages a pcr-server subprocess against an ephemeral DB.
// Lifecycle: build (if missing) -> spawn -> waitReady -> stop.
type localServer struct {
	binaryPath string
	addr       string
	token      string
	keepData   bool

	cmd     *exec.Cmd
	tempDir string
	logFile *os.File
	// exited is closed by the Wait() goroutine launched in start() once the
	// subprocess terminates. Both waitReady and stop consume it: waitReady
	// to fail-fast if the server dies during startup, stop to know when the
	// SIGTERM (or SIGKILL fallback) has been reaped.
	exited chan struct{}
}

// newLocalServer prepares a server config but does not start it.
func newLocalServer(binaryPath, addr, token string, keepData bool) *localServer {
	return &localServer{
		binaryPath: binaryPath,
		addr:       addr,
		token:      token,
		keepData:   keepData,
	}
}

// start builds the binary if absent, creates a temp DB directory, and
// spawns the server. Returns once the process has been launched (does not
// wait for the server to accept HTTP connections -- use waitReady for that).
func (s *localServer) start(ctx context.Context) error {
	if err := s.ensureBinary(ctx); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp("", "pcr-smoke-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	s.tempDir = tempDir

	logPath := filepath.Join(tempDir, "server.log")
	//nolint:gosec // G304: logPath is under our own MkdirTemp output, not user input
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	s.logFile = logFile

	cmd := exec.CommandContext(ctx, s.binaryPath) //nolint:gosec // path comes from --binary flag (operator-controlled), not user input
	cmd.Env = append(os.Environ(),
		"PCR_API_TOKENS="+s.token,
		// At least 32 bytes so config.loadSessionSecret accepts it.
		"PCR_SESSION_SECRET=smoke-session-secret-with-padding-xx",
		"PCR_COOKIE_SECURE=false",
		"PCR_DATABASE_PATH="+filepath.Join(tempDir, "registry.db"),
		"PCR_ADDR="+s.addr,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Detach into its own process group so we can signal it cleanly without
	// also signalling our own process tree.
	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s: %w", s.binaryPath, err)
	}
	s.cmd = cmd
	s.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	return nil
}

// waitReady polls /api/v1/health until 200 or the deadline expires.
// Health is intentionally outside auth, so the bearer token is not required.
func (s *localServer) waitReady(ctx context.Context, baseURL string) error {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	httpc := &http.Client{Timeout: time.Second}
	url := strings.TrimRight(baseURL, "/") + "/api/v1/health"

	for {
		req, err := http.NewRequestWithContext(deadline, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := httpc.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		// Fail fast if the subprocess has already exited — otherwise the
		// poll loop would burn the full 10s deadline waiting for a server
		// that's already gone.
		select {
		case <-s.exited:
			return fmt.Errorf("server exited before becoming ready; see %s", s.logFile.Name())
		case <-deadline.Done():
			return fmt.Errorf("server did not become ready within 10s; see %s", s.logFile.Name())
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// stop sends SIGTERM, waits up to 5s for graceful shutdown, then SIGKILL.
// Cleans up the temp DB unless keepData was set. Wait() is owned by the
// goroutine spawned in start(); we observe termination via s.exited.
func (s *localServer) stop() {
	if s.cmd != nil && s.cmd.Process != nil && s.exited != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	}
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
	if s.tempDir != "" && !s.keepData {
		_ = os.RemoveAll(s.tempDir)
	}
}

// dumpLog writes the server's captured stdout/stderr to w. Used on failure
// so the operator can see why the server misbehaved.
func (s *localServer) dumpLog(w io.Writer) {
	if s.logFile == nil {
		return
	}
	f, err := os.Open(s.logFile.Name())
	if err != nil {
		_, _ = fmt.Fprintf(w, "(failed to open server log %s: %v)\n", s.logFile.Name(), err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(w, "--- server log ---")
	_, _ = io.Copy(w, f)
	_, _ = fmt.Fprintln(w, "--- end server log ---")
}

// ensureBinary builds the server binary if it doesn't already exist at binaryPath.
func (s *localServer) ensureBinary(ctx context.Context) error {
	if _, err := os.Stat(s.binaryPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", s.binaryPath, err)
	}

	fmt.Printf("    (building %s ...)\n", s.binaryPath)
	if err := os.MkdirAll(filepath.Dir(s.binaryPath), 0o750); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", s.binaryPath, "./cmd/server") //nolint:gosec // hardcoded args, no user input
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}
	return nil
}
