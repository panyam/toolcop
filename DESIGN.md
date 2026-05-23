# toolcop — Design

Context-aware, programmable permission gate for Claude Code tool calls.

## Goal

Replace flaky prefix-based `Bash(...)` matchers in `settings.json` with a daemon
that runs real logic: YAML matchers for declarative cases, compiled Go modules
for stateful ones (state machines, rate limits, sequence-aware decisions). All
decisions go through one `PreToolUse` hook.

## Non-goals (V1)

- **Not a command transformer.** Use `direnv` for env vars, PATH shims for
  command rewrites. Mixing gate-and-transform muddies the model.
- **Not a sandbox.** No OS-level isolation; this is policy, not enforcement
  against a malicious actor.
- **Not multi-user / multi-host.** Single user, single host, unix socket.
- **No hot-reload of Go modules.** Compile-in, daemon restart is ~10ms.
- **No LLM-in-the-loop decisions.** Could be a module later; not in core.

## Architecture

```
  Claude Code (any session)
        │  stdin: PreToolUse JSON
        ▼
   ┌──────────────┐    unix socket    ┌─────────────────────────┐
   │ toolcop      │ ────────────────► │ toolcopd                │
   │ (thin client)│ ◄──────────────── │ - rules.yaml (hot)      │
   └──────────────┘                   │ - Go modules (compiled) │
        │  stdout: PreToolUse JSON    │ - sqlite stores         │
        ▼                             │ - bash tokenizer        │
   Claude Code                        └─────────────────────────┘
```

**Thin client** (`toolcop`, default subcommand): reads JSON on stdin, opens
`$XDG_RUNTIME_DIR/toolcop.sock`, forwards request, writes response to stdout,
exits. Auto-forks daemon if socket unreachable. Hard timeout 200ms; falls
through to `ask` on any failure.

**Daemon** (`toolcop daemon`): one process per user, listens on the socket,
loads rules + modules at startup, owns sqlite, watches `rules.yaml` for
changes via fsnotify.

Both are the **same Go binary** dispatched by subcommand. Static build with
`CGO_ENABLED=0 -ldflags="-s -w"`.

## Wire protocol

CLI ↔ daemon uses **length-prefixed JSON frames** over the unix socket — not
gRPC. Decision tracked in issue 1.

Frame format:
```
[4-byte big-endian length][JSON payload]
```

Schema lives in `pkg/api/protocol.go` as Go structs (`Request`, `Response`,
`TailEvent`). JSON-serializable for any non-Go client.

A `Transport` interface in the daemon allows swapping implementations behind
a single interface — V1 ships only `UnixSocketJSON`. Future transports
(HTTP/2, Connect-RPC) plug in without call-site changes.

**Client language is hot-swappable.** Because the wire format is framed JSON
and the daemon is long-running, the thin client can be rewritten in any
language (Rust, C, Zig) without touching the daemon. The contract is the
wire format, not the language. The only client-side logic beyond I/O
plumbing is auto-forking the daemon if the socket is unreachable — a few
lines in any language.

**Why not gRPC**: client is fork+exec'd per tool call; gRPC's HTTP/2 setup
adds ~2–5ms cold-start vs ~0.5–1ms for raw socket+JSON. Single-host
single-user removes the portability argument. Build complexity (`.proto` +
codegen) isn't earned by one request type.

**Revisit triggers** (real requirement, not hypothetical):
- Non-Go client outside toolcop needs to talk to the daemon → reconsider
  Connect-RPC (not raw gRPC)
- Daemon needs network exposure → Connect-RPC behind existing Transport
- Protocol grows past ~5 message types with versioning needs

## Hook integration

User-level `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": ".*",
        "hooks": [{ "type": "command", "command": "/Users/you/bin/toolcop" }]
      }
    ]
  }
}
```

Hook protocol (Claude → toolcop on stdin):
```json
{
  "session_id": "...",
  "transcript_path": "...",
  "cwd": "...",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "A=1 gh api repos/foo/bar", "description": "..." }
}
```

Response (toolcop → Claude on stdout):
```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "allow|deny|ask",
    "permissionDecisionReason": "gh-readonly: allowed gh api read-only call"
  }
}
```

## Decision model

Each rule/module emits a `Decision { Verdict, Reason, Source, AskPrompt? }`.
All matching rules + all modules vote. Fold by:

