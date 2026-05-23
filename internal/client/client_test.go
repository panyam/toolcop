package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/panyam/toolcop/internal/daemon"
	"github.com/panyam/toolcop/pkg/api"
)

func tempSocket(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "tcop*.sock")
	if err != nil {
		t.Fatalf("temp socket: %v", err)
	}
	p := f.Name()
	f.Close()
	os.Remove(p)
	t.Cleanup(func() { os.Remove(p) })
	return p
}

type fixedEval struct{ d api.Decision }

func (e fixedEval) Evaluate(api.Request) []api.Decision {
	if e.d.Verdict == "" {
		return nil
	}
	return []api.Decision{e.d}
}

func TestClientRunHappyPath(t *testing.T) {
	sock := tempSocket(t)
	d := daemon.New(sock, fixedEval{d: api.Allow("ok")}, nil)
	if err := d.Listen(); err != nil {
		t.Fatalf("daemon Listen: %v", err)
	}
	go d.Serve()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()

	hookJSON := `{"session_id":"s1","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`

	var stdout, stderr bytes.Buffer
	cfg := Config{
		SocketPath: sock,
		AutoFork:   false,
		Stdin:      strings.NewReader(hookJSON),
		Stdout:     &stdout,
		Stderr:     &stderr,
	}
	if err := Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var resp api.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode stdout: %v (stdout=%q stderr=%q)", err, stdout.String(), stderr.String())
	}
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAllow {
		t.Errorf("decision = %q want %q",
			resp.HookSpecificOutput.PermissionDecision, api.PermissionAllow)
	}
	if !strings.Contains(resp.HookSpecificOutput.PermissionDecisionReason, "ok") {
		t.Errorf("reason missing: %q", resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestClientFailsOpenWhenDaemonDown(t *testing.T) {
	// AutoFork=false and a socket that doesn't exist — Run must still emit
	// a valid response (ask) so the tool call doesn't hang.
	sock := tempSocket(t) // ensures cleanup; file doesn't exist
	hookJSON := `{"session_id":"s1","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`

	var stdout, stderr bytes.Buffer
	cfg := Config{
		SocketPath: sock,
		AutoFork:   false,
		Stdin:      strings.NewReader(hookJSON),
		Stdout:     &stdout,
		Stderr:     &stderr,
	}
	if err := Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var resp api.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("fail-open should be ask, got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
	if !strings.Contains(stderr.String(), "falling back to ask") {
		t.Errorf("expected fail-open log on stderr, got %q", stderr.String())
	}
}

func TestClientFailsOpenOnGarbageStdin(t *testing.T) {
	sock := tempSocket(t)
	var stdout, stderr bytes.Buffer
	cfg := Config{
		SocketPath: sock,
		AutoFork:   false,
		Stdin:      strings.NewReader("not json"),
		Stdout:     &stdout,
		Stderr:     &stderr,
	}
	if err := Run(cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var resp api.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("garbage input should fall back to ask, got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
}

// Sanity check: failOpen never leaves stdout empty, so Claude always has
// something to parse.
func TestFailOpenWritesValidResponse(t *testing.T) {
	var stdout bytes.Buffer
	cfg := Config{Stdout: &stdout, Stderr: io.Discard}
	if err := failOpen(cfg, "synthetic %s", "error"); err != nil {
		t.Fatalf("failOpen: %v", err)
	}
	var resp api.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("failOpen decision = %q want ask",
			resp.HookSpecificOutput.PermissionDecision)
	}
}
