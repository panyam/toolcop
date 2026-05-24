// Package rules loads YAML rule files and evaluates them against incoming
// requests. This is the Phase-1 declarative matcher; Phase-2 Go modules
// extend it with stateful logic via the same Decision contract.
//
// # Compound-command safety
//
// For Bash, every command in the parsed AST (chains, loops, conditionals,
// subshells, command substitution) is evaluated independently. If any
// segment of a compound has *no* matching rule, the engine synthesizes an
// `ask` decision for it — preventing one segment's `allow` from silently
// covering for another unmatched (and therefore unvetted) segment.
package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/panyam/toolcop/internal/parse"
	"github.com/panyam/toolcop/pkg/api"
	"gopkg.in/yaml.v3"
)

// Rule is one YAML rule entry. Fields default to zero values (no constraint).
type Rule struct {
	Name   string `yaml:"name"`
	Match  Match  `yaml:"match"`
	Decide string `yaml:"decide"`
	Reason string `yaml:"reason,omitempty"`
	Prompt string `yaml:"prompt,omitempty"`
	Final  bool   `yaml:"final,omitempty"`

	cmdRegex *regexp.Regexp
}

// Match is the AND-combined matcher. Empty fields are ignored.
type Match struct {
	Tool           string            `yaml:"tool,omitempty"`
	ToolIn         []string          `yaml:"tool_in,omitempty"`
	Program        string            `yaml:"program,omitempty"`
	ProgramIn      []string          `yaml:"program_in,omitempty"`
	Subcommand     string            `yaml:"subcommand,omitempty"`
	SubcommandIn   []string          `yaml:"subcommand_in,omitempty"`
	ArgsStartsWith []string          `yaml:"args_starts_with,omitempty"`
	CommandRegex   string            `yaml:"command_regex,omitempty"`
	EnvHas         StringList        `yaml:"env_has,omitempty"`
	EnvEquals      map[string]string `yaml:"env_equals,omitempty"`
}

// StringList accepts either a single YAML scalar or a sequence.
type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var single string
		if err := value.Decode(&single); err != nil {
			return err
		}
		*s = []string{single}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

type Config struct {
	Rules []Rule `yaml:"rules"`
}

type Engine struct {
	Rules []Rule
}

func Load(path string) (*Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Engine{}, nil
		}
		return nil, err
	}
	return Parse(data)
}

func Parse(data []byte) (*Engine, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("toolcop/rules: parse: %w", err)
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if r.Decide != "" && !validDecide(r.Decide) {
			return nil, fmt.Errorf("toolcop/rules: rule %q: invalid decide %q (want allow|deny|ask)", r.Name, r.Decide)
		}
		if r.Match.CommandRegex != "" {
			re, err := regexp.Compile(r.Match.CommandRegex)
			if err != nil {
				return nil, fmt.Errorf("toolcop/rules: rule %q: regex: %w", r.Name, err)
			}
			r.cmdRegex = re
		}
	}
	return &Engine{Rules: cfg.Rules}, nil
}

func validDecide(s string) bool {
	return s == "allow" || s == "deny" || s == "ask"
}

// Evaluate runs every rule against the request and returns Decisions to
// be folded by [api.Combine].
//
// Two-pass strategy:
//
//  1. Request-level rules (those with no per-command matchers — pure
//     tool / command_regex) fire ONCE per request. Their verdict applies
//     to the whole call.
//  2. Command-level rules (those with program / subcommand / args /
//     env matchers) fire PER segment of a Bash compound. Each segment is
//     evaluated independently.
//
// If a compound has any segment with no command-level match AND no
// request-level rule covered the request, a synthetic `ask` decision is
// emitted for that segment — preventing a Bash-specific allow on one
// segment from silently covering an unvetted segment elsewhere.
func (e *Engine) Evaluate(req api.Request) []api.Decision {
	set := parseBashIfApplicable(req)

	var decisions []api.Decision

	// Pass 1: request-level-only rules.
	requestLevelFired := false
	for i := range e.Rules {
		r := &e.Rules[i]
		if r.hasBashMatchers() {
			continue
		}
		if !r.matchesRequestLevel(&req, set) {
			continue
		}
		decisions = append(decisions, r.decision())
		requestLevelFired = true
	}

	// Non-Bash or unparseable Bash: no per-segment pass.
	if set == nil || len(set.Commands) == 0 {
		return decisions
	}

	// Pass 2: command-level rules, per segment.
	compound := set.IsCompound()
	for idx, cmd := range set.Commands {
		perCmd := e.decisionsForCommandBashOnly(&req, set, cmd)
		if compound && len(perCmd) == 0 && !requestLevelFired {
			// No rule of any kind covered this segment. Don't let
			// another segment's allow silently rescue it.
			decisions = append(decisions, api.Decision{
				Verdict: api.VerdictAsk,
				Source:  "(unmatched-segment)",
				Reason:  fmt.Sprintf("compound segment %d (%q) had no matching rule", idx, cmd.Program),
			})
			continue
		}
		decisions = append(decisions, perCmd...)
	}
	return decisions
}

