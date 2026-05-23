package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte(`{"hello":"world"}`)
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("roundtrip mismatch: got %q want %q", got, payload)
	}
}

func TestFrameMultiple(t *testing.T) {
	var buf bytes.Buffer
	payloads := [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"b":2}`),
		[]byte(`""`),
	}
	for _, p := range payloads {
		if err := WriteFrame(&buf, p); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for i, want := range payloads {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame[%d]: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame[%d]: got %q want %q", i, got, want)
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF after last frame, got %v", err)
	}
}

func TestFrameOversize(t *testing.T) {
	var buf bytes.Buffer
	huge := make([]byte, MaxFrameSize+1)
	if err := WriteFrame(&buf, huge); err == nil {
		t.Errorf("WriteFrame accepted oversize payload")
	}
}

func TestReadFrameTruncated(t *testing.T) {
	// header says 100 bytes but stream only has 5
	header := []byte{0, 0, 0, 100}
	body := []byte("short")
	r := bytes.NewReader(append(header, body...))
	_, err := ReadFrame(r)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("expected ErrUnexpectedEOF, got %v", err)
	}
}

func TestMessageRoundtrip(t *testing.T) {
	req := Request{
		SessionID:     "sid-123",
		Cwd:           "/tmp/foo",
		HookEventName: HookEventPreToolUse,
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"ls -la","description":"list"}`),
	}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, req); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	var got Request
	if err := ReadMessage(&buf, &got); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if got.SessionID != req.SessionID || got.ToolName != req.ToolName {
		t.Errorf("message mismatch: got %+v want %+v", got, req)
	}
	// ToolInput is RawMessage; compare as JSON text
	if !bytes.Equal(got.ToolInput, req.ToolInput) {
		t.Errorf("ToolInput mismatch: got %q want %q", got.ToolInput, req.ToolInput)
	}
}

func TestResponseWireShape(t *testing.T) {
	resp := Decision{
		Verdict: VerdictAllow,
		Reason:  "gh-readonly: allowed",
		Source:  "gh-readonly",
	}.ToResponse()
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`"hookSpecificOutput"`,
		`"hookEventName":"PreToolUse"`,
		`"permissionDecision":"allow"`,
		`"permissionDecisionReason":"gh-readonly: allowed"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response missing %q in %s", want, got)
		}
	}
}

func TestPassRendersAsAsk(t *testing.T) {
	// A Pass verdict means "no opinion" — the daemon defers to Claude's
	// normal prompt UI by emitting `ask`.
	resp := Pass().ToResponse()
	if resp.HookSpecificOutput.PermissionDecision != PermissionAsk {
		t.Errorf("Pass.ToResponse() decision = %q, want %q",
			resp.HookSpecificOutput.PermissionDecision, PermissionAsk)
	}
}
