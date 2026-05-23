// Package rules loads YAML rule files and evaluates them against incoming
// requests. This is the Phase-1 declarative matcher; Phase-2 Go modules
// extend it with stateful logic via the same Decision contract.
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
	Decide string `yaml:"decide"`           // allow | deny | ask
	Reason string `yaml:"reason,omitempty"` // free-form, shown to Claude
	Prompt string `yaml:"prompt,omitempty"` // ask only; reserved for module-variable expansion in Phase 2
	Final  bool   `yaml:"final,omitempty"`  // hierarchy-override block; honored in Phase 3

	cmdRegex *regexp.Regexp // compiled at Parse time
}

// Match is the AND-combined matcher. Empty fields are ignored.
//
// Phase-1 subset: tool / tool_in, program / program_in, subcommand /
// subcommand_in, args_starts_with, command_regex, env_has, env_equals.
type Match struct {
	Tool           string            `yaml:"tool,omitempty"`
	ToolIn         []string          `yaml:"tool_in,omitempty"`
	Program        string            `yaml:"program,omitempty"`
	ProgramIn      []string          `yaml:"program_in,omitempty"`
	Subcommand     string            `yaml:"subcommand,omitempty"`
	SubcommandIn   []string          `yaml:"subcommand_in,omitempty"`
	ArgsStartsWith []string          `yaml:"args_starts_with,omitempty"`
	CommandRegex   string            `yaml:"command_regex,omitempty"`
	EnvHas         StringList        `yaml:"env_has,omitempty"`    // all listed keys must be present in the command's env prefix
	EnvEquals      map[string]string `yaml:"env_equals,omitempty"` // all listed key=value pairs must match exactly
}

// StringList accepts either a single YAML scalar or a sequence:
//
//	env_has: GH_TOKEN          # scalar form
//	env_has: [GH_TOKEN, OTHER] # sequence form
type StringList []string

// UnmarshalYAML implements yaml.Unmarshaler so the field accepts both
// scalar and sequence forms.
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

// Config is the on-disk shape: a top-level `rules:` list.
type Config struct {
	Rules []Rule `yaml:"rules"`
}

// Engine holds compiled rules for fast repeated evaluation.
type Engine struct {
	Rules []Rule
}

// Load reads the YAML file at path. A missing file yields an empty Engine
// (rules.yaml is optional — no rules means every request falls through to
// the default `ask`).
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

// Parse compiles YAML bytes directly. Used by tests and by `toolcop test`.
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

// Evaluate runs every rule against the request and returns the Decisions
// of the rules that matched. The caller folds them via [api.Combine].
func (e *Engine) Evaluate(req api.Request) []api.Decision {
	parsed := parseBashIfApplicable(req)
	var decisions []api.Decision
	for i := range e.Rules {
		r := &e.Rules[i]
		if !r.matches(&req, parsed) {
			continue
		}
		decisions = append(decisions, r.decision())
	}
	return decisions
}

func parseBashIfApplicable(req api.Request) *parse.Parsed {
	if req.ToolName != "Bash" || len(req.ToolInput) == 0 {
		return nil
	}
	var input api.BashInput
	if err := json.Unmarshal(req.ToolInput, &input); err != nil {
		return nil
	}
	p, err := parse.ParseBash(input.Command)
	if err != nil {
		return nil
	}
	return p
}

func (r *Rule) matches(req *api.Request, parsed *parse.Parsed) bool {
	m := r.Match

	if m.Tool != "" && m.Tool != req.ToolName {
		return false
	}
	if len(m.ToolIn) > 0 && !containsStr(m.ToolIn, req.ToolName) {
		return false
	}

	needsBash := m.Program != "" || len(m.ProgramIn) > 0 ||
		m.Subcommand != "" || len(m.SubcommandIn) > 0 ||
		len(m.ArgsStartsWith) > 0 ||
		len(m.EnvHas) > 0 || len(m.EnvEquals) > 0
	if needsBash {
		if parsed == nil {
			return false
		}
		if m.Program != "" && m.Program != parsed.Program {
			return false
		}
		if len(m.ProgramIn) > 0 && !containsStr(m.ProgramIn, parsed.Program) {
			return false
		}
		if m.Subcommand != "" && m.Subcommand != parsed.Subcommand {
			return false
		}
		if len(m.SubcommandIn) > 0 && !containsStr(m.SubcommandIn, parsed.Subcommand) {
			return false
		}
		if len(m.ArgsStartsWith) > 0 && !startsWithStr(parsed.Args, m.ArgsStartsWith) {
			return false
		}
		for _, key := range m.EnvHas {
			if _, ok := parsed.Env[key]; !ok {
				return false
			}
		}
		for key, want := range m.EnvEquals {
			if got, ok := parsed.Env[key]; !ok || got != want {
				return false
			}
		}
	}

	if r.cmdRegex != nil {
		if parsed == nil {
			return false
		}
		if !r.cmdRegex.MatchString(parsed.Raw) {
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
