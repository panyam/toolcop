// Package parse tokenizes raw Bash command strings using mvdan/sh.
//
// It returns every reachable command invocation in the AST — not just the
// leftmost. Compound commands (chains, loops, conditionals, subshells,
// command substitution, function bodies) all have their inner commands
// surfaced so the rules engine can evaluate each independently.
//
// # Safety property
//
// The rule of thumb is: when we can't prove safety, we don't claim it.
//
//   - Every reachable [CallExpr] in the AST is collected.
//   - On a parse error, the whole command set is empty — callers treat
//     this as "no opinion" which the daemon renders as `ask`.
//   - Compound commands where any segment has no matching rule are
//     forced to `ask` by the rules engine, even if other segments would
//     match `allow` rules.
//
// # Known limitations
//
// Commands like `find -exec rm {} \;`, `xargs rm`, and `bash -c "..."`
// are syntactically a single CallExpr to `find`/`xargs`/`bash`; the
// inner command isn't a separate AST node. Use `command_regex` rules
// (or Phase-2 modules with semantic knowledge) to catch these.
package parse

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// CommandSet is the parsed form of a Bash command line, decomposed into
// every command invocation the parser could extract from the AST.
type CommandSet struct {
	Raw      string
	Commands []*Command
}

// Primary returns the first command in the set, or nil if empty.
func (s *CommandSet) Primary() *Command {
	if s == nil || len(s.Commands) == 0 {
		return nil
	}
	return s.Commands[0]
}

// IsCompound reports whether the command set has more than one command —
// chains, loops, conditionals, subshells, or command substitution
// produced multiple call sites.
func (s *CommandSet) IsCompound() bool {
	return s != nil && len(s.Commands) > 1
}

// Command is one extracted command invocation.
type Command struct {
	Env        map[string]string // env-prefix assignments (A=1 B=2 cmd)
	Program    string            // executable name after wrapper strip
	Subcommand string            // first positional after Program
	Args       []string          // all positionals after Program (including Subcommand)
}

// ParseBash tokenizes a Bash command string.
//
// Walks the entire AST collecting every CallExpr. For complex parts that
// can't be statically resolved (variable expansions, parameter expansions),
// the affected arg becomes empty — matchers fall back to command_regex
// against [CommandSet.Raw] for those cases.
func ParseBash(cmd string) (*CommandSet, error) {
	set := &CommandSet{Raw: cmd}
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, fmt.Errorf("toolcop/parse: %w", err)
	}

	syntax.Walk(f, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			// CallExpr can be an "assigns only" node (A=1 B=2 with no
			// program). Only collect when there's an actual command.
			if len(call.Args) > 0 {
				set.Commands = append(set.Commands, fromCallExpr(call))
			}
		}
		return true
	})
	return set, nil
}

func fromCallExpr(call *syntax.CallExpr) *Command {
	cmd := &Command{Env: map[string]string{}}

	for _, a := range call.Assigns {
		val := ""
		if a.Value != nil {
			val = wordText(a.Value)
		}
		cmd.Env[a.Name.Value] = val
	}

	var args []string
	for _, w := range call.Args {
		args = append(args, wordText(w))
	}
	args = stripWrappers(args)

	if len(args) > 0 {
		cmd.Program = args[0]
		if len(args) > 1 {
			cmd.Subcommand = args[1]
		}
		cmd.Args = args[1:]
	}
	return cmd
}

// wordText extracts the literal text of a word, unquoting single and
// double quotes. Variable expansions and command substitutions are
// skipped — they contribute nothing to the static view.
func wordText(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range w.Parts {
		switch t := part.(type) {
		case *syntax.Lit:
			sb.WriteString(t.Value)
		case *syntax.SglQuoted:
			sb.WriteString(t.Value)
		case *syntax.DblQuoted:
			for _, inner := range t.Parts {
				if lit, ok := inner.(*syntax.Lit); ok {
					sb.WriteString(lit.Value)
				}
			}
		}
	}
	return sb.String()
}

// stripWrappers peels off common process-wrapper commands so the
// underlying program is what matchers see. Conservative: if the
// wrapper's option syntax doesn't match expectations, we stop stripping
// rather than guessing wrong. The user can always fall back to
// command_regex.
func stripWrappers(args []string) []string {
	for len(args) > 0 {
		switch args[0] {
		case "timeout":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				switch args[0] {
				case "-k", "-s", "--kill-after", "--signal":
					if len(args) >= 2 {
						args = args[2:]
					} else {
						return nil
					}
				default:
					args = args[1:]
				}
			}
			if len(args) > 0 {
				args = args[1:] // skip DURATION
			}
		case "nice":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if args[0] == "-n" && len(args) >= 2 {
					args = args[2:]
				} else {
					args = args[1:]
				}
			}
		case "nohup", "time":
			args = args[1:]
		case "stdbuf":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if len(args[0]) == 2 && len(args) >= 2 {
					args = args[2:]
				} else {
					args = args[1:]
				}
			}
		case "env":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				args = args[1:]
			}
			for len(args) > 0 && isEnvAssignment(args[0]) {
				args = args[1:]
			}
		default:
			return args
		}
	}
	return args
}

func isEnvAssignment(s string) bool {
	eq := strings.Index(s, "=")
	if eq <= 0 {
		return false
	}
	name := s[:eq]
	for i, c := range name {
		ok := c == '_' ||
			(c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(i > 0 && c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}
