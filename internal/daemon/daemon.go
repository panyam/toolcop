// Package daemon implements the long-running toolcop server.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/panyam/toolcop/pkg/api"
)

// Evaluator produces zero or more Decisions for a given request. The daemon
// folds them with api.Combine to get the final verdict. Phase-1 ships a
// PassEvaluator stub; the rules engine implements this in task #6.
type Evaluator interface {
	Evaluate(req api.Request) []api.Decision
}

// PassEvaluator emits no decisions, so every request falls through to Pass
// → wire `ask`. The safe default before any rules are loaded.
type PassEvaluator struct{}

func (PassEvaluator) Evaluate(api.Request) []api.Decision { return nil }

// connHandleTimeout caps a single client connection. The thin client
// finishes in <50ms; anything longer is a stuck client we should drop.
const connHandleTimeout = 5 * time.Second

// Daemon owns the unix-socket listener and dispatches per-connection
// handlers.
type Daemon struct {
	socketPath string
	listener   net.Listener
	eval       Evaluator
	log        *log.Logger

	mu      sync.Mutex
	quit    chan struct{}
	wg      sync.WaitGroup
	started bool
}

// New constructs a Daemon. socketPath and eval are required; log defaults
// to a stderr logger if nil.
func New(socketPath string, eval Evaluator, logger *log.Logger) *Daemon {
	if logger == nil {
		logger = log.New(os.Stderr, "toolcopd ", log.LstdFlags|log.Lmicroseconds)
	}
	return &Daemon{
		socketPath: socketPath,
		eval:       eval,
		log:        logger,
		quit:       make(chan struct{}),
	}
}

// Listen creates the parent directory, removes any stale socket, binds, and
// chmod's the socket to 0600.
func (d *Daemon) Listen() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return errors.New("daemon already started")
	}
	if err := os.MkdirAll(filepath.Dir(d.socketPath), 0700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(d.socketPath), err)
	}
	// Best-effort stale-socket removal. If a live daemon owns it, the
	// Listen below will fail with EADDRINUSE and we abort.
	if _, err := os.Stat(d.socketPath); err == nil {
		if isAliveAt(d.socketPath) {
			return fmt.Errorf("another daemon appears to be listening on %s", d.socketPath)
		}
		_ = os.Remove(d.socketPath)
	}
	l, err := net.Listen("unix", d.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.socketPath, err)
	}
	if err := os.Chmod(d.socketPath, 0600); err != nil {
		l.Close()
		return fmt.Errorf("chmod %s: %w", d.socketPath, err)
	}
	d.listener = l
	d.started = true
	d.log.Printf("listening on %s", d.socketPath)
	return nil
}

// Serve runs the accept loop until Shutdown is called. Blocks the caller.
func (d *Daemon) Serve() error {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.quit:
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}
		d.wg.Add(1)
		go d.handleConn(conn)
	}
}

// Shutdown stops accepting and waits for in-flight handlers up to ctx
// deadline. Returns ctx.Err() if the deadline elapses first.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	d.started = false
	close(d.quit)
	if d.listener != nil {
		d.listener.Close()
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		_ = os.Remove(d.socketPath)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer d.wg.Done()
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(connHandleTimeout))

	var req api.Request
	if err := api.ReadMessage(conn, &req); err != nil {
		d.log.Printf("read: %v", err)
		return
	}

	start := time.Now()
	decisions := d.eval.Evaluate(req)
	final := api.Combine(decisions)
	elapsed := time.Since(start)

	d.log.Printf("decision=%s tool=%s source=%q reason=%q took=%s",
		final.Verdict, req.ToolName, final.Source, final.Reason, elapsed)

	if err := api.WriteMessage(conn, final.ToResponse()); err != nil {
		d.log.Printf("write: %v", err)
	}
}

// isAliveAt tries to connect to an existing socket; if the connect succeeds
// (or fails with anything other than ECONNREFUSED / file-missing), assume a
// live daemon owns it. Used during Listen to refuse double-binds.
func isAliveAt(socketPath string) bool {
	c, err := net.DialTimeout("unix", socketPath, 50*time.Millisecond)
	if err == nil {
		c.Close()
		return true
	}
	return false
}

// DefaultSocketPath picks a per-user socket location.
//
// Resolution order:
//  1. $TOOLCOP_SOCKET — explicit override (used for tests, parallel runs,
//     and per-shell isolation)
//  2. $XDG_RUNTIME_DIR/toolcop.sock — Linux systemd-user default
//  3. $TMPDIR/toolcop.sock — macOS standard
//  4. ~/.toolcop/toolcop.sock — fallback
func DefaultSocketPath() string {
	if p := os.Getenv("TOOLCOP_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "toolcop.sock")
	}
	if dir := os.Getenv("TMPDIR"); dir != "" {
		return filepath.Join(dir, "toolcop.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".toolcop", "toolcop.sock")
}