1. **`deny` beats everything.** Reason is preserved.
2. **`ask` beats `allow`.** Custom prompt (if any) wins; ties broken by source order.
3. **`allow` requires no `ask` or `deny`.**
4. **Default = `ask`** (falls through to Claude's normal prompt UI).

Mirrors settings.json semantics so muscle memory carries over.

## Configuration hierarchy

Walk from `cwd` upward, then user level:

```
$cwd/.toolcop/rules.yaml
$cwd/../.toolcop/rules.yaml
...up to home...
~/.config/toolcop/rules.yaml
```

Plus per-scope local override that's gitignore-able:
```
$cwd/.toolcop/rules.local.yaml   ← personal, not committed
$cwd/.toolcop/rules.yaml         ← team-wide, committed
```

**Cascade semantics:** closer scope wins for rule lookup by `name`. Inner
scopes can override outer rules by re-declaring the same `name:`.

**`final: true`** on a rule prevents inner scopes from overriding it:
- User-level rule with `final: true` cannot be overridden by any project or
  folder rule. Matches "managed settings" semantics.
- Project-level rule with `final: true` cannot be overridden by deeper folders
  or `.local.yaml`.
- Attempting to override a `final` rule logs a warning and is ignored.

## Rule language (YAML)

```yaml
rules:
  - name: gh-readonly
    match:
      tool: Bash
      program: gh
      subcommand_in: [api, pr, issue, run, workflow]
      args_starts_with_any:
        - [api]
        - [pr, view]
        - [pr, list]
        - [pr, checks]
        - [issue, view]
        - [issue, list]
    decide: allow
    reason: gh read-only call

  - name: env-prefixed-passthrough
    match:
      tool: Bash
      # env: stripped before matching; visible if you want to assert on it
      program: { in: [git, gh, grep, find, fd, rg] }
      subcommand_in: [status, log, diff, show, view, list, ls]
    decide: allow

  - name: never-rm-rf-home
    final: true
    match:
      tool: Bash
      command_regex: 'rm\s+-rf\s+(~|\$HOME|/Users/[^/]+)(\s|$)'
    decide: deny
    reason: refuses rm -rf of home directory

  - name: ask-before-git-push
    match:
      tool: Bash
      program: git
      subcommand: push
    decide: ask
    prompt: "git push to {{ .Parsed.Args | join \" \" }} — confirm?"
```

**Match fields** (all optional, AND-combined):
- `tool` — string or `{ in: [...] }`
- `program` — Bash only; the executable name after env-prefix strip
- `subcommand` / `subcommand_in` — the first positional arg
- `args_starts_with` / `args_starts_with_any` — list-prefix match on args
- `command_regex` — escape hatch
- `cwd_under` — restrict by working dir
- `env_has` — match env-prefixed key (e.g., `GH_TOKEN`)
- `file_path` — Read/Edit/Write only (gitignore-style globs)
- `url_domain` — WebFetch only

**Decide fields:**
- `decide` — `allow` | `deny` | `ask`
- `reason` — string, rendered in Claude's UI
- `prompt` — `ask` only; Go template with access to `.Parsed`, `.Request`,
  and any module-published variables (see Module API)
- `final` — boolean

Parsing uses `mvdan/sh` so quoting, env prefixes, pipes, `&&` chains are
correctly tokenized. Compound commands are matched per-segment.

## Module API (Go)

Modules live in `~/.config/toolcop/modules/*.go` and are compiled into the
daemon via a generated `modules_gen.go`. `toolcop modules build` regenerates
and rebuilds.

```go
package modules

import "github.com/panyam/toolcop/api"

type GhInvestigation struct{}

func (m *GhInvestigation) Name() string { return "gh-investigation" }

func (m *GhInvestigation) Evaluate(ctx api.Context, req api.Request) api.Decision {
    if req.Tool != "Bash" || req.Parsed.Program != "gh" {
        return api.Pass()
    }
    sess := ctx.ClaudeSession()
    if sess.GetBool("in-gh-investigation") {
        if sess.Age("in-gh-investigation") < 5*time.Minute {
            return api.Allow("inside gh-investigation window")
        }
    }
    if isReadOnlyGh(req.Parsed) {
        sess.SetBool("in-gh-investigation", true)
        return api.Allow("entering gh-investigation mode")
    }
    return api.Pass()
}

func init() { api.Register(&GhInvestigation{}) }
```

**Optional capability interfaces** (detected by type assertion):
```go
type SessionAware interface { OnSessionStart(api.Context, sessionID string) }
type Variables interface { Variables(req api.Request) map[string]any }  // for ask prompts
type Healthchecker interface { Healthcheck() error }
```

Modules **always log to stderr** (daemon enforces this — module that writes to
stdout is killed at startup). Modules are pure-ish: state lives only in
provided stores.

## Stores

Three KV stores, **SQLite-backed** via `modernc.org/sqlite` (pure-Go, no CGO)
with `github.com/jmoiron/sqlx` as the SQL layer. Single file at
`~/.local/state/toolcop/state.db`, WAL mode. Decision tracked in issue 2.

| Store | Key scope | Lifetime | Use |
|---|---|---|---|
| `ctx.ClaudeSession()` | `session_id` | Until daemon explicitly evicts (90d default) | Per-session state machines |
| `ctx.WorkingContext()` | `cwd` + idle-bucket (30m window) | Configurable | "While I'm working in this repo" |
| `ctx.Global()` | none | Forever | Cross-session counters, rate limits, learned patterns |

API:
```go
type KV interface {
    Get(key string) ([]byte, bool)
    Set(key string, val []byte, ttl ...time.Duration)
    Incr(key string, delta int64) int64
    Age(key string) time.Duration
    Delete(key string)
}
```

Typed helpers: `GetBool/SetBool`, `GetString/SetString`, `GetJSON/SetJSON`.

Module-facing API is plain KV — modules never see SQL. The daemon's
admin/observability surface (`toolcop session inspect/list`, `toolcop
suggest`, TTL eviction) uses sqlx struct scanning against the same table.

`/clear` in Claude Code yields a new `session_id`; the old `ClaudeSession`
state stays in sqlite (for forensics via `toolcop session inspect <sid>`) but
is not joined to the new session. State at `WorkingContext` and `Global`
scopes is unaffected — this is the right behavior.

## CLI surface

```
toolcop                              # thin client (default; reads stdin)
toolcop daemon                       # run daemon
toolcop status                       # daemon health, modules, recent counts
toolcop tail [--session SID]         # live decision stream (TUI)
toolcop test "A=1 gh api foo"        # dry-run: prints which rule fires, full decision trace
toolcop allow '<pattern>'            # append rule to current scope's rules.yaml
toolcop deny  '<pattern>'
toolcop suggest                      # mine ask-decisions, propose rules
toolcop modules list
toolcop modules build                # regen + rebuild daemon
toolcop session inspect <sid>        # forensic view of a past Claude session
toolcop reload                       # re-read rules.yaml (also automatic via fsnotify)
toolcop logs [--follow]              # daemon log
```

## Observability

- **Decisions log**: `~/.local/state/toolcop/decisions.jsonl` — one line per
  call: timestamp, session_id, tool, command, decision, source rule/module,
  reason, eval duration (μs).
- **`toolcop tail`** — pretty-prints decisions live. Colors by verdict. The
  killer DX feature for "why was I prompted?"
- **`toolcop suggest`** — reads decisions.jsonl, clusters frequently-`ask`'d
  patterns, proposes YAML rules. Closes the iteration loop.
- **Per-rule hit counters** exposed via `toolcop status`.
- **Daemon log** at `~/.local/state/toolcop/daemon.log` (stderr of daemon).

## Failure modes

| Failure | Behavior |
|---|---|
| Daemon socket unreachable | Client auto-forks daemon, retries with 50ms backoff cap |
| Daemon hung / slow | Client hard timeout 200ms, returns `ask` |
| Module panics during Evaluate | Daemon recovers, logs, treats as `Pass()`, increments error counter |
| Module writes to stdout | Daemon kills it at startup; refuses to start if any module does this on the first call |
| Malformed rules.yaml | Daemon keeps last known good, logs error, exposes via `toolcop status` |
| sqlite locked | Decision proceeds without state read; logs warning |
| stdin truncated | Returns `ask` with reason `malformed input` |

## Multi-Claude

Per-session isolation via `session_id`-keyed `ClaudeSession` store. Shared
state via `Global`. Per-repo shared state via `WorkingContext`. Concurrent
connections handled by goroutine-per-conn.

## Implementation phases

**Phase 1 — MVP gate.** Single Go binary. Thin client + daemon over unix
socket. Auto-fork. YAML matchers (subset: `tool`, `program`, `subcommand`,
`args_starts_with`, `command_regex`). Hard-coded user-level config path. No
modules yet, no stores. `toolcop test` and `toolcop tail`.

**Phase 2 — Stores + modules.** sqlite stores, module registration, `modules
build`. One reference module (gh-investigation).

**Phase 3 — Hierarchical config + `final:true`.** cwd-walk, `.toolcop/`
discovery, override semantics, override-of-final detection and logging.
Project + local split.

**Phase 4 — Iteration loop.** `toolcop allow/deny` writers, `toolcop
suggest`, `ask` prompt templates with module variables.

**Phase 5 — Polish.** systemd/launchd unit files, install script, integration
into stack-catalog as a component.

## Open questions (deferred)

- **Multi-host state sync.** Not needed; flag if it ever is.
- **Encrypted state for secrets.** Modules shouldn't store secrets; revisit
  if a use case appears.
- **Module sandboxing.** Modules are user-authored, trusted code. No
  sandbox.
- **A "shadow mode"** where toolcop logs what it *would* decide without
  actually deciding — useful for tuning new rules. Likely Phase 4.
