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

Coming with Phase 1.

## License

GPL-3.0. See [LICENSE](./LICENSE).

