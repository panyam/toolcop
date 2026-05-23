// Package parse tokenizes raw Bash command strings using mvdan/sh.
//
// The resulting [Parsed] struct is what rule matchers and modules see when
// dispatching on Bash commands. Env prefixes are extracted, common wrappers
// (timeout, nice, nohup, env, ...) are stripped, and pipelines / chained
// commands are followed to their first segment so Program reflects the
// "real" command of interest.
package parse

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Parsed is a structured view of a Bash command string.
type Parsed struct {
	Raw         string            // original input, verbatim
	Env         map[string]string // env-prefix assignments (A=1 B=2 cmd)
	Program     string            // executable name after wrapper strip
	Subcommand  string            // first positional after Program (convenience)
	Args        []string          // all positionals after Program (including Subcommand)
	HasPipeline bool              // top-level statement was a pipeline
}

// ParseBash tokenizes a Bash command string.
//
// For complex parts that can't be statically resolved (variable expansions,
// command substitution), the affected arg becomes empty in the structured
// view. Matchers can fall back to a regex match against [Parsed.Raw] for
// those cases.
func ParseBash(cmd string) (*Parsed, error) {
	p := &Parsed{Raw: cmd, Env: map[string]string{}}
	parser := syntax.NewParser()
	f, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, fmt.Errorf("toolcop/parse: %w", err)
	}
	if len(f.Stmts) == 0 {
		return p, nil
	}

	stmt := f.Stmts[0]

	// Follow pipelines and chains (&&, ||, ;) to the first segment.
	for {
		bc, ok := stmt.Cmd.(*syntax.BinaryCmd)
		if !ok {
			break
		}
		if bc.Op == syntax.Pipe || bc.Op == syntax.PipeAll {
			p.HasPipeline = true
		}
		stmt = bc.X
	}

	callExpr, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok {
		// Subshell, function def, or some other construct — return what
		// we have so callers can still inspect Raw / Env.
		return p, nil
	}

	for _, a := range callExpr.Assigns {
		val := ""
		if a.Value != nil {
			val = wordText(a.Value)
		}
		p.Env[a.Name.Value] = val
	}

	var args []string
	for _, w := range callExpr.Args {
		args = append(args, wordText(w))
	}

	args = stripWrappers(args)

	if len(args) > 0 {
		p.Program = args[0]
		if len(args) > 1 {
			p.Subcommand = args[1]
		}
		p.Args = args[1:]
	}
	return p, nil
}

// wordText extracts the literal text of a word, unquoting single and double
// quotes. Variable expansions and command substitutions are skipped (they
// contribute nothing to the static view).
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

// stripWrappers peels off common process-wrapper commands so the underlying
// program is what matchers see. Conservative: if the wrapper's option
// syntax doesn't match our expectations, we stop stripping and leave args
// as-is — the user can always fall back to command_regex.
func stripWrappers(args []string) []string {
	for len(args) > 0 {
		switch args[0] {
		case "timeout":
			// timeout [OPTIONS]... DURATION COMMAND [ARG]...
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
			// nice [-n N] COMMAND [ARG]...
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
			// stdbuf -i MODE -o MODE -e MODE COMMAND [ARG]...
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				if len(args[0]) == 2 && len(args) >= 2 {
					args = args[2:]
				} else {
					args = args[1:]
				}
			}
		case "env":
			// env [OPTION]... [-] [NAME=VALUE]... [COMMAND [ARG]...]
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
