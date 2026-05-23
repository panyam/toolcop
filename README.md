# toolcop

A context-aware, programmable permission gate for Claude Code tool calls. Replaces flaky prefix-based `Bash(...)` matchers in `settings.json` with a long-running daemon that runs real logic — YAML matchers for declarative cases, compiled Go modules for stateful ones.

**Status**: pre-alpha. See [DESIGN.md](./DESIGN.md) for architecture, scope, and phase plan.

## Layout

- `cmd/toolcop/` — single binary entry point; dispatches to client or daemon subcommands
- `pkg/api/` — wire protocol (Request / Response / Decision over length-prefixed JSON frames)
- `internal/daemon/` — unix-socket listener, rule evaluator, optional Go modules
- `internal/client/` — thin client used as the `PreToolUse` hook command
- `internal/rules/` — YAML rule loader and matcher
- `internal/parse/` — Bash command tokenizer (`mvdan/sh` wrapper)

## Quickstart

```bash
# 1. Build and install (to ~/.local/bin by default; PREFIX= to override)
make install

# 2. Drop a starter rules file
mkdir -p ~/.config/toolcop
cp examples/rules.yaml ~/.config/toolcop/rules.yaml

# 3. Verify a few commands without wiring up Claude yet
toolcop test "gh api repos/foo/bar"        # → allow
toolcop test "GH_TOKEN=x gh api foo"        # → allow (env-stripped)
toolcop test "timeout 30 npm test"          # → ask (no matching rule yet)
toolcop test "rm -rf ~/foo"                 # → deny

# 4. Wire as a PreToolUse hook — merge examples/settings.json into
#    ~/.claude/settings.json (replace the path with your installed binary).
#    The daemon auto-starts the first time a hook fires.

# 5. Watch what's happening (Phase 4 feature; not yet implemented)
# toolcop tail
```

Run `toolcop --help` for the full subcommand list.

## How it works

```
Claude Code
    │  stdin: PreToolUse JSON
    ▼
┌────────────┐   unix socket    ┌─────────────────────────┐
│ toolcop    │ ───────────────► │ toolcopd                │
│ (thin)     │ ◄─────────────── │ - rules.yaml (hot path) │
└────────────┘                  │ - Go modules (Phase 2)  │
    │ stdout: PreToolUse JSON   │ - sqlite stores (later) │
    ▼                           └─────────────────────────┘
Claude Code
```

The hook binary is paper-thin: read stdin, forward to daemon, write stdout. Daemon holds the rules and state. See [DESIGN.md](./DESIGN.md) for architecture details.

## Development

```bash
make build        # builds bin/toolcop
make test         # runs go test ./...
make smoke        # end-to-end: starts daemon, sends a few requests, verifies
make install      # installs to $(PREFIX)/bin (default ~/.local)
```

## License

GPL-3.0. See [LICENSE](./LICENSE).