// decisionsForCommandBashOnly returns Decisions for command-level rules
// (i.e., those with at least one Bash-specific matcher) that match the
// given single command.
func (e *Engine) decisionsForCommandBashOnly(req *api.Request, set *parse.CommandSet, cmd *parse.Command) []api.Decision {
	var decs []api.Decision
	for i := range e.Rules {
		r := &e.Rules[i]
		if !r.hasBashMatchers() {
			continue
		}
		if !r.matchesRequestLevel(req, set) {
			continue
		}
		if !r.matchesCommand(cmd) {
			continue
		}
		decs = append(decs, r.decision())
	}
	return decs
}

func parseBashIfApplicable(req api.Request) *parse.CommandSet {
	if req.ToolName != "Bash" || len(req.ToolInput) == 0 {
		return nil
	}
	var input api.BashInput
	if err := json.Unmarshal(req.ToolInput, &input); err != nil {
		return nil
	}
	set, err := parse.ParseBash(input.Command)
	if err != nil {
		return nil
	}
	return set
}

// matchesRequestLevel checks rule fields that apply to the whole request,
// not to a single command in the compound: tool / tool_in, command_regex.
func (r *Rule) matchesRequestLevel(req *api.Request, set *parse.CommandSet) bool {
	m := r.Match
	if m.Tool != "" && m.Tool != req.ToolName {
		return false
	}
	if len(m.ToolIn) > 0 && !containsStr(m.ToolIn, req.ToolName) {
		return false
	}
	if r.cmdRegex != nil {
		if set == nil {
			return false
		}
		if !r.cmdRegex.MatchString(set.Raw) {
			return false
		}
	}
	return true
}

// hasBashMatchers reports whether the rule has any command-level
// matchers — i.e., it needs a parsed [parse.Command] to evaluate.
func (r *Rule) hasBashMatchers() bool {
	m := r.Match
	return m.Program != "" || len(m.ProgramIn) > 0 ||
		m.Subcommand != "" || len(m.SubcommandIn) > 0 ||
		len(m.ArgsStartsWith) > 0 ||
		len(m.EnvHas) > 0 || len(m.EnvEquals) > 0
}

// matchesCommand checks command-level fields against a single command.
func (r *Rule) matchesCommand(cmd *parse.Command) bool {
	m := r.Match
	if m.Program != "" && m.Program != cmd.Program {
		return false
	}
	if len(m.ProgramIn) > 0 && !containsStr(m.ProgramIn, cmd.Program) {
		return false
	}
	if m.Subcommand != "" && m.Subcommand != cmd.Subcommand {
		return false
	}
	if len(m.SubcommandIn) > 0 && !containsStr(m.SubcommandIn, cmd.Subcommand) {
		return false
	}
	if len(m.ArgsStartsWith) > 0 && !startsWithStr(cmd.Args, m.ArgsStartsWith) {
		return false
	}
	for _, key := range m.EnvHas {
		if _, ok := cmd.Env[key]; !ok {
			return false
		}
	}
	for key, want := range m.EnvEquals {
		if got, ok := cmd.Env[key]; !ok || got != want {
			return false
		}
	}
	return true
}

func (r *Rule) decision() api.Decision {
	d := api.Decision{
		Source: r.Name,
		Reason: r.Reason,
		Prompt: r.Prompt,
	}
	switch r.Decide {
	case "allow":
		d.Verdict = api.VerdictAllow
	case "deny":
		d.Verdict = api.VerdictDeny
	case "ask":
		d.Verdict = api.VerdictAsk
	default:
		d.Verdict = api.VerdictPass
	}
	return d
}

func containsStr(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func startsWithStr(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if args[i] != p {
			return false
		}
	}
	return true
}
