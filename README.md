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

# 2. Wire toolcop into Claude Code: adds the PreToolUse hook to
#    ~/.claude/settings.json (backed up first), and seeds a starter
#    ~/.config/toolcop/rules.yaml if you don't already have one.
toolcop onboard --dry-run    # preview the change first
toolcop onboard              # do it

# 3. Pilot in shadow mode for a session or two — rules are evaluated
#    and logged, but the daemon always emits `ask` so behavior is
#    indistinguishable from running without toolcop. Inspect the log
#    to see what would have changed once you flip to live.
toolcop daemon --shadow      # run foreground, or background it
tail -f ~/.local/state/toolcop/daemon.log

# 4. When you're happy with what shadow logged, go live by just
#    killing the shadow daemon — the next tool call auto-forks a
#    normal one with the same rules. No config change needed.
pkill -f 'toolcop daemon --shadow'

# 5. Verify a few commands offline whenever you tweak rules
toolcop test "gh api repos/foo/bar"        # → allow
toolcop test "GH_TOKEN=x gh api foo"       # → allow (env-stripped)
toolcop test "git status && rm -rf ~/foo"  # → deny (compound vetted)
toolcop test "timeout 30 npm test"         # → ask (no matching rule yet)

# 6. To remove the hook later
toolcop offboard
```

**Shadow mode caveat**: in `--shadow`, deny rules are inert (everything
becomes `ask`). Use it only while validating new rules — switch back to
live mode before you actually rely on safety rules like `never-rm-rf-home`.

For richer rules than the starter set, see [`examples/rules.yaml`](./examples/rules.yaml). Run `toolcop --help` for the full subcommand list.

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

