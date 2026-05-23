// Package client implements the thin PreToolUse hook client.
//
// The client reads the hook JSON from stdin, frames and forwards it to the
// daemon over a unix socket, then writes the daemon's response to stdout
// for Claude Code to consume. On any failure — daemon down, slow, garbled,
// timeout — the client "fails open" by emitting a Pass→ask response so the
// user sees Claude's normal prompt rather than a hung tool call.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/panyam/toolcop/pkg/api"
)

const (
	// TotalTimeout is the hard cap on a single client invocation. The hook
	// fires synchronously on every tool call, so anything longer hurts.
	TotalTimeout = 200 * time.Millisecond

	dialTimeout      = 50 * time.Millisecond
	retryBackoffInit = 10 * time.Millisecond
	retryBackoffMax  = 50 * time.Millisecond
)

// Config controls Run. Zero values pick sensible defaults.
type Config struct {
	SocketPath string    // empty → caller-supplied default
	AutoFork   bool      // if true, exec self as `daemon` on missing socket
	DaemonExe  string    // path to the daemon binary; empty → os.Executable()
	Stdin      io.Reader // nil → os.Stdin
	Stdout     io.Writer // nil → os.Stdout
	Stderr     io.Writer // nil → os.Stderr
	LogPath    string    // daemon stderr/stdout sink when auto-forking
}

// Run executes one client cycle. It writes a JSON Response to Stdout on
// every code path; the returned error reflects only Stdout write failures.
func Run(cfg Config) error {
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	deadline := time.Now().Add(TotalTimeout)

	inputData, err := io.ReadAll(cfg.Stdin)
	if err != nil {
		return failOpen(cfg, "read stdin: %v", err)
	}

	// Validate the JSON parses so we don't waste daemon time on garbage.
	var req api.Request
	if err := json.Unmarshal(inputData, &req); err != nil {
		return failOpen(cfg, "invalid hook JSON: %v", err)
	}

	conn, err := connectWithAutofork(cfg, deadline)
	if err != nil {
		return failOpen(cfg, "connect: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	if err := api.WriteFrame(conn, inputData); err != nil {
		return failOpen(cfg, "send: %v", err)
	}

	respData, err := api.ReadFrame(conn)
	if err != nil {
		return failOpen(cfg, "read response: %v", err)
	}

	// Validate before passing on to Claude.
	var resp api.Response
	if err := json.Unmarshal(respData, &resp); err != nil {
		return failOpen(cfg, "invalid daemon response: %v", err)
	}

	_, err = cfg.Stdout.Write(respData)
	return err
}

// failOpen logs the cause to stderr and emits a Pass→ask response.
func failOpen(cfg Config, format string, args ...any) error {
	fmt.Fprintf(cfg.Stderr, "toolcop: "+format+"; falling back to ask\n", args...)
	resp := api.Pass().ToResponse()
	data, _ := json.Marshal(resp)
	_, err := cfg.Stdout.Write(data)
	return err
}

func connectWithAutofork(cfg Config, deadline time.Time) (net.Conn, error) {
	conn, err := dial(cfg.SocketPath, deadline)
	if err == nil {
		return conn, nil
	}

	if cfg.AutoFork && shouldFork(err) {
		if forkErr := forkDaemon(cfg); forkErr != nil {
			return nil, fmt.Errorf("fork daemon: %w (dial: %v)", forkErr, err)
		}
		return retryDial(cfg.SocketPath, deadline)
	}

	return nil, err
}

func dial(socketPath string, deadline time.Time) (net.Conn, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, errors.New("deadline exceeded")
	}
	timeout := remaining
	if timeout > dialTimeout {
		timeout = dialTimeout
	}
	return net.DialTimeout("unix", socketPath, timeout)
}

func retryDial(socketPath string, deadline time.Time) (net.Conn, error) {
	backoff := retryBackoffInit
	for time.Now().Before(deadline) {
		if remaining := time.Until(deadline); remaining < backoff {
			if remaining <= 0 {
				break
			}
			backoff = remaining
		}
		time.Sleep(backoff)
		if conn, err := dial(socketPath, deadline); err == nil {
			return conn, nil
		}
		if backoff < retryBackoffMax {
			backoff *= 2
			if backoff > retryBackoffMax {
				backoff = retryBackoffMax
			}
		}
	}
	return nil, errors.New("daemon never became reachable before deadline")
}

// shouldFork reports whether the dial error looks like "daemon isn't
// running" — ECONNREFUSED (stale socket file, no listener) or ENOENT (no
// socket file at all). Other errors (permission, unsupported, etc.) are not
// recoverable by forking.
func shouldFork(err error) bool {
	var netErr *net.OpError
	if !errors.As(err, &netErr) {
		return false
	}
	var sysErr *os.SyscallError
	if !errors.As(netErr.Err, &sysErr) {
		// On some systems the inner error isn't wrapped.
		return errors.Is(netErr.Err, syscall.ECONNREFUSED) ||
			errors.Is(netErr.Err, syscall.ENOENT)
	}
	return errors.Is(sysErr.Err, syscall.ECONNREFUSED) ||
		errors.Is(sysErr.Err, syscall.ENOENT)
}

func forkDaemon(cfg Config) error {
	exe := cfg.DaemonExe
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}

	args := []string{"daemon"}
	if cfg.SocketPath != "" {
		args = append(args, "--socket", cfg.SocketPath)
	}

	logFile := openLogFile(cfg.LogPath)

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func openLogFile(path string) *os.File {
	if path == "" {
		path = DefaultLogPath()
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		// As a fallback, send to /dev/null so the daemon doesn't write to
		// our stdout (which Claude reads).
		f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	return f
}

// DefaultLogPath returns the canonical daemon log path.
func DefaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/toolcop-daemon.log"
	}
	return filepath.Join(home, ".local", "state", "toolcop", "daemon.log")
}
