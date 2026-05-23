// Package api defines the wire protocol between the toolcop CLI and daemon.
//
// All messages are length-prefixed JSON frames on a unix socket:
//
//	[4-byte big-endian length][JSON payload]
//
// The schema here is the canonical contract; any non-Go client (Rust, C,
// etc.) consumes the same JSON shapes. The wire format also matches the
// PreToolUse hook JSON Claude Code emits on stdin and expects on stdout, so
// the thin client can pass frames through with minimal transformation.
package api

import "encoding/json"

// Hook event names emitted by Claude Code.
const (
	HookEventPreToolUse = "PreToolUse"
)

// permissionDecision string values Claude Code accepts on the wire.
const (
	PermissionAllow = "allow"
	PermissionDeny  = "deny"
	PermissionAsk   = "ask"
)

// Request is the PreToolUse hook input from Claude Code.
//
// Field names mirror the JSON exactly. ToolInput is left as RawMessage because
// its shape varies by tool (BashInput, ReadInput, EditInput, ...); callers
// decode it after dispatching on ToolName.
type Request struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
}

// Response is what the daemon returns and the client writes to stdout for
// Claude Code to consume.
type Response struct {
	HookSpecificOutput HookSpecificOutput `json:"hookSpecificOutput"`
}

// HookSpecificOutput carries the permission verdict and reason. The field
// names use Claude's camelCase convention (not the snake_case of Request).
type HookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// BashInput is the typed shape of Request.ToolInput when ToolName == "Bash".
// Unmarshaled on demand by rule matchers and modules that care about Bash.
type BashInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}
