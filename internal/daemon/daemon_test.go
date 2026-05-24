package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/panyam/toolcop/pkg/api"
)

// tempSocket returns a short socket path. macOS limits sun_path to ~104
// bytes, which t.TempDir() blows past with long test names.
func tempSocket(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "tcop*.sock")
	if err != nil {
		t.Fatalf("create temp socket: %v", err)
	}
	p := f.Name()
	f.Close()
	os.Remove(p) // remove so net.Listen can bind
	t.Cleanup(func() { os.Remove(p) })
	return p
}

// echoEval emits a fixed Decision for every request.
type echoEval struct{ d api.Decision }

func (e echoEval) Evaluate(api.Request) []api.Decision {
	if e.d.Verdict == "" {
		return nil
	}
	return []api.Decision{e.d}
}

func startTestDaemon(t *testing.T, eval Evaluator) (string, func()) {
	t.Helper()
	sock := tempSocket(t)
	d := New(sock, eval, nil)
	if err := d.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() {
		_ = d.Serve()
	}()
	return sock, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}
}

func TestDaemonAllow(t *testing.T) {
	sock, stop := startTestDaemon(t, echoEval{d: api.Allow("test-rule")})
	defer stop()

	resp := roundtrip(t, sock, sampleRequest("Bash"))
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAllow {
		t.Errorf("decision = %q, want %q",
			resp.HookSpecificOutput.PermissionDecision, api.PermissionAllow)
	}
	if resp.HookSpecificOutput.PermissionDecisionReason != "test-rule" {
		t.Errorf("reason = %q, want %q",
			resp.HookSpecificOutput.PermissionDecisionReason, "test-rule")
	}
}

func TestDaemonPassFallsThroughToAsk(t *testing.T) {
	// PassEvaluator emits no decisions; expect wire `ask`.
	sock, stop := startTestDaemon(t, PassEvaluator{})
	defer stop()

	resp := roundtrip(t, sock, sampleRequest("Bash"))
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("Pass evaluator should render as ask, got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
}

func TestDaemonDoubleListen(t *testing.T) {
	sock := tempSocket(t)
	d1 := New(sock, PassEvaluator{}, nil)
	if err := d1.Listen(); err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer d1.Shutdown(context.Background())

	d2 := New(sock, PassEvaluator{}, nil)
	if err := d2.Listen(); err == nil {
		t.Errorf("expected second Listen to fail on live socket")
	}
}

func TestDaemonShadowMasksAllow(t *testing.T) {
	sock := tempSocket(t)
	d := New(sock, echoEval{d: api.Allow("test-rule")}, nil)
	d.Shadow = true
	if err := d.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go d.Serve()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()

	resp := roundtrip(t, sock, sampleRequest("Bash"))
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("shadow should mask allow as ask, got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
}

func TestDaemonShadowMasksDeny(t *testing.T) {
	sock := tempSocket(t)
	d := New(sock, echoEval{d: api.Deny("dangerous")}, nil)
	d.Shadow = true
	if err := d.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go d.Serve()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()

	resp := roundtrip(t, sock, sampleRequest("Bash"))
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("shadow should mask deny as ask (inert during pilot), got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
}

func TestDaemonShadowPassesThroughAsk(t *testing.T) {
	// When the verdict already would have been Ask, shadow mode shouldn't
	// fabricate a "shadow override" since there's nothing to change.
	sock := tempSocket(t)
	d := New(sock, echoEval{d: api.Ask("clarify")}, nil)
	d.Shadow = true
	if err := d.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go d.Serve()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		d.Shutdown(ctx)
	}()

	resp := roundtrip(t, sock, sampleRequest("Bash"))
	if resp.HookSpecificOutput.PermissionDecision != api.PermissionAsk {
		t.Errorf("ask should pass through unchanged, got %q",
			resp.HookSpecificOutput.PermissionDecision)
	}
	// The reason should be the original Ask reason, not the shadow override.
	if resp.HookSpecificOutput.PermissionDecisionReason != "clarify" {
		t.Errorf("ask should preserve original reason, got %q",
			resp.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestDaemonStaleSocketReplaced(t *testing.T) {
	sock := tempSocket(t)
	// Create a dead socket file (not listening).
	d := New(sock, PassEvaluator{}, nil)
	if err := d.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Shut down without removing? Shutdown does remove. So simulate
	// crash by closing the listener directly and leaving the file.
	d.listener.Close()
	// File is still on disk (in some OSes); other OSes clean it.
	// Either way a fresh daemon should be able to bind.
	d2 := New(sock, PassEvaluator{}, nil)
	if err := d2.Listen(); err != nil {
		t.Fatalf("second Listen should succeed on dead socket: %v", err)
	}
	defer d2.Shutdown(context.Background())
}

// helpers --------------------------------------------------------------------

func sampleRequest(tool string) api.Request {
	return api.Request{
		SessionID:     "test-session",
		Cwd:           "/tmp",
		HookEventName: api.HookEventPreToolUse,
		ToolName:      tool,
		ToolInput:     json.RawMessage(`{"command":"ls -la"}`),
	}
}

func roundtrip(t *testing.T, sock string, req api.Request) api.Response {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))

	if err := api.WriteMessage(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	var resp api.Response
	if err := api.ReadMessage(conn, &resp); err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	return resp
}
